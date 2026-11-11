package telegram

import (
	"context"
	"errors"

	pb "github.com/AlonaBot/AlonaBot/contracts/pb/alonabotv1"
	"github.com/AlonaBot/AlonaBot/sdk/adapter"
	"google.golang.org/protobuf/proto"
)

const editCapability = "message.edit"

var _ adapter.Editor = (*instance)(nil)

// Edit 用 editMessageText 修改一条文本消息，完整保留单项回执与平台错误。
// 失败时不重试或发送新消息，平台拒绝为 REJECTED，结果不确定为 UNKNOWN。
func (i *instance) Edit(ctx context.Context, target *pb.Target, platformMessageID string, chain *pb.MessageChain) (*pb.SendResult, *pb.ErrorInfo) {
	// 1. 编辑始终只有一个物理操作；预检拒绝也保留这一项。
	part := &pb.PartResult{Status: pb.PartStatus_PART_STATUS_REJECTED}
	for index := range chain.GetSegments() {
		part.SegmentIndexes = append(part.SegmentIndexes, uint32(index))
	}
	result := &pb.SendResult{Target: target, Status: pb.SendStatus_SEND_STATUS_REJECTED, PartResults: []*pb.PartResult{part}}
	params, safe := buildEditParams(target, platformMessageID, chain)
	if safe != nil {
		part.Error = safe
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
		result.Status, part.Status = pb.SendStatus_SEND_STATUS_UNKNOWN, pb.PartStatus_PART_STATUS_UNKNOWN
		var api *apiError
		if errors.As(err, &api) {
			result.Status, part.Status = pb.SendStatus_SEND_STATUS_REJECTED, pb.PartStatus_PART_STATUS_REJECTED
		}
		safe = i.platformSendError(err)
		part.Error = safe
		return result, safe
	}
	// 3. 确认成功保留原 ID；返回消息的 date 属于原发送时间，不作为编辑接受时间。
	result.Status, part.Status = pb.SendStatus_SEND_STATUS_ACCEPTED, pb.PartStatus_PART_STATUS_ACCEPTED
	result.PlatformMessageIds = []string{platformMessageID}
	result.PrimaryPlatformMessageId = proto.String(platformMessageID)
	part.PlatformMessageIds = []string{platformMessageID}
	return result, nil
}

func buildEditParams(target *pb.Target, platformMessageID string, chain *pb.MessageChain) (map[string]any, *pb.ErrorInfo) {
	// 1. 平台标识按发送的规则转换；编辑由 chat_id/message_id 定位，不传 thread 参数。
	chatID, ok := decimalID(target.GetChatId())
	if !ok {
		return nil, invalidPlan("telegram target requires a decimal chat_id")
	}
	messageID, ok := decimalID(platformMessageID)
	if !ok {
		return nil, invalidPlan("telegram edit requires a decimal message ID")
	}
	// 2. 仅接受一段文本和可选按钮，复用与发送相同的渲染与平台约束。
	var text *pb.Segment
	var markup map[string]any
	for _, segment := range chain.GetSegments() {
		switch data := segment.GetData().(type) {
		case *pb.Segment_Text:
			if text != nil {
				return nil, invalidPlan("edit chain requires exactly one text segment")
			}
			text = segment
		case *pb.Segment_Buttons:
			if markup != nil {
				return nil, invalidPlan("edit chain allows at most one buttons segment")
			}
			var safe *pb.ErrorInfo
			markup, safe = renderButtons(data.Buttons)
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
	params, safe := renderText([]*pb.Segment{text})
	if safe != nil {
		return nil, safe
	}
	params["chat_id"], params["message_id"] = chatID, messageID
	if markup != nil {
		params["reply_markup"] = markup
	}
	return params, nil
}
