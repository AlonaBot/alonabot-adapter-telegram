package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/AlonaBot/AlonaBot/adapters/telegram/perm"
	msg "github.com/AlonaBot/AlonaBot/sdk/message"
)

const (
	administratorCacheDuration = 10 * time.Minute // MESSAGE-MODEL §6.1 规定的固定缓存期限。
	groupChat                  = "group"
	supergroupChat             = "supergroup"
	ownerStatus                = "creator"
	administratorStatus        = "administrator"
)

type administratorCache struct {
	fetchedAt time.Time
	members   []chatMember
}

// addPlatformPermissions 在 normalize 成功后调用；缓存只由接收循环读写。
func (i *instance) addPlatformPermissions(ctx context.Context, raw json.RawMessage, events []*msg.Event) error {
	var update struct {
		Message       *telegramMessage       `json:"message"`
		EditedMessage *telegramMessage       `json:"edited_message"`
		MyChatMember  *membershipUpdate      `json:"my_chat_member"`
		ChatMember    *membershipUpdate      `json:"chat_member"`
		Reaction      *reactionUpdate        `json:"message_reaction"`
		CallbackQuery *telegramCallbackQuery `json:"callback_query"`
	}
	if err := json.Unmarshal(raw, &update); err != nil {
		return err
	}
	// 1. 成员变更先作废缓存，未产生标准事件的变更也生效。
	for _, member := range []*membershipUpdate{update.MyChatMember, update.ChatMember} {
		if member != nil {
			delete(i.administrators, member.Chat.ID)
		}
	}
	var chat telegramChat
	switch {
	case update.Message != nil:
		chat = update.Message.Chat
	case update.EditedMessage != nil:
		chat = update.EditedMessage.Chat
	case update.MyChatMember != nil:
		chat = update.MyChatMember.Chat
	case update.ChatMember != nil:
		chat = update.ChatMember.Chat
	case update.Reaction != nil:
		chat = update.Reaction.Chat
	case update.CallbackQuery != nil && update.CallbackQuery.Message != nil:
		chat = update.CallbackQuery.Message.Chat
	}
	if chat.Type != groupChat && chat.Type != supergroupChat {
		return nil
	}
	hasActor := false
	for _, event := range events {
		if event.Actor != nil {
			hasActor = true
			break
		}
	}
	if !hasActor {
		return nil
	}
	// 2. 同一 update 共用一次获取结果；失败只诊断，不缓存或阻止交付。
	cached, ok := i.administrators[chat.ID]
	if !ok || i.now().Sub(cached.fetchedAt) > administratorCacheDuration {
		var members []chatMember
		request := struct {
			ChatID int64 `json:"chat_id"`
		}{ChatID: chat.ID}
		err := i.client.call(ctx, "getChatAdministrators", request, &members)
		if err == nil && members == nil {
			err = errors.New("telegram chat administrators result is not an array")
		}
		if err != nil {
			slog.Warn("get telegram chat administrators failed", "adapter_instance_id", i.id, "chat_id", chat.ID, "error", i.client.safeError(err))
			return nil
		}
		cached = administratorCache{fetchedAt: i.now(), members: members}
		i.administrators[chat.ID] = cached
	}
	// 3. 只写入平台直接给出的权限，隐含关系留给 Core 展开。
	for _, event := range events {
		if event.Actor == nil {
			continue
		}
		for _, member := range cached.members {
			if event.Actor.ExternalUserID != strconv.FormatInt(member.User.ID, 10) {
				continue
			}
			switch member.Status {
			case ownerStatus:
				event.Actor.Permissions = []string{perm.ChatOwner}
			case administratorStatus:
				event.Actor.Permissions = []string{perm.ChatAdmin}
			}
			break
		}
	}
	return nil
}
