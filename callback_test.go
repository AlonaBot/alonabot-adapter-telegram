package telegram

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/AlonaBot/AlonaBot/adapters/telegram/telegramtest"
	pb "github.com/AlonaBot/AlonaBot/contracts/pb/alonabotv1"
)

func TestCallbackDelivery(t *testing.T) {
	server := telegramtest.NewServer(t, "local-secret-token")
	server.Push(
		json.RawMessage(`{"update_id":10,"callback_query":{"id":"callback-1","from":{"id":7},"data":"opaque-token","message":{"message_id":42,"chat":{"id":-100,"type":"supergroup"},"message_thread_id":8,"is_topic_message":true}}}`),
		json.RawMessage(`{"update_id":11,"callback_query":{"id":"inline-2","from":{"id":8},"data":"inline-token","inline_message_id":"inline"}}`),
	)
	core := newRecordingCore()
	core.deliver = func(ctx context.Context, event *pb.Event, raw []byte) (bool, error) {
		requests := server.Requests()
		if len(requests) != 1 || requests[0].Method != "answerCallbackQuery" || requests[0].Params["callback_query_id"] != "callback-1" {
			t.Errorf("callback delivered before answer: %v", requests)
		}
		core.mu.Lock()
		core.events = append(core.events, event)
		core.mu.Unlock()
		return true, core.SaveCursor(ctx, raw)
	}
	running, err := New().Start(t.Context(), startRequest(t, server), core)
	if err != nil {
		t.Fatal(err)
	}
	registerStop(t, running)
	for range 2 {
		waitSaved(t, core.saved)
	}
	if err := running.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	core.mu.Lock()
	defer core.mu.Unlock()
	if len(core.events) != 1 || !reflect.DeepEqual(core.offsets, []int64{11, 12}) {
		t.Fatalf("events=%v offsets=%v", core.events, core.offsets)
	}
	event := core.events[0]
	payload := event.GetEventPayload().GetButtonClicked()
	if event.EventType != "button.clicked" || event.GetSourceEventId() != "10" || event.Target.ChatId != "-100" || event.Target.GetThreadId() != "8" || event.Actor.GetExternalUserId() != "7" || event.Actor.GetAdapterInstanceId() != "test-instance" || payload.GetValue() != "opaque-token" || payload.GetCallbackQueryId() != "callback-1" || payload.GetOriginPlatformMessageId() != "42" || payload.GetActionId() != "" {
		t.Fatalf("callback event: %v", event)
	}
	requests := server.Requests()
	if len(requests) != 2 || requests[1].Method != "answerCallbackQuery" || requests[1].Params["callback_query_id"] != "inline-2" {
		t.Fatalf("inline callback answer: %v", requests)
	}
}
