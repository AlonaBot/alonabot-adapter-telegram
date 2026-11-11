package telegram

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/AlonaBot/AlonaBot/adapters/telegram/telegramtest"
	pb "github.com/AlonaBot/AlonaBot/contracts/pb/alonabotv1"
	"github.com/AlonaBot/AlonaBot/sdk/adapter"
	"google.golang.org/protobuf/proto"
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
			chain := &pb.MessageChain{Segments: []*pb.Segment{{Data: &pb.Segment_Text{Text: &pb.TextSegment{Text: "updated"}}}}}
			indexes := []uint32{0}
			params := map[string]any{"chat_id": float64(-123), "message_id": float64(42), "text": "updated"}
			switch scenario {
			case "not modified":
				server.FailMethod("editMessageText", 1, http.StatusBadRequest, "Bad Request: message is not modified")
			case "buttons":
				chain.Segments = append(chain.Segments, &pb.Segment{Data: &pb.Segment_Buttons{Buttons: &pb.ButtonsSegment{Rows: []*pb.ButtonRow{{Buttons: []*pb.Button{{Label: "Open", CoreToken: proto.String("core-token")}}}}}}})
				indexes = append(indexes, 1)
				params["reply_markup"] = map[string]any{"inline_keyboard": []any{[]any{map[string]any{"text": "Open", "callback_data": "core-token"}}}}
			}
			target := &pb.Target{AdapterId: AdapterID, AdapterInstanceId: "test-instance", AccountId: "42", ChatId: "-123", ThreadId: proto.String("99")}
			result, safe := running.(adapter.Editor).Edit(context.Background(), target, "42", chain)
			requests := server.Requests()
			if len(requests) != 1 || requests[0].Method != "editMessageText" || !reflect.DeepEqual(requests[0].Params, params) || len(requests[0].Files) != 0 {
				t.Fatalf("edit requests: %v, want params %v", requests, params)
			}
			if result == nil || !proto.Equal(result.Target, target) || len(result.PartResults) != 1 {
				t.Fatalf("edit result: %v", result)
			}
			part := result.PartResults[0]
			if part.PartIndex != 0 || !reflect.DeepEqual(part.SegmentIndexes, indexes) {
				t.Fatalf("edit part: %v", part)
			}
			if scenario == "not modified" {
				if safe == nil || safe.Code != pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT || !strings.Contains(safe.SafeMessage, "Bad Request: message is not modified") || result.Status != pb.SendStatus_SEND_STATUS_REJECTED || part.Status != pb.PartStatus_PART_STATUS_REJECTED || !proto.Equal(part.Error, safe) || len(result.PlatformMessageIds) != 0 || result.PrimaryPlatformMessageId != nil || len(part.PlatformMessageIds) != 0 {
					t.Fatalf("rejected edit: %v %v", result, safe)
				}
				return
			}
			if safe != nil || result.Status != pb.SendStatus_SEND_STATUS_ACCEPTED || part.Status != pb.PartStatus_PART_STATUS_ACCEPTED || !reflect.DeepEqual(result.PlatformMessageIds, []string{"42"}) || result.GetPrimaryPlatformMessageId() != "42" || !reflect.DeepEqual(part.PlatformMessageIds, []string{"42"}) || part.Error != nil {
				t.Fatalf("accepted edit: %v %v", result, safe)
			}
		})
	}
}
