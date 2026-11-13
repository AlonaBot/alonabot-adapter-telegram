package telegram

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/AlonaBot/AlonaBot/sdk/errcode"
	msg "github.com/AlonaBot/AlonaBot/sdk/message"
)

const maxAlbumMedia = 10 // MESSAGE-MODEL §6.2：连续图片、视频每组最多十项。

type plannedUpload struct {
	field    string
	token    string
	filename string
}

type sendOperation struct {
	method         string
	params         map[string]any
	uploads        []plannedUpload
	segmentIndexes []uint32
}

type sendPlan struct {
	operations []sendOperation
}

func buildSendPlan(to msg.Session, chain msg.Chain) (*sendPlan, *errcode.Error) {
	// 1. 校验目标和全部片段；计划阶段不读取媒体或调用平台。
	_, chat, thread, valid := to.Parts()
	chatID, ok := decimalID(chat)
	if !valid || !ok {
		return nil, invalidPlan("telegram target requires a decimal chat_id")
	}
	common := map[string]any{"chat_id": chatID}
	if thread != "" {
		threadID, ok := decimalID(thread)
		if !ok {
			return nil, invalidPlan("telegram target requires a decimal thread_id")
		}
		common["message_thread_id"] = threadID
	}
	plan := &sendPlan{}
	var content msg.Chain
	var indexes []uint32
	referenceIndex, buttonsIndex := -1, -1
	var reply, markup map[string]any
	for index, segment := range chain {
		if segment == nil {
			return nil, invalidPlan("missing message segment")
		}
		switch data := segment.(type) {
		case msg.ReferenceSegment:
			if referenceIndex >= 0 {
				return nil, invalidPlan("chain requires at most one reference")
			}
			referenceIndex = index
			other, messageID, _, valid := data.Ref.Parts()
			id, ok := decimalID(messageID)
			if !valid || !ok {
				return nil, invalidPlan("reference requires a decimal message ID")
			}
			if other != "" {
				_, otherChat, _, _ := other.Parts()
				otherID, ok := decimalID(otherChat)
				if !ok {
					return nil, invalidPlan("reference requires a decimal chat_id")
				}
				if otherID != chatID {
					return nil, planError(errcode.Unsupported, "cross-chat replies are unsupported")
				}
			}
			reply = map[string]any{"message_id": id}
		case msg.ButtonsSegment:
			if buttonsIndex >= 0 {
				return nil, invalidPlan("chain requires at most one buttons segment")
			}
			buttonsIndex = index
			var err *errcode.Error
			markup, err = renderButtons(data)
			if err != nil {
				return nil, err
			}
		case msg.TextSegment, msg.MentionSegment, msg.ImageSegment, msg.VideoSegment, msg.FileSegment:
			content = append(content, segment)
			indexes = append(indexes, uint32(index))
		case msg.ForwardSegment:
			return nil, planError(errcode.Unsupported, "telegram forward segments are unsupported")
		default:
			return nil, invalidPlan("missing message segment data")
		}
	}
	if len(content) == 0 {
		return nil, invalidPlan("chain requires sendable content")
	}
	// 2. 按原顺序组合邻接文本、媒体组与独立文件。
	for start := 0; start < len(content); {
		end := start + 1
		operation := sendOperation{params: make(map[string]any)}
		switch {
		case isText(content[start]):
			for end < len(content) && isText(content[end]) {
				end++
			}
			rendered, err := renderText(content[start:end])
			if err != nil {
				return nil, err
			}
			operation.method = "sendMessage"
			operation.params = rendered
		case isAlbum(content[start]):
			for end < len(content) && end-start < maxAlbumMedia && isAlbum(content[end]) {
				end++
			}
			var media []map[string]any
			for index := start; index < end; index++ {
				rendered, upload, err := renderMedia(content[index], indexes[index])
				if err != nil {
					return nil, err
				}
				media = append(media, rendered)
				if upload != nil {
					operation.uploads = append(operation.uploads, *upload)
				}
			}
			if len(media) > 1 {
				operation.method = "sendMediaGroup"
				operation.params["media"] = media
			} else {
				kind := media[0]["type"].(string)
				if kind == "photo" {
					operation.method = "sendPhoto"
				} else {
					operation.method = "sendVideo"
				}
				operation.params[kind] = media[0]["media"]
				if caption, ok := media[0]["caption"]; ok {
					operation.params["caption"] = caption
				}
				if duration, ok := media[0]["duration"]; ok {
					operation.params["duration"] = duration
				}
			}
		case isFile(content[start]):
			rendered, upload, err := renderMedia(content[start], indexes[start])
			if err != nil {
				return nil, err
			}
			operation.method = "sendDocument"
			operation.params["document"] = rendered["media"]
			if caption, ok := rendered["caption"]; ok {
				operation.params["caption"] = caption
			}
			if upload != nil {
				operation.uploads = append(operation.uploads, *upload)
			}
		default:
			return nil, invalidPlan("missing message segment payload")
		}
		for key, value := range common {
			operation.params[key] = value
		}
		operation.segmentIndexes = slices.Clone(indexes[start:end])
		plan.operations = append(plan.operations, operation)
		start = end
	}
	// 3. 引用归首步，按钮归末步；相册按钮在首发前拒绝。
	if referenceIndex >= 0 {
		plan.operations[0].params["reply_parameters"] = reply
		plan.operations[0].segmentIndexes = append(plan.operations[0].segmentIndexes, uint32(referenceIndex))
	}
	if buttonsIndex >= 0 {
		last := &plan.operations[len(plan.operations)-1]
		if last.method == "sendMediaGroup" {
			return nil, planError(errcode.Unsupported, "telegram cannot attach buttons to an album")
		}
		last.params["reply_markup"] = markup
		last.segmentIndexes = append(last.segmentIndexes, uint32(buttonsIndex))
	}
	for index := range plan.operations {
		slices.Sort(plan.operations[index].segmentIndexes)
	}
	return plan, nil
}

func isText(s msg.Segment) bool {
	switch s.(type) {
	case msg.TextSegment, msg.MentionSegment:
		return true
	}
	return false
}
func isAlbum(s msg.Segment) bool {
	switch s.(type) {
	case msg.ImageSegment, msg.VideoSegment:
		return true
	}
	return false
}
func isFile(s msg.Segment) bool { _, ok := s.(msg.FileSegment); return ok }

func renderMedia(segment msg.Segment, index uint32) (map[string]any, *plannedUpload, *errcode.Error) {
	var source msg.MediaSource
	var caption string
	kind, filename := "", ""
	result := make(map[string]any)
	switch media := segment.(type) {
	case msg.ImageSegment:
		source, caption, kind = media.Source, media.Caption, "photo"
	case msg.VideoSegment:
		source, caption, kind = media.Source, media.Caption, "video"
		if media.Duration != 0 {
			result["duration"] = uint64(media.Duration / time.Second)
		}
	case msg.FileSegment:
		source, caption, kind = media.Source, media.Caption, "document"
		filename = media.Filename
		if strings.ContainsAny(filename, "/\\") {
			return nil, nil, invalidPlan("file filename must not contain a path")
		}
	}
	if caption != "" {
		if utf16Length(caption) > maxCaptionUTF16 {
			return nil, nil, invalidPlan("telegram caption exceeds UTF-16 length limit")
		}
		result["caption"] = caption
	}
	result["type"] = kind
	switch {
	case source.PlatformFile != "":
		result["media"] = source.PlatformFile
		return result, nil, nil
	case source.MediaRef != "":
		field := fmt.Sprintf("media_%d", index)
		result["media"] = "attach://" + field
		if filename == "" {
			filename = field
		}
		return result, &plannedUpload{field: field, token: source.MediaRef, filename: filename}, nil
	case source.HTTPSURL != "":
		parsed, err := url.Parse(source.HTTPSURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return nil, nil, invalidPlan("media URL must use https")
		}
		result["media"] = source.HTTPSURL
		return result, nil, nil
	default:
		return nil, nil, invalidPlan("media source must be platform_file, media_ref or https_url")
	}
}
