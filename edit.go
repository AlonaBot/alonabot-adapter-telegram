package telegram

import (
	"context"
	"errors"

	"github.com/AlonaBot/AlonaBot/sdk/adapter"
	"github.com/AlonaBot/AlonaBot/sdk/errcode"
	msg "github.com/AlonaBot/AlonaBot/sdk/message"
)

const editCapability = "message.edit"

var _ adapter.Editor = (*instance)(nil)

// Edit 用 editMessageText 修改一条文本消息，完整保留单项回执与平台错误。
// 失败时不重试或发送新消息，平台拒绝为 REJECTED，结果不确定为 UNKNOWN。
func (i *instance) Edit(ctx context.Context, to string, platformMessageID string, chain msg.Chain) (*msg.SendResult, *errcode.Error) {
	// 1. 编辑始终只有一个物理操作；预检拒绝也保留这一项。
	part := msg.PartResult{Status: msg.PartRejected}
	for index := range chain {
		part.SegmentIndexes = append(part.SegmentIndexes, uint32(index))
	}
	result := &msg.SendResult{Session: to, AdapterInstanceID: i.id, Status: msg.SendRejected, PartResults: []msg.PartResult{part}}
	partResult := &result.PartResults[0]
	params, safe := buildEditParams(to, platformMessageID, chain)
	if safe != nil {
		partResult.Error = safe
		return result, safe
	}
	used := map[string]bool{editCapability: true, "message.markdown": params["parse_mode"] == markdownMode, "message.buttons": params["reply_markup"] != nil}
	for _, capability := range i.Capabilities() {
		if used[capability.Name] {
			result.CapabilitiesUsed = append(result.CapabilitiesUsed, capability)
		}
	}
	// 2. 提交唯一一次请求，沿用发送的明确拒绝和未知结果分类。
	var message sentMessage
	err := i.client.call(ctx, "editMessageText", params, &message)
	if err == nil && message.ID == nil {
		err = errors.New("telegram message has no message_id")
	}
	if err != nil {
		result.Status, partResult.Status = msg.SendUnknown, msg.PartUnknown
		var api *apiError
		if errors.As(err, &api) {
			result.Status, partResult.Status = msg.SendRejected, msg.PartRejected
		}
		safe = i.platformSendError(err)
		partResult.Error = safe
		return result, safe
	}
	// 3. 确认成功保留原 ID；返回消息的 date 属于原发送时间，不作为编辑接受时间。
	result.Status, partResult.Status = msg.SendAccepted, msg.PartAccepted
	result.PlatformMessageIDs = []string{platformMessageID}
	result.PrimaryPlatformMessageID = platformMessageID
	partResult.PlatformMessageIDs = []string{platformMessageID}
	return result, nil
}

func buildEditParams(to string, platformMessageID string, chain msg.Chain) (map[string]any, *errcode.Error) {
	// 1. 编辑由 chat_id/message_id 定位，不传 thread 参数。
	_, chat, _, parseErr := msg.ParseSID(to)
	chatID, ok := decimalID(chat)
	if parseErr != nil || !ok {
		return nil, invalidPlan("telegram target requires a decimal chat_id")
	}
	messageID, ok := decimalID(platformMessageID)
	if !ok {
		return nil, invalidPlan("telegram edit requires a decimal message ID")
	}
	// 2. 仅接受一段文本与可选按钮，复用发送渲染。
	var text msg.Segment
	var markup map[string]any
	for _, segment := range chain {
		switch data := segment.(type) {
		case msg.TextSegment:
			if text != nil {
				return nil, invalidPlan("edit chain requires exactly one text segment")
			}
			text = data
		case msg.ButtonsSegment:
			if markup != nil {
				return nil, invalidPlan("edit chain allows at most one buttons segment")
			}
			var safe *errcode.Error
			markup, safe = renderButtons(data)
			if safe != nil {
				return nil, safe
			}
		default:
			return nil, invalidPlan("edit chain allows only text and buttons segments")
		}
	}
	if text == nil {
		return nil, invalidPlan("edit chain requires exactly one text segment")
	}
	params, safe := renderText(msg.Chain{text})
	if safe != nil {
		return nil, safe
	}
	params["chat_id"], params["message_id"] = chatID, messageID
	if markup != nil {
		params["reply_markup"] = markup
	}
	return params, nil
}
