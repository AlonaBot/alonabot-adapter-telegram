// Package telegram 实现官方 Telegram Adapter 的账号检查、long polling 接收、平台权限、媒体取件、发送与文本编辑。
//
// [New] 创建供 sdk.RunAdapter 使用的 Adapter。每个实例独占 HTTP 客户端和接收循环；
// Core 持久化逐条游标，实例仅在收到应答后推进内存状态（MESSAGE-MODEL §6.1）。
package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/AlonaBot/AlonaBot/adapters/telegram/perm"
	"github.com/AlonaBot/AlonaBot/sdk"
	"github.com/AlonaBot/AlonaBot/sdk/adapter"
	"github.com/AlonaBot/AlonaBot/sdk/errcode"
	msg "github.com/AlonaBot/AlonaBot/sdk/message"
)

// AdapterID 标识 Telegram 平台。
const AdapterID = "telegram"
const (
	pollTimeoutSeconds    = 30
	receiveRoundDuration  = 7 * 24 * time.Hour
	initialReceiveBackoff = time.Second
	maxReceiveBackoff     = 30 * time.Second
)

var allowedUpdates = []string{"message", "edited_message", "channel_post", "edited_channel_post", "callback_query", "my_chat_member", "chat_member", "message_reaction"}

type telegramAdapter struct {
	mu        sync.Mutex // 保护 starting/active 实例槽；网络请求期间不持锁。
	instances map[string]*instance
	now       func() time.Time
}

// New 创建独立的 Telegram Adapter；账号身份在实例 Start 时通过 getMe 验证。
func New() adapter.Adapter {
	return &telegramAdapter{instances: make(map[string]*instance), now: time.Now}
}

func platformCapabilities() []msg.Capability {
	return []msg.Capability{{Name: "button.clicked", Version: 1, Supported: true}, {Name: "message.send", Version: 1, Supported: true}, {Name: "message.markdown", Version: 1, Supported: true}, {Name: "message.album", Version: 1, Supported: true}, {Name: "message.reference", Version: 1, Supported: true}, {Name: "message.buttons", Version: 1, Supported: true}, {Name: editCapability, Version: 1, Supported: true}, {Name: "message.created", Version: 1, Supported: true}, {Name: "message.edited", Version: 1, Supported: true}, {Name: "member.joined", Version: 1, Supported: true}, {Name: "member.left", Version: 1, Supported: true}, {Name: "reaction.added", Version: 1, Supported: true}}
}

// Descriptor 声明本 Adapter 的接收、发送、编辑能力与会话平台权限。
func (*telegramAdapter) Descriptor() *adapter.Descriptor {
	return &adapter.Descriptor{
		AdapterID: AdapterID, Capabilities: platformCapabilities(),
		Permissions: []adapter.PermissionDeclaration{
			{Name: perm.ChatOwner, Implies: []string{perm.ChatAdmin}, LabelKey: "permission.chat_owner"},
			{Name: perm.ChatAdmin, Implies: []string{"chat_admin"}, LabelKey: "permission.chat_admin"},
		},
	}
}

// Start 验证账号和 webhook 后启动该实例唯一的接收循环。
func (a *telegramAdapter) Start(ctx context.Context, request adapter.StartRequest, core adapter.Core) (adapter.Instance, error) {
	// 1. 在配置边界验证参数，错误只返回字段名。
	var config struct {
		Token      string `json:"token"`
		APIBaseURL string `json:"api_base_url"`
		ProxyMode  string `json:"proxy_mode"`
		Proxy      string `json:"proxy"`
	}
	if err := json.Unmarshal(request.ConfigJSON, &config); err != nil || config.Token == "" {
		return nil, &sdk.Error{Code: errcode.InvalidArgument, Message: "invalid telegram configuration"}
	}
	if config.APIBaseURL == "" {
		config.APIBaseURL = defaultAPIBaseURL
	}
	if config.ProxyMode == "" {
		config.ProxyMode = proxyModeNone
	}
	client, err := newBotClient(config.Token, config.APIBaseURL, config.ProxyMode, config.Proxy)
	if err != nil {
		return nil, &sdk.Error{Code: errcode.InvalidArgument, Message: err.Error()}
	}
	saved := cursor{RoundStartUnixMS: a.now().UnixMilli()}
	if len(request.Cursor) > 0 {
		if err := json.Unmarshal(request.Cursor, &saved); err != nil {
			client.http.CloseIdleConnections()
			return nil, &sdk.Error{Code: errcode.InvalidArgument, Message: "invalid telegram cursor"}
		}
	}
	i := &instance{client: client, core: core, id: request.InstanceID, now: a.now, done: make(chan struct{}), administrators: make(map[int64]administratorCache)}
	a.mu.Lock()
	_, exists := a.instances[i.id]
	if !exists {
		a.instances[i.id] = i
	}
	a.mu.Unlock()
	if exists {
		client.http.CloseIdleConnections()
		return nil, &sdk.Error{Code: errcode.AlreadyExists, Message: "telegram instance already started"}
	}
	started := false
	defer func() {
		if !started {
			client.http.CloseIdleConnections()
			a.mu.Lock()
			delete(a.instances, i.id)
			a.mu.Unlock()
		}
	}()
	// 2. 验证账号与 webhook；启动失败时释放实例槽。
	var me struct {
		ID        int64  `json:"id"`
		Username  string `json:"username"`
		FirstName string `json:"first_name"`
	}
	if err := client.call(ctx, "getMe", struct{}{}, &me); err != nil {
		return nil, startError(err)
	}
	if me.ID == 0 {
		return nil, &sdk.Error{Code: errcode.Unavailable, Message: "telegram getMe returned no account ID"}
	}
	i.account = adapter.Account{ID: strconv.FormatInt(me.ID, 10), DisplayName: me.FirstName, Username: me.Username}
	i.botID = me.ID
	var webhook struct {
		URL string `json:"url"`
	}
	if err := client.call(ctx, "getWebhookInfo", struct{}{}, &webhook); err != nil {
		return nil, startError(err)
	}
	if webhook.URL != "" {
		return nil, &sdk.Error{Code: errcode.FailedPrecondition, Message: "telegram webhook is already configured"}
	}
	// 3. Start 的 RPC 结束不取消实例；循环与 Stop 单独拥有其 context。
	receiveContext, cancel := context.WithCancel(context.WithoutCancel(ctx))
	i.cancel = cancel
	started = true
	go func() {
		defer cancel()
		defer close(i.done) // Stop 等待全部回调完成后才返回。
		defer client.http.CloseIdleConnections()
		defer func() { a.mu.Lock(); delete(a.instances, i.id); a.mu.Unlock() }()
		i.receive(receiveContext, saved)
	}()
	return i, nil
}

func startError(err error) error {
	code := errcode.Unavailable
	var api *apiError
	if errors.As(err, &api) && api.status == http.StatusUnauthorized {
		code = errcode.PermissionDenied
	}
	return &sdk.Error{Code: code, Message: err.Error()}
}

type cursor struct {
	Offset             int64 `json:"offset"`
	RoundStartUnixMS   int64 `json:"round_start_unix_ms"`
	LastAcceptedUnixMS int64 `json:"last_accepted_unix_ms"`
}

// instance 的接收状态仅由 receive goroutine 修改；Stop 通过 cancel/done 交接生命周期。
type instance struct {
	fileMu         sync.Mutex // 仅保护文件路径缓存的读写。
	files          map[string]*platformFileLocation
	client         *botClient
	core           adapter.Core
	id             string
	account        adapter.Account
	botID          int64
	cancel         context.CancelFunc
	done           chan struct{}
	now            func() time.Time
	administrators map[int64]administratorCache // 仅由接收循环拥有，按会话缓存管理员列表。
}

// Account 返回 getMe 验证过的机器人身份。
func (i *instance) Account() adapter.Account { return i.account }

// Capabilities 返回与 Adapter 描述共用的能力表。
func (*instance) Capabilities() []msg.Capability { return platformCapabilities() }

// Stop 取消在途请求，等待全部接收回调退出后返回。
func (i *instance) Stop(_ context.Context) error {
	i.cancel()
	<-i.done // 接收循环拥有全部交付，退出后再确认停止。
	return nil
}

func (i *instance) receive(ctx context.Context, saved cursor) {
	backoff := initialReceiveBackoff
	for ctx.Err() == nil {
		// 1. 拉取一批 update，不在拉取时提前推进游标。
		var batch []json.RawMessage
		err := i.client.call(ctx, "getUpdates", struct {
			Offset  int64    `json:"offset,omitempty"`
			Timeout int      `json:"timeout"`
			Allowed []string `json:"allowed_updates"`
		}{saved.Offset, pollTimeoutSeconds, allowedUpdates}, &batch)
		if ctx.Err() != nil {
			return
		}
		// 2. 凭证或 owner 冲突上报后退出；其他拉取错误交给退避阶段。
		if err != nil {
			var api *apiError
			if errors.As(err, &api) && (api.status == http.StatusUnauthorized || api.status == http.StatusConflict) {
				message := "telegram rejected the token"
				if api.status == http.StatusConflict {
					message = "another getUpdates or webhook owns this bot"
				}
				code := errcode.PermissionDenied
				if api.status == http.StatusConflict {
					code = errcode.FailedPrecondition
				}
				if reportErr := i.core.ReportFailure(ctx, &errcode.Error{Code: code, Message: message}); reportErr != nil {
					slog.Error("report telegram failure", "error", i.client.safeError(reportErr))
				}
				return
			}
		} else {
			// 3. 逐条解析和交付，等待 Core 确认后再处理下一条。
			for _, raw := range batch {
				if ctx.Err() != nil {
					return
				}
				saved, err = i.processUpdate(ctx, raw, saved)
				if err != nil {
					break
				}
			}
		}
		if ctx.Err() != nil {
			return
		}
		// 4. Core 确认失败保留旧游标并退避；无法定位的 update 上报后退出。
		if err != nil {
			if errors.Is(err, errInvalidUpdateID) {
				slog.Warn("telegram receive failed", "adapter_instance_id", i.id, "error", i.client.safeError(err))
				if reportErr := i.core.ReportFailure(ctx, &errcode.Error{Code: errcode.Unavailable, Message: errInvalidUpdateID.Error()}); reportErr != nil {
					slog.Error("report telegram failure", "error", i.client.safeError(reportErr))
				}
				return
			}
			var public *sdk.Error
			if errors.As(err, &public) && public.Code == errcode.FailedPrecondition {
				return
			}
			slog.Warn("telegram receive failed", "adapter_instance_id", i.id, "error", i.client.safeError(err))
			delay := backoff
			var api *apiError
			if errors.As(err, &api) && api.status == http.StatusTooManyRequests && api.retryAfter > 0 {
				delay = api.retryAfter
			}
			if !waitReceive(ctx, delay) {
				return
			}
			backoff = nextReceiveBackoff(backoff)
			continue
		}
		backoff = initialReceiveBackoff
		// 5. 当前 long poll 已结束；先持久化轮次，再发起新的请求。
		now := i.now()
		if now.UnixMilli()-max(saved.RoundStartUnixMS, saved.LastAcceptedUnixMS) >= receiveRoundDuration.Milliseconds() {
			next := cursor{RoundStartUnixMS: now.UnixMilli()}
			if err := i.save(ctx, nil, next); err != nil {
				var public *sdk.Error
				if errors.As(err, &public) && public.Code == errcode.FailedPrecondition {
					return
				}
				slog.Warn("save telegram receive round failed", "error", i.client.safeError(err))
				if !waitReceive(ctx, backoff) {
					return
				}
				backoff = nextReceiveBackoff(backoff)
				continue
			}
			saved = next
		}
	}
}

var errInvalidUpdateID = errors.New("telegram update has no valid update_id")

func (i *instance) processUpdate(ctx context.Context, raw json.RawMessage, saved cursor) (cursor, error) {
	// 1. 单独读取 update_id，使其他字段解析失败时仍可安全推进游标。
	var update struct {
		ID            *int64          `json:"update_id"`
		CallbackQuery json.RawMessage `json:"callback_query"`
	}
	if err := json.Unmarshal(raw, &update); err != nil {
		return saved, errors.Join(errInvalidUpdateID, err)
	}
	if update.ID == nil {
		return saved, errInvalidUpdateID
	}
	next := saved
	next.Offset = *update.ID + 1
	var events []*msg.Event
	var err error
	if len(update.CallbackQuery) > 0 {
		events, err = i.callback(ctx, update.CallbackQuery, *update.ID, raw)
	} else {
		events, err = i.normalize(raw)
	}
	if err == nil {
		err = i.addPlatformPermissions(ctx, raw, events)
	}
	if err != nil {
		slog.Warn("skip malformed telegram update", "adapter_instance_id", i.id, "update_id", *update.ID, "error", i.client.safeError(err))
		events = nil
	}
	// 2. 无法标准化或不支持的 update 仅推进游标，保存失败仍按旧游标重拉。
	if len(events) == 0 {
		if err := i.save(ctx, nil, next); err != nil {
			return saved, err
		}
		return next, nil
	}
	// 3. 一个 update 的全部事件完成前，Core 仍保存旧游标以便重拉。
	next.LastAcceptedUnixMS = i.now().UnixMilli()
	for n, event := range events {
		if err := ctx.Err(); err != nil {
			return saved, err
		}
		deliveryCursor := saved
		if n == len(events)-1 {
			deliveryCursor = next
		}
		if err := i.save(ctx, event, deliveryCursor); err != nil {
			return saved, err
		}
	}
	return next, nil
}

func (i *instance) save(ctx context.Context, event *msg.Event, next cursor) error {
	encoded, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if event == nil {
		return i.core.SaveCursor(ctx, encoded)
	}
	_, err = i.core.Deliver(ctx, event, encoded)
	return err
}

func waitReceive(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextReceiveBackoff(current time.Duration) time.Duration {
	return min(current*2, maxReceiveBackoff)
}
