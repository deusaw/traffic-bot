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

	// If in setup wizard, handle wizard steps (except /start which restarts it)
	if cfg.SetupStep > 0 {
		if text == "/start" {
			// Restart wizard
			UpdateConfig(func(c *AppConfig) { c.SetupStep = 1 })
			SendMessage(msg.Chat.ID, "欢迎使用！请先设置您的套餐每月总流量配额 (支持 GB 或 TB)。\n格式示例：500 GB 或 1 TB")
			return
		}
		if strings.HasPrefix(text, "/") {
			SendMessage(msg.Chat.ID, "⏳ 请先完成初始化设置，再使用其他指令。")
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
/calibrate <数值> <单位> - 校准流量偏移量
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
	// Expected: /calibrate 450 GB
	parts := strings.Fields(text)
	if len(parts) != 3 {
		SendMessage(chatID, "❌ 格式：/calibrate <数值> <GB|TB>\n例如：/calibrate 450 GB")
		return
	}

	val, err := strconv.ParseFloat(parts[1], 64)
	if err != nil {
		SendMessage(chatID, "❌ 数值格式错误。")
		return
	}

	unit := strings.ToUpper(parts[2])
	var actualBytes float64
	switch unit {
	case "GB":
		actualBytes = val * bytesPerGB
	case "TB":
		actualBytes = val * bytesPerTB
	case "MB":
		actualBytes = val * bytesPerMB
	default:
		SendMessage(chatID, "❌ 单位仅支持 MB / GB / TB。")
		return
	}

	cfg, _ := GetConfig()
	SyncVnStatToDB(env.InterfaceName)

	start, end := GetCycleDates(cfg.ResetDay)
	cycleBytes, _ := GetCycleTraffic(start.Format("2006-01-02"), end.Format("2006-01-02"))

	offset := actualBytes - float64(cycleBytes)
	UpdateConfig(func(c *AppConfig) { c.CalibrationOffset = offset })

	SendMessage(chatID, fmt.Sprintf("✅ 校准完成！\n本地统计：%s\n实际用量：%s\n偏移量：%s",
		FormatBytes(float64(cycleBytes)),
		FormatBytes(actualBytes),
		FormatBytes(offset)))
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
