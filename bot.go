package main

import (
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

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

// Labels and destinations are user input, so never interpret them as Markdown.
func SendPlainMessage(chatID int64, text string) {
	runes := []rune(text)
	for len(runes) > 0 {
		n, units := 0, 0
		for n < len(runes) {
			next := 1
			if utf16.IsSurrogate(runes[n]) || runes[n] > 0xffff {
				next = 2
			}
			if units+next > 3800 {
				break
			}
			units += next
			n++
		}
		if _, err := bot.Send(tgbotapi.NewMessage(chatID, string(runes[:n]))); err != nil {
			log.Printf("发送监控消息失败: %v", err)
		}
		runes = runes[n:]
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

func handleMessage(env *AppEnv, msg *tgbotapi.Message) {
	cfg, err := GetConfig()
	if err != nil {
		SendMessage(msg.Chat.ID, "❌ Failed to read config. Please check the database.")
		return
	}

	text := strings.TrimSpace(msg.Text)

	// Interactive steps: 1-5=wizard, 6=sync input, 7=calibrate input, 8=calibrate confirm
	if cfg.SetupStep > 0 {
		if text == "/start" {
			UpdateConfig(func(c *AppConfig) { c.SetupStep = 1 })
			SendMessage(msg.Chat.ID, "Welcome! Please set your monthly bandwidth quota (GB or TB).\nExample: 500 GB or 1 TB")
			return
		}
		if text == "/cancel" && cfg.SetupStep >= 6 {
			UpdateConfig(func(c *AppConfig) { c.SetupStep = 0; clearPendingCalibration(c) })
			SendMessage(msg.Chat.ID, "Operation cancelled.")
			return
		}
		if strings.HasPrefix(text, "/") && cfg.SetupStep <= 5 {
			SendMessage(msg.Chat.ID, "⏳ Please complete the initial setup first.")
			return
		}
		switch cfg.SetupStep {
		case 1, 2, 3, 4, 5:
			handleWizard(msg.Chat.ID, cfg, text)
		case 6:
			handleSyncInput(env, msg.Chat.ID, text)
		case 7:
			handleCalibrateInput(env, msg.Chat.ID, text)
		case 8:
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
	case text == "/config":
		handleConfig(msg.Chat.ID)
	case strings.HasPrefix(text, "/sync"):
		handleSync(env, msg.Chat.ID, text)
	case strings.HasPrefix(text, "/calibrate"):
		handleCalibrate(env, msg.Chat.ID, text)
	case text == "/report":
		handleReport(env, msg.Chat.ID)
	case text == "/devices" || strings.HasPrefix(text, "/devices ") || text == "/sites" || strings.HasPrefix(text, "/sites "):
		handleMonitor(env, msg.Chat.ID, text)
	default:
		SendMessage(msg.Chat.ID, "Unknown command. Use /help to see available commands.")
	}
}

func handleWizard(chatID int64, cfg *AppConfig, text string) {
	switch cfg.SetupStep {
	case 1:
		bw, err := parseBandwidth(text)
		if err != nil {
			SendMessage(chatID, "❌ Invalid format. Example: 500 GB or 1 TB")
			return
		}
		UpdateConfig(func(c *AppConfig) { c.TotalBandwidth = bw; c.SetupStep = 2 })
		SendMessage(chatID, "✅ Bandwidth saved.\nPlease set the monthly reset day (1-31).\nExample: 15")
	case 2:
		day, err := strconv.Atoi(text)
		if err != nil || day < 1 || day > 31 {
			SendMessage(chatID, "❌ Please enter a number between 1 and 31.")
			return
		}
		UpdateConfig(func(c *AppConfig) { c.ResetDay = day; c.SetupStep = 3 })
		SendMessage(chatID, "✅ Reset day saved.\nPlease set the daily report time (24h format HH:MM).\nExample: 08:30")
	case 3:
		matched, _ := regexp.MatchString(`^\d{2}:\d{2}$`, text)
		if !matched {
			SendMessage(chatID, "❌ Invalid format. Example: 08:30")
			return
		}
		parts := strings.Split(text, ":")
		h, _ := strconv.Atoi(parts[0])
		m, _ := strconv.Atoi(parts[1])
		if h < 0 || h > 23 || m < 0 || m > 59 {
			SendMessage(chatID, "❌ Invalid time. Hours: 0-23, Minutes: 0-59.")
			return
		}
		UpdateConfig(func(c *AppConfig) { c.DailyPushTime = text; c.SetupStep = 4 })
		SendMessage(chatID, "✅ Push time saved.\nPlease set your local timezone (for push notifications).\nExample: `Asia/Shanghai`, `Asia/Tokyo`, `UTC`")
	case 4:
		tz := strings.TrimSpace(text)
		if _, err := time.LoadLocation(tz); err != nil {
			SendMessage(chatID, "❌ Invalid timezone. Use IANA format like `Asia/Shanghai`, `America/New_York`, or `UTC`.")
			return
		}
		UpdateConfig(func(c *AppConfig) { c.PushTimezone = tz; c.SetupStep = 5 })
		SendMessage(chatID, fmt.Sprintf("✅ Push timezone set to `%s`.\nNow set the billing timezone (when your provider resets traffic).\nExample: `America/New_York` for BandwagonHost DC9\n\nThis does NOT change your system clock.", tz))
	case 5:
		tz := strings.TrimSpace(text)
		if _, err := time.LoadLocation(tz); err != nil {
			SendMessage(chatID, "❌ Invalid timezone. Use IANA format like `America/New_York`, `Asia/Shanghai`, or `UTC`.")
			return
		}
		UpdateConfig(func(c *AppConfig) { c.BillingTimezone = tz; c.SetupStep = 0 })
		SendMessage(chatID, fmt.Sprintf("🎉 Setup complete! Billing timezone: `%s`.\nTraffic monitoring is now active. Use /status to check current usage or /help for all commands.", tz))
	}
}

func handleHelp(chatID int64) {
	help := `📋 *Traffic Bot Commands*

/status - View current billing cycle traffic status
/daily - View daily traffic breakdown
/devices - 今日各设备用量（按邮箱标签）
/sites - 今日网站分类用量，可加设备标签
/sync - Manually sync panel usage
/calibrate - Set calibration factor
/config - View current settings
/report - Send an immediate daily report
/settings - Reconfigure (bandwidth/reset day/push time/timezones)
/help - Show this help message`
	SendMessage(chatID, help)
}

func handleSettings(chatID int64) {
	UpdateConfig(func(c *AppConfig) { c.SetupStep = 1 })
	SendMessage(chatID, "Please set your monthly bandwidth quota (GB or TB).\nExample: 500 GB or 1 TB")
}

func handleConfig(chatID int64) {
	cfg, _ := GetConfig()
	pushTz := cfg.PushTimezone
	if pushTz == "" {
		pushTz = "System Local"
	}
	billingTz := cfg.BillingTimezone
	if billingTz == "" {
		billingTz = "System Local"
	}
	reply := fmt.Sprintf("⚙️ *Current Settings*\n\nBandwidth Quota: %s\nReset Day: %d of each month\nPush Time: %s (%s)\nBilling Timezone: %s\nCalibration Factor: %.4f",
		FormatBytes(cfg.TotalBandwidth), cfg.ResetDay, cfg.DailyPushTime, pushTz, billingTz, cfg.CalibrationFactor)
	SendMessage(chatID, reply)
}

// ==================== /sync ====================

func handleSync(env *AppEnv, chatID int64, text string) {
	parts := strings.Fields(text)
	if len(parts) == 3 {
		handleSyncInput(env, chatID, parts[1]+" "+parts[2])
		return
	}
	UpdateConfig(func(c *AppConfig) { c.SetupStep = 6 })
	SendMessage(chatID, "🔄 *Manual Sync*\n\nEnter the total usage shown on your VPS panel (MB / GB / TB).\nExample: `6.81 GB`\n\nSend /cancel to abort")
}

func handleSyncInput(env *AppEnv, chatID int64, text string) {
	actual, err := parseBandwidthAllUnits(text)
	if err != nil {
		SendMessage(chatID, "❌ Invalid format. Example: `6.81 GB` or `1.5 TB`")
		return
	}

	if err := SyncVnStatToDB(env.InterfaceName); err != nil {
		log.Printf("sync refresh: %v", err)
		SendMessage(chatID, "⚠️ Cannot refresh traffic; sync cancelled and previous baseline preserved.")
		return
	}
	cfg, start, end, err := GetCycleConfig()
	if err != nil {
		SendPlainMessage(chatID, "配置读取失败，本次同步未完成。")
		return
	}
	cycleBytes, err := GetCycleTraffic(start.Format("2006-01-02"), end.Format("2006-01-02"))
	if err != nil {
		SendPlainMessage(chatID, "流量读取失败，本次同步未完成。")
		return
	}

	// What user currently sees
	oldTotal := CalcTotalUsed(cfg, cycleBytes)

	// offset = sync值 - vnStat*倍率，这样 vnStat*factor + offset = actual
	factor := cfg.CalibrationFactor
	if factor <= 0 {
		factor = 1.0
	}
	newOffset := actual - float64(cycleBytes)*factor
	proposal := 0.0
	// Recommend factor only when meaningful:
	if oldTotal > 0 && cfg.UsageOffset == 0 {
		ratio := actual / oldTotal
		suggested := factor * ratio
		diff := ratio - 1.0
		if diff < 0 {
			diff = -diff
		}
		if diff > 0.01 && finiteNumber(suggested) && suggested > 0 {
			proposal = suggested
		}
	}
	synced := false
	err = UpdateConfig(func(c *AppConfig) {
		if c.LastResetCycle != cfg.LastResetCycle {
			return
		}
		c.UsageOffset, c.SetupStep = newOffset, 0
		clearPendingCalibration(c)
		if proposal > 0 {
			c.SetupStep = 8
			c.PendingFactor, c.PendingRawBytes, c.PendingUsage = proposal, cycleBytes, actual
		}
		synced = true
	})
	if err != nil || !synced {
		SendPlainMessage(chatID, "同步未完成：计费周期已变化或设置无法保存，请重新查询后再试。")
		return
	}
	reply := fmt.Sprintf("✅ Synced! Current cycle usage set to %s\nPrevious value: %s", FormatBytes(actual), FormatBytes(oldTotal))
	if proposal > 0 {
		reply += fmt.Sprintf("\n\nSuggested calibration factor: %.4f\nReply *yes* to apply, or anything else to skip", proposal)
	}
	SendMessage(chatID, reply)
}

// ==================== /calibrate ====================

func handleCalibrate(env *AppEnv, chatID int64, text string) {
	parts := strings.Fields(text)

	// Show current state first
	if err := SyncVnStatToDB(env.InterfaceName); err != nil {
		log.Printf("calibrate refresh: %v", err)
		SendMessage(chatID, "⚠️ Cannot refresh traffic; calibration cancelled.")
		return
	}
	cfg, _ := GetConfig()
	start, end := GetCycleDates(cfg.ResetDay, cfg.BillingTimezone)
	cycleBytes, _ := GetCycleTraffic(start.Format("2006-01-02"), end.Format("2006-01-02"))

	// Direct set: /calibrate 1.25
	if len(parts) == 2 {
		factor, err := strconv.ParseFloat(parts[1], 64)
		if err != nil || !finiteNumber(factor) || factor <= 0 {
			SendMessage(chatID, "❌ Factor must be a positive number, e.g. 1.25")
			return
		}
		UpdateConfig(func(c *AppConfig) { c.CalibrationFactor = factor; clearPendingCalibration(c) })
		SendMessage(chatID, fmt.Sprintf("✅ Calibration factor set to %.4f", factor))
		return
	}

	// Interactive mode
	totalUsed := CalcTotalUsed(cfg, cycleBytes)
	info := fmt.Sprintf("📐 *Calibration*\n\nCurrent factor: %.4f\nCurrent usage: %s\nvnStat raw: %s",
		cfg.CalibrationFactor, FormatBytes(totalUsed), FormatBytes(float64(cycleBytes)))

	info += "\n\nEnter calibration factor (e.g. `1.25` means local stats are low, multiply by 1.25)\nSend /cancel to abort"
	UpdateConfig(func(c *AppConfig) { c.SetupStep = 7 })
	SendMessage(chatID, info)
}

func handleCalibrateInput(env *AppEnv, chatID int64, text string) {
	factor, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil || !finiteNumber(factor) || factor <= 0 {
		SendMessage(chatID, "❌ Please enter a positive number, e.g. 1.25")
		return
	}
	UpdateConfig(func(c *AppConfig) {
		c.CalibrationFactor = factor
		c.SetupStep = 0
		clearPendingCalibration(c)
	})
	SendMessage(chatID, fmt.Sprintf("✅ Calibration factor set to %.4f", factor))
}

func handleCalibrateConfirm(chatID int64, text string) {
	text = strings.TrimSpace(text)
	if text == "是" || strings.ToLower(text) == "yes" || strings.ToLower(text) == "y" {
		factor, err := confirmCalibration(0)
		if err != nil {
			SendPlainMessage(chatID, "校准确认已失效或无法保存，请重新执行 /sync 或 /calibrate。")
			return
		}
		SendMessage(chatID, fmt.Sprintf("✅ Calibration factor set to %.4f", factor))
		return
	}
	// Try parsing as custom factor
	factor, err := strconv.ParseFloat(text, 64)
	if err == nil && finiteNumber(factor) && factor > 0 {
		if _, err = confirmCalibration(factor); err != nil {
			SendPlainMessage(chatID, "校准确认已失效或无法保存，请重新执行 /sync 或 /calibrate。")
			return
		}
		SendMessage(chatID, fmt.Sprintf("✅ Calibration factor set to %.4f", factor))
		return
	}
	UpdateConfig(func(c *AppConfig) { c.SetupStep = 0; clearPendingCalibration(c) })
	SendMessage(chatID, "Cancelled. Factor unchanged.")
}

func confirmCalibration(custom float64) (float64, error) {
	if !finiteNumber(custom) || custom < 0 {
		return 0, fmt.Errorf("invalid calibration factor")
	}
	var applied float64
	err := UpdateConfig(func(c *AppConfig) {
		if c.SetupStep == 8 && finiteNumber(c.PendingFactor) && c.PendingFactor > 0 {
			factor := c.PendingFactor
			if custom > 0 && finiteNumber(custom) {
				factor = custom
			}
			// The manual sync already set an offset. Re-anchor it to the
			// persisted sample so applying a factor never corrects it twice.
			c.CalibrationFactor = factor
			c.UsageOffset = c.PendingUsage - float64(c.PendingRawBytes)*factor
			applied = factor
		}
		c.SetupStep = 0
		clearPendingCalibration(c)
	})
	if err != nil {
		return 0, err
	}
	if applied <= 0 {
		return 0, fmt.Errorf("expired calibration confirmation")
	}
	return applied, nil
}

// ==================== /report ====================

func handleReport(env *AppEnv, chatID int64) {
	cfg, _ := GetConfig()
	sendDailyReport(env, cfg)
}

// ==================== /status & /daily ====================

func handleStatus(env *AppEnv, chatID int64) {
	if err := SyncVnStatToDB(env.InterfaceName); err != nil {
		log.Printf("status refresh: %v", err)
		SendMessage(chatID, "⚠️ Cannot refresh traffic. Check vnStat daemon logs; old totals are not a live reading.")
		return
	}

	cfg, start, end, err := GetCycleConfig()
	if err != nil {
		SendPlainMessage(chatID, "配置读取失败，请检查数据库。")
		return
	}
	startStr := start.Format("2006-01-02")
	endStr := end.Format("2006-01-02")
	cycleBytes, _ := GetCycleTraffic(startStr, endStr)

	totalUsed := CalcTotalUsed(cfg, cycleBytes)
	percent := 0.0
	if cfg.TotalBandwidth > 0 {
		percent = totalUsed / cfg.TotalBandwidth * 100
	}
	daysLeft := DaysUntilReset(cfg.ResetDay, cfg.BillingTimezone)
	bar := ProgressBar(percent, 20)

	reply := fmt.Sprintf("📊 *Traffic Status*\n\n%s %.1f%%\n\nUsed: %s / %s\nBilling Cycle: %s ~ %s\nDays until reset: %d",
		bar, percent,
		FormatBytes(totalUsed), FormatBytes(cfg.TotalBandwidth),
		startStr, endStr, daysLeft)

	if cfg.CalibrationFactor != 1.0 {
		reply += fmt.Sprintf("\nCalibration Factor: %.4f", cfg.CalibrationFactor)
	}
	SendMessage(chatID, reply)
}

func handleDaily(env *AppEnv, chatID int64) {
	if err := SyncVnStatToDB(env.InterfaceName); err != nil {
		log.Printf("daily refresh: %v", err)
		SendMessage(chatID, "⚠️ Cannot refresh traffic. Check vnStat daemon logs.")
		return
	}

	cfg, start, end, err := GetCycleConfig()
	if err != nil {
		SendPlainMessage(chatID, "配置读取失败，请检查数据库。")
		return
	}
	startStr := start.Format("2006-01-02")
	today := time.Now().Format("2006-01-02")
	endStr := today
	if end.Format("2006-01-02") < today {
		endStr = end.Format("2006-01-02")
	}

	logs, err := GetDailyLogs(startStr, endStr)
	if err != nil || len(logs) == 0 {
		SendMessage(chatID, "📭 No traffic data for the current cycle.")
		return
	}

	factor := cfg.CalibrationFactor
	if factor <= 0 {
		factor = 1.0
	}

	reply := "📅 *Daily Traffic (Current Cycle)*\nDates use the VPS timezone.\n\n"
	var totalAll int64
	for _, l := range logs {
		dayTotal := l.RxBytes + l.TxBytes
		totalAll += dayTotal
		adjusted := float64(dayTotal) * factor
		reply += fmt.Sprintf("`%s` | ↓%s ↑%s | Total %s\n",
			l.RecordDate,
			FormatBytes(float64(l.RxBytes)*factor),
			FormatBytes(float64(l.TxBytes)*factor),
			FormatBytes(adjusted))
	}

	// Show raw total (with factor) and the calibrated total (with factor + offset) if they differ
	rawTotal := float64(totalAll) * factor
	calibratedTotal := CalcTotalUsed(cfg, totalAll)
	reply += fmt.Sprintf("\nTotal: %s", FormatBytes(rawTotal))
	if cfg.UsageOffset != 0 {
		reply += fmt.Sprintf("\nSynced total: %s", FormatBytes(calibratedTotal))
	}
	SendMessage(chatID, reply)
}

// ==================== Parsers ====================

func parseBandwidthAllUnits(text string) (float64, error) {
	parts := strings.Fields(strings.TrimSpace(text))
	if len(parts) != 2 {
		return 0, fmt.Errorf("invalid format")
	}
	val, err := strconv.ParseFloat(parts[0], 64)
	if err != nil || !finiteNumber(val) || val < 0 {
		return 0, fmt.Errorf("invalid number")
	}
	return bandwidthValue(val, parts[1], true)
}

func bandwidthValue(val float64, unit string, allowMB bool) (float64, error) {
	var multiplier float64
	switch strings.ToUpper(unit) {
	case "MB":
		if !allowMB {
			return 0, fmt.Errorf("unsupported unit")
		}
		multiplier = bytesPerMB
	case "GB":
		multiplier = bytesPerGB
	case "TB":
		multiplier = bytesPerTB
	default:
		return 0, fmt.Errorf("unsupported unit")
	}
	result := val * multiplier
	if !finiteNumber(result) {
		return 0, fmt.Errorf("number outside supported range")
	}
	return result, nil
}

func parseBandwidth(text string) (float64, error) {
	parts := strings.Fields(strings.TrimSpace(text))
	if len(parts) != 2 {
		return 0, fmt.Errorf("invalid format")
	}
	val, err := strconv.ParseFloat(parts[0], 64)
	if err != nil || !finiteNumber(val) || val <= 0 {
		return 0, fmt.Errorf("invalid number")
	}
	return bandwidthValue(val, parts[1], false)
}
