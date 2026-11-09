package telegramtest

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// Request 保存一次发送类调用的参数和上传文件。
type Request struct {
	Method string
	Params map[string]any // multipart 的 JSON 字段已解码。
	Files  map[string][]byte
}

type sendCall struct {
	method string
	nth    int
}
type sendFault struct {
	status      int
	description string
	zero        bool
	drop        bool
}
type sendState struct {
	requests  []Request
	counts    map[string]int
	faults    map[sendCall]sendFault
	messageID int64
}

// Requests 按到达顺序返回发送类请求的深副本。
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Request, len(s.send.requests))
	for i, request := range s.send.requests {
		files := make(map[string][]byte, len(request.Files))
		for name, content := range request.Files {
			files[name] = slices.Clone(content)
		}
		result[i] = Request{Method: request.Method, Params: cloneParams(request.Params).(map[string]any), Files: files}
	}
	return result
}

func cloneParams(value any) any {
	switch value := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, child := range value {
			result[key] = cloneParams(child)
		}
		return result
	case []any:
		result := make([]any, len(value))
		for i, child := range value {
			result[i] = cloneParams(child)
		}
		return result
	default:
		return value
	}
}

// FailMethod 令方法的第 nth 次调用返回指定 HTTP 状态与错误描述；nth 从 1 起。
func (s *Server) FailMethod(method string, nth int, status int, description string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setSendFault(sendCall{method, nth}, sendFault{status: status, description: description})
}

// ZeroMessageID 令方法的第 nth 次成功调用返回零消息 ID；相册的全部消息生效。
func (s *Server) ZeroMessageID(method string, nth int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setSendFault(sendCall{method, nth}, sendFault{zero: true})
}

// Drop 令方法的第 nth 次调用在响应写出前断开连接。
func (s *Server) Drop(method string, nth int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setSendFault(sendCall{method, nth}, sendFault{drop: true})
}

// setSendFault 在持有 mu 时登记指定调用的故障。
func (s *Server) setSendFault(call sendCall, fault sendFault) {
	if s.send.faults == nil {
		s.send.faults = make(map[sendCall]sendFault)
	}
	s.send.faults[call] = fault
}

func readSendRequest(r *http.Request, method string) (Request, error) {
	request := Request{Method: method, Params: make(map[string]any), Files: make(map[string][]byte)}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		err := json.NewDecoder(r.Body).Decode(&request.Params)
		return request, err
	}
	reader, err := r.MultipartReader()
	if err != nil {
		return request, err
	}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			return request, nil
		}
		if err != nil {
			return request, err
		}
		content, err := io.ReadAll(part)
		if err != nil {
			return request, err
		}
		if part.FileName() != "" {
			request.Files[part.FormName()] = content
			continue
		}
		var value any = string(content)
		switch part.FormName() {
		case "media", "reply_markup", "reply_parameters", "entities", "caption_entities":
			if err := json.Unmarshal(content, &value); err != nil {
				return request, err
			}
		}
		request.Params[part.FormName()] = value
	}
}

func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request, method string) {
	// 1. 解析完整请求后，在同一把锁内登记顺序、调用次数和消息 ID。
	request, err := readSendRequest(r, method)
	if err != nil {
		s.respond(w, http.StatusBadRequest, nil)
		return
	}
	chatID := request.Params["chat_id"]
	if value, ok := chatID.(string); ok {
		chatID, err = strconv.ParseInt(value, 10, 64)
		if err != nil {
			s.respond(w, http.StatusBadRequest, nil)
			return
		}
	}
	count := 1
	var editID int64
	if method == "sendMediaGroup" {
		media, ok := request.Params["media"].([]any)
		if !ok || len(media) == 0 {
			s.respond(w, http.StatusBadRequest, nil)
			return
		}
		count = len(media)
	}
	if method == "editMessageReplyMarkup" {
		switch value := request.Params["message_id"].(type) {
		case float64:
			editID = int64(value)
		case string:
			editID, err = strconv.ParseInt(value, 10, 64)
		default:
			err = strconv.ErrSyntax
		}
		if err != nil {
			s.respond(w, http.StatusBadRequest, nil)
			return
		}
	}
	s.mu.Lock()
	s.send.requests = append(s.send.requests, request)
	if s.send.counts == nil {
		s.send.counts = make(map[string]int)
	}
	s.send.counts[method]++
	fault := s.send.faults[sendCall{method, s.send.counts[method]}]
	var result any
	if fault.status == 0 && !fault.drop {
		messages := make([]any, count)
		for i := range messages {
			s.send.messageID++
			id := s.send.messageID
			if method == "editMessageReplyMarkup" {
				id = editID
			}
			if fault.zero {
				id = 0
			}
			messages[i] = map[string]any{"message_id": id, "date": 0, "chat": map[string]any{"id": chatID, "type": "private"}}
		}
		if method == "sendMediaGroup" {
			result = messages
		} else {
			result = messages[0]
		}
	}
	s.mu.Unlock()
	// 2. 故障发生在写出响应前，调用记录仍保留。
	if fault.drop {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			s.t.Errorf("hijack Bot API connection: %s", err)
			return
		}
		if err := conn.Close(); err != nil {
			s.t.Errorf("close Bot API connection: %s", err)
		}
		return
	}
	if fault.status != 0 {
		response := map[string]any{"ok": false, "error_code": fault.status, "description": fault.description}
		if fault.status == http.StatusTooManyRequests {
			response["parameters"] = map[string]int{"retry_after": 1}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(fault.status)
		if err := json.NewEncoder(w).Encode(response); err != nil {
			s.t.Errorf("write Bot API failure: %s", err)
		}
		return
	}
	s.respond(w, http.StatusOK, result)
}
