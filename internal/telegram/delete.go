package telegram

import (
	"context"
	"errors"

	"github.com/AlonaBot/AlonaBot/sdk/adapter"
	"github.com/AlonaBot/AlonaBot/sdk/errcode"
	msg "github.com/AlonaBot/AlonaBot/sdk/message"
)

var _ adapter.Deleter = (*instance)(nil)

// Delete 完整预检后按输入顺序每批撤回至多 100 条；失败后保留后续未尝试状态。
func (i *instance) Delete(ctx context.Context, to string, platformMessageIDs []string) (*msg.DeleteResult, *errcode.Error) {
	result := &msg.DeleteResult{Session: to, AdapterInstanceID: i.id}
	for _, id := range platformMessageIDs {
		result.Items = append(result.Items, msg.DeleteItemResult{PlatformMessageID: id, Status: msg.PartNotAttempted})
	}
	_, chat, _, parseErr := msg.ParseSID(to)
	chatID, ok := decimalID(chat)
	if parseErr != nil || !ok {
		return result, invalidPlan("telegram target requires a decimal chat_id")
	}
	ids := make([]int64, len(platformMessageIDs))
	for n, id := range platformMessageIDs {
		parsed, valid := decimalID(id)
		if !valid || parsed <= 0 {
			return result, invalidPlan("telegram delete requires positive decimal message IDs")
		}
		ids[n] = parsed
	}
	for start := 0; start < len(ids); start += 100 {
		end := min(start+100, len(ids))
		var accepted bool
		err := i.client.call(ctx, "deleteMessages", map[string]any{"chat_id": chatID, "message_ids": ids[start:end]}, &accepted)
		if err == nil && !accepted {
			err = errors.New("telegram deleteMessages returned false")
		}
		status := msg.PartAccepted
		var safe *errcode.Error
		if err != nil {
			status = msg.PartUnknown
			var api *apiError
			if errors.As(err, &api) {
				status = msg.PartRejected
			}
			safe = i.platformSendError(err)
		}
		for n := start; n < end; n++ {
			result.Items[n].Status, result.Items[n].Error = status, safe
		}
		if safe != nil {
			return result, safe
		}
	}
	return result, nil
}
