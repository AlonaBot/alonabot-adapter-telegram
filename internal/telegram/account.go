package telegram

import (
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/AlonaBot/AlonaBot/sdk/adapter"
)

//go:embed account.html
var accountHTML []byte

func (a *telegramAdapter) accountRoutes() []adapter.HTTPRoute {
	return []adapter.HTTPRoute{
		{Method: http.MethodGet, Path: "/account/", Page: &adapter.PageDescriptor{Title: "Bot token", Purpose: adapter.PageInstanceConfig}, Handler: http.HandlerFunc(serveAccountPage)},
		{Method: http.MethodPost, Path: "/account/check", Handler: http.HandlerFunc(a.checkAccount)},
	}
}

func serveAccountPage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(accountHTML)
}

func (a *telegramAdapter) checkAccount(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Token      string `json:"token"`
		APIBaseURL string `json:"api_base_url"`
		ProxyMode  string `json:"proxy_mode"`
		Proxy      string `json:"proxy"`
		InstanceID string `json:"instance_id"`
	}
	w.Header().Set("Content-Type", "application/json")
	fail := func(status int, message string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
	}
	const maxAccountRequestBytes = 4 << 10
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAccountRequestBytes))
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			fail(http.StatusRequestEntityTooLarge, "account check request is too large")
		} else {
			fail(http.StatusBadRequest, "cannot read account check request")
		}
		return
	}
	if err := json.Unmarshal(body, &input); err != nil {
		fail(http.StatusBadRequest, "invalid account check JSON")
		return
	}
	if strings.TrimSpace(input.Token) == "" {
		fail(http.StatusBadRequest, "token is required")
		return
	}
	if input.APIBaseURL == "" {
		input.APIBaseURL = defaultAPIBaseURL
	}
	if input.ProxyMode == "" {
		input.ProxyMode = proxyModeNone
	}
	if input.ProxyMode == proxyModeCustom && input.Proxy == "" {
		a.mu.Lock()
		if i := a.instances[input.InstanceID]; input.InstanceID != "" && i != nil && i.cancel != nil {
			input.Proxy = i.client.proxy
		}
		a.mu.Unlock()
		if input.Proxy == "" {
			fail(http.StatusConflict, "enter a custom proxy to verify this token")
			return
		}
	}
	client, err := newBotClient(input.Token, input.APIBaseURL, input.ProxyMode, input.Proxy)
	if err != nil {
		fail(http.StatusBadRequest, err.Error())
		return
	}
	defer client.http.CloseIdleConnections()
	me, err := client.getMe(r.Context())
	if err != nil {
		if api, ok := errors.AsType[*apiError](err); ok {
			// WebUI 将 HTTP 401 视为管理会话失效；保留上游状态和描述在响应正文。
			fail(http.StatusUnprocessableEntity, api.Error())
		} else {
			// 传输错误可能包含环境代理的地址，页面只接收安全文案。
			fail(http.StatusBadGateway, "telegram getMe request failed")
		}
		return
	}
	_ = json.NewEncoder(w).Encode(me)
}
