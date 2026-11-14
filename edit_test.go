package telegram

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/AlonaBot/AlonaBot/adapters/telegram/telegramtest"
	"github.com/AlonaBot/AlonaBot/sdk/adapter"
	"github.com/AlonaBot/AlonaBot/sdk/errcode"
	msg "github.com/AlonaBot/AlonaBot/sdk/message"
)

// TestEditMessageText 验证真实实例的编辑参数、单项回执、平台拒绝与按钮渲染。
func TestEditMessageText(t *testing.T) {
	for _, scenario := range []string{"accepted", "not modified", "buttons"} {
		t.Run(scenario, func(t *testing.T) {
			server := telegramtest.NewServer(t, "local-secret-token")
			running, err := New().Start(context.Background(), startRequest(t, server), newRecordingCore())
			if err != nil {
				t.Fatal(err)
			}
			registerStop(t, running)
			chain := msg.Chain{msg.Text("updated")}
			indexes := []uint32{0}
			params := map[string]any{"chat_id": float64(-123), "message_id": float64(42), "text": "updated"}
			switch scenario {
			case "not modified":
				server.FailMethod("editMessageText", 1, http.StatusBadRequest, "Bad Request: message is not modified")
			case "buttons":
				chain = append(chain, msg.Buttons([]msg.Button{{Label: "Open", CoreToken: "core-token"}}))
				indexes = append(indexes, 1)
				params["reply_markup"] = map[string]any{"inline_keyboard": []any{[]any{map[string]any{"text": "Open", "callback_data": "core-token"}}}}
			}
			target := msg.SID(AdapterID, "-123", "99")
			result, safe := running.(adapter.Editor).Edit(context.Background(), target, "42", chain)
			requests := server.Requests()
			if len(requests) != 1 || requests[0].Method != "editMessageText" || !reflect.DeepEqual(requests[0].Params, params) || len(requests[0].Files) != 0 {
				t.Fatalf("edit requests: %v, want params %v", requests, params)
			}
			if result == nil || result.Session != target || result.AdapterInstanceID != "test-instance" || len(result.PartResults) != 1 {
				t.Fatalf("edit result: %v", result)
			}
			part := result.PartResults[0]
			if part.PartIndex != 0 || !reflect.DeepEqual(part.SegmentIndexes, indexes) {
				t.Fatalf("edit part: %v", part)
			}
			if scenario == "not modified" {
				if safe == nil || safe.Code != errcode.InvalidArgument || !strings.Contains(safe.Message, "Bad Request: message is not modified") || result.Status != msg.SendRejected || part.Status != msg.PartRejected || !reflect.DeepEqual(part.Error, safe) || len(result.PlatformMessageIDs) != 0 || result.PrimaryPlatformMessageID != "" || len(part.PlatformMessageIDs) != 0 {
					t.Fatalf("rejected edit: %v %v", result, safe)
				}
				return
			}
			if safe != nil || result.Status != msg.SendAccepted || part.Status != msg.PartAccepted || !reflect.DeepEqual(result.PlatformMessageIDs, []string{"42"}) || result.PrimaryPlatformMessageID != "42" || !reflect.DeepEqual(part.PlatformMessageIDs, []string{"42"}) || part.Error != nil {
				t.Fatalf("accepted edit: %v %v", result, safe)
			}
		})
	}
}
