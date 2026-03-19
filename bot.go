package main

import (
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

var bot *tgbotapi.BotAPI

func InitBot(token string) error {
	var err error
	bot, err = tgbotapi.NewBotAPI(token)
	if err != nil {
		return fmt.Errorf("Telegram Bot 初始化失败: %v", err)
	}
	log.Printf("Bot 已登录: %s", bot.Self.UserName)
	return nil
}

func SendMessage(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown"
	if _, err := bot.Send(msg); err != nil {
		log.Printf("发送消息失败: %v", err)
	}
}

func StartPolling(env *AppEnv) {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil {
			continue
		}
		if update.Message.Chat.ID != env.AuthChatID {
			log.Printf("未授权访问: ChatID=%d", update.Message.Chat.ID)
			continue
		}
		handleMessage(env, update.Message)
	}
}

// pendingFactor stores proposed calibration factor between steps 5→6.
var pendingFactor float64

func handleMessage(env *AppEnv, msg *tgbotapi.Message) {
	cfg, err := GetConfig()
	if err != nil {
		SendMessage(msg.Chat.ID, "❌ 读取配置失败，请检查数据库。")
		return
	}

	text := strings.TrimSpace(msg.Text)

	// Interactive steps: 1-3=wizard, 4=sync input, 5=calibrate input, 6=calibrate confirm
	if cfg.SetupStep > 0 {
		if text == "/start" {
			UpdateConfig(func(c *AppConfig) { c.SetupStep = 1 })
			SendMessage(msg.Chat.ID, "欢迎使用！请先设置您的套餐每月总流量配额 (支持 GB 或 TB)。\n格式示例：500 GB 或 1 TB")
			return
		}
		if text == "/cancel" && cfg.SetupStep >= 4 {
			UpdateConfig(func(c *AppConfig) { c.SetupStep = 0 })
			SendMessage(msg.Chat.ID, "已取消操作。")
			return
		}
		if strings.HasPrefix(text, "/") && cfg.SetupStep <= 3 {
			SendMessage(msg.Chat.ID, "⏳ 请先完成初始化设置，再使用其他指令。")
			return
		}
		switch cfg.SetupStep {
		case 1, 2, 3:
			handleWizard(msg.Chat.ID, cfg, text)
		case 4:
			handleSyncInput(env, msg.Chat.ID, text)
		case 5:
			handleCalibrateInput(env, msg.Chat.ID, text)
		case 6:
			handleCalibrateConfirm(msg.Chat.ID, text)
		}
		return
	}

	// Normal command routing
	switch {
	case text == "/start" || text == "/help":
		handleHelp(msg.Chat.ID)
	case text == "/status":
		handleStatus(env, msg.Chat.ID)
	case text == "/daily":
		handleDaily(env, msg.Chat.ID)
	case text == "/settings":
		handleSettings(msg.Chat.ID)
	case strings.HasPrefix(text, "/sync"):
		handleSync(env, msg.Chat.ID, text)
	case strings.HasPrefix(text, "/calibrate"):
		handleCalibrate(env, msg.Chat.ID, text)
	default:
		SendMessage(msg.Chat.ID, "未知指令，请使用 /help 查看可用命令。")
	}
}

func handleWizard(chatID int64, cfg *AppConfig, text string) {
	switch cfg.SetupStep {
	case 1:
		bw, err := parseBandwidth(text)
		if err != nil {
			SendMessage(chatID, "❌ 格式错误，请输入如：500 GB 或 1 TB")
			return
		}
		UpdateConfig(func(c *AppConfig) { c.TotalBandwidth = bw; c.SetupStep = 2 })
		SendMessage(chatID, "✅ 总流量已保存。\n请设置每月的流量重置日期 (1-31)。\n格式示例：15")
	case 2:
		day, err := strconv.Atoi(text)
		if err != nil || day < 1 || day > 31 {
			SendMessage(chatID, "❌ 请输入 1-31 之间的数字。")
			return
		}
		UpdateConfig(func(c *AppConfig) { c.ResetDay = day; c.SetupStep = 3 })
		SendMessage(chatID, "✅ 重置日已保存。\n请设置每天接收流量日报的时间 (24小时制 HH:MM)。\n格式示例：08:30")
	case 3:
		matched, _ := regexp.MatchString(`^\d{2}:\d{2}$`, text)
		if !matched {
			SendMessage(chatID, "❌ 格式错误，请输入如：08:30")
			return
		}
		parts := strings.Split(text, ":")
		h, _ := strconv.Atoi(parts[0])
		m, _ := strconv.Atoi(parts[1])
		if h < 0 || h > 23 || m < 0 || m > 59 {
			SendMessage(chatID, "❌ 时间无效，小时 0-23，分钟 0-59。")
			return
		}
		UpdateConfig(func(c *AppConfig) { c.DailyPushTime = text; c.SetupStep = 0 })
		SendMessage(chatID, "🎉 配置完成！系统已开始监控您的 VPS 流量。您随时可以使用 /status 查看当前状态，或使用 /help 查看更多指令。")
	}
}

func handleHelp(chatID int64) {
	help := `📋 *Traffic Bot 指令列表*

/status - 查看当前计费周期流量状态
/daily - 查看当前周期每日流量明细
/sync - 手动同步面板实际用量
/calibrate - 设置流量校准倍率
/settings - 重新配置（总流量/重置日/推送时间）
/help - 显示此帮助信息`
	SendMessage(chatID, help)
}

func handleSettings(chatID int64) {
	UpdateConfig(func(c *AppConfig) { c.SetupStep = 1 })
	SendMessage(chatID, "请设置您的套餐每月总流量配额 (支持 GB 或 TB)。\n格式示例：500 GB 或 1 TB")
}

// ==================== /sync ====================

func handleSync(env *AppEnv, chatID int64, text string) {
	parts := strings.Fields(text)
	if len(parts) == 3 {
		handleSyncInput(env, chatID, parts[1]+" "+parts[2])
		return
	}
	UpdateConfig(func(c *AppConfig) { c.SetupStep = 4 })
	SendMessage(chatID, "🔄 *手动同步*\n\n请输入 VPS 面板上显示的当前周期已用总流量（支持 MB / GB / TB）。\n格式示例：`6.81 GB`\n\n发送 /cancel 取消")
}

func handleSyncInput(env *AppEnv, chatID int64, text string) {
	actual, err := parseBandwidthAllUnits(text)
	if err != nil {
		SendMessage(chatID, "❌ 格式错误，请输入如：`6.81 GB` 或 `1.5 TB`")
		return
	}

	SyncVnStatToDB(env.InterfaceName)
	cfg, _ := GetConfig()
	start, end := GetCycleDates(cfg.ResetDay)
	cycleBytes, _ := GetCycleTraffic(start.Format("2006-01-02"), end.Format("2006-01-02"))

	UpdateConfig(func(c *AppConfig) {
		c.SyncUsage = actual
		c.SyncLocalBase = float64(cycleBytes)
		c.SetupStep = 0
	})

	// If local data exists, suggest a calibration factor
	reply := fmt.Sprintf("✅ 已同步！当前周期用量已设为 %s", FormatBytes(actual))
	if cycleBytes > 0 {
		suggestedFactor := actual / float64(cycleBytes)
		reply += fmt.Sprintf("\n\n本地统计：%s\n建议校准倍率：%.4f\n如需应用，请使用 /calibrate",
			FormatBytes(float64(cycleBytes)), suggestedFactor)
	}
	SendMessage(chatID, reply)
}

// ==================== /calibrate ====================

func handleCalibrate(env *AppEnv, chatID int64, text string) {
	parts := strings.Fields(text)

	// Show current state first
	SyncVnStatToDB(env.InterfaceName)
	cfg, _ := GetConfig()
	start, end := GetCycleDates(cfg.ResetDay)
	cycleBytes, _ := GetCycleTraffic(start.Format("2006-01-02"), end.Format("2006-01-02"))

	// Direct set: /calibrate 1.25
	if len(parts) == 2 {
		factor, err := strconv.ParseFloat(parts[1], 64)
		if err != nil || factor <= 0 {
			SendMessage(chatID, "❌ 倍率必须是大于 0 的数字，如：1.25")
			return
		}
		UpdateConfig(func(c *AppConfig) { c.CalibrationFactor = factor })
		SendMessage(chatID, fmt.Sprintf("✅ 校准倍率已设为 %.4f", factor))
		return
	}

	// Interactive mode
	info := fmt.Sprintf("📐 *流量校准*\n\n当前倍率：%.4f\n本地统计：%s",
		cfg.CalibrationFactor, FormatBytes(float64(cycleBytes)))

	if cfg.SyncUsage > 0 && cycleBytes > 0 {
		suggested := cfg.SyncUsage / float64(cycleBytes)
		info += fmt.Sprintf("\n上次同步用量：%s\n建议倍率：%.4f\n\n回复 *是* 应用建议倍率，或输入自定义倍率（如 `1.25`）\n发送 /cancel 取消",
			FormatBytes(cfg.SyncUsage), suggested)
		pendingFactor = suggested
		UpdateConfig(func(c *AppConfig) { c.SetupStep = 6 })
	} else {
		info += "\n\n请输入校准倍率（如 `1.25` 表示本地统计偏低需乘以1.25）\n发送 /cancel 取消"
		UpdateConfig(func(c *AppConfig) { c.SetupStep = 5 })
	}
	SendMessage(chatID, info)
}

func handleCalibrateInput(env *AppEnv, chatID int64, text string) {
	factor, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil || factor <= 0 {
		SendMessage(chatID, "❌ 请输入大于 0 的数字，如：1.25")
		return
	}
	UpdateConfig(func(c *AppConfig) {
		c.CalibrationFactor = factor
		c.SetupStep = 0
	})
	SendMessage(chatID, fmt.Sprintf("✅ 校准倍率已设为 %.4f", factor))
}

func handleCalibrateConfirm(chatID int64, text string) {
	text = strings.TrimSpace(text)
	if text == "是" || strings.ToLower(text) == "yes" || strings.ToLower(text) == "y" {
		UpdateConfig(func(c *AppConfig) {
			c.CalibrationFactor = pendingFactor
			c.SetupStep = 0
		})
		SendMessage(chatID, fmt.Sprintf("✅ 校准倍率已设为 %.4f", pendingFactor))
		return
	}
	// Try parsing as custom factor
	factor, err := strconv.ParseFloat(text, 64)
	if err == nil && factor > 0 {
		UpdateConfig(func(c *AppConfig) {
			c.CalibrationFactor = factor
			c.SetupStep = 0
		})
		SendMessage(chatID, fmt.Sprintf("✅ 校准倍率已设为 %.4f", factor))
		return
	}
	UpdateConfig(func(c *AppConfig) { c.SetupStep = 0 })
	SendMessage(chatID, "已取消，倍率未变更。")
}

// ==================== /status & /daily ====================

func handleStatus(env *AppEnv, chatID int64) {
	cfg, _ := GetConfig()
	SyncVnStatToDB(env.InterfaceName)

	start, end := GetCycleDates(cfg.ResetDay)
	startStr := start.Format("2006-01-02")
	endStr := end.Format("2006-01-02")
	cycleBytes, _ := GetCycleTraffic(startStr, endStr)

	totalUsed := CalcTotalUsed(cfg, cycleBytes)
	percent := 0.0
	if cfg.TotalBandwidth > 0 {
		percent = totalUsed / cfg.TotalBandwidth * 100
	}
	daysLeft := DaysUntilReset(cfg.ResetDay)
	bar := ProgressBar(percent, 20)

	reply := fmt.Sprintf("📊 *流量状态*\n\n%s %.1f%%\n\n已用：%s / %s\n计费周期：%s ~ %s\n距离重置还有：%d 天",
		bar, percent,
		FormatBytes(totalUsed), FormatBytes(cfg.TotalBandwidth),
		startStr, endStr, daysLeft)

	if cfg.CalibrationFactor != 1.0 {
		reply += fmt.Sprintf("\n校准倍率：%.4f", cfg.CalibrationFactor)
	}
	if cfg.SyncUsage > 0 {
		reply += fmt.Sprintf("\n同步基准：%s", FormatBytes(cfg.SyncUsage))
	}
	SendMessage(chatID, reply)
}

func handleDaily(env *AppEnv, chatID int64) {
	cfg, _ := GetConfig()
	SyncVnStatToDB(env.InterfaceName)

	start, end := GetCycleDates(cfg.ResetDay)
	startStr := start.Format("2006-01-02")
	today := time.Now().Format("2006-01-02")
	endStr := today
	if end.Format("2006-01-02") < today {
		endStr = end.Format("2006-01-02")
	}

	logs, err := GetDailyLogs(startStr, endStr)
	if err != nil || len(logs) == 0 {
		SendMessage(chatID, "📭 当前周期暂无流量数据。")
		return
	}

	factor := cfg.CalibrationFactor
	if factor <= 0 {
		factor = 1.0
	}

	reply := "📅 *当前周期每日流量*\n\n"
	var totalAll int64
	for _, l := range logs {
		dayTotal := l.RxBytes + l.TxBytes
		totalAll += dayTotal
		adjusted := float64(dayTotal) * factor
		reply += fmt.Sprintf("`%s` | ↓%s ↑%s | 合计 %s\n",
			l.RecordDate,
			FormatBytes(float64(l.RxBytes)*factor),
			FormatBytes(float64(l.TxBytes)*factor),
			FormatBytes(adjusted))
	}
	reply += fmt.Sprintf("\n合计：%s", FormatBytes(float64(totalAll)*factor))
	SendMessage(chatID, reply)
}

// ==================== Parsers ====================

func parseBandwidthAllUnits(text string) (float64, error) {
	parts := strings.Fields(strings.TrimSpace(text))
	if len(parts) != 2 {
		return 0, fmt.Errorf("invalid format")
	}
	val, err := strconv.ParseFloat(parts[0], 64)
	if err != nil || val < 0 {
		return 0, fmt.Errorf("invalid number")
	}
	switch strings.ToUpper(parts[1]) {
	case "MB":
		return val * bytesPerMB, nil
	case "GB":
		return val * bytesPerGB, nil
	case "TB":
		return val * bytesPerTB, nil
	default:
		return 0, fmt.Errorf("unsupported unit")
	}
}

func parseBandwidth(text string) (float64, error) {
	parts := strings.Fields(strings.TrimSpace(text))
	if len(parts) != 2 {
		return 0, fmt.Errorf("invalid format")
	}
	val, err := strconv.ParseFloat(parts[0], 64)
	if err != nil || val <= 0 {
		return 0, fmt.Errorf("invalid number")
	}
	switch strings.ToUpper(parts[1]) {
	case "GB":
		return val * bytesPerGB, nil
	case "TB":
		return val * bytesPerTB, nil
	default:
		return 0, fmt.Errorf("unsupported unit")
	}
}
