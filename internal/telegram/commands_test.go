package telegram

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/AlonaBot/AlonaBot/sdk/adapter"
	"github.com/AlonaBot/AlonaBot/sdk/errcode"
	"github.com/AlonaBot/alonabot-adapter-telegram/telegramtest"
)

// TestCommandMenu 固定 Bot API 100/101 边界、完整覆盖与 scope/语言隔离和失败后原菜单保留。
func TestCommandMenu(t *testing.T) {
	server := telegramtest.NewServer(t, "local-secret-token")
	running := startCompletion(t, server, newRecordingCore())
	if config := running.CommandMenuConfig(); !config.AutoSyncCommands || !config.ClearCommandsOnDisable {
		t.Fatalf("defaults=%+v", config)
	}
	scopes := []adapter.CommandMenuScope{{Type: "chat", ChatID: "-9"}, {Type: "chat_administrators", ChatID: "-9"}, {Type: "chat_member", ChatID: "-9", UserID: "7"}}
	for _, scope := range scopes {
		for _, language := range []string{"", "en", "zh"} {
			want := []adapter.CommandMenuEntry{{Name: "help", Description: "Help " + scope.Type + language}}
			if safe := running.SetCommandMenu(t.Context(), adapter.CommandMenu{Scope: scope, LanguageCode: language, Commands: want}); safe != nil {
				t.Fatal(safe)
			}
			got, safe := running.GetCommandMenu(t.Context(), scope, language)
			if safe != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("menu=%+v error=%v", got, safe)
			}
		}
	}
	scope := scopes[0]
	commands := make([]adapter.CommandMenuEntry, 100)
	for n := range commands {
		commands[n] = adapter.CommandMenuEntry{Name: fmt.Sprintf("cmd%d", n), Description: "Description"}
	}
	menu := adapter.CommandMenu{Scope: scope, LanguageCode: "en", Commands: commands}
	if safe := running.SetCommandMenu(t.Context(), menu); safe != nil {
		t.Fatal(safe)
	}
	before := len(server.MenuRequests())
	menu.Commands = append(menu.Commands, adapter.CommandMenuEntry{Name: "overflow", Description: "Overflow"})
	if safe := running.SetCommandMenu(t.Context(), menu); safe == nil || safe.Code != errcode.InvalidArgument {
		t.Fatalf("101=%v", safe)
	}
	if len(server.MenuRequests()) != before {
		t.Fatal("101 commands reached platform")
	}
	got, safe := running.GetCommandMenu(t.Context(), scope, "en")
	if safe != nil || !reflect.DeepEqual(got, commands) {
		t.Fatalf("last success lost: %v %v", got, safe)
	}
	nth := server.Calls("setMyCommands") + 1
	server.FailMethod("setMyCommands", nth, 400, "invalid commands from platform")
	if safe := running.SetCommandMenu(t.Context(), adapter.CommandMenu{Scope: scope, LanguageCode: "en"}); safe == nil || safe.Code != errcode.InvalidArgument {
		t.Fatalf("platform error=%v", safe)
	}
	got, safe = running.GetCommandMenu(t.Context(), scope, "en")
	if safe != nil || !reflect.DeepEqual(got, commands) {
		t.Fatal("failed set replaced menu")
	}
	server.Drop("setMyCommands", server.Calls("setMyCommands")+1)
	if safe := running.SetCommandMenu(t.Context(), adapter.CommandMenu{Scope: scope, LanguageCode: "en"}); safe == nil {
		t.Fatal("unknown set was accepted")
	}
	if safe := running.DeleteCommandMenu(t.Context(), scope, "en"); safe != nil {
		t.Fatal(safe)
	}
	got, safe = running.GetCommandMenu(t.Context(), scope, "en")
	if safe != nil || len(got) != 0 {
		t.Fatalf("delete=%+v %v", got, safe)
	}
	other, safe := running.GetCommandMenu(t.Context(), scope, "zh")
	if safe != nil || len(other) != 1 {
		t.Fatalf("language isolation=%+v %v", other, safe)
	}
	other, safe = running.GetCommandMenu(t.Context(), scopes[1], "en")
	if safe != nil || len(other) != 1 {
		t.Fatalf("scope isolation=%+v %v", other, safe)
	}
	if safe := running.SetCommandMenu(t.Context(), adapter.CommandMenu{Scope: adapter.CommandMenuScope{Type: "default"}}); safe != nil {
		t.Fatal(safe)
	}
	before = len(server.MenuRequests())
	for _, bad := range []adapter.CommandMenu{
		{Scope: scope, LanguageCode: "en-US"},
		{Scope: adapter.CommandMenuScope{Type: "chat_member", ChatID: "-9"}},
		{Scope: scope, Commands: []adapter.CommandMenuEntry{{Name: "Bad", Description: "Description"}}},
		{Scope: scope, Commands: []adapter.CommandMenuEntry{{Name: "good", Description: ""}}},
		{Scope: scope, Commands: []adapter.CommandMenuEntry{{Name: "good", Description: "a"}, {Name: "good", Description: "b"}}},
	} {
		if safe := running.SetCommandMenu(t.Context(), bad); safe == nil {
			t.Fatalf("invalid menu=%+v", bad)
		}
	}
	if len(server.MenuRequests()) != before {
		t.Fatal("invalid menu reached platform")
	}
}

func TestManualCommandConfig(t *testing.T) {
	server := telegramtest.NewServer(t, "local-secret-token")
	request := startRequest(t, server)
	config := map[string]any{"token": "local-secret-token", "api_base_url": server.URL(), "auto_sync_commands": false, "clear_commands_on_disable": false}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	request.ConfigJSON = raw
	running, err := New().Start(t.Context(), request, newRecordingCore())
	if err != nil {
		t.Fatal(err)
	}
	registerStop(t, running)
	if got := running.(adapter.CommandMenuController).CommandMenuConfig(); got.AutoSyncCommands || got.ClearCommandsOnDisable {
		t.Fatalf("manual config=%+v", got)
	}
}
