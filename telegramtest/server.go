// Package telegramtest 提供通过真实 HTTP 调用的本地 Telegram Bot API 模拟服务。
//
// [NewServer] 创建由测试拥有的 [Server]，支持账号检查、webhook 冲突、long polling
// 与接收故障注入、getFile 和文件下载；调用记录可验证订阅范围和逐条游标。
package telegramtest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Server 保存 Bot API 的账号、待接收 update 和请求记录。
// 所有可变状态由 mu 保护；changed 在更新或关闭时唤醒等待中的 long poll。
type Server struct {
	t             testing.TB
	http          *httptest.Server
	token         string
	mu            sync.Mutex
	userID        int64
	username      string
	webhook       string
	updates       []queuedUpdate
	offsets       []int64
	allowed       [][]string
	subscription  []string
	failureStatus int
	failures      int
	changed       chan struct{}
	closed        bool
	files         map[string]platformFile
}

type queuedUpdate struct {
	id     int64
	raw    json.RawMessage
	fields map[string]json.RawMessage
}

// NewServer 创建测试服务，并在 t.Cleanup 时取消 long poll、关闭 HTTP 服务。
func NewServer(t testing.TB, token string) *Server {
	t.Helper()
	s := &Server{t: t, token: token, userID: 1, username: "test_bot", changed: make(chan struct{})}
	s.http = httptest.NewServer(http.HandlerFunc(s.serveHTTP))
	t.Cleanup(func() {
		s.mu.Lock()
		s.closed = true
		s.notify()
		s.mu.Unlock()
		s.http.Close()
	})
	return s
}

// URL 返回可直接配置为 api_base_url 的地址。
func (s *Server) URL() string { return s.http.URL }

// SetMe 设置 getMe 返回的机器人身份。
func (s *Server) SetMe(userID int64, username string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.userID = userID
	s.username = username
}

// SetWebhook 设置 getWebhookInfo 返回的 url。
func (s *Server) SetWebhook(url string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.webhook = url
}

// Push 复制并按 update_id 排序保存 update，唤醒 long poll；非法 JSON 使测试失败。
func (s *Server) Push(updates ...json.RawMessage) {
	s.t.Helper()
	pending := make([]queuedUpdate, 0, len(updates))
	for _, update := range updates {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(update, &fields); err != nil {
			s.t.Fatalf("decode update: %s", err)
		}
		var id int64
		rawID, ok := fields["update_id"]
		if !ok {
			s.t.Fatal("update_id is required")
		}
		if err := json.Unmarshal(rawID, &id); err != nil {
			s.t.Fatalf("decode update_id: %s", err)
		}
		pending = append(pending, queuedUpdate{id: id, raw: slices.Clone(update), fields: fields})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, update := range pending {
		// 新订阅只影响之后产生的 update，已排队的旧类型仍可返回。
		if len(s.subscription) > 0 {
			matched := false
			for _, name := range s.subscription {
				if _, ok := update.fields[name]; ok {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		s.updates = append(s.updates, update)
	}
	slices.SortStableFunc(s.updates, func(a, b queuedUpdate) int {
		if a.id < b.id {
			return -1
		}
		if a.id > b.id {
			return 1
		}
		return 0
	})
	s.notify()
}

// FailNext 令接下来的 n 次 getUpdates 返回给定 HTTP 状态与 Bot API 错误体。
func (s *Server) FailNext(status int, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failureStatus = status
	s.failures = n
	s.notify()
}

// Offsets 返回 getUpdates 的 offset 记录副本；未带 offset 记为 0。
func (s *Server) Offsets() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.offsets)
}

// AllowedUpdates 返回 getUpdates 的 allowed_updates 记录深副本。
func (s *Server) AllowedUpdates() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([][]string, len(s.allowed))
	for i, v := range s.allowed {
		result[i] = slices.Clone(v)
	}
	return result
}

// notify 仅在持有 mu 时调用；替换 channel 使下一轮只等待新变化。
func (s *Server) notify() { close(s.changed); s.changed = make(chan struct{}) }

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/file/bot"+s.token+"/") {
		s.download(w, r)
		return
	}
	prefix := "/bot" + s.token + "/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		s.respond(w, http.StatusUnauthorized, nil)
		return
	}
	method := strings.TrimPrefix(r.URL.Path, prefix)
	switch method {
	case "getMe":
		s.mu.Lock()
		result := map[string]any{"id": s.userID, "is_bot": true, "first_name": s.username, "username": s.username}
		s.mu.Unlock()
		s.respond(w, http.StatusOK, result)
	case "getWebhookInfo":
		s.mu.Lock()
		result := map[string]any{"url": s.webhook, "has_custom_certificate": false, "pending_update_count": 0}
		s.mu.Unlock()
		s.respond(w, http.StatusOK, result)
	case "getUpdates":
		s.getUpdates(w, r)
	case "getFile":
		s.getFile(w, r)
	default:
		s.respond(w, http.StatusNotFound, nil)
	}
}

func (s *Server) getUpdates(w http.ResponseWriter, r *http.Request) {
	// 1. 同时接受 Bot API 的 JSON 和 query/form 编码。
	var request struct {
		Offset  int64    `json:"offset"`
		Timeout int64    `json:"timeout"`
		Allowed []string `json:"allowed_updates"`
	}
	var err error
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		err = json.NewDecoder(r.Body).Decode(&request)
	} else {
		err = r.ParseForm()
		if err == nil && r.Form.Has("offset") {
			request.Offset, err = strconv.ParseInt(r.Form.Get("offset"), 10, 64)
		}
		if err == nil && r.Form.Has("timeout") {
			request.Timeout, err = strconv.ParseInt(r.Form.Get("timeout"), 10, 64)
		}
		if err == nil && r.Form.Has("allowed_updates") {
			err = json.Unmarshal([]byte(r.Form.Get("allowed_updates")), &request.Allowed)
		}
	}
	if err != nil || request.Timeout < 0 {
		s.respond(w, http.StatusBadRequest, nil)
		return
	}
	s.mu.Lock()
	s.offsets = append(s.offsets, request.Offset)
	s.allowed = append(s.allowed, slices.Clone(request.Allowed))
	if request.Allowed != nil {
		s.subscription = slices.Clone(request.Allowed)
	}
	s.mu.Unlock()
	timer := time.NewTimer(time.Duration(request.Timeout) * time.Second)
	defer timer.Stop()
	// 2. 有数据立即返回；无数据等更新、取消或 timeout，避免忙轮询。
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		if s.failures > 0 {
			s.failures--
			status := s.failureStatus
			s.mu.Unlock()
			s.respond(w, status, nil)
			return
		}
		// offset 确认此前的 update；后续归零也不会重新返回已确认数据。
		if request.Offset > 0 {
			s.updates = slices.DeleteFunc(s.updates, func(update queuedUpdate) bool { return update.id < request.Offset })
		}
		result := make([]json.RawMessage, 0)
		for _, update := range s.updates {
			if update.id >= request.Offset {
				result = append(result, update.raw)
			}
		}
		changed := s.changed
		s.mu.Unlock()
		// 已排队的 update 可包含旧订阅类型，供 Adapter 验证忽略并推进游标。
		if len(result) > 0 {
			s.respond(w, http.StatusOK, result)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-timer.C:
			s.respond(w, http.StatusOK, result)
			return
		case <-changed:
		}
	}
}

func (s *Server) respond(w http.ResponseWriter, status int, result any) {
	response := map[string]any{"ok": status == http.StatusOK}
	if status == http.StatusOK {
		response["result"] = result
	} else {
		response["error_code"] = status
		response["description"] = http.StatusText(status)
		if status == http.StatusTooManyRequests {
			response["parameters"] = map[string]int{"retry_after": 1}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		s.t.Errorf("write Bot API response: %s", err)
	}
}
