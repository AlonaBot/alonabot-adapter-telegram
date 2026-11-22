package action

import (
	"context"
	"time"

	"github.com/AlonaBot/AlonaBot/sdk"
	platform "github.com/AlonaBot/AlonaBot/sdk/action"
	"github.com/AlonaBot/AlonaBot/sdk/errcode"
	msg "github.com/AlonaBot/AlonaBot/sdk/message"
)

// Typing 立即发送 typing 并每 4 秒刷新，ctx 结束或平台错误时退出并返回原因。
func Typing(ctx context.Context, to string, opts ...sdk.SendOption) error {
	client := platform.NewClient(ctx)
	ticker := time.NewTicker(4 * time.Second)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := SendChatAction(client, to, SendChatActionParams{Action: "typing"}, opts...); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// StreamDraft 随输入更新同一 DraftID；关闭 updates 后将最后内容经 sdk.Send 及出站 Hook 正式发送。
// 未收到输入或 ctx 取消时保留原因，已经发布的草稿由 Telegram 到期清理。
func StreamDraft(ctx context.Context, to, draftID string, updates <-chan DraftUpdate, opts ...sdk.SendOption) (*msg.SendResult, error) {
	client := platform.NewClient(ctx)
	var last DraftUpdate
	received := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case update, ok := <-updates:
			if !ok {
				if !received {
					return nil, errcode.InvalidArgument.New("draft stream has no updates")
				}
				return sdk.Send(ctx, to, msg.TextSegment{Text: last.Text, Format: last.Format}, opts...)
			}
			params := SendMessageDraftParams{DraftID: draftID, Text: update.Text}
			if update.Format != 0 && update.Format != msg.FormatPlain && update.Format != msg.FormatMarkdown {
				return nil, errcode.Unsupported.New("unsupported draft text format")
			}
			if update.Format == msg.FormatMarkdown {
				params.ParseMode = "MarkdownV2"
			}
			if _, err := SendMessageDraft(client, to, params, opts...); err != nil {
				return nil, err
			}
			last, received = update, true
		}
	}
}
