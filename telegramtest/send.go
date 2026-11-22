package telegramtest

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// Request 保存一次平台消息、动作或菜单操作的参数和上传文件。
type Request struct {
	Method       string
	Params       map[string]any // multipart 的 JSON 字段已解码。
	Files        map[string][]byte
	FileMetadata map[string]FileMetadata
}

// FileMetadata 保留上传文件的原名和 multipart 媒体类型。
type FileMetadata struct {
	Filename    string
	ContentType string
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
	requests     []Request
	menuRequests []Request
	counts       map[string]int
	faults       map[sendCall]sendFault
	messageID    int64
	results      map[string]json.RawMessage
	menus        map[string][]any
}

// SetResult 配置方法的完整 result；故障响应也可附带此结果以验证部分错误。
func (s *Server) SetResult(method string, result json.RawMessage) {
	s.t.Helper()
	if !json.Valid(result) {
		s.t.Fatal("invalid Bot API result JSON")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.send.results == nil {
		s.send.results = make(map[string]json.RawMessage)
	}
	s.send.results[method] = slices.Clone(result)
}

// Requests 按到达顺序返回消息、编辑、回调与动作记录；菜单操作见 MenuRequests。
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneRequests(s.send.requests)
}

// MenuRequests 返回命令菜单操作记录；自动菜单同步不会改变既有 Requests 的消息顺序。
func (s *Server) MenuRequests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneRequests(s.send.menuRequests)
}

func cloneRequests(requests []Request) []Request {
	result := make([]Request, len(requests))
	for i, request := range requests {
		files := make(map[string][]byte, len(request.Files))
		for name, content := range request.Files {
			files[name] = slices.Clone(content)
		}
		result[i] = Request{Method: request.Method, Params: cloneParams(request.Params).(map[string]any), Files: files, FileMetadata: maps.Clone(request.FileMetadata)}
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
	request := Request{Method: method, Params: make(map[string]any), Files: make(map[string][]byte), FileMetadata: make(map[string]FileMetadata)}
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
			request.FileMetadata[part.FormName()] = FileMetadata{Filename: part.FileName(), ContentType: part.Header.Get("Content-Type")}
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
	editing := method == "editMessageReplyMarkup" || method == "editMessageText"
	if method == "sendMediaGroup" {
		media, ok := request.Params["media"].([]any)
		if !ok || len(media) == 0 {
			s.respond(w, http.StatusBadRequest, nil)
			return
		}
		count = len(media)
	}
	if editing {
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
	if method == "setMyCommands" || method == "getMyCommands" || method == "deleteMyCommands" {
		s.send.menuRequests = append(s.send.menuRequests, request)
	} else {
		s.send.requests = append(s.send.requests, request)
	}
	if s.send.counts == nil {
		s.send.counts = make(map[string]int)
	}
	s.send.counts[method]++
	fault := s.send.faults[sendCall{method, s.send.counts[method]}]
	var result any
	if fault.status == 0 && !fault.drop {
		var handled bool
		result, handled = s.actionResult(request, chatID)
		if !handled {
			messages := make([]any, count)
			for i := range messages {
				s.send.messageID++
				id := s.send.messageID
				if editing {
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
		s.mu.Lock()
		if result, ok := s.send.results[method]; ok {
			response["result"] = result
		}
		s.mu.Unlock()
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
