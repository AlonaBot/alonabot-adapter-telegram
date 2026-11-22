package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"unicode/utf8"

	"github.com/AlonaBot/AlonaBot/sdk/adapter"
	"github.com/AlonaBot/AlonaBot/sdk/errcode"
	msg "github.com/AlonaBot/AlonaBot/sdk/message"
	"github.com/AlonaBot/alonabot-adapter-telegram/action"
)

var _ adapter.ActionExecutor = (*instance)(nil)

func actionDeclarations() []adapter.ActionDeclaration {
	const chat = adapter.ActionScopeChat
	return []adapter.ActionDeclaration{
		adapter.Action[action.BanChatMemberParams, bool]("telegram.ban_chat_member", 1, chat),
		adapter.Action[action.UnbanChatMemberParams, bool]("telegram.unban_chat_member", 1, chat),
		adapter.Action[action.RestrictChatMemberParams, bool]("telegram.restrict_chat_member", 1, chat),
		adapter.Action[action.PinChatMessageParams, bool]("telegram.pin_chat_message", 1, chat),
		adapter.Action[action.UnpinChatMessageParams, bool]("telegram.unpin_chat_message", 1, chat),
		adapter.Action[action.SetMessageReactionParams, bool]("telegram.set_message_reaction", 1, chat),
		adapter.Action[action.Empty, actionChat]("telegram.get_chat", 1, chat),
		adapter.Action[action.GetChatMemberParams, actionMember]("telegram.get_chat_member", 1, chat),
		adapter.Action[action.Empty, uint64]("telegram.get_chat_member_count", 1, chat),
		adapter.Action[action.Empty, []actionMember]("telegram.get_chat_administrators", 1, chat),
		adapter.Action[action.SetChatTitleParams, bool]("telegram.set_chat_title", 1, chat),
		adapter.Action[action.Empty, bool]("telegram.leave_chat", 1, chat),
		adapter.Action[action.SendChatActionParams, bool]("telegram.send_chat_action", 1, chat),
		adapter.Action[action.SendMessageDraftParams, bool]("telegram.send_message_draft", 1, chat),
	}
}

var actionMethods = map[string]string{
	"telegram.ban_chat_member": "banChatMember", "telegram.unban_chat_member": "unbanChatMember",
	"telegram.restrict_chat_member": "restrictChatMember", "telegram.pin_chat_message": "pinChatMessage",
	"telegram.unpin_chat_message": "unpinChatMessage", "telegram.set_message_reaction": "setMessageReaction",
	"telegram.get_chat": "getChat", "telegram.get_chat_member": "getChatMember",
	"telegram.get_chat_member_count": "getChatMemberCount", "telegram.get_chat_administrators": "getChatAdministrators",
	"telegram.set_chat_title": "setChatTitle", "telegram.leave_chat": "leaveChat",
	"telegram.send_chat_action": "sendChatAction", "telegram.send_message_draft": "sendMessageDraft",
}

// Action 从已准入 Target 取得聊天和话题，并将十进制字符串 ID 转成平台整数。
// 返回平台 result JSON 原文，公开带类型入口负责把结果 ID 读为十进制字符串。
func (i *instance) Action(ctx context.Context, to, name string, version uint32, raw json.RawMessage) (json.RawMessage, *errcode.Error) {
	method, ok := actionMethods[name]
	if !ok || version != 1 {
		return nil, planError(errcode.Unsupported, "unsupported telegram action or version")
	}
	_, chat, thread, err := msg.ParseSID(to)
	chatID, valid := decimalID(chat)
	if err != nil || !valid {
		return nil, invalidPlan("telegram action requires a decimal chat_id")
	}
	var params map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&params); err != nil || params == nil {
		return nil, invalidPlan("action params must be an object")
	}
	params["chat_id"] = chatID
	switch method {
	case "banChatMember", "unbanChatMember", "restrictChatMember", "getChatMember":
		id, ok := params["user_id"].(string)
		userID, valid := decimalID(id)
		if !ok || !valid || userID <= 0 {
			return nil, invalidPlan("user_id must be a positive decimal string")
		}
		params["user_id"] = userID
	case "pinChatMessage", "unpinChatMessage", "setMessageReaction":
		messageID, _ := params["message_id"].(string)
		if messageID == "" && method == "unpinChatMessage" {
			break
		}
		id, valid := decimalID(messageID)
		if !valid || id <= 0 {
			return nil, invalidPlan("message_id must be a positive decimal string")
		}
		params["message_id"] = id
	case "setChatTitle":
		title, ok := params["title"].(string)
		if !ok || utf8.RuneCountInString(title) < 1 || utf8.RuneCountInString(title) > 128 {
			return nil, invalidPlan("title must contain 1 to 128 characters")
		}
	case "sendChatAction":
		state, _ := params["action"].(string)
		switch state {
		case "typing", "upload_photo", "record_video", "upload_video", "record_voice", "upload_voice", "upload_document", "choose_sticker", "find_location", "record_video_note", "upload_video_note":
		default:
			return nil, invalidPlan("unsupported chat action")
		}
	case "sendMessageDraft":
		if chatID <= 0 {
			return nil, planError(errcode.Unsupported, "drafts require a private chat")
		}
		draft, _ := params["draft_id"].(string)
		id, valid := decimalID(draft)
		if !valid || id == 0 {
			return nil, invalidPlan("draft_id must be a non-zero decimal string")
		}
		params["draft_id"] = id
		text, ok := params["text"].(string)
		if !ok || utf16Length(text) > maxTextUTF16 {
			return nil, invalidPlan("draft text exceeds Telegram limits")
		}
		if mode, _ := params["parse_mode"].(string); mode != "" && mode != "MarkdownV2" && mode != "Markdown" && mode != "HTML" {
			return nil, invalidPlan("unsupported draft parse_mode")
		}
	}
	if method == "sendChatAction" || method == "sendMessageDraft" {
		if thread != "" {
			id, valid := decimalID(thread)
			if !valid || id <= 0 {
				return nil, invalidPlan("thread_id must be a positive decimal string")
			}
			params["message_thread_id"] = id
		}
	}
	if method == "setMessageReaction" {
		reactions, _ := params["reaction"].([]any)
		if len(reactions) > 1 {
			return nil, invalidPlan("bots can set at most one reaction")
		}
		for _, item := range reactions {
			r, ok := item.(map[string]any)
			if !ok {
				return nil, invalidPlan("invalid reaction")
			}
			if r["type"] != "emoji" && r["type"] != "custom_emoji" {
				return nil, invalidPlan("bots support emoji and custom_emoji reactions")
			}
			field := "emoji"
			if r["type"] == "custom_emoji" {
				field = "custom_emoji_id"
			}
			if value, _ := r[field].(string); value == "" {
				return nil, invalidPlan("reaction requires " + field)
			}
		}
		if reactions == nil {
			params["reaction"] = []any{}
		}
	}
	// 文本提及中的用户 ID 也遵守插件侧字符串、Bot API 整数的边界。
	if entities, ok := params["entities"].([]any); ok {
		for _, item := range entities {
			entity, ok := item.(map[string]any)
			if !ok {
				return nil, invalidPlan("invalid draft entity")
			}
			if user, ok := entity["user"].(map[string]any); ok {
				value, _ := user["id"].(string)
				id, valid := decimalID(value)
				if !valid || id <= 0 {
					return nil, invalidPlan("entity user id must be a positive decimal string")
				}
				user["id"] = id
			}
		}
	}
	var result json.RawMessage
	if err := i.client.call(ctx, method, params, &result); err != nil {
		return result, i.platformSendError(err)
	}
	switch method {
	case "banChatMember", "unbanChatMember", "restrictChatMember":
		i.administratorsMu.Lock()
		delete(i.administrators, chatID)
		i.administratorsMu.Unlock()
	}
	return result, nil
}

// 结果 Schema 遵循 Bot API 的原始数字 ID，并允许平台后续扩展字段。
type actionChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}
type actionUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
}
type actionMember struct {
	Status string     `json:"status"`
	User   actionUser `json:"user"`
}
