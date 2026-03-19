package main

import (
	"fmt"
	"os"
	"strconv"
)

// AppEnv holds environment-level configuration loaded at startup.
type AppEnv struct {
	BotToken      string // Telegram Bot API token
	AuthChatID    int64  // Authorized Telegram Chat ID
	InterfaceName string // Network interface to monitor (default: eth0)
	DBPath        string // SQLite database file path
}

func LoadEnv() (*AppEnv, error) {
	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("环境变量 BOT_TOKEN 未设置")
	}

	chatIDStr := os.Getenv("CHAT_ID")
	if chatIDStr == "" {
		return nil, fmt.Errorf("环境变量 CHAT_ID 未设置")
	}
	chatID, err := strconv.ParseInt(chatIDStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("CHAT_ID 格式错误: %v", err)
	}

	iface := os.Getenv("INTERFACE")
	if iface == "" {
		iface = "eth0"
	}

	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "/opt/traffic-bot/traffic-bot.db"
	}

	return &AppEnv{
		BotToken:      token,
		AuthChatID:    chatID,
		InterfaceName: iface,
		DBPath:        dbPath,
	}, nil
}
