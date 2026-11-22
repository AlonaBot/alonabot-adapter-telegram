package telegramtest

import "encoding/json"

// actionResult 与 sendMessage 共用请求记录、故障和互斥锁。
func (s *Server) actionResult(request Request, chatID any) (any, bool) {
	if result, ok := s.send.results[request.Method]; ok {
		return result, true
	}
	switch request.Method {
	case "answerCallbackQuery", "deleteMessages", "banChatMember", "unbanChatMember", "restrictChatMember", "pinChatMessage", "unpinChatMessage", "setMessageReaction", "setChatTitle", "leaveChat", "sendChatAction", "sendMessageDraft":
		return true, true
	case "getChat":
		return map[string]any{"id": chatID, "type": "private", "first_name": "Test"}, true
	case "getChatMember":
		return map[string]any{"status": "member", "user": map[string]any{"id": request.Params["user_id"], "is_bot": false, "first_name": "Test"}}, true
	case "getChatMemberCount":
		return 3, true
	case "setMyCommands", "getMyCommands", "deleteMyCommands":
		key, err := json.Marshal([]any{request.Params["scope"], request.Params["language_code"]})
		if err != nil {
			s.t.Fatal(err)
		}
		if s.send.menus == nil {
			s.send.menus = make(map[string][]any)
		}
		switch request.Method {
		case "setMyCommands":
			commands, _ := request.Params["commands"].([]any)
			s.send.menus[string(key)] = commands
			return true, true
		case "deleteMyCommands":
			delete(s.send.menus, string(key))
			return true, true
		default:
			commands := s.send.menus[string(key)]
			if commands == nil {
				commands = []any{}
			}
			return commands, true
		}
	}
	return nil, false
}
