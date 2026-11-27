package telegram

import (
	"context"
	"errors"

	"github.com/AlonaBot/AlonaBot/sdk/adapter"
	"github.com/AlonaBot/AlonaBot/sdk/errcode"
	msg "github.com/AlonaBot/AlonaBot/sdk/message"
)

var _ adapter.MessageChecker = (*instance)(nil)

// CheckMessage 复用发送编排和纯校验，接受尚未签发 CoreToken 的按钮。
// 来源的可信 MIME 缺失时保留 unknown，全程不取件或调用平台。
func (*instance) CheckMessage(ctx context.Context, to string, chain msg.Chain) (msg.SupportStatus, *errcode.Error) {
	if err := ctx.Err(); err != nil {
		code := errcode.Cancelled
		if errors.Is(err, context.DeadlineExceeded) {
			code = errcode.DeadlineExceeded
		}
		return msg.SupportUnknown, planError(code, err.Error())
	}
	topology, safe := planTopology(to, chain)
	if safe != nil {
		return msg.SupportUnsupported, safe
	}
	for _, group := range topology.groups {
		content := topology.content[group.start:group.end]
		if isText(content[0]) {
			if _, safe := renderText(content); safe != nil {
				return msg.SupportUnsupported, safe
			}
			continue
		}
		for _, segment := range content {
			if _, safe := checkMedia(segment); safe != nil {
				return msg.SupportUnsupported, safe
			}
		}
	}
	if topology.unknown {
		return msg.SupportUnknown, nil
	}
	return msg.SupportSupported, nil
}
