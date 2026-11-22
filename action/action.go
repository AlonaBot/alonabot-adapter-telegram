package action

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/AlonaBot/AlonaBot/sdk"
	platform "github.com/AlonaBot/AlonaBot/sdk/action"
	msg "github.com/AlonaBot/AlonaBot/sdk/message"
)

// Call 保留平台动作的完整结果 JSON 和错误；name 使用 telegram. 前缀，版本固定为 1。
func Call(c platform.Caller, to, name string, params any, opts ...sdk.SendOption) (json.RawMessage, error) {
	return c.CallAction(to, name, 1, params, opts...)
}

func call[R any](c platform.Caller, to, name string, params any, opts ...sdk.SendOption) (R, error) {
	var result R
	raw, err := Call(c, to, name, params, opts...)
	if len(raw) == 0 {
		return result, err
	}
	decodeErr := json.Unmarshal(raw, &result)
	return result, errors.Join(err, decodeErr)
}

// BanChatMember 封禁目标会话中的成员。
func BanChatMember(c platform.Caller, to string, params BanChatMemberParams, opts ...sdk.SendOption) (bool, error) {
	return call[bool](c, to, "telegram.ban_chat_member", params, opts...)
}

// UnbanChatMember 解除成员封禁。
func UnbanChatMember(c platform.Caller, to string, params UnbanChatMemberParams, opts ...sdk.SendOption) (bool, error) {
	return call[bool](c, to, "telegram.unban_chat_member", params, opts...)
}

// RestrictChatMember 设置超级群成员权限。
func RestrictChatMember(c platform.Caller, to string, params RestrictChatMemberParams, opts ...sdk.SendOption) (bool, error) {
	return call[bool](c, to, "telegram.restrict_chat_member", params, opts...)
}

// PinChatMessage 置顶消息句柄所指的消息。
func PinChatMessage(c platform.Caller, ref string, params PinChatMessageParams, opts ...sdk.SendOption) (bool, error) {
	to, id, options, err := messageTarget(ref, opts)
	if err != nil {
		return false, err
	}
	params.MessageID = id
	return call[bool](c, to, "telegram.pin_chat_message", params, options...)
}

// UnpinChatMessage 接受消息句柄；传会话字符串且 MessageID 为空时取消最近的置顶。
func UnpinChatMessage(c platform.Caller, ref string, params UnpinChatMessageParams, opts ...sdk.SendOption) (bool, error) {
	if !strings.Contains(ref, "/") {
		return call[bool](c, ref, "telegram.unpin_chat_message", params, opts...)
	}
	to, id, options, err := messageTarget(ref, opts)
	if err != nil {
		return false, err
	}
	params.MessageID = id
	return call[bool](c, to, "telegram.unpin_chat_message", params, options...)
}

// SetMessageReaction 设置或清除机器人的消息回应。
func SetMessageReaction(c platform.Caller, ref string, params SetMessageReactionParams, opts ...sdk.SendOption) (bool, error) {
	to, id, options, err := messageTarget(ref, opts)
	if err != nil {
		return false, err
	}
	params.MessageID = id
	return call[bool](c, to, "telegram.set_message_reaction", params, options...)
}

// 显式句柄绑定的实例随目标传递，避免多账号下用到另一机器人的消息。
func messageTarget(ref string, opts []sdk.SendOption) (string, string, []sdk.SendOption, error) {
	to, id, instanceID, err := msg.ParseRef(ref)
	if err != nil {
		return "", "", nil, err
	}
	if instanceID != "" {
		opts = append(opts, sdk.Via(instanceID))
	}
	return to, id, opts, nil
}

// GetChat 读取会话信息。
func GetChat(c platform.Caller, to string, opts ...sdk.SendOption) (Chat, error) {
	return call[Chat](c, to, "telegram.get_chat", Empty{}, opts...)
}

// GetChatMember 读取指定成员状态。
func GetChatMember(c platform.Caller, to string, params GetChatMemberParams, opts ...sdk.SendOption) (ChatMember, error) {
	return call[ChatMember](c, to, "telegram.get_chat_member", params, opts...)
}

// GetChatMemberCount 读取成员总数。
func GetChatMemberCount(c platform.Caller, to string, opts ...sdk.SendOption) (uint64, error) {
	return call[uint64](c, to, "telegram.get_chat_member_count", Empty{}, opts...)
}

// GetChatAdministrators 读取管理员列表。
func GetChatAdministrators(c platform.Caller, to string, opts ...sdk.SendOption) ([]ChatMember, error) {
	return call[[]ChatMember](c, to, "telegram.get_chat_administrators", Empty{}, opts...)
}

// SetChatTitle 修改会话标题。
func SetChatTitle(c platform.Caller, to string, params SetChatTitleParams, opts ...sdk.SendOption) (bool, error) {
	return call[bool](c, to, "telegram.set_chat_title", params, opts...)
}

// LeaveChat 让机器人退出目标会话。
func LeaveChat(c platform.Caller, to string, opts ...sdk.SendOption) (bool, error) {
	return call[bool](c, to, "telegram.leave_chat", Empty{}, opts...)
}

// SendChatAction 立即发布一次输入状态。
func SendChatAction(c platform.Caller, to string, params SendChatActionParams, opts ...sdk.SendOption) (bool, error) {
	return call[bool](c, to, "telegram.send_chat_action", params, opts...)
}

// SendMessageDraft 更新私聊临时草稿；最终内容应经普通 Send 发送。
func SendMessageDraft(c platform.Caller, to string, params SendMessageDraftParams, opts ...sdk.SendOption) (bool, error) {
	return call[bool](c, to, "telegram.send_message_draft", params, opts...)
}
