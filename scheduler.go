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

		// Push time uses user's local timezone
		// Billing cycle dates use provider's billing timezone
		now := NowInZone(cfg.PushTimezone)
		todayStr := now.Format("2006-01-02")
		currentTime := now.Format("15:04")

		if currentTime == cfg.DailyPushTime && lastSentDate != todayStr {
			lastSentDate = todayStr
			sendDailyReport(env, cfg)
			runDataCleanup(cfg)
		}

		// Fallback: if we somehow missed the exact minute (e.g. heavy load),
		// check if we're within 2 minutes past push time and haven't sent today
		if lastSentDate != todayStr && cfg.DailyPushTime != "" {
			pushH, pushM := 0, 0
			fmt.Sscanf(cfg.DailyPushTime, "%d:%d", &pushH, &pushM)
			nowH, nowM := now.Hour(), now.Minute()
			diffMin := (nowH*60 + nowM) - (pushH*60 + pushM)
			if diffMin > 0 && diffMin <= 2 {
				lastSentDate = todayStr
				sendDailyReport(env, cfg)
				runDataCleanup(cfg)
			}
		}
	}
}

func sendDailyReport(env *AppEnv, cfg *AppConfig) {
	// Sync latest vnStat data
	if err := SyncVnStatToDB(env.InterfaceName); err != nil {
		log.Printf("日报同步失败: %v", err)
		SendMessage(env.AuthChatID, "⚠️ Traffic refresh failed; daily report is unavailable. Check vnStat service logs.")
		return
	}
	current, start, end, err := GetCycleConfig()
	if err != nil {
		log.Printf("日报配置读取失败: %v", err)
		return
	}
	cfg = current

	// Select yesterday in the user's push timezone, never the billing timezone.
	// Existing vnStat records still represent VPS-local calendar days.
	yesterday, bucketNote := dailyReportDate(time.Now(), cfg.PushTimezone)
	yesterdayText, err := dailyUsageText(yesterday, cfg.CalibrationFactor)
	if err != nil {
		log.Printf("日报查询失败: %v", err)
		SendMessage(env.AuthChatID, "⚠️ Daily report database query failed.")
		return
	}
	cycleBytes, _ := GetCycleTraffic(start.Format("2006-01-02"), end.Format("2006-01-02"))
	totalUsed := CalcTotalUsed(cfg, cycleBytes)
	daysLeft := DaysUntilReset(cfg.ResetDay, cfg.BillingTimezone)

	percent := 0.0
	if cfg.TotalBandwidth > 0 {
		percent = totalUsed / cfg.TotalBandwidth * 100
	}

	report := fmt.Sprintf(`📊 *Daily Traffic Report*

Yesterday's date (%s): %s
%s
Cycle total: %s / %s (%.1f%%)
Days until reset: %d`,
		yesterday,
		yesterdayText,
		bucketNote,
		FormatBytes(totalUsed),
		FormatBytes(cfg.TotalBandwidth),
		percent,
		daysLeft)

	SendMessage(env.AuthChatID, report)
	log.Println("每日日报已推送")
}

func dailyReportDate(now time.Time, pushTimezone string) (string, string) {
	loc, err := time.LoadLocation(pushTimezone)
	if err != nil {
		loc = time.UTC
	}
	date := now.In(loc).AddDate(0, 0, -1).Format("2006-01-02")
	note := "Daily buckets use VPS timezone (same as /daily)."
	if date >= now.Format("2006-01-02") {
		note += " Provisional: this VPS day has not ended yet."
	}
	return date, note
}

func dailyUsageText(date string, factor float64) (string, error) {
	logs, err := GetDailyLogs(date, date)
	if err != nil {
		return "", err
	}
	if len(logs) == 0 {
		return "No recorded data (not zero usage)", nil
	}
	if factor <= 0 {
		factor = 1
	}
	return FormatBytes(float64(logs[0].RxBytes+logs[0].TxBytes) * factor), nil
}

func runDataCleanup(cfg *AppConfig) {
	prevStart := GetPrevCycleStart(cfg.ResetDay, cfg.BillingTimezone)
	if err := CleanOldData(prevStart.Format("2006-01-02")); err != nil {
		log.Printf("数据清理失败: %v", err)
	} else {
		log.Println("历史数据清理完成")
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
		if err := SyncVnStatToDB(env.InterfaceName); err != nil {
			log.Printf("告警同步失败: %v", err)
			continue
		}
		cfg, start, end, err := GetCycleConfig()
		if err != nil || cfg.SetupStep > 0 || cfg.TotalBandwidth <= 0 {
			continue
		}
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
