package telegram

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/AlonaBot/AlonaBot/sdk"
	"github.com/AlonaBot/AlonaBot/sdk/errcode"
	msg "github.com/AlonaBot/AlonaBot/sdk/message"
)

const mediaReadChunk = 1 << 20 // CONTRACTS §1.3：每次媒体读取至多 1 MiB。

type sentMessage struct {
	ID   *int64 `json:"message_id"`
	Date int64  `json:"date"`
}

// Send 完整预检后依次执行物理操作，失败时返回已发生的结果和安全错误。
func (i *instance) Send(ctx context.Context, to msg.Session, chain msg.Chain) (*msg.SendResult, *errcode.Error) {
	result := &msg.SendResult{Session: to, AdapterInstanceID: i.id, Status: msg.SendRejected}
	plan, safe := buildSendPlan(to, chain)
	if safe != nil {
		return result, safe
	}
	// 1. 先列出全部计划步骤，失败后的未尝试操作仍保留在回执中。
	for index, operation := range plan.operations {
		result.PartResults = append(result.PartResults, msg.PartResult{PartIndex: uint32(index), SegmentIndexes: operation.segmentIndexes, Status: msg.PartNotAttempted})
	}
	for index, operation := range plan.operations {
		part := &result.PartResults[index]
		// 2. 到达当前操作才读取媒体；读取失败时不发起该步平台请求。
		files := make([][]byte, 0, len(operation.uploads))
		for _, upload := range operation.uploads {
			content, err := i.readSendMedia(ctx, upload.token)
			if err != nil {
				safe = i.mediaSendError(err)
				break
			}
			files = append(files, content)
		}
		if safe != nil {
			part.Status = msg.PartRejected
			part.Error = safe
			break
		}
		i.usedSendCapabilities(result, operation)
		var messages []sentMessage
		var message sentMessage
		var response any = &message
		if operation.method == "sendMediaGroup" {
			response = &messages
		}
		var err error
		if len(operation.uploads) > 0 {
			err = i.client.multipart(ctx, operation.method, operation.params, operation.uploads, files, response)
		} else {
			err = i.client.call(ctx, operation.method, operation.params, response)
		}
		if err == nil {
			if operation.method != "sendMediaGroup" {
				messages = []sentMessage{message}
			}
			if len(messages) == 0 {
				err = errors.New("telegram returned no messages")
			}
			for _, message := range messages {
				if message.ID == nil {
					err = errors.New("telegram message has no message_id")
					break
				}
			}
		}
		if err != nil {
			part.Status = msg.PartUnknown
			var api *apiError
			if errors.As(err, &api) {
				part.Status = msg.PartRejected
			}
			safe = i.platformSendError(err)
			part.Error = safe
			break
		}
		// 3. 保留所有已确认 ID；零 ID 只记录平台已接受的事实。
		part.Status = msg.PartAccepted
		for _, message := range messages {
			if *message.ID == 0 {
				part.MessageIDsUnavailable = true
				continue
			}
			id := strconv.FormatInt(*message.ID, 10)
			part.PlatformMessageIDs = append(part.PlatformMessageIDs, id)
			result.PlatformMessageIDs = append(result.PlatformMessageIDs, id)
			if result.PrimaryPlatformMessageID == "" {
				result.PrimaryPlatformMessageID = id
			}
			if result.ProviderAcceptedAt.IsZero() && message.Date != 0 {
				result.ProviderAcceptedAt = time.Unix(message.Date, 0)
			}
		}
	}
	result.Status = msg.SendAccepted
	if safe != nil {
		result.Status = msg.SendPartial
		switch result.PartResults[0].Status {
		case msg.PartRejected:
			result.Status = msg.SendRejected
		case msg.PartUnknown:
			result.Status = msg.SendUnknown
		}
	}
	return result, safe
}

func (i *instance) readSendMedia(ctx context.Context, token string) ([]byte, error) {
	var content []byte
	var offset uint64
	for {
		chunk, next, eof, err := i.core.ReadMedia(ctx, token, offset, mediaReadChunk)
		if err != nil {
			return nil, err
		}
		content = append(content, chunk...)
		if eof {
			return content, nil
		}
		offset = next
	}
}

func (i *instance) mediaSendError(err error) *errcode.Error {
	var public *sdk.Error
	if errors.As(err, &public) {
		return planError(public.Code, i.client.redact.Replace(public.Message))
	}
	return planError(errcode.Unavailable, i.client.safeError(err).Error())
}

func (i *instance) platformSendError(err error) *errcode.Error {
	code := errcode.Unavailable
	message := i.client.safeError(err).Error()
	var api *apiError
	if errors.As(err, &api) {
		switch api.status {
		case http.StatusBadRequest:
			code = errcode.InvalidArgument
		case http.StatusUnauthorized:
			code = errcode.Unauthenticated
		case http.StatusForbidden:
			code = errcode.PermissionDenied
		case http.StatusTooManyRequests:
			code = errcode.ResourceExhausted
			message = fmt.Sprintf("%s; retry_after=%d seconds", message, int64(api.retryAfter/time.Second))
		}
	}
	return planError(code, message)
}

func (i *instance) usedSendCapabilities(result *msg.SendResult, operation sendOperation) {
	names := []string{"message.send"}
	if operation.params["parse_mode"] == markdownMode {
		names = append(names, "message.markdown")
	}
	if operation.method == "sendMediaGroup" {
		names = append(names, "message.album")
	}
	if operation.params["reply_parameters"] != nil {
		names = append(names, "message.reference")
	}
	if operation.params["reply_markup"] != nil {
		names = append(names, "message.buttons")
	}
	for _, name := range names {
		found := false
		for _, used := range result.CapabilitiesUsed {
			if used.Name == name {
				found = true
				break
			}
		}
		if found {
			continue
		}
		for _, declared := range i.Capabilities() {
			if declared.Name == name {
				result.CapabilitiesUsed = append(result.CapabilitiesUsed, declared)
				break
			}
		}
	}
}
