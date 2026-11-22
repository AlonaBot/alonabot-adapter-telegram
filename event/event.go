// Package event 定义 Telegram 服务消息对应的 v1 扩展事件载荷。
package event

// 扩展事件名用于 Plugin.AdapterEvent；载荷中的数字 ID 均为十进制字符串。
const (
	ForumTopicCreatedName  = "telegram.forum_topic_created"
	ForumTopicEditedName   = "telegram.forum_topic_edited"
	ForumTopicClosedName   = "telegram.forum_topic_closed"
	ForumTopicReopenedName = "telegram.forum_topic_reopened"
	MessagePinnedName      = "telegram.message_pinned"
	ChatTitleChangedName   = "telegram.chat_title_changed"
	ChatMigratedName       = "telegram.chat_migrated"
)

// ForumTopicCreated 保存新话题名称和图标；话题 ID 在事件会话中。
type ForumTopicCreated struct {
	Name              string `json:"name"`
	IconColor         int    `json:"icon_color"`
	IconCustomEmojiID string `json:"icon_custom_emoji_id,omitempty"`
}

// ForumTopicEdited 的指针字段区分未修改与清空自定义图标。
type ForumTopicEdited struct {
	Name              *string `json:"name,omitempty"`
	IconCustomEmojiID *string `json:"icon_custom_emoji_id,omitempty"`
}

// ForumTopicClosed 标记关闭的话题。
type ForumTopicClosed struct{}

// ForumTopicReopened 标记重新开放的话题。
type ForumTopicReopened struct{}

// MessagePinned 保存被置顶消息的平台 ID。
type MessagePinned struct {
	MessageID string `json:"message_id"`
}

// ChatTitleChanged 保存平台报告的新标题。
type ChatTitleChanged struct {
	NewChatTitle string `json:"new_chat_title"`
}

// ChatMigrated 仅从新超级群的 migrate_from_chat_id 生成一次。
type ChatMigrated struct {
	MigrateFromChatID string `json:"migrate_from_chat_id"`
}
