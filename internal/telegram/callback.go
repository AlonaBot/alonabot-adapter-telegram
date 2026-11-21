package telegram

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"

	msg "github.com/AlonaBot/AlonaBot/sdk/message"
)

type telegramCallbackQuery struct {
	ID      string           `json:"id"`
	From    *telegramUser    `json:"from"`
	Message *telegramMessage `json:"message"`
	Data    string           `json:"data"`
}

// callback 先确认平台回调，再构造交给 Core 验证的点击事件。
func (i *instance) callback(ctx context.Context, queryJSON json.RawMessage, updateID int64, raw json.RawMessage) ([]*msg.Event, error) {
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
	event.OccurredAt = i.now()
	if query.Message.IsTopicMessage && query.Message.ThreadID != nil {
		event.Session = msg.SID(AdapterID, strconv.FormatInt(query.Message.Chat.ID, 10), strconv.FormatInt(*query.Message.ThreadID, 10))
	}
	event.Type = "button.clicked"
	event.Payload = &msg.ButtonClickedPayload{
		Value: query.Data, CallbackQueryID: query.ID,
		OriginPlatformMessageID: strconv.FormatInt(query.Message.ID, 10),
	}
	i.source(event, updateID, raw)
	return []*msg.Event{event}, nil
}
