package telegram

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	msg "github.com/AlonaBot/AlonaBot/sdk/message"
)

type telegramUser struct {
	ID           int64   `json:"id"`
	Username     string  `json:"username"`
	FirstName    string  `json:"first_name"`
	LastName     string  `json:"last_name"`
	LanguageCode *string `json:"language_code"`
}

type telegramChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type telegramEntity struct {
	Type     string        `json:"type"`
	Offset   int           `json:"offset"`
	Length   int           `json:"length"`
	URL      *string       `json:"url"`
	Language *string       `json:"language"`
	User     *telegramUser `json:"user"`
}

type telegramMessage struct {
	ID              int64            `json:"message_id"`
	Date            int64            `json:"date"`
	Chat            telegramChat     `json:"chat"`
	From            *telegramUser    `json:"from"`
	SenderChat      *telegramChat    `json:"sender_chat"`
	Reply           *telegramMessage `json:"reply_to_message"`
	ThreadID        *int64           `json:"message_thread_id"`
	IsTopicMessage  bool             `json:"is_topic_message"`
	Text            *string          `json:"text"`
	Caption         *string          `json:"caption"`
	Entities        []telegramEntity `json:"entities"`
	CaptionEntities []telegramEntity `json:"caption_entities"`
	MediaGroupID    *string          `json:"media_group_id"`
	Photo           []telegramFile   `json:"photo"`
	Video           *telegramFile    `json:"video"`
	Document        *telegramFile    `json:"document"`
	Animation       *telegramFile    `json:"animation"`
	Audio           *telegramFile    `json:"audio"`
	Voice           *telegramFile    `json:"voice"`
}

type chatMember struct {
	Status   string       `json:"status"`
	IsMember bool         `json:"is_member"`
	User     telegramUser `json:"user"`
}

func (m chatMember) present() bool {
	switch m.Status {
	case "creator", "administrator", "member":
		return true
	case "restricted":
		return m.IsMember
	default:
		return false
	}
}

type membershipUpdate struct {
	Chat telegramChat  `json:"chat"`
	From *telegramUser `json:"from"`
	Date int64         `json:"date"`
	Old  chatMember    `json:"old_chat_member"`
	New  chatMember    `json:"new_chat_member"`
}

type telegramReaction struct {
	Type          string `json:"type"`
	Emoji         string `json:"emoji"`
	CustomEmojiID string `json:"custom_emoji_id"`
}

func (r telegramReaction) text() string {
	switch r.Type {
	case "emoji":
		return r.Emoji
	case "custom_emoji":
		return r.CustomEmojiID
	default:
		return r.Type
	}
}

type reactionUpdate struct {
	Chat      telegramChat       `json:"chat"`
	User      *telegramUser      `json:"user"`
	Date      int64              `json:"date"`
	MessageID int64              `json:"message_id"`
	Old       []telegramReaction `json:"old_reaction"`
	New       []telegramReaction `json:"new_reaction"`
}

func (i *instance) normalize(raw json.RawMessage) ([]*msg.Event, error) {
	var update struct {
		ID                int64             `json:"update_id"`
		Message           *telegramMessage  `json:"message"`
		ChannelPost       *telegramMessage  `json:"channel_post"`
		EditedMessage     *telegramMessage  `json:"edited_message"`
		EditedChannelPost *telegramMessage  `json:"edited_channel_post"`
		MyChatMember      *membershipUpdate `json:"my_chat_member"`
		ChatMember        *membershipUpdate `json:"chat_member"`
		Reaction          *reactionUpdate   `json:"message_reaction"`
	}
	if err := json.Unmarshal(raw, &update); err != nil {
		return nil, err
	}
	// 1. 一个 Telegram Message 保持一个事件，不聚合相册。
	received := update.Message
	if received == nil {
		received = update.ChannelPost
	}
	edited := false
	if received == nil {
		received = update.EditedMessage
		edited = received != nil
	}
	if received == nil {
		received = update.EditedChannelPost
		edited = received != nil
	}
	if received != nil {
		event, err := i.messageEvent(received, edited)
		if err != nil {
			return nil, err
		}
		i.source(event, update.ID, raw)
		return []*msg.Event{event}, nil
	}
	// 2. 成员事件的 Actor 来自操作人，payload 来自被操作成员。
	member := update.MyChatMember
	if member == nil {
		member = update.ChatMember
	}
	if member != nil {
		if member.Old.present() == member.New.present() {
			return nil, nil
		}
		event := i.baseEvent(member.Chat, member.Date, member.From)
		payload := &msg.MemberPayload{MemberExternalUserID: strconv.FormatInt(member.New.User.ID, 10)}
		if member.New.present() {
			event.Type = "member.joined"
			event.Payload = payload
		} else {
			event.Type = "member.left"
			event.Payload = payload
		}
		i.source(event, update.ID, raw)
		return []*msg.Event{event}, nil
	}
	// 3. 同一个 reaction update 可以新增多项，每项形成一个 reaction.added。
	var events []*msg.Event
	if reaction := update.Reaction; reaction != nil {
		old := make(map[telegramReaction]bool, len(reaction.Old))
		for _, r := range reaction.Old {
			old[r] = true
		}
		for _, r := range reaction.New {
			if old[r] {
				continue
			}
			event := i.baseEvent(reaction.Chat, reaction.Date, reaction.User)
			event.Type = "reaction.added"
			event.Payload = &msg.ReactionAddedPayload{PlatformMessageID: strconv.FormatInt(reaction.MessageID, 10), Reaction: r.text()}
			i.source(event, update.ID, raw)
			events = append(events, event)
		}
	}
	return events, nil
}

func (i *instance) baseEvent(chat telegramChat, date int64, user *telegramUser) *msg.Event {
	var occurred time.Time
	if date != 0 {
		occurred = time.Unix(date, 0)
	}
	var chatType msg.ChatType
	switch chat.Type {
	case "private":
		chatType = msg.ChatPrivate
	case groupChat, supergroupChat:
		chatType = msg.ChatGroup
	case "channel":
		chatType = msg.ChatChannel
	}
	return &msg.Event{AdapterID: AdapterID, AdapterInstanceID: i.id, AccountID: i.account.ID, Session: msg.SID(AdapterID, strconv.FormatInt(chat.ID, 10), ""), ChatType: chatType, OccurredAt: occurred, Actor: i.actor(user)}
}

func (*instance) source(event *msg.Event, id int64, raw json.RawMessage) {
	event.SourceEventID = strconv.FormatInt(id, 10)
	event.PlatformPayload = raw
}

func (i *instance) actor(user *telegramUser) *msg.Actor {
	if user == nil {
		return nil
	}
	name := user.Username
	if name == "" {
		name = strings.TrimSpace(user.FirstName + " " + user.LastName)
	}
	actor := &msg.Actor{ExternalUserID: strconv.FormatInt(user.ID, 10), AdapterInstanceID: i.id}
	if name != "" {
		actor.DisplayName = name
	}
	if user.LanguageCode != nil {
		actor.Locale = localeCase(*user.LanguageCode)
	}
	return actor
}

// localeCase 只规范化 BCP 47 的大小写；扩展和 private-use 子标签始终小写。
func localeCase(tag string) string {
	parts := strings.Split(strings.ToLower(tag), "-")
	extension := len(parts[0]) == 1
	for n := 1; n < len(parts); n++ {
		part := parts[n]
		if len(part) == 1 {
			extension = true
		}
		if extension {
			continue
		}
		if len(part) == 4 {
			parts[n] = strings.ToUpper(part[:1]) + part[1:]
		} else if len(part) == 2 {
			parts[n] = strings.ToUpper(part)
		}
	}
	return strings.Join(parts, "-")
}

func (i *instance) messageEvent(received *telegramMessage, edited bool) (*msg.Event, error) {
	event := i.baseEvent(received.Chat, received.Date, received.From)
	if received.SenderChat != nil {
		event.Actor = nil
	}
	if received.IsTopicMessage && received.ThreadID != nil {
		event.Session = msg.SID(AdapterID, strconv.FormatInt(received.Chat.ID, 10), strconv.FormatInt(*received.ThreadID, 10))
	}
	chain := msg.Chain{}
	text, entities := received.Text, received.Entities
	if text == nil {
		text, entities = received.Caption, received.CaptionEntities
	}
	toMe := received.Chat.Type == "private" || (received.Reply != nil && received.Reply.From != nil && received.Reply.From.ID == i.botID)
	if text != nil {
		segment := msg.TextSegment{Text: *text}
		for _, entity := range entities {
			fragment, err := entityText(*text, entity.Offset, entity.Length)
			if err != nil {
				return nil, err
			}
			if entity.Type == "mention" && i.account.Username != "" && strings.EqualFold(fragment, "@"+i.account.Username) {
				toMe = true
			}
			if entity.Type == "text_mention" && entity.User != nil && entity.User.ID == i.botID {
				toMe = true
			}
			mapped := mapEntity(entity)
			if mapped != nil {
				segment.Entities = append(segment.Entities, *mapped)
			}
		}
		chain = msg.Chain{segment}
	}
	if media := received.mediaSegment(); media != nil {
		// caption 随媒体片段交付，避免同时形成重复的文本回复。
		if received.Text == nil {
			chain = nil
		}
		chain = append(chain, media)
	}
	platformID := strconv.FormatInt(received.ID, 10)
	event.Message = &msg.PlatformMessage{PlatformMessageID: platformID, Chain: chain, MediaGroupID: deref(received.MediaGroupID)}
	if edited {
		event.Type = "message.edited"
		event.Payload = &msg.EditedPayload{PlatformMessageID: platformID, Chain: chain}
	} else {
		event.Type = "message.created"
		event.Payload = &msg.CreatedPayload{PlatformMessageID: platformID, Chain: chain, ToMe: toMe}
	}
	return event, nil
}

// entityText 将 UTF-16 区间定位到完整字符边界；切分代理对的外部输入返回错误。
func entityText(text string, offset, length int) (string, error) {
	units := utf16.Encode([]rune(text))
	if offset < 0 || length < 0 || offset > len(units) || length > len(units)-offset {
		return "", fmt.Errorf("telegram entity range %d:%d exceeds text", offset, length)
	}
	end := offset + length
	if (offset < len(units) && units[offset] >= 0xDC00 && units[offset] <= 0xDFFF) || (end < len(units) && units[end] >= 0xDC00 && units[end] <= 0xDFFF) {
		return "", fmt.Errorf("telegram entity range %d:%d splits a UTF-16 character", offset, length)
	}
	return string(utf16.Decode(units[offset:end])), nil
}

var entityTypes = map[string]msg.EntityType{"bold": msg.EntityBold, "italic": msg.EntityItalic, "underline": msg.EntityUnderline, "strikethrough": msg.EntityStrikethrough, "spoiler": msg.EntitySpoiler, "code": msg.EntityCode, "pre": msg.EntityPre, "text_link": msg.EntityTextLink, "text_mention": msg.EntityTextMention}

func mapEntity(entity telegramEntity) *msg.TextEntity {
	kind, ok := entityTypes[entity.Type]
	// mention、bot_command 等保持普通文本，平台特有元数据仍在原始 update 中。
	if !ok {
		return nil
	}
	result := &msg.TextEntity{Type: kind, OffsetUTF16: uint32(entity.Offset), LengthUTF16: uint32(entity.Length), URL: deref(entity.URL), Language: deref(entity.Language)}
	if entity.User != nil {
		result.ExternalUserID = strconv.FormatInt(entity.User.ID, 10)
	}
	return result
}

func deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}
