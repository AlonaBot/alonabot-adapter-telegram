// alonabot-adapter-telegram 命令运行官方 Telegram Adapter。
package main

import (
	"log/slog"
	"os"

	"github.com/AlonaBot/AlonaBot/adapters/telegram"
	"github.com/AlonaBot/AlonaBot/sdk"
)

func main() {
	if err := sdk.RunAdapter(telegram.New()); err != nil {
		slog.Error("telegram adapter stopped with an error", "error", err)
		os.Exit(1)
	}
}
