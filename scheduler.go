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

	yesterdayBytes, _ := GetYesterdayTraffic()
	start, end := GetCycleDates(cfg.ResetDay)
	cycleBytes, _ := GetCycleTraffic(start.Format("2006-01-02"), end.Format("2006-01-02"))
	totalUsed := float64(cycleBytes) + cfg.CalibrationOffset
	if totalUsed < 0 {
		totalUsed = 0
	}
	daysLeft := DaysUntilReset(cfg.ResetDay)

	percent := 0.0
	if cfg.TotalBandwidth > 0 {
		percent = totalUsed / cfg.TotalBandwidth * 100
	}

	report := fmt.Sprintf(`📊 *每日流量日报*

昨日消耗：%s
当前周期累计：%s / %s (%.1f%%)
距离下次重置：%d 天`,
		FormatBytes(float64(yesterdayBytes)),
		FormatBytes(totalUsed),
		FormatBytes(cfg.TotalBandwidth),
		percent,
		daysLeft)

	SendMessage(env.AuthChatID, report)
	log.Println("每日日报已推送")
}

func runDataCleanup(cfg *AppConfig) {
	prevStart := GetPrevCycleStart(cfg.ResetDay)
	if err := CleanOldData(prevStart.Format("2006-01-02")); err != nil {
		log.Printf("数据清理失败: %v", err)
	} else {
		log.Println("历史数据清理完成")
	}
}

// resetAlertIfNewCycle resets the alert percentage if we've entered a new billing cycle.
func resetAlertIfNewCycle(cfg *AppConfig) {
	start, _ := GetCycleDates(cfg.ResetDay)
	// If today is the reset day, reset alert
	if time.Now().Day() == start.Day() && time.Now().Month() == start.Month() {
		UpdateConfig(func(c *AppConfig) { c.LastAlertPercent = 0 })
		log.Println("新计费周期，告警百分比已重置")
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

		start, end := GetCycleDates(cfg.ResetDay)
		cycleBytes, _ := GetCycleTraffic(start.Format("2006-01-02"), end.Format("2006-01-02"))
		totalUsed := float64(cycleBytes) + cfg.CalibrationOffset
		if totalUsed < 0 {
			totalUsed = 0
		}

		percent := totalUsed / cfg.TotalBandwidth * 100

		// Check thresholds: 10, 20, 30 ... 100
		currentThreshold := int(percent/10) * 10
		if currentThreshold > 100 {
			currentThreshold = 100
		}

		if currentThreshold > cfg.LastAlertPercent && currentThreshold >= 10 {
			// Send alerts for each missed threshold
			for t := cfg.LastAlertPercent + 10; t <= currentThreshold; t += 10 {
				alert := fmt.Sprintf("⚠️ 流量告警：当前账单周期流量已消耗 %d%%！\n已用：%s / 总计：%s",
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
