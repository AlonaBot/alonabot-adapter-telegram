package telegram

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AlonaBot/AlonaBot/sdk/adapter"
	"github.com/AlonaBot/alonabot-adapter-telegram/telegramtest"
)

func TestAccountCheckRoute(t *testing.T) {
	bot := telegramtest.NewServer(t, "valid-token")
	bot.SetMe(42, "example_bot")
	mux := http.NewServeMux()
	for _, route := range New().Descriptor().HTTP {
		if route.Auth != 0 {
			t.Fatalf("route %s must require a management session", route.Path)
		}
		if route.Path == "/account/" && (route.Page == nil || route.Page.Purpose != adapter.PageInstanceConfig || route.Page.Title != "Bot token") {
			t.Fatalf("unexpected account page descriptor: %+v", route.Page)
		}
		mux.Handle(route.Method+" "+route.Path, route.Handler)
	}
	for _, tc := range []struct {
		token  string
		status int
		want   string
	}{
		{"valid-token", http.StatusOK, `{"id":42,"username":"example_bot","first_name":"example_bot"}`},
		{"rejected-token", http.StatusUnprocessableEntity, `{"error":"telegram API status 401: Unauthorized"}`},
	} {
		body, err := json.Marshal(map[string]string{"token": tc.token, "api_base_url": bot.URL()})
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/account/check", strings.NewReader(string(body))))
		if response.Code != tc.status || strings.TrimSpace(response.Body.String()) != tc.want {
			t.Errorf("account check = %d %s; want %d %s", response.Code, response.Body, tc.status, tc.want)
		}
	}
}
