package telegram

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"

	pb "github.com/AlonaBot/AlonaBot/contracts/pb/alonabotv1"
)

// Telegram Bot API 的发送文本、caption 和 callback_data 上限。
const (
	maxTextUTF16     = 4096
	maxCaptionUTF16  = 1024
	maxCallbackBytes = 64
	markdownMode     = "MarkdownV2"
)

func planError(code pb.ErrorCode, message string) *pb.ErrorInfo {
	return &pb.ErrorInfo{Code: code, SafeMessage: message}
}

func invalidPlan(message string) *pb.ErrorInfo {
	return planError(pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, message)
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

func renderEntity(text string, entity *pb.TextEntity, base int) (map[string]any, *pb.ErrorInfo) {
	if entity == nil {
		return nil, invalidPlan("missing text entity")
	}
	start, length := uint64(entity.OffsetUtf16), uint64(entity.LengthUtf16)
	end := start + length
	if length == 0 || end > uint64(utf16Length(text)) || !entityBoundary(text, start) || !entityBoundary(text, end) {
		return nil, invalidPlan("invalid UTF-16 entity range")
	}
	names := map[pb.EntityType]string{
		pb.EntityType_ENTITY_TYPE_BOLD: "bold", pb.EntityType_ENTITY_TYPE_ITALIC: "italic",
		pb.EntityType_ENTITY_TYPE_UNDERLINE: "underline", pb.EntityType_ENTITY_TYPE_STRIKETHROUGH: "strikethrough",
		pb.EntityType_ENTITY_TYPE_SPOILER: "spoiler", pb.EntityType_ENTITY_TYPE_CODE: "code", pb.EntityType_ENTITY_TYPE_PRE: "pre",
		pb.EntityType_ENTITY_TYPE_TEXT_LINK: "text_link", pb.EntityType_ENTITY_TYPE_TEXT_MENTION: "text_mention",
	}
	name, ok := names[entity.Type]
	if !ok {
		return nil, planError(pb.ErrorCode_ERROR_CODE_UNSUPPORTED, "unsupported text entity")
	}
	result := map[string]any{"type": name, "offset": base + int(start), "length": int(length)}
	switch entity.Type {
	case pb.EntityType_ENTITY_TYPE_TEXT_LINK:
		if entity.GetUrl() == "" {
			return nil, invalidPlan("text_link requires url")
		}
		result["url"] = entity.GetUrl()
	case pb.EntityType_ENTITY_TYPE_TEXT_MENTION:
		if entity.GetExternalUserId() == "" {
			return nil, invalidPlan("text_mention requires external_user_id")
		}
		id, ok := decimalID(entity.GetExternalUserId())
		if !ok {
			return nil, planError(pb.ErrorCode_ERROR_CODE_UNSUPPORTED, "telegram mention requires a decimal user ID")
		}
		result["user"] = map[string]any{"id": id}
	case pb.EntityType_ENTITY_TYPE_PRE:
		if entity.Language != nil {
			result["language"] = entity.GetLanguage()
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

func renderText(segments []*pb.Segment) (map[string]any, *pb.ErrorInfo) {
	// 1. 确定整段格式，避免合并后把普通文字解释为 Markdown。
	markdown := false
	for _, segment := range segments {
		if text := segment.GetText(); text != nil && text.GetFormat() == pb.TextFormat_TEXT_FORMAT_MARKDOWN {
			markdown = true
		}
	}
	var output strings.Builder
	var entities []map[string]any
	units := 0
	for _, segment := range segments {
		if text := segment.GetText(); text != nil {
			switch text.GetFormat() {
			case pb.TextFormat_TEXT_FORMAT_UNSPECIFIED, pb.TextFormat_TEXT_FORMAT_PLAIN:
			case pb.TextFormat_TEXT_FORMAT_MARKDOWN:
				if len(text.Entities) > 0 {
					return nil, invalidPlan("markdown cannot include entities")
				}
			default:
				return nil, planError(pb.ErrorCode_ERROR_CODE_UNSUPPORTED, "unsupported text format")
			}
			for _, entity := range text.Entities {
				rendered, err := renderEntity(text.Text, entity, units)
				if err != nil {
					return nil, err
				}
				entities = append(entities, rendered)
			}
			if markdown && len(text.Entities) > 0 {
				return nil, planError(pb.ErrorCode_ERROR_CODE_UNSUPPORTED, "markdown cannot be combined with explicit entities")
			}
			if markdown && text.GetFormat() != pb.TextFormat_TEXT_FORMAT_MARKDOWN {
				output.WriteString(escapeMarkdown(text.Text))
			} else {
				output.WriteString(text.Text)
			}
			units += utf16Length(text.Text)
			continue
		}
		mention := segment.GetMention()
		if mention == nil {
			return nil, invalidPlan("missing text or mention")
		}
		id, ok := decimalID(mention.ExternalUserId)
		if !ok {
			return nil, planError(pb.ErrorCode_ERROR_CODE_UNSUPPORTED, "telegram mention requires a decimal user ID")
		}
		display := mention.GetDisplay()
		if mention.Display == nil {
			display = mention.ExternalUserId
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

func renderButtons(buttons *pb.ButtonsSegment) (map[string]any, *pb.ErrorInfo) {
	if buttons == nil || len(buttons.Rows) == 0 {
		return nil, invalidPlan("buttons require rows")
	}
	rows := make([][]map[string]any, 0, len(buttons.Rows))
	for _, row := range buttons.Rows {
		if row == nil || len(row.Buttons) == 0 {
			return nil, invalidPlan("button row must not be empty")
		}
		rendered := make([]map[string]any, 0, len(row.Buttons))
		for _, button := range row.Buttons {
			if button == nil || len(button.GetCoreToken()) == 0 || len(button.GetCoreToken()) > maxCallbackBytes {
				return nil, invalidPlan("button core_token must contain 1 to 64 bytes")
			}
			if button.Label == "" {
				return nil, invalidPlan("button label must not be empty")
			}
			rendered = append(rendered, map[string]any{"text": button.Label, "callback_data": button.GetCoreToken()})
		}
		rows = append(rows, rendered)
	}
	return map[string]any{"inline_keyboard": rows}, nil
}
