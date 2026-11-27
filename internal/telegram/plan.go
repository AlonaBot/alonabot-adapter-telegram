package telegram

import (
	"bytes"
	"encoding/base64"
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
	mime     string
	content  []byte
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

// sendTopology 保存只读编排；渲染阶段再解析上传内容和 CoreToken。
type sendTopology struct {
	common                       map[string]any
	content                      msg.Chain
	indexes                      []uint32
	groups                       []sendGroup
	reply                        map[string]any
	referenceIndex, buttonsIndex int
	unknown                      bool // 图片来源缺少决定动画/相册的可信 MIME。
}

type sendGroup struct {
	start, end int
}

func planTopology(to string, chain msg.Chain) (*sendTopology, *errcode.Error) {
	// 1. 校验目标和全部片段；计划阶段不读取媒体或调用平台。
	_, chat, thread, parseErr := msg.ParseSID(to)
	chatID, ok := decimalID(chat)
	if parseErr != nil || !ok {
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
	topology := &sendTopology{}
	var content msg.Chain
	var indexes []uint32
	referenceIndex, buttonsIndex := -1, -1
	var reply map[string]any
	for index, segment := range chain {
		if segment == nil {
			return nil, invalidPlan("missing message segment")
		}
		switch data := segment.(type) {
		case msg.ReferenceSegment:
			if referenceIndex >= 0 {
				return nil, invalidPlan(replyRule + ": chain requires at most one reference")
			}
			referenceIndex = index
			other, messageID, _, parseErr := msg.ParseRef(data.Ref)
			id, ok := decimalID(messageID)
			if parseErr != nil || !ok {
				return nil, invalidPlan("reference requires a decimal message ID")
			}
			if other != "" {
				_, otherChat, _, _ := msg.ParseSID(other)
				otherID, ok := decimalID(otherChat)
				if !ok {
					return nil, invalidPlan("reference requires a decimal chat_id")
				}
				if otherID != chatID {
					return nil, planError(errcode.Unsupported, replyRule+": cross-chat replies are unsupported")
				}
			}
			reply = map[string]any{"message_id": id}
		case msg.ButtonsSegment:
			if buttonsIndex >= 0 {
				return nil, invalidPlan(buttonsRule + ": chain requires at most one buttons segment")
			}
			buttonsIndex = index
			if err := checkButtons(data); err != nil {
				return nil, err
			}
		case msg.TextSegment, msg.MentionSegment, msg.ImageSegment, msg.VideoSegment, msg.FileSegment, msg.VoiceSegment:
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
	// 2. 分组规则由只读预检和真实发送共用。
	lastUncertain := false
	for start := 0; start < len(content); {
		end := start + 1
		uncertain := imageMIMEUnknown(content[start])
		switch {
		case isText(content[start]):
			for end < len(content) && isText(content[end]) {
				end++
			}
		case isAlbum(content[start]):
			for end < len(content) && end-start < maxAlbumMedia && isAlbum(content[end]) {
				uncertain = uncertain || imageMIMEUnknown(content[end])
				end++
			}
			// 未知图片可能切断前一相册，改变同一连续媒体段的后续分组。
			if start > 0 && isAlbum(content[start-1]) {
				uncertain = uncertain || lastUncertain
			}
		}
		topology.groups = append(topology.groups, sendGroup{start: start, end: end})
		topology.unknown = topology.unknown || uncertain
		lastUncertain = uncertain
		start = end
	}
	last := topology.groups[len(topology.groups)-1]
	if buttonsIndex >= 0 && last.end-last.start > 1 && isAlbum(content[last.start]) && !lastUncertain {
		return nil, planError(errcode.Unsupported, buttonsRule+": telegram cannot attach buttons to an album; add text after the album")
	}
	topology.common, topology.content, topology.indexes = common, content, indexes
	topology.referenceIndex, topology.buttonsIndex, topology.reply = referenceIndex, buttonsIndex, reply
	return topology, nil
}

func buildSendPlan(to string, chain msg.Chain) (*sendPlan, *errcode.Error) {
	// 1. 与只读预检共用目标、引用与片段分组。
	topology, err := planTopology(to, chain)
	if err != nil {
		return nil, err
	}
	content, indexes, common := topology.content, topology.indexes, topology.common
	plan := &sendPlan{}
	// 2. 按原顺序组合邻接文本、媒体组与独立文件。
	for _, group := range topology.groups {
		start, end := group.start, group.end
		operation := sendOperation{params: make(map[string]any)}
		switch {
		case isText(content[start]):
			rendered, err := renderText(content[start:end])
			if err != nil {
				return nil, err
			}
			operation.method = "sendMessage"
			operation.params = rendered
		case isAlbum(content[start]):
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
		case isFile(content[start]) || isStandaloneMedia(content[start]):
			rendered, upload, err := renderMedia(content[start], indexes[start])
			if err != nil {
				return nil, err
			}
			kind := rendered["type"].(string)
			operation.method = map[string]string{"document": "sendDocument", "voice": "sendVoice", "animation": "sendAnimation"}[kind]
			operation.params[kind] = rendered["media"]
			if caption, ok := rendered["caption"]; ok {
				operation.params["caption"] = caption
			}
			if duration, ok := rendered["duration"]; ok {
				operation.params["duration"] = duration
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
	}
	// 3. 引用归首步，按钮归末步；相册按钮在首发前拒绝。
	if topology.referenceIndex >= 0 {
		plan.operations[0].params["reply_parameters"] = topology.reply
		plan.operations[0].segmentIndexes = append(plan.operations[0].segmentIndexes, uint32(topology.referenceIndex))
	}
	if topology.buttonsIndex >= 0 {
		last := &plan.operations[len(plan.operations)-1]
		if last.method == "sendMediaGroup" {
			return nil, planError(errcode.Unsupported, buttonsRule+": telegram cannot attach buttons to an album; add text after the album")
		}
		markup, err := renderButtons(chain[topology.buttonsIndex].(msg.ButtonsSegment))
		if err != nil {
			return nil, err
		}
		last.params["reply_markup"] = markup
		last.segmentIndexes = append(last.segmentIndexes, uint32(topology.buttonsIndex))
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
	if isStandaloneMedia(s) {
		return false
	}
	switch s.(type) {
	case msg.ImageSegment, msg.VideoSegment:
		return true
	}
	return false
}
func isFile(s msg.Segment) bool { _, ok := s.(msg.FileSegment); return ok }

func isStandaloneMedia(s msg.Segment) bool {
	if _, ok := s.(msg.VoiceSegment); ok {
		return true
	}
	image, ok := s.(msg.ImageSegment)
	if !ok {
		return false
	}
	if image.Source.MediaRef != "" || image.Source.PlatformFile != "" || image.Source.Incoming != nil {
		return image.Source.MIME == "image/gif"
	}
	if image.Source.InlineBase64 != "" {
		prefix, _ := base64.StdEncoding.DecodeString(image.Source.InlineBase64[:min(8, len(image.Source.InlineBase64))])
		return bytes.HasPrefix(prefix, []byte("GIF8"))
	}
	return false
}

func renderMedia(segment msg.Segment, index uint32) (map[string]any, *plannedUpload, *errcode.Error) {
	content, err := checkMedia(segment)
	if err != nil {
		return nil, nil, err
	}
	var source msg.MediaSource
	var caption string
	kind, filename := "", ""
	result := make(map[string]any)
	switch media := segment.(type) {
	case msg.ImageSegment:
		source, caption, kind = media.Source, media.Caption, "photo"
		if isStandaloneMedia(media) {
			kind = "animation"
		}
	case msg.VoiceSegment:
		source, caption, kind = media.Source, media.Caption, "voice"
		if media.Duration != 0 {
			result["duration"] = uint64(media.Duration / time.Second)
		}
	case msg.VideoSegment:
		source, caption, kind = media.Source, media.Caption, "video"
		if media.Duration != 0 {
			result["duration"] = uint64(media.Duration / time.Second)
		}
	case msg.FileSegment:
		source, caption, kind = media.Source, media.Caption, "document"
		filename = media.Filename
	}
	if caption != "" {
		result["caption"] = caption
	}
	result["type"] = kind
	switch {
	case source.PlatformFile != "":
		result["media"] = source.PlatformFile
		return result, nil, nil
	case source.MediaRef != "" || source.InlineBase64 != "":
		field := fmt.Sprintf("media_%d", index)
		result["media"] = "attach://" + field
		contentType := source.MIME
		if kind == "animation" {
			contentType = "image/gif"
		}
		if filename == "" {
			filename = field
			if contentType == "image/gif" {
				filename += ".gif"
			}
		}
		return result, &plannedUpload{field: field, token: source.MediaRef, filename: filename, mime: contentType, content: content}, nil
	case source.HTTPSURL != "":
		result["media"] = source.HTTPSURL
		return result, nil, nil
	default:
		return nil, nil, invalidPlan("media source must be platform_file, media_ref, inline_base64 or https_url")
	}
}

func mediaSource(segment msg.Segment) (msg.MediaSource, string) {
	switch media := segment.(type) {
	case msg.ImageSegment:
		return media.Source, media.Caption
	case msg.VideoSegment:
		return media.Source, media.Caption
	case msg.VoiceSegment:
		return media.Source, media.Caption
	case msg.FileSegment:
		return media.Source, media.Caption
	}
	return msg.MediaSource{}, ""
}

func checkMedia(segment msg.Segment) ([]byte, *errcode.Error) {
	source, caption := mediaSource(segment)
	if file, ok := segment.(msg.FileSegment); ok && strings.ContainsAny(file.Filename, "/\\") {
		return nil, invalidPlan("file filename must not contain a path")
	}
	if utf16Length(caption) > maxCaptionUTF16 {
		return nil, invalidPlan("telegram caption exceeds UTF-16 length limit")
	}
	switch {
	case source.PlatformFile != "", source.MediaRef != "", source.Incoming != nil:
		return nil, nil
	case source.InlineBase64 != "":
		content, err := base64.StdEncoding.DecodeString(source.InlineBase64)
		if err != nil {
			return nil, invalidPlan("invalid inline_base64: " + err.Error())
		}
		return content, nil
	case source.HTTPSURL != "":
		parsed, err := url.Parse(source.HTTPSURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return nil, invalidPlan("media URL must use https")
		}
		return nil, nil
	default:
		return nil, invalidPlan("media source must be platform_file, media_ref, incoming, inline_base64 or https_url")
	}
}

func imageMIMEUnknown(segment msg.Segment) bool {
	image, ok := segment.(msg.ImageSegment)
	if !ok {
		return false
	}
	source := image.Source
	return source.MIME == "" && (source.MediaRef != "" || source.Incoming != nil || source.PlatformFile != "")
}
