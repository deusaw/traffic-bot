package main

import (
	"fmt"
	"log"
	"time"
)

// StartScheduler launches background goroutines for daily report, alert checking, and data cleanup.
func StartScheduler(env *AppEnv) {
	go dailyReportLoop(env)
	go alertCheckLoop(env)
}

// dailyReportLoop checks every minute if it's time to send the daily report.
func dailyReportLoop(env *AppEnv) {
	lastSentDate := ""
	for {
		time.Sleep(30 * time.Second)

		cfg, err := GetConfig()
		if err != nil || cfg.SetupStep > 0 {
			continue
		}

		// Push time uses system local clock (user's real time)
		// Billing cycle dates use cfg.Timezone (provider's reset timezone)
		now := time.Now()
		todayStr := now.Format("2006-01-02")
		currentTime := now.Format("15:04")

		if currentTime == cfg.DailyPushTime && lastSentDate != todayStr {
			lastSentDate = todayStr
			sendDailyReport(env, cfg)
			runDataCleanup(cfg)
			resetAlertIfNewCycle(cfg)
		}
	}
}

func sendDailyReport(env *AppEnv, cfg *AppConfig) {
	// Sync latest vnStat data
	if err := SyncVnStatToDB(env.InterfaceName); err != nil {
		log.Printf("日报同步失败: %v", err)
	}

	yesterdayBytes, _ := GetYesterdayTraffic(cfg.Timezone)
	start, end := GetCycleDates(cfg.ResetDay, cfg.Timezone)
	cycleBytes, _ := GetCycleTraffic(start.Format("2006-01-02"), end.Format("2006-01-02"))
	totalUsed := CalcTotalUsed(cfg, cycleBytes)
	daysLeft := DaysUntilReset(cfg.ResetDay, cfg.Timezone)

	percent := 0.0
	if cfg.TotalBandwidth > 0 {
		percent = totalUsed / cfg.TotalBandwidth * 100
	}

	report := fmt.Sprintf(`📊 *Daily Traffic Report*

Yesterday: %s
Cycle total: %s / %s (%.1f%%)
Days until reset: %d`,
		FormatBytes(float64(yesterdayBytes)*cfg.CalibrationFactor),
		FormatBytes(totalUsed),
		FormatBytes(cfg.TotalBandwidth),
		percent,
		daysLeft)

	SendMessage(env.AuthChatID, report)
	log.Println("每日日报已推送")
}

func runDataCleanup(cfg *AppConfig) {
	prevStart := GetPrevCycleStart(cfg.ResetDay, cfg.Timezone)
	if err := CleanOldData(prevStart.Format("2006-01-02")); err != nil {
		log.Printf("数据清理失败: %v", err)
	} else {
		log.Println("历史数据清理完成")
	}
}

// resetAlertIfNewCycle resets alert and offset when a NEW billing cycle begins.
// Compares current cycle start date with last recorded reset cycle to ensure one-time reset.
func resetAlertIfNewCycle(cfg *AppConfig) {
	start, _ := GetCycleDates(cfg.ResetDay, cfg.Timezone)
	cycleStart := start.Format("2006-01-02")

	// Only reset if this is a different cycle than the last one we reset for
	if cycleStart != cfg.LastResetCycle {
		UpdateConfig(func(c *AppConfig) {
			c.LastAlertPercent = 0
			c.UsageOffset = 0
			c.LastResetCycle = cycleStart
		})
		log.Println("新计费周期，告警百分比和同步偏移已重置")
	}
}

// alertCheckLoop runs every 5 minutes to check if traffic thresholds are crossed.
func alertCheckLoop(env *AppEnv) {
	for {
		time.Sleep(5 * time.Minute)

		cfg, err := GetConfig()
		if err != nil || cfg.SetupStep > 0 || cfg.TotalBandwidth <= 0 {
			continue
		}

		// Sync data
		SyncVnStatToDB(env.InterfaceName)

		start, end := GetCycleDates(cfg.ResetDay, cfg.Timezone)
		cycleBytes, _ := GetCycleTraffic(start.Format("2006-01-02"), end.Format("2006-01-02"))
		totalUsed := CalcTotalUsed(cfg, cycleBytes)

		percent := totalUsed / cfg.TotalBandwidth * 100

		// Check thresholds: 10, 20, 30 ... 100
		currentThreshold := int(percent/10) * 10
		if currentThreshold > 100 {
			currentThreshold = 100
		}

		if currentThreshold > cfg.LastAlertPercent && currentThreshold >= 10 {
			// Send alerts for each missed threshold
			for t := cfg.LastAlertPercent + 10; t <= currentThreshold; t += 10 {
				alert := fmt.Sprintf("⚠️ Traffic Alert: %d%% of billing cycle quota used!\nUsed: %s / Total: %s",
					t,
					FormatBytes(totalUsed),
					FormatBytes(cfg.TotalBandwidth))
				SendMessage(env.AuthChatID, alert)
				log.Printf("告警已发送: %d%%", t)
			}
			UpdateConfig(func(c *AppConfig) { c.LastAlertPercent = currentThreshold })
		}
	}
}
