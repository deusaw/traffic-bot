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

// StartPolling begins the main update loop.
func StartPolling(env *AppEnv) {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil {
			continue
		}
		// Auth check
		if update.Message.Chat.ID != env.AuthChatID {
			log.Printf("未授权访问: ChatID=%d", update.Message.Chat.ID)
			continue
		}
		handleMessage(env, update.Message)
	}
}

func handleMessage(env *AppEnv, msg *tgbotapi.Message) {
	cfg, err := GetConfig()
	if err != nil {
		SendMessage(msg.Chat.ID, "❌ 读取配置失败，请检查数据库。")
		return
	}

	text := strings.TrimSpace(msg.Text)

	// If in setup wizard (1-3) or calibrate flow (4=input, 5=confirm)
	if cfg.SetupStep > 0 {
		if text == "/start" {
			UpdateConfig(func(c *AppConfig) { c.SetupStep = 1 })
			SendMessage(msg.Chat.ID, "欢迎使用！请先设置您的套餐每月总流量配额 (支持 GB 或 TB)。\n格式示例：500 GB 或 1 TB")
			return
		}
		// Allow /cancel to exit calibrate mode
		if (cfg.SetupStep == 4 || cfg.SetupStep == 5) && text == "/cancel" {
			UpdateConfig(func(c *AppConfig) { c.SetupStep = 0 })
			SendMessage(msg.Chat.ID, "已取消同步。")
			return
		}
		if strings.HasPrefix(text, "/") && cfg.SetupStep <= 3 {
			SendMessage(msg.Chat.ID, "⏳ 请先完成初始化设置，再使用其他指令。")
			return
		}
		if cfg.SetupStep == 4 {
			handleCalibrateInput(env, msg.Chat.ID, text)
			return
		}
		if cfg.SetupStep == 5 {
			handleCalibrateConfirm(env, msg.Chat.ID, text)
			return
		}
		handleWizard(msg.Chat.ID, cfg, text)
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
	case strings.HasPrefix(text, "/calibrate"):
		handleCalibrate(env, msg.Chat.ID, text)
	default:
		// Could be a settings update if setup_step was set by /settings
		SendMessage(msg.Chat.ID, "未知指令，请使用 /help 查看可用命令。")
	}
}

func handleWizard(chatID int64, cfg *AppConfig, text string) {
	switch cfg.SetupStep {
	case 1: // Awaiting total bandwidth
		bw, err := parseBandwidth(text)
		if err != nil {
			SendMessage(chatID, "❌ 格式错误，请输入如：500 GB 或 1 TB")
			return
		}
		UpdateConfig(func(c *AppConfig) {
			c.TotalBandwidth = bw
			c.SetupStep = 2
		})
		SendMessage(chatID, "✅ 总流量已保存。\n请设置每月的流量重置日期 (1-31)。\n格式示例：15")

	case 2: // Awaiting reset day
		day, err := strconv.Atoi(text)
		if err != nil || day < 1 || day > 31 {
			SendMessage(chatID, "❌ 请输入 1-31 之间的数字。")
			return
		}
		UpdateConfig(func(c *AppConfig) {
			c.ResetDay = day
			c.SetupStep = 3
		})
		SendMessage(chatID, "✅ 重置日已保存。\n请设置每天接收流量日报的时间 (24小时制 HH:MM)。\n格式示例：08:30")

	case 3: // Awaiting push time
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
		UpdateConfig(func(c *AppConfig) {
			c.DailyPushTime = text
			c.SetupStep = 0
		})
		SendMessage(chatID, "🎉 配置完成！系统已开始监控您的 VPS 流量。您随时可以使用 /status 查看当前状态，或使用 /help 查看更多指令。")
	}
}

func handleHelp(chatID int64) {
	help := `📋 *Traffic Bot 指令列表*

/status - 查看当前计费周期流量状态
/daily - 查看当前周期每日流量明细
/settings - 重新配置（总流量/重置日/推送时间）
/calibrate - 同步面板实际用量（自动计算偏移量）
/help - 显示此帮助信息`
	SendMessage(chatID, help)
}

func handleStatus(env *AppEnv, chatID int64) {
	cfg, _ := GetConfig()
	// Sync latest data
	SyncVnStatToDB(env.InterfaceName)

	start, end := GetCycleDates(cfg.ResetDay)
	startStr := start.Format("2006-01-02")
	endStr := end.Format("2006-01-02")

	cycleBytes, _ := GetCycleTraffic(startStr, endStr)
	totalUsed := float64(cycleBytes) + cfg.CalibrationOffset
	if totalUsed < 0 {
		totalUsed = 0
	}

	percent := 0.0
	if cfg.TotalBandwidth > 0 {
		percent = totalUsed / cfg.TotalBandwidth * 100
	}
	daysLeft := DaysUntilReset(cfg.ResetDay)
	bar := ProgressBar(percent, 20)

	reply := fmt.Sprintf(`📊 *流量状态*

%s %.1f%%

已用：%s / %s
计费周期：%s ~ %s
距离重置还有：%d 天`,
		bar, percent,
		FormatBytes(totalUsed), FormatBytes(cfg.TotalBandwidth),
		startStr, endStr,
		daysLeft)

	if cfg.CalibrationOffset != 0 {
		reply += fmt.Sprintf("\n校准偏移量：%s", FormatBytes(cfg.CalibrationOffset))
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

	reply := "📅 *当前周期每日流量*\n\n"
	var totalAll int64
	for _, l := range logs {
		dayTotal := l.RxBytes + l.TxBytes
		totalAll += dayTotal
		reply += fmt.Sprintf("`%s` | ↓%s ↑%s | 合计 %s\n",
			l.RecordDate,
			FormatBytes(float64(l.RxBytes)),
			FormatBytes(float64(l.TxBytes)),
			FormatBytes(float64(dayTotal)))
	}
	reply += fmt.Sprintf("\n合计：%s", FormatBytes(float64(totalAll)))
	SendMessage(chatID, reply)
}

func handleSettings(chatID int64) {
	UpdateConfig(func(c *AppConfig) { c.SetupStep = 1 })
	SendMessage(chatID, "欢迎使用！请先设置您的套餐每月总流量配额 (支持 GB 或 TB)。\n格式示例：500 GB 或 1 TB")
}

func handleCalibrate(env *AppEnv, chatID int64, text string) {
	parts := strings.Fields(text)
	if len(parts) == 3 {
		// Inline shortcut: /calibrate 6.81 GB — go straight to input handling
		handleCalibrateInput(env, chatID, parts[1]+" "+parts[2])
		return
	}
	// Enter conversational mode — step 4: awaiting actual usage input
	UpdateConfig(func(c *AppConfig) { c.SetupStep = 4 })
	SendMessage(chatID, "🔄 *流量同步*\n\n请输入 VPS 面板上显示的当前周期已用流量（支持 MB / GB / TB）。\n格式示例：`6.81 GB`\n\n发送 /cancel 取消")
}

// pendingOffset stores the proposed offset between conversational steps (step 4 → 5).
var pendingOffset float64
var pendingActual float64

func handleCalibrateInput(env *AppEnv, chatID int64, text string) {
	actual, err := parseBandwidthAllUnits(text)
	if err != nil {
		SendMessage(chatID, "❌ 格式错误，请输入如：`6.81 GB` 或 `1.5 TB`")
		return
	}

	cfg, _ := GetConfig()
	SyncVnStatToDB(env.InterfaceName)

	start, end := GetCycleDates(cfg.ResetDay)
	cycleBytes, _ := GetCycleTraffic(start.Format("2006-01-02"), end.Format("2006-01-02"))
	localTotal := float64(cycleBytes) + cfg.CalibrationOffset
	if localTotal < 0 {
		localTotal = 0
	}

	offset := actual - float64(cycleBytes)
	pendingOffset = offset
	pendingActual = actual

	diff := actual - localTotal

	reply := fmt.Sprintf("📊 *同步对比*\n\n"+
		"面板实际用量：%s\n"+
		"本地统计用量：%s\n",
		FormatBytes(actual),
		FormatBytes(localTotal))

	if cfg.CalibrationOffset != 0 {
		reply += fmt.Sprintf("当前偏移量：%s\n", FormatBytes(cfg.CalibrationOffset))
	}

	if diff > 0 {
		reply += fmt.Sprintf("差额：+%s\n", FormatBytes(diff))
	} else if diff < 0 {
		reply += fmt.Sprintf("差额：%s\n", FormatBytes(diff))
	} else {
		reply += "差额：无，数据一致 ✅\n"
		UpdateConfig(func(c *AppConfig) { c.SetupStep = 0 })
		SendMessage(chatID, reply+"\n无需校准。")
		return
	}

	reply += fmt.Sprintf("\n建议偏移量：%s\n\n回复 *是* 应用此偏移量，或 /cancel 取消", FormatBytes(offset))

	UpdateConfig(func(c *AppConfig) { c.SetupStep = 5 })
	SendMessage(chatID, reply)
}

func handleCalibrateConfirm(env *AppEnv, chatID int64, text string) {
	text = strings.TrimSpace(text)
	if text == "是" || strings.ToLower(text) == "yes" || strings.ToLower(text) == "y" {
		UpdateConfig(func(c *AppConfig) {
			c.CalibrationOffset = pendingOffset
			c.SetupStep = 0
		})
		SendMessage(chatID, fmt.Sprintf("✅ 同步完成！偏移量已设为 %s\n当前实际用量：%s",
			FormatBytes(pendingOffset),
			FormatBytes(pendingActual)))
	} else {
		UpdateConfig(func(c *AppConfig) { c.SetupStep = 0 })
		SendMessage(chatID, "已取消同步，偏移量未变更。")
	}
}

// parseBandwidthAllUnits parses "6.81 GB", "500 MB", "1 TB" into bytes.
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

// parseBandwidth parses "500 GB" or "1 TB" into bytes.
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
