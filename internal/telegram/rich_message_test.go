package telegram

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/AlonaBot/AlonaBot/sdk/adapter"
	msg "github.com/AlonaBot/AlonaBot/sdk/message"
	"github.com/AlonaBot/alonabot-adapter-telegram/telegramtest"
)

// TestRichMessageAlbumButtons 验证相册后的正文承载按钮，引用仅归首投递且回执保留原下标。
func TestRichMessageAlbumButtons(t *testing.T) {
	server := telegramtest.NewServer(t, "rich-message-test")
	config, err := json.Marshal(map[string]string{"token": "rich-message-test", "api_base_url": server.URL()})
	if err != nil {
		t.Fatal(err)
	}
	running, err := New().Start(context.Background(), adapter.StartRequest{InstanceID: "rich-instance", ConfigJSON: config}, newRecordingCore())
	if err != nil {
		t.Fatal(err)
	}
	registerStop(t, running)
	chain := msg.Chain{
		msg.Quote(msg.Ref("telegram:-123", "42", "")),
		msg.ImageURL("https://example.com/first.jpg"),
		msg.ImageURL("https://example.com/second.jpg"),
		msg.Text("Choose an option"),
		msg.Buttons([]msg.Button{{Label: "Continue", CoreToken: "issued-token"}}),
	}
	result, safe := running.Send(context.Background(), "telegram:-123", chain)
	if safe != nil || result.Status != msg.SendAccepted || len(result.PartResults) != 2 {
		t.Fatalf("send: result=%+v error=%v", result, safe)
	}
	requests := server.Requests()
	if len(requests) != 2 || requests[0].Method != "sendMediaGroup" || requests[1].Method != "sendMessage" {
		t.Fatalf("delivery order: %+v", requests)
	}
	if requests[0].Params["reply_markup"] != nil || requests[1].Params["reply_parameters"] != nil {
		t.Fatalf("reply or keyboard repeated: %+v", requests)
	}
	if requests[0].Params["reply_parameters"].(map[string]any)["message_id"] != float64(42) {
		t.Fatal("first delivery lost quote")
	}
	keyboard := requests[1].Params["reply_markup"].(map[string]any)["inline_keyboard"].([]any)
	button := keyboard[0].([]any)[0].(map[string]any)
	if requests[1].Params["text"] != "Choose an option" || button["text"] != "Continue" || button["callback_data"] != "issued-token" {
		t.Fatalf("last delivery: %+v", requests[1])
	}
	for index, want := range [][]uint32{{0, 1, 2}, {3, 4}} {
		part := result.PartResults[index]
		if part.Status != msg.PartAccepted || !reflect.DeepEqual(part.SegmentIndexes, want) {
			t.Fatalf("receipt %d: %+v", index, part)
		}
	}
	if !reflect.DeepEqual(result.PlatformMessageIDs, []string{"1", "2", "3"}) {
		t.Fatalf("message IDs: %v", result.PlatformMessageIDs)
	}
}
