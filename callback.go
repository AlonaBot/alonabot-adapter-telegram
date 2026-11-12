package telegram

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"

	pb "github.com/AlonaBot/AlonaBot/contracts/pb/alonabotv1"
	"google.golang.org/protobuf/proto"
)

type telegramCallbackQuery struct {
	ID      string           `json:"id"`
	From    *telegramUser    `json:"from"`
	Message *telegramMessage `json:"message"`
	Data    string           `json:"data"`
}

// callback 先确认平台回调，再构造交给 Core 验证的点击事件。
func (i *instance) callback(ctx context.Context, queryJSON json.RawMessage, updateID int64, raw json.RawMessage) ([]*pb.Event, error) {
	var query telegramCallbackQuery
	if err := json.Unmarshal(queryJSON, &query); err != nil {
		return nil, err
	}
	var answered bool
	if err := i.client.call(ctx, "answerCallbackQuery", struct {
		ID string `json:"callback_query_id"`
	}{query.ID}, &answered); err != nil {
		slog.Warn("answer telegram callback failed", "adapter_instance_id", i.id, "error", i.client.safeError(err))
	}
	if query.Message == nil {
		slog.Info("skip telegram inline callback", "adapter_instance_id", i.id, "update_id", updateID)
		return nil, nil
	}
	event := i.baseEvent(query.Message.Chat, 0, query.From)
	event.OccurredAtUnixMs = i.now().UnixMilli()
	if query.Message.IsTopicMessage && query.Message.ThreadID != nil {
		event.Target.ThreadId = proto.String(strconv.FormatInt(*query.Message.ThreadID, 10))
	}
	event.EventType = "button.clicked"
	event.EventPayload = &pb.EventPayload{Kind: &pb.EventPayload_ButtonClicked{ButtonClicked: &pb.ButtonClickedPayload{
		Value: query.Data, CallbackQueryId: query.ID,
		OriginPlatformMessageId: proto.String(strconv.FormatInt(query.Message.ID, 10)),
	}}}
	i.source(event, updateID, raw)
	return []*pb.Event{event}, nil
}
