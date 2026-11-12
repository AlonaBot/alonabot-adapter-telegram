package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AlonaBot/AlonaBot/adapters/telegram/telegramtest"
	"github.com/AlonaBot/AlonaBot/sdk"
	"github.com/AlonaBot/AlonaBot/sdk/adapter"
	"github.com/AlonaBot/AlonaBot/sdk/errcode"
	msg "github.com/AlonaBot/AlonaBot/sdk/message"
)

type recordingCore struct {
	mu       sync.Mutex
	offsets  []int64
	cursors  []cursor
	events   []*msg.Event
	failures chan *errcode.Error
	saved    chan struct{}
	onSave   func(cursor)
	deliver  func(context.Context, *msg.Event, []byte) (bool, error)
}

func (c *recordingCore) Deliver(ctx context.Context, event *msg.Event, raw []byte) (bool, error) {
	if c.deliver != nil {
		return c.deliver(ctx, event, raw)
	}
	c.mu.Lock()
	c.events = append(c.events, event)
	c.mu.Unlock()
	return false, c.SaveCursor(ctx, raw)
}

func (c *recordingCore) SaveCursor(_ context.Context, raw []byte) error {
	var next cursor
	if err := json.Unmarshal(raw, &next); err != nil {
		return err
	}
	c.mu.Lock()
	c.offsets = append(c.offsets, next.Offset)
	c.cursors = append(c.cursors, next)
	c.mu.Unlock()
	if c.onSave != nil {
		c.onSave(next)
	}
	c.saved <- struct{}{}
	return nil
}

func (c *recordingCore) ReportFailure(_ context.Context, e *errcode.Error) error {
	c.failures <- e
	return nil
}

func (c *recordingCore) ReadMedia(context.Context, string, uint64, uint32) ([]byte, uint64, bool, error) {
	return nil, 0, false, &sdk.Error{Code: errcode.Unsupported, Message: "outbound media is not configured in this receiving fixture"}
}

// SetDefault 会同时修改 log 包的输出与标志，清理时一并恢复。
func setDefaultLogger(t *testing.T, logger *slog.Logger) {
	t.Helper()
	previous, writer, flags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(logger)
	t.Cleanup(func() {
		slog.SetDefault(previous)
		log.SetOutput(writer)
		log.SetFlags(flags)
	})
}

func newRecordingCore() *recordingCore {
	return &recordingCore{failures: make(chan *errcode.Error, 8), saved: make(chan struct{}, 16)}
}

func startRequest(t *testing.T, server *telegramtest.Server) adapter.StartRequest {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"token": "local-secret-token", "api_base_url": server.URL()})
	if err != nil {
		t.Fatal(err)
	}
	return adapter.StartRequest{InstanceID: "test-instance", ConfigJSON: raw}
}

// accountStartRequest 为账号映射与 mention 验收提供不同的用户名和展示名。
func accountStartRequest(t *testing.T, server *telegramtest.Server) adapter.StartRequest {
	t.Helper()
	target, err := url.Parse(server.URL())
	if err != nil {
		t.Fatal(err)
	}
	reverse := httputil.NewSingleHostReverseProxy(target)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if path.Base(r.URL.Path) == "getMe" {
			w.Header().Set("Content-Type", "application/json")
			if _, err := w.Write([]byte(`{"ok":true,"result":{"id":42,"is_bot":true,"username":"example_bot","first_name":"Example"}}`)); err != nil {
				t.Error(err)
			}
			return
		}
		reverse.ServeHTTP(w, r)
	}))
	t.Cleanup(api.Close)
	raw, err := json.Marshal(map[string]string{"token": "local-secret-token", "api_base_url": api.URL})
	if err != nil {
		t.Fatal(err)
	}
	return adapter.StartRequest{InstanceID: "test-instance", ConfigJSON: raw}
}

func waitSaved(t *testing.T, channel <-chan struct{}) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(5 * time.Second):
		t.Fatal("receive did not finish")
	}
}

func TestReceiveLifecycle(t *testing.T) {
	server := telegramtest.NewServer(t, "local-secret-token")
	server.Push(json.RawMessage(`{"update_id":10,"message":{"message_id":1,"date":123,"chat":{"id":9,"type":"private"},"text":"hello"}}`), json.RawMessage(`{"update_id":11,"callback_query":{}}`), json.RawMessage(`{"update_id":12,"message":{"message_id":2,"date":124,"chat":{"id":9,"type":"private"},"text":"world"}}`))
	core := newRecordingCore()
	a := New()
	request := accountStartRequest(t, server)
	instance, err := a.Start(context.Background(), request, core)
	if err != nil {
		t.Fatal(err)
	}
	registerStop(t, instance)
	if instance.Account().ID != "42" || instance.Account().DisplayName != "Example" || instance.Account().Username != "example_bot" {
		t.Fatalf("account: %+v", instance.Account())
	}
	for range 3 {
		waitSaved(t, core.saved)
	}
	if err := instance.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	core.mu.Lock()
	defer core.mu.Unlock()
	if len(core.events) != 2 || len(core.offsets) != 3 || core.offsets[0] != 11 || core.offsets[1] != 12 || core.offsets[2] != 13 {
		t.Fatalf("events=%d offsets=%v", len(core.events), core.offsets)
	}
	if core.events[0].OccurredAt.UnixMilli() != 123000 || core.events[0].AccountID != "42" {
		t.Fatal("invalid event identity or date")
	}
	server.SetWebhook("https://example.test/hook")
	_, err = a.Start(context.Background(), request, newRecordingCore())
	var public *sdk.Error
	if !errors.As(err, &public) || public.Code != errcode.FailedPrecondition {
		t.Fatalf("webhook error: %v", err)
	}
}

func TestFatalAndRetry(t *testing.T) {
	for _, status := range []int{401, 409, 502} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			server := telegramtest.NewServer(t, "local-secret-token")
			server.FailNext(status, 2)
			core := newRecordingCore()
			instance, err := New().Start(context.Background(), startRequest(t, server), core)
			if err != nil {
				t.Fatal(err)
			}
			registerStop(t, instance)
			if status == 502 {
				server.Push(json.RawMessage(`{"update_id":1,"message":{"message_id":1,"date":123,"chat":{"id":9,"type":"private"},"text":"hello"}}`))
				waitSaved(t, core.saved)
				if err := instance.Stop(context.Background()); err != nil {
					t.Fatal(err)
				}
				core.mu.Lock()
				count := len(core.events)
				core.mu.Unlock()
				if count != 1 || len(server.Offsets()) < 3 {
					t.Fatalf("recovery events=%d polls=%v", count, server.Offsets())
				}
			} else {
				select {
				case failure := <-core.failures:
					want := "telegram rejected the token"
					if status == 409 {
						want = "another getUpdates or webhook owns this bot"
					}
					if failure.Message != want {
						t.Fatalf("safe failure: %v", failure)
					}
				case <-time.After(time.Second):
					t.Fatal("failure was not reported")
				}
				if err := instance.Stop(context.Background()); err != nil {
					t.Fatal(err)
				}
				if len(core.failures) != 0 {
					t.Fatal("failure reported more than once")
				}
				if len(server.Offsets()) != 1 {
					t.Fatalf("fatal receive retried: %v", server.Offsets())
				}
			}
		})
	}
}

func TestRoundRebuild(t *testing.T) {
	server := telegramtest.NewServer(t, "local-secret-token")
	server.Push(json.RawMessage(`{"update_id":100,"callback_query":{}}`))
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var clock atomic.Int64
	clock.Store(old.UnixMilli())
	a := New().(*telegramAdapter)
	a.now = func() time.Time { return time.UnixMilli(clock.Load()) }
	core := newRecordingCore()
	core.onSave = func(c cursor) {
		if c.Offset == 101 {
			clock.Store(old.Add(receiveRoundDuration).UnixMilli())
		}
	}
	request := startRequest(t, server)
	request.Cursor = []byte(`{"offset":100,"round_start_unix_ms":1767225600000,"last_accepted_unix_ms":0}`)
	instance, err := a.Start(context.Background(), request, core)
	if err != nil {
		t.Fatal(err)
	}
	registerStop(t, instance)
	for range 2 {
		waitSaved(t, core.saved)
	}
	awaitCondition(t, func() bool { return len(server.Offsets()) >= 2 })
	if err := instance.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	core.mu.Lock()
	defer core.mu.Unlock()
	if len(core.offsets) < 2 || core.offsets[0] != 101 || core.offsets[1] != 0 || core.cursors[1].RoundStartUnixMS != old.Add(receiveRoundDuration).UnixMilli() || core.cursors[1].LastAcceptedUnixMS != 0 {
		t.Fatalf("round cursors: %v", core.offsets)
	}
	offsets := server.Offsets()
	if len(offsets) < 2 || offsets[0] != 100 || offsets[1] != 0 {
		t.Fatalf("round offsets: %v", offsets)
	}
}

func TestMessageMetadata(t *testing.T) {
	server := telegramtest.NewServer(t, "local-secret-token")
	messages := []struct {
		body  string
		toMe  bool
		actor bool
	}{
		{`"chat":{"id":-9,"type":"private"},"text":"private"`, true, true},
		{`"chat":{"id":-9,"type":"group"},"text":"reply","reply_to_message":{"from":{"id":42}}`, true, true},
		{`"chat":{"id":-9,"type":"group"},"text":"😀 @EXAMPLE_BOT","entities":[{"type":"mention","offset":3,"length":12}]`, true, true},
		{`"chat":{"id":-9,"type":"group"},"text":"@Example","entities":[{"type":"mention","offset":0,"length":8}]`, false, true},
		{`"chat":{"id":-9,"type":"group"},"text":"bot","entities":[{"type":"text_mention","offset":0,"length":3,"user":{"id":42}}]`, true, true},
		{`"chat":{"id":-9,"type":"group"},"text":"hello"`, false, true},
		{`"chat":{"id":-9,"type":"channel"},"text":"anonymous","sender_chat":{"id":-9}`, false, false},
		{`"chat":{"id":-9,"type":"supergroup"},"caption":"😀 bold","caption_entities":[{"type":"bold","offset":3,"length":4}],"media_group_id":"album","message_thread_id":77,"is_topic_message":true`, false, true},
		{`"chat":{"id":-9,"type":"supergroup"},"text":"reply thread","message_thread_id":88`, false, true},
	}
	for n, c := range messages {
		server.Push(json.RawMessage(fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"date":123,"from":{"id":7,"username":"reader","language_code":"en-us"},%s}}`, n+1, n+11, c.body)))
	}
	core := newRecordingCore()
	instance, err := New().Start(context.Background(), accountStartRequest(t, server), core)
	if err != nil {
		t.Fatal(err)
	}
	registerStop(t, instance)
	for range messages {
		waitSaved(t, core.saved)
	}
	if err := instance.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	core.mu.Lock()
	defer core.mu.Unlock()
	for n, c := range messages {
		e := core.events[n]
		if got := e.Payload.(*msg.CreatedPayload).ToMe; got != c.toMe {
			t.Errorf("message %d to_me=%v", n, got)
		}
		if (e.Actor != nil) != c.actor {
			t.Errorf("message %d actor=%v", n, e.Actor)
		}
		if e.Actor != nil && (e.Actor.Locale != "en-US" || e.Actor.ExternalUserID != "7" || e.Actor.AdapterInstanceID != "test-instance" || len(e.Actor.Permissions) != 0) {
			t.Errorf("actor: %v", e.Actor)
		}
		if e.AdapterID != AdapterID || e.AdapterInstanceID != "test-instance" || e.AccountID != "42" || (e.Session != "telegram:-9" && e.Session != "telegram:-9#77") || e.ID != "" || e.Source != 0 || len(e.Capabilities) != 0 || e.SourceEventID != strconv.Itoa(n+1) || !json.Valid(e.PlatformPayload) {
			t.Errorf("event identity: %v", e)
		}
	}
	album := core.events[len(messages)-2]
	entity := album.Message.Chain[0].(msg.TextSegment).Entities[0]
	if album.Message.MediaGroupID != "album" || album.Session != "telegram:-9#77" || entity.OffsetUTF16 != 3 || entity.LengthUTF16 != 4 || entity.Type != msg.EntityBold {
		t.Fatalf("album or entity: %v", album)
	}
	if target := core.events[len(messages)-1].Session; target != "telegram:-9" {
		t.Fatalf("reply thread target = %v, want no thread_id", target)
	}
}

func TestSubscriptionAndRestart(t *testing.T) {
	server := telegramtest.NewServer(t, "local-secret-token")
	server.Push(json.RawMessage(`{"update_id":10,"callback_query":{}}`))
	core := newRecordingCore()
	a := New()
	request := startRequest(t, server)
	instance, err := a.Start(context.Background(), request, core)
	if err != nil {
		t.Fatal(err)
	}
	registerStop(t, instance)
	waitSaved(t, core.saved)
	if err := instance.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	previous := len(server.Offsets())
	request.Cursor = []byte(fmt.Sprintf(`{"offset":11,"round_start_unix_ms":%d}`, time.Now().UnixMilli()))
	instance, err = a.Start(context.Background(), request, newRecordingCore())
	if err != nil {
		t.Fatal(err)
	}
	registerStop(t, instance)
	awaitCondition(t, func() bool { return len(server.Offsets()) > previous })
	if got := server.Offsets()[previous]; got != 11 {
		t.Fatalf("restart offset=%d", got)
	}
	expected := []string{"message", "edited_message", "channel_post", "edited_channel_post", "callback_query", "my_chat_member", "chat_member", "message_reaction"}
	for _, allowed := range server.AllowedUpdates() {
		if !reflect.DeepEqual(allowed, expected) {
			t.Fatalf("allowed_updates: %v", allowed)
		}
	}
}

func awaitCondition(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for !ready() {
		select {
		case <-deadline.C:
			t.Fatal("condition was not reached")
		case <-tick.C:
		}
	}
}

func TestInstanceProxy(t *testing.T) {
	server := telegramtest.NewServer(t, "local-secret-token")
	target, err := url.Parse(server.URL())
	if err != nil {
		t.Fatal(err)
	}
	reverse := httputil.NewSingleHostReverseProxy(target)
	var mu sync.Mutex
	var methods []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, path.Base(r.URL.Path))
		mu.Unlock()
		reverse.ServeHTTP(w, r)
	}))
	defer proxy.Close()
	config, err := json.Marshal(map[string]string{"token": "local-secret-token", "api_base_url": server.URL(), "proxy_mode": "custom", "proxy": proxy.URL})
	if err != nil {
		t.Fatal(err)
	}
	server.Push(json.RawMessage(`{"update_id":1,"callback_query":{}}`))
	core := newRecordingCore()
	instance, err := New().Start(context.Background(), adapter.StartRequest{InstanceID: "proxy", ConfigJSON: config}, core)
	if err != nil {
		t.Fatal(err)
	}
	registerStop(t, instance)
	waitSaved(t, core.saved)
	if err := instance.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Stop 可能取消一次已被代理记录、却未到达假服务器的长轮询，所以两边计数允许差 1。
	// 先读服务器计数：代理在转发前记录，服务器已计入的请求此时一定已在代理记录里。
	serverPolls := len(server.Offsets())
	mu.Lock()
	defer mu.Unlock()
	if len(methods) < 3 || methods[0] != "getMe" || methods[1] != "getWebhookInfo" {
		t.Fatalf("proxy methods: %v", methods)
	}
	proxyPolls := 0
	for _, method := range methods {
		if method == "getUpdates" {
			proxyPolls++
		}
	}
	if proxyPolls < serverPolls || proxyPolls > serverPolls+1 {
		t.Fatalf("proxy polls=%d server polls=%d", proxyPolls, serverPolls)
	}
}

func TestDirectProxyModeIgnoresEnvironment(t *testing.T) {
	server := telegramtest.NewServer(t, "local-secret-token")
	target, err := url.Parse(server.URL())
	if err != nil {
		t.Fatal(err)
	}
	reverse := httputil.NewSingleHostReverseProxy(target)
	var requests atomic.Int64
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		reverse.ServeHTTP(w, r)
	}))
	t.Cleanup(proxy.Close)
	t.Setenv("HTTP_PROXY", proxy.URL)
	config, err := json.Marshal(map[string]string{
		"token": "local-secret-token", "api_base_url": "http://telegram.invalid",
		"proxy_mode": "none", "proxy": proxy.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	inst, err := New().Start(t.Context(), adapter.StartRequest{InstanceID: "direct", ConfigJSON: config}, newRecordingCore())
	if inst != nil {
		registerStop(t, inst)
	}
	var public *sdk.Error
	if !errors.As(err, &public) || public.Code != errcode.Unavailable {
		t.Fatalf("Start error = %v, want UNAVAILABLE", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("proxy requests = %d, want 0", got)
	}
}

func TestCredentialsStayRedacted(t *testing.T) {
	var logs bytes.Buffer
	setDefaultLogger(t, slog.New(slog.NewTextHandler(&logs, nil)))
	server := telegramtest.NewServer(t, "local-secret-token")
	server.Push(json.RawMessage(`{"update_id":1,"message":{"message_id":1,"date":123,"chat":{"id":9,"type":"private"},"text":"hello"}}`))
	core := newRecordingCore()
	var attempts atomic.Int64
	core.deliver = func(ctx context.Context, e *msg.Event, raw []byte) (bool, error) {
		if attempts.Add(1) == 1 {
			return false, fmt.Errorf("failed URL %s/botlocal-secret-token/getUpdates", server.URL())
		}
		return false, core.SaveCursor(ctx, raw)
	}
	instance, err := New().Start(context.Background(), startRequest(t, server), core)
	if err != nil {
		t.Fatal(err)
	}
	registerStop(t, instance)
	waitSaved(t, core.saved)
	if err := instance.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "local-secret-token") || !strings.Contains(logs.String(), "<redacted>") {
		t.Fatalf("unsafe logs: %s", logs.String())
	}
	// 真实 HTTP 连接错误会包含完整请求 URL，Start 也必须先脱敏。
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	config, err := json.Marshal(map[string]string{"token": "local-secret-token", "api_base_url": closed.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = New().Start(context.Background(), adapter.StartRequest{InstanceID: "unreachable", ConfigJSON: config}, newRecordingCore())
	if err == nil || strings.Contains(err.Error(), "local-secret-token") || !strings.Contains(err.Error(), "bot<redacted>") {
		t.Fatalf("unsafe Start error: %v", err)
	}
}

func TestSequentialAcknowledgements(t *testing.T) {
	server := telegramtest.NewServer(t, "local-secret-token")
	for n := 1; n <= 3; n++ {
		server.Push(json.RawMessage(fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"date":123,"chat":{"id":9,"type":"private"},"text":"hello"}}`, n, n)))
	}
	core := newRecordingCore()
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int64
	core.deliver = func(ctx context.Context, e *msg.Event, raw []byte) (bool, error) {
		if calls.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return false, ctx.Err()
			}
		}
		core.mu.Lock()
		core.events = append(core.events, e)
		core.mu.Unlock()
		return false, core.SaveCursor(ctx, raw)
	}
	instance, err := New().Start(context.Background(), startRequest(t, server), core)
	if err != nil {
		t.Fatal(err)
	}
	registerStop(t, instance)
	waitSaved(t, entered)
	core.mu.Lock()
	saved := len(core.offsets)
	core.mu.Unlock()
	if calls.Load() != 1 || saved != 0 {
		t.Fatalf("batch advanced before ack: calls=%d saved=%d", calls.Load(), saved)
	}
	close(release)
	for range 3 {
		waitSaved(t, core.saved)
	}
	if err := instance.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("deliveries=%d", calls.Load())
	}
}

func TestNonMessageEvents(t *testing.T) {
	server := telegramtest.NewServer(t, "local-secret-token")
	for _, raw := range []string{
		`{"update_id":1,"channel_post":{"message_id":10,"date":100,"chat":{"id":-1,"type":"channel"},"text":"post"}}`,
		`{"update_id":2,"edited_message":{"message_id":11,"date":101,"chat":{"id":-1,"type":"group"},"text":"edit"}}`,
		`{"update_id":3,"edited_channel_post":{"message_id":12,"date":102,"chat":{"id":-1,"type":"channel"},"text":"edit"}}`,
		`{"update_id":4,"my_chat_member":{"date":103,"chat":{"id":-1},"from":{"id":7},"old_chat_member":{"status":"left","user":{"id":42}},"new_chat_member":{"status":"member","user":{"id":42}}}}`,
		`{"update_id":5,"chat_member":{"date":104,"chat":{"id":-1},"from":{"id":7},"old_chat_member":{"status":"member","user":{"id":8}},"new_chat_member":{"status":"kicked","user":{"id":8}}}}`,
		`{"update_id":6,"message_reaction":{"date":105,"chat":{"id":-1},"user":{"id":7},"message_id":10,"old_reaction":[{"type":"emoji","emoji":"👍"}],"new_reaction":[{"type":"emoji","emoji":"👍"},{"type":"emoji","emoji":"❤"}]}}`,
	} {
		server.Push(json.RawMessage(raw))
	}
	core := newRecordingCore()
	instance, err := New().Start(context.Background(), startRequest(t, server), core)
	if err != nil {
		t.Fatal(err)
	}
	registerStop(t, instance)
	for range 6 {
		waitSaved(t, core.saved)
	}
	if err := instance.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	core.mu.Lock()
	defer core.mu.Unlock()
	for n, want := range []string{"message.created", "message.edited", "message.edited", "member.joined", "member.left", "reaction.added"} {
		e := core.events[n]
		if e.Type != want || e.OccurredAt.UnixMilli() != int64(100+n)*1000 {
			t.Errorf("event %d: %v", n, e)
		}
		if n == 1 || n == 2 {
			if e.Payload.(*msg.EditedPayload).OldChain != nil {
				t.Fatal("fabricated old chain")
			}
		}
	}
	if core.events[3].Actor.ExternalUserID != "7" || core.events[3].Payload.(*msg.MemberPayload).MemberExternalUserID != "42" || core.events[5].Payload.(*msg.ReactionAddedPayload).Reaction != "❤" {
		t.Fatalf("member/reaction payloads: %v", core.events)
	}
}

func registerStop(t *testing.T, running adapter.Instance) {
	t.Helper()
	t.Cleanup(func() {
		if err := running.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
}

func TestReactionCursorSurvivesRestart(t *testing.T) {
	server := telegramtest.NewServer(t, "local-secret-token")
	server.Push(json.RawMessage(`{"update_id":10,"message_reaction":{"date":123,"chat":{"id":-1},"user":{"id":7},"message_id":1,"old_reaction":[],"new_reaction":[{"type":"emoji","emoji":"👍"},{"type":"emoji","emoji":"❤"}]}}`))
	core := newRecordingCore()
	second := make(chan struct{})
	var calls atomic.Int64
	core.deliver = func(ctx context.Context, e *msg.Event, raw []byte) (bool, error) {
		if calls.Add(1) == 2 {
			close(second)
			<-ctx.Done()
			return false, ctx.Err()
		}
		return true, core.SaveCursor(ctx, raw)
	}
	a := New()
	request := startRequest(t, server)
	running, err := a.Start(context.Background(), request, core)
	if err != nil {
		t.Fatal(err)
	}
	registerStop(t, running)
	waitSaved(t, second)
	if err := running.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	core.mu.Lock()
	saved := core.cursors[0]
	core.mu.Unlock()
	if saved.Offset != 0 {
		t.Fatalf("reaction update completed before its final event: offset=%d", saved.Offset)
	}
	request.Cursor, err = json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	restarted := newRecordingCore()
	running, err = a.Start(context.Background(), request, restarted)
	if err != nil {
		t.Fatal(err)
	}
	registerStop(t, running)
	for range 2 {
		waitSaved(t, restarted.saved)
	}
	if err := running.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	restarted.mu.Lock()
	defer restarted.mu.Unlock()
	if len(restarted.events) != 2 || restarted.events[0].Payload.(*msg.ReactionAddedPayload).Reaction != "👍" || restarted.events[1].Payload.(*msg.ReactionAddedPayload).Reaction != "❤" || restarted.offsets[0] != 0 || restarted.offsets[1] != 11 {
		t.Fatalf("restarted reactions=%v cursors=%v", restarted.events, restarted.offsets)
	}
}

func TestMalformedUpdateDoesNotBlockFollowingMessage(t *testing.T) {
	for name, bad := range map[string]string{
		"entity_range":     `{"update_id":10,"message":{"message_id":1,"date":123,"chat":{"id":9,"type":"private"},"text":"private-update-text","entities":[{"type":"bold","offset":0,"length":50}]}}`,
		"entity_surrogate": `{"update_id":10,"message":{"message_id":1,"date":123,"chat":{"id":9,"type":"private"},"text":"😀","entities":[{"type":"bold","offset":1,"length":1}]}}`,
		"message_shape":    `{"update_id":10,"message":"private-update-text"}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := telegramtest.NewServer(t, "local-secret-token")
			server.Push(json.RawMessage(bad), json.RawMessage(`{"update_id":11,"message":{"message_id":2,"date":124,"chat":{"id":9,"type":"private"},"text":"following"}}`))
			core := newRecordingCore()
			running, err := New().Start(context.Background(), startRequest(t, server), core)
			if err != nil {
				t.Fatal(err)
			}
			registerStop(t, running)
			for range 2 {
				waitSaved(t, core.saved)
			}
			if err := running.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}
			core.mu.Lock()
			defer core.mu.Unlock()
			if len(core.events) != 1 || core.events[0].SourceEventID != "11" || !reflect.DeepEqual(core.offsets, []int64{11, 12}) {
				t.Fatalf("events=%v offsets=%v", core.events, core.offsets)
			}
		})
	}
}

func TestPlatformPermissions(t *testing.T) {
	server := telegramtest.NewServer(t, "local-secret-token")
	server.SetChatAdministrators(-100, http.StatusOK,
		json.RawMessage(`{"status":"creator","user":{"id":7,"is_bot":false,"first_name":"Owner"},"is_anonymous":false}`),
		json.RawMessage(`{"status":"administrator","user":{"id":8,"is_bot":false,"first_name":"Admin"},"is_anonymous":false}`))
	core := newRecordingCore()
	running, err := New().Start(context.Background(), startRequest(t, server), core)
	if err != nil {
		t.Fatal(err)
	}
	registerStop(t, running)
	message := func(id, chatID, userID int64, chatType string) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"date":123,"chat":{"id":%d,"type":%q},"from":{"id":%d},"text":"hello"}}`, id, id, chatID, chatType, userID))
	}
	checkEvent := func(index int, sourceID, actorID string, permissions []string) {
		t.Helper()
		core.mu.Lock()
		defer core.mu.Unlock()
		if len(core.events) <= index {
			t.Fatalf("events=%d, want event %d", len(core.events), index)
		}
		event := core.events[index]
		if event.SourceEventID != sourceID || event.Actor == nil || event.Actor.ExternalUserID != actorID || !reflect.DeepEqual(event.Actor.Permissions, permissions) {
			t.Fatalf("event %d: source=%q actor=%v, want source=%q actor=%q permissions=%v", index, event.SourceEventID, event.Actor, sourceID, actorID, permissions)
		}
	}
	checkCalls := func(want int) {
		t.Helper()
		if got := server.Calls("getChatAdministrators"); got != want {
			t.Fatalf("getChatAdministrators calls=%d, want %d", got, want)
		}
	}
	server.Push(message(1, -100, 7, "supergroup"), message(2, -100, 8, "supergroup"), message(3, -100, 9, "supergroup"))
	for range 3 {
		waitSaved(t, core.saved)
	}
	checkEvent(0, "1", "7", []string{"telegram.chat_owner"})
	checkEvent(1, "2", "8", []string{"telegram.chat_admin"})
	checkEvent(2, "3", "9", nil)
	checkCalls(1)

	server.Push(message(4, 7, 7, "private"))
	waitSaved(t, core.saved)
	checkEvent(3, "4", "7", nil)
	checkCalls(1)

	// 成员变更本身和随后消息都使用刷新后的平台列表。
	server.SetChatAdministrators(-100, http.StatusOK, json.RawMessage(`{"status":"administrator","user":{"id":7}}`))
	server.Push(json.RawMessage(`{"update_id":5,"chat_member":{"date":123,"chat":{"id":-100,"type":"supergroup"},"from":{"id":7},"old_chat_member":{"status":"left","user":{"id":9}},"new_chat_member":{"status":"member","user":{"id":9}}}}`),
		message(6, -100, 7, "supergroup"))
	for range 2 {
		waitSaved(t, core.saved)
	}
	checkEvent(4, "5", "7", []string{"telegram.chat_admin"})
	checkEvent(5, "6", "7", []string{"telegram.chat_admin"})
	checkCalls(2)

	// 不产生成员进出事件的权限调整也作废缓存；获取失败仍交付消息。
	server.SetChatAdministrators(-100, http.StatusInternalServerError)
	server.Push(json.RawMessage(`{"update_id":7,"chat_member":{"date":123,"chat":{"id":-100,"type":"supergroup"},"from":{"id":7},"old_chat_member":{"status":"member","user":{"id":9}},"new_chat_member":{"status":"administrator","user":{"id":9}}}}`),
		message(8, -100, 7, "supergroup"))
	for range 2 {
		waitSaved(t, core.saved)
	}
	checkEvent(6, "8", "7", nil)
	checkCalls(3)
	if err := running.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	core.mu.Lock()
	defer core.mu.Unlock()
	if len(core.events) != 7 || !reflect.DeepEqual(core.offsets, []int64{2, 3, 4, 5, 6, 7, 8, 9}) {
		t.Fatalf("events=%d offsets=%v", len(core.events), core.offsets)
	}
}

func TestInvalidUpdateIDReportsFailure(t *testing.T) {
	var polls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var result any
		switch path.Base(r.URL.Path) {
		case "getMe":
			result = map[string]any{"id": 42, "username": "example_bot"}
		case "getWebhookInfo":
			result = map[string]any{"url": ""}
		case "getUpdates":
			polls.Add(1)
			result = []any{map[string]any{"update_id": "invalid"}}
		default:
			t.Errorf("unexpected method: %s", path.Base(r.URL.Path))
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	raw, err := json.Marshal(map[string]string{"token": "local-secret-token", "api_base_url": server.URL})
	if err != nil {
		t.Fatal(err)
	}
	core := newRecordingCore()
	running, err := New().Start(context.Background(), adapter.StartRequest{InstanceID: "invalid-id", ConfigJSON: raw}, core)
	if err != nil {
		t.Fatal(err)
	}
	registerStop(t, running)
	select {
	case failure := <-core.failures:
		if failure.Message != "telegram update has no valid update_id" {
			t.Fatalf("failure: %v", failure)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("invalid ID was not reported")
	}
	if err := running.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	core.mu.Lock()
	defer core.mu.Unlock()
	if polls.Load() != 1 || len(core.offsets) != 0 || len(core.events) != 0 {
		t.Fatalf("invalid ID retried or guessed cursor: polls=%d offsets=%v", polls.Load(), core.offsets)
	}
}
