package telegram

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/AlonaBot/AlonaBot/sdk/adapter"
	msg "github.com/AlonaBot/AlonaBot/sdk/message"
	"github.com/AlonaBot/alonabot-adapter-telegram/event"
)

func eventDeclarations() []adapter.EventDeclaration {
	return []adapter.EventDeclaration{
		adapter.Event[event.ForumTopicCreated](event.ForumTopicCreatedName, 1),
		adapter.Event[event.ForumTopicEdited](event.ForumTopicEditedName, 1),
		adapter.Event[event.ForumTopicClosed](event.ForumTopicClosedName, 1),
		adapter.Event[event.ForumTopicReopened](event.ForumTopicReopenedName, 1),
		adapter.Event[event.MessagePinned](event.MessagePinnedName, 1),
		adapter.Event[event.ChatTitleChanged](event.ChatTitleChangedName, 1),
		adapter.Event[event.ChatMigrated](event.ChatMigratedName, 1),
	}
}

// Bot API Message 的服务字段；位置、投票、giveaway 等普通内容保留空 Chain 与原始载荷。
const serviceFields = `new_chat_members left_chat_member chat_owner_left chat_owner_changed new_chat_title new_chat_photo
 delete_chat_photo group_chat_created supergroup_chat_created channel_chat_created message_auto_delete_timer_changed
 migrate_to_chat_id migrate_from_chat_id pinned_message successful_payment refunded_payment users_shared chat_shared gift unique_gift
 gift_upgrade_sent connected_website write_access_allowed passport_data proximity_alert_triggered boost_added chat_background_set
 checklist_tasks_done checklist_tasks_added community_chat_added community_chat_joined community_chat_removed direct_message_price_changed
 forum_topic_created forum_topic_edited forum_topic_closed forum_topic_reopened general_forum_topic_hidden general_forum_topic_unhidden
 giveaway_created giveaway_completed managed_bot_created paid_message_price_changed paid_messages_waived poll_option_added poll_option_deleted
 suggested_post_approved suggested_post_approval_failed suggested_post_declined suggested_post_paid suggested_post_refunded ton_wallet_transfer
 video_chat_scheduled video_chat_started video_chat_ended video_chat_participants_invited web_app_data`

func (i *instance) serviceEvent(received *telegramMessage, raw json.RawMessage) (*msg.Event, bool, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, false, err
	}
	name := ""
	for _, field := range strings.Fields(serviceFields) {
		if value, ok := fields[field]; ok && string(value) != "null" {
			name = field
			break
		}
	}
	if name == "" {
		return nil, false, nil
	}
	thread := ""
	if received.ThreadID != nil {
		thread = strconv.FormatInt(*received.ThreadID, 10)
	}
	var payload any
	switch name {
	case "forum_topic_created":
		p := event.ForumTopicCreated{}
		if err := json.Unmarshal(fields[name], &p); err != nil {
			return nil, true, err
		}
		payload = p
	case "forum_topic_edited":
		p := event.ForumTopicEdited{}
		if err := json.Unmarshal(fields[name], &p); err != nil {
			return nil, true, err
		}
		payload = p
	case "forum_topic_closed":
		payload = event.ForumTopicClosed{}
	case "forum_topic_reopened":
		payload = event.ForumTopicReopened{}
	case "pinned_message":
		var pinned struct {
			ID int64 `json:"message_id"`
		}
		if err := json.Unmarshal(fields[name], &pinned); err != nil {
			return nil, true, err
		}
		name = "message_pinned"
		payload = event.MessagePinned{MessageID: strconv.FormatInt(pinned.ID, 10)}
	case "new_chat_title":
		var title string
		if err := json.Unmarshal(fields[name], &title); err != nil {
			return nil, true, err
		}
		name = "chat_title_changed"
		payload = event.ChatTitleChanged{NewChatTitle: title}
	case "migrate_from_chat_id":
		var old int64
		if err := json.Unmarshal(fields[name], &old); err != nil {
			return nil, true, err
		}
		name = "chat_migrated"
		payload = event.ChatMigrated{MigrateFromChatID: strconv.FormatInt(old, 10)}
	default:
		return nil, true, nil
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, true, err
	}
	e := i.baseEvent(received.Chat, received.Date, received.From)
	if received.SenderChat != nil {
		e.Actor = nil
	}
	if thread != "" && received.IsTopicMessage {
		e.Session = msg.SID(AdapterID, strconv.FormatInt(received.Chat.ID, 10), thread)
	}
	e.Type = "telegram." + name
	e.Payload = &msg.AdapterEventPayload{Version: 1, PayloadJSON: string(encoded)}
	return e, true, nil
}
