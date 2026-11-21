package telegram

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/AlonaBot/AlonaBot/sdk/errcode"
	msg "github.com/AlonaBot/AlonaBot/sdk/message"
)

// Telegram Bot API 的发送文本、caption 和 callback_data 上限。
const (
	maxTextUTF16     = 4096
	maxCaptionUTF16  = 1024
	maxCallbackBytes = 64
	markdownMode     = "MarkdownV2"
)

func planError(code errcode.Code, message string) *errcode.Error {
	return &errcode.Error{Code: code, Message: message}
}

func invalidPlan(message string) *errcode.Error {
	return planError(errcode.InvalidArgument, message)
}

func decimalID(value string) (int64, bool) {
	id, err := strconv.ParseInt(value, 10, 64)
	return id, err == nil
}

func utf16Length(text string) int { return len(utf16.Encode([]rune(text))) }

func entityBoundary(text string, offset uint64) bool {
	var position uint64
	for _, r := range text {
		if position == offset {
			return true
		}
		position += uint64(utf16.RuneLen(r))
	}
	return position == offset
}

func renderEntity(text string, entity msg.TextEntity, base int) (map[string]any, *errcode.Error) {
	start, length := uint64(entity.OffsetUTF16), uint64(entity.LengthUTF16)
	end := start + length
	if length == 0 || end > uint64(utf16Length(text)) || !entityBoundary(text, start) || !entityBoundary(text, end) {
		return nil, invalidPlan("invalid UTF-16 entity range")
	}
	names := map[msg.EntityType]string{
		msg.EntityBold: "bold", msg.EntityItalic: "italic",
		msg.EntityUnderline: "underline", msg.EntityStrikethrough: "strikethrough",
		msg.EntitySpoiler: "spoiler", msg.EntityCode: "code", msg.EntityPre: "pre",
		msg.EntityTextLink: "text_link", msg.EntityTextMention: "text_mention",
	}
	name, ok := names[entity.Type]
	if !ok {
		return nil, planError(errcode.Unsupported, "unsupported text entity")
	}
	result := map[string]any{"type": name, "offset": base + int(start), "length": int(length)}
	switch entity.Type {
	case msg.EntityTextLink:
		if entity.URL == "" {
			return nil, invalidPlan("text_link requires url")
		}
		result["url"] = entity.URL
	case msg.EntityTextMention:
		if entity.ExternalUserID == "" {
			return nil, invalidPlan("text_mention requires external_user_id")
		}
		id, ok := decimalID(entity.ExternalUserID)
		if !ok {
			return nil, planError(errcode.Unsupported, "telegram mention requires a decimal user ID")
		}
		result["user"] = map[string]any{"id": id}
	case msg.EntityPre:
		if entity.Language != "" {
			result["language"] = entity.Language
		}
	}
	return result, nil
}

func escapeMarkdown(text string) string {
	var result strings.Builder
	for _, r := range text {
		if strings.ContainsRune("_*[]()~`>#+-=|{}.!\\", r) {
			result.WriteByte('\\')
		}
		result.WriteRune(r)
	}
	return result.String()
}

func renderText(segments msg.Chain) (map[string]any, *errcode.Error) {
	// 1. 确定整段格式，避免合并后把普通文字解释为 Markdown。
	markdown := false
	for _, segment := range segments {
		if text, ok := segment.(msg.TextSegment); ok && text.Format == msg.FormatMarkdown {
			markdown = true
		}
	}
	var output strings.Builder
	var entities []map[string]any
	units := 0
	for _, segment := range segments {
		if text, ok := segment.(msg.TextSegment); ok {
			switch text.Format {
			case msg.TextFormat(0), msg.FormatPlain:
			case msg.FormatMarkdown:
				if len(text.Entities) > 0 {
					return nil, invalidPlan("markdown cannot include entities")
				}
			default:
				return nil, planError(errcode.Unsupported, "unsupported text format")
			}
			for _, entity := range text.Entities {
				rendered, err := renderEntity(text.Text, entity, units)
				if err != nil {
					return nil, err
				}
				entities = append(entities, rendered)
			}
			if markdown && len(text.Entities) > 0 {
				return nil, planError(errcode.Unsupported, "markdown cannot be combined with explicit entities")
			}
			if markdown && text.Format != msg.FormatMarkdown {
				output.WriteString(escapeMarkdown(text.Text))
			} else {
				output.WriteString(text.Text)
			}
			units += utf16Length(text.Text)
			continue
		}
		mention, ok := segment.(msg.MentionSegment)
		if !ok {
			return nil, invalidPlan("missing text or mention")
		}
		id, ok := decimalID(mention.ExternalUserID)
		if !ok {
			return nil, planError(errcode.Unsupported, "telegram mention requires a decimal user ID")
		}
		display := mention.Display
		if mention.Display == "" {
			display = mention.ExternalUserID
		}
		if display == "" {
			return nil, invalidPlan("mention display must not be empty")
		}
		length := utf16Length(display)
		if markdown {
			fmt.Fprintf(&output, "[%s](tg://user?id=%d)", escapeMarkdown(display), id)
		} else {
			output.WriteString(display)
			entities = append(entities, map[string]any{"type": "text_mention", "offset": units, "length": length, "user": map[string]any{"id": id}})
		}
		units += length
	}
	// 2. 在产生请求前拒绝超限；保留原文字和实体，不截断。
	if units == 0 || units > maxTextUTF16 {
		return nil, invalidPlan("telegram text exceeds UTF-16 length limits")
	}
	result := map[string]any{"text": output.String()}
	if markdown {
		result["parse_mode"] = markdownMode
	} else if len(entities) > 0 {
		result["entities"] = entities
	}
	return result, nil
}

func renderButtons(buttons msg.ButtonsSegment) (map[string]any, *errcode.Error) {
	if len(buttons.Rows) == 0 {
		return nil, invalidPlan("buttons require rows")
	}
	rows := make([][]map[string]any, 0, len(buttons.Rows))
	for _, row := range buttons.Rows {
		if len(row) == 0 {
			return nil, invalidPlan("button row must not be empty")
		}
		rendered := make([]map[string]any, 0, len(row))
		for _, button := range row {
			if len(button.CoreToken) == 0 || len(button.CoreToken) > maxCallbackBytes {
				return nil, invalidPlan("button core_token must contain 1 to 64 bytes")
			}
			if button.Label == "" {
				return nil, invalidPlan("button label must not be empty")
			}
			rendered = append(rendered, map[string]any{"text": button.Label, "callback_data": button.CoreToken})
		}
		rows = append(rows, rendered)
	}
	return map[string]any{"inline_keyboard": rows}, nil
}
