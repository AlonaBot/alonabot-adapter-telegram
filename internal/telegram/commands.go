package telegram

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/AlonaBot/AlonaBot/sdk/adapter"
	"github.com/AlonaBot/AlonaBot/sdk/errcode"
)

var _ adapter.CommandMenuController = (*instance)(nil)

// CommandMenuConfig 上报已解析的实例配置；模式、所有权和串行执行由 Core 管理。
func (i *instance) CommandMenuConfig() adapter.CommandMenuConfig { return i.commandConfig }

func commandMenuParams(scope adapter.CommandMenuScope, language string) (map[string]any, *errcode.Error) {
	position := map[string]any{"type": scope.Type}
	switch scope.Type {
	case "default":
		if scope.ChatID != "" || scope.UserID != "" {
			return nil, invalidPlan("default scope must not contain chat_id or user_id")
		}
	case "chat", "chat_administrators", "chat_member":
		id, valid := decimalID(scope.ChatID)
		if !valid || id == 0 {
			return nil, invalidPlan("menu scope requires a non-zero decimal chat_id")
		}
		position["chat_id"] = id
		if scope.Type == "chat_member" {
			userID, valid := decimalID(scope.UserID)
			if !valid || userID <= 0 {
				return nil, invalidPlan("chat_member scope requires a positive decimal user_id")
			}
			position["user_id"] = userID
		} else if scope.UserID != "" {
			return nil, invalidPlan("user_id requires chat_member scope")
		}
	default:
		return nil, invalidPlan("unsupported command menu scope")
	}
	if language != "" && (len(language) != 2 || language[0] < 'a' || language[0] > 'z' || language[1] < 'a' || language[1] > 'z') {
		return nil, invalidPlan("language_code must be a two-letter lowercase ISO 639-1 code")
	}
	return map[string]any{"scope": position, "language_code": language}, nil
}

type botCommand struct {
	Name        string `json:"command"`
	Description string `json:"description"`
}

// SetCommandMenu 在首个请求前验证整份菜单；空集合也按原位置整份覆盖。
func (i *instance) SetCommandMenu(ctx context.Context, menu adapter.CommandMenu) *errcode.Error {
	params, safe := commandMenuParams(menu.Scope, menu.LanguageCode)
	if safe != nil {
		return safe
	}
	if len(menu.Commands) > 100 {
		return invalidPlan("command menu exceeds 100 commands")
	}
	if menu.Scope.Type == "default" && len(menu.Commands) > 0 {
		return invalidPlan("managed default command menu must be empty")
	}
	commands := make([]botCommand, 0, len(menu.Commands))
	seen := make(map[string]bool)
	for _, command := range menu.Commands {
		if len(command.Name) < 1 || len(command.Name) > 32 {
			return invalidPlan("command must contain 1 to 32 lowercase letters, digits or underscores")
		}
		for _, c := range command.Name {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
				return invalidPlan("invalid Telegram command name: " + command.Name)
			}
		}
		if seen[command.Name] {
			return invalidPlan("duplicate command: " + command.Name)
		}
		seen[command.Name] = true
		if length := utf8.RuneCountInString(command.Description); length < 1 || length > 256 {
			return invalidPlan("command description must contain 1 to 256 characters")
		}
		commands = append(commands, botCommand{Name: command.Name, Description: command.Description})
	}
	params["commands"] = commands
	var accepted bool
	if err := i.client.call(ctx, "setMyCommands", params, &accepted); err != nil {
		return i.platformSendError(err)
	}
	if !accepted {
		return i.platformSendError(errors.New("telegram setMyCommands returned false"))
	}
	return nil
}

// GetCommandMenu 读取指定 scope 和语言的完整当前菜单。
func (i *instance) GetCommandMenu(ctx context.Context, scope adapter.CommandMenuScope, languageCode string) ([]adapter.CommandMenuEntry, *errcode.Error) {
	params, safe := commandMenuParams(scope, languageCode)
	if safe != nil {
		return nil, safe
	}
	var commands []botCommand
	if err := i.client.call(ctx, "getMyCommands", params, &commands); err != nil {
		return nil, i.platformSendError(err)
	}
	if commands == nil {
		return nil, i.platformSendError(errors.New("telegram getMyCommands result is not an array"))
	}
	result := make([]adapter.CommandMenuEntry, 0, len(commands))
	for _, command := range commands {
		result = append(result, adapter.CommandMenuEntry{Name: command.Name, Description: command.Description})
	}
	return result, nil
}

// DeleteCommandMenu 只删除显式位置，错误交给 Core 保留及重处理。
func (i *instance) DeleteCommandMenu(ctx context.Context, scope adapter.CommandMenuScope, languageCode string) *errcode.Error {
	params, safe := commandMenuParams(scope, languageCode)
	if safe != nil {
		return safe
	}
	var accepted bool
	if err := i.client.call(ctx, "deleteMyCommands", params, &accepted); err != nil {
		return i.platformSendError(err)
	}
	if !accepted {
		return i.platformSendError(errors.New("telegram deleteMyCommands returned false"))
	}
	return nil
}
