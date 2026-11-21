// Package telegram 提供官方平台适配器的公开构造入口。
package telegram

import (
	"github.com/AlonaBot/AlonaBot/sdk/adapter"
	implementation "github.com/AlonaBot/alonabot-adapter-telegram/internal/telegram"
)

// AdapterID 标识平台名称。
const AdapterID = implementation.AdapterID

// component 保持入口包身份，业务实现由内部适配器持有。
type component struct{ adapter.Adapter }

// New 创建独立的适配器。
func New() adapter.Adapter { return &component{implementation.New()} }
