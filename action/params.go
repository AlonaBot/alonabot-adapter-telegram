// Package action 提供 Telegram v1 平台动作及输入状态、草稿助手。
// 所有 ID 使用十进制字符串；消息操作传入 message.Ref 句柄。
package action

import msg "github.com/AlonaBot/AlonaBot/sdk/message"

// Empty 用于只需目标会话的动作。
type Empty struct{}

// BanChatMemberParams 封禁成员；UntilDate 为 Unix 秒，零表示永久。
type BanChatMemberParams struct {
	UserID         string `json:"user_id"`
	UntilDate      int64  `json:"until_date,omitempty"`
	RevokeMessages bool   `json:"revoke_messages,omitempty"`
}

// UnbanChatMemberParams 解除成员封禁。
type UnbanChatMemberParams struct {
	UserID       string `json:"user_id"`
	OnlyIfBanned bool   `json:"only_if_banned,omitempty"`
}

// ChatPermissions 中 false 显式撤销对应权限。
type ChatPermissions struct {
	CanSendMessages       bool `json:"can_send_messages"`
	CanSendAudios         bool `json:"can_send_audios"`
	CanSendDocuments      bool `json:"can_send_documents"`
	CanSendPhotos         bool `json:"can_send_photos"`
	CanSendVideos         bool `json:"can_send_videos"`
	CanSendVideoNotes     bool `json:"can_send_video_notes"`
	CanSendVoiceNotes     bool `json:"can_send_voice_notes"`
	CanSendPolls          bool `json:"can_send_polls"`
	CanSendOtherMessages  bool `json:"can_send_other_messages"`
	CanAddWebPagePreviews bool `json:"can_add_web_page_previews"`
	CanChangeInfo         bool `json:"can_change_info"`
	CanInviteUsers        bool `json:"can_invite_users"`
	CanPinMessages        bool `json:"can_pin_messages"`
	CanManageTopics       bool `json:"can_manage_topics"`
	CanEditTag            bool `json:"can_edit_tag"`
}

// RestrictChatMemberParams 设置成员权限。
type RestrictChatMemberParams struct {
	UserID                        string          `json:"user_id"`
	Permissions                   ChatPermissions `json:"permissions"`
	UseIndependentChatPermissions bool            `json:"use_independent_chat_permissions,omitempty"`
	UntilDate                     int64           `json:"until_date,omitempty"`
}

// PinChatMessageParams 的 MessageID 由 PinChatMessage 从消息句柄填写。
type PinChatMessageParams struct {
	MessageID           string `json:"message_id"`
	DisableNotification bool   `json:"disable_notification,omitempty"`
}

// UnpinChatMessageParams 的空 MessageID 表示取消最近的置顶。
type UnpinChatMessageParams struct {
	MessageID string `json:"message_id,omitempty"`
}

// Reaction 表示 emoji 或 custom_emoji 回应。
type Reaction struct {
	Type          string `json:"type"`
	Emoji         string `json:"emoji,omitempty"`
	CustomEmojiID string `json:"custom_emoji_id,omitempty"`
}

// SetMessageReactionParams 的空 Reaction 清除机器人的回应。
type SetMessageReactionParams struct {
	MessageID string     `json:"message_id"`
	Reaction  []Reaction `json:"reaction"`
	IsBig     bool       `json:"is_big,omitempty"`
}

// GetChatMemberParams 指定查询成员。
type GetChatMemberParams struct {
	UserID string `json:"user_id"`
}

// SetChatTitleParams 指定完整新标题。
type SetChatTitleParams struct {
	Title string `json:"title"`
}

// SendChatActionParams 指定 typing、upload_photo 等平台输入状态。
type SendChatActionParams struct {
	Action string `json:"action"`
}

// SendMessageDraftParams 仅支持私聊，DraftID 为非零十进制字符串；空 Text 显示 Thinking 占位。
type SendMessageDraftParams struct {
	DraftID    string       `json:"draft_id"`
	Text       string       `json:"text"`
	ParseMode  string       `json:"parse_mode,omitempty"`
	Entities   []TextEntity `json:"entities,omitempty"`
	CanStop    bool         `json:"can_stop,omitempty"`
	KeepOnStop bool         `json:"keep_on_stop,omitempty"`
}

// TextEntity 使用 Bot API 的 UTF-16 offset/length；文本提及的 UserID 放在 User 中。
type TextEntity struct {
	Type          string `json:"type"`
	Offset        uint32 `json:"offset"`
	Length        uint32 `json:"length"`
	URL           string `json:"url,omitempty"`
	User          *User  `json:"user,omitempty"`
	Language      string `json:"language,omitempty"`
	CustomEmojiID string `json:"custom_emoji_id,omitempty"`
}

// Chat 保留常用会话字段；完整平台结果可通过 Call 获取。
type Chat struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Title     string `json:"title,omitempty"`
	Username  string `json:"username,omitempty"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
	IsForum   bool   `json:"is_forum,omitempty"`
}

// User 保存平台成员身份。
type User struct {
	ID        string `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name,omitempty"`
	Username  string `json:"username,omitempty"`
}

// ChatMember 保存成员状态；完整权限字段可通过 Call 获取。
type ChatMember struct {
	Status      string `json:"status"`
	User        User   `json:"user"`
	IsMember    bool   `json:"is_member,omitempty"`
	CustomTitle string `json:"custom_title,omitempty"`
	UntilDate   int64  `json:"until_date,omitempty"`
}

// DraftUpdate 更新临时草稿；输入关闭后 StreamDraft 经普通 Send 发送最后的 Text。
type DraftUpdate struct {
	Text   string
	Format msg.TextFormat
}
