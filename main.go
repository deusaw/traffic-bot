package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	log.Println("Traffic Bot 启动中...")

	// 1. Load environment config
	env, err := LoadEnv()
	if err != nil {
		log.Fatalf("配置加载失败: %v", err)
	}

	// 2. Initialize SQLite
	if err := InitDB(env.DBPath); err != nil {
		log.Fatalf("数据库初始化失败: %v", err)
	}
	defer CloseDB()
	log.Println("数据库已就绪")

	// 3. Initialize Telegram Bot
	if err := InitBot(env.BotToken); err != nil {
		log.Fatalf("Bot 初始化失败: %v", err)
	}

	// 4. Initial vnStat sync
	if err := SyncVnStatToDB(env.InterfaceName); err != nil {
		log.Printf("初始 vnStat 同步警告: %v (可能 vnStat 尚未有数据)", err)
	}

	// 5. Start background scheduler (daily report + alert check)
	StartScheduler(env)
	log.Println("定时任务已启动")

	// 6. Handle graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// 7. Start Telegram polling in background
	go StartPolling(env)
	log.Println("开始接收 Telegram 消息...")

	// Block until signal
	sig := <-sigCh
	log.Printf("收到信号 %v，正在关闭...", sig)
}
