package telegram

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	pb "github.com/AlonaBot/AlonaBot/contracts/pb/alonabotv1"
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

func buildSendPlan(target *pb.Target, chain *pb.MessageChain) (*sendPlan, *pb.ErrorInfo) {
	// 1. 校验目标和全部片段；计划阶段不读取媒体或调用平台。
	chatID, ok := decimalID(target.GetChatId())
	if !ok {
		return nil, invalidPlan("telegram target requires a decimal chat_id")
	}
	common := map[string]any{"chat_id": chatID}
	if target.ThreadId != nil {
		threadID, ok := decimalID(target.GetThreadId())
		if !ok {
			return nil, invalidPlan("telegram target requires a decimal thread_id")
		}
		common["message_thread_id"] = threadID
	}
	plan := &sendPlan{}
	var content []*pb.Segment
	var indexes []uint32
	referenceIndex, buttonsIndex := -1, -1
	var reply, markup map[string]any
	for index, segment := range chain.GetSegments() {
		if segment == nil {
			return nil, invalidPlan("missing message segment")
		}
		switch data := segment.Data.(type) {
		case *pb.Segment_Reference:
			if referenceIndex >= 0 || data.Reference == nil {
				return nil, invalidPlan("chain requires at most one reference")
			}
			referenceIndex = index
			id, ok := decimalID(data.Reference.OriginalPlatformMessageId)
			if !ok {
				return nil, invalidPlan("reference requires a decimal message ID")
			}
			if other := data.Reference.Target; other != nil {
				otherID, ok := decimalID(other.ChatId)
				if !ok {
					return nil, invalidPlan("reference requires a decimal chat_id")
				}
				if otherID != chatID {
					return nil, planError(pb.ErrorCode_ERROR_CODE_UNSUPPORTED, "cross-chat replies are unsupported")
				}
			}
			reply = map[string]any{"message_id": id}
		case *pb.Segment_Buttons:
			if buttonsIndex >= 0 {
				return nil, invalidPlan("chain requires at most one buttons segment")
			}
			buttonsIndex = index
			var err *pb.ErrorInfo
			markup, err = renderButtons(data.Buttons)
			if err != nil {
				return nil, err
			}
		case *pb.Segment_Text, *pb.Segment_Mention, *pb.Segment_Image, *pb.Segment_Video, *pb.Segment_File:
			content = append(content, segment)
			indexes = append(indexes, uint32(index))
		case *pb.Segment_Forward:
			return nil, planError(pb.ErrorCode_ERROR_CODE_UNSUPPORTED, "telegram forward segments are unsupported")
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
		case content[start].GetText() != nil || content[start].GetMention() != nil:
			for end < len(content) && (content[end].GetText() != nil || content[end].GetMention() != nil) {
				end++
			}
			rendered, err := renderText(content[start:end])
			if err != nil {
				return nil, err
			}
			operation.method = "sendMessage"
			operation.params = rendered
		case content[start].GetImage() != nil || content[start].GetVideo() != nil:
			for end < len(content) && end-start < maxAlbumMedia && (content[end].GetImage() != nil || content[end].GetVideo() != nil) {
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
		case content[start].GetFile() != nil:
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
			return nil, planError(pb.ErrorCode_ERROR_CODE_UNSUPPORTED, "telegram cannot attach buttons to an album")
		}
		last.params["reply_markup"] = markup
		last.segmentIndexes = append(last.segmentIndexes, uint32(buttonsIndex))
	}
	for index := range plan.operations {
		slices.Sort(plan.operations[index].segmentIndexes)
	}
	return plan, nil
}

func renderMedia(segment *pb.Segment, index uint32) (map[string]any, *plannedUpload, *pb.ErrorInfo) {
	var source *pb.MediaSource
	var caption *string
	kind, filename := "", ""
	result := make(map[string]any)
	switch {
	case segment.GetImage() != nil:
		image := segment.GetImage()
		source, caption, kind = image.Source, image.Caption, "photo"
	case segment.GetVideo() != nil:
		video := segment.GetVideo()
		source, caption, kind = video.Source, video.Caption, "video"
		if video.DurationMs != nil {
			result["duration"] = video.GetDurationMs() / uint64(time.Second/time.Millisecond)
		}
	case segment.GetFile() != nil:
		file := segment.GetFile()
		source, caption, kind = file.Source, file.Caption, "document"
		filename = file.GetFilename()
		if strings.ContainsAny(filename, "/\\") {
			return nil, nil, invalidPlan("file filename must not contain a path")
		}
	}
	if caption != nil {
		if utf16Length(*caption) > maxCaptionUTF16 {
			return nil, nil, invalidPlan("telegram caption exceeds UTF-16 length limit")
		}
		result["caption"] = *caption
	}
	result["type"] = kind
	switch value := source.GetSource().(type) {
	case *pb.MediaSource_MediaRef:
		if value.MediaRef == "" {
			return nil, nil, invalidPlan("media_ref must not be empty")
		}
		field := fmt.Sprintf("media_%d", index)
		result["media"] = "attach://" + field
		if filename == "" {
			filename = field
		}
		return result, &plannedUpload{field: field, token: value.MediaRef, filename: filename}, nil
	case *pb.MediaSource_HttpsUrl:
		parsed, err := url.Parse(value.HttpsUrl)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return nil, nil, invalidPlan("media URL must use https")
		}
		result["media"] = value.HttpsUrl
		return result, nil, nil
	default:
		return nil, nil, invalidPlan("media source must be media_ref or https_url")
	}
}
