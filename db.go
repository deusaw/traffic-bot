package main

import (
	"database/sql"
	"fmt"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// AppConfig maps to the app_config table (single-row).
type AppConfig struct {
	TotalBandwidth    float64 // Total quota in bytes
	ResetDay          int
	DailyPushTime     string // "HH:MM"
	PushTimezone      string // IANA timezone for push time, e.g. "Asia/Shanghai"
	BillingTimezone   string // IANA timezone for billing cycle, e.g. "America/New_York"
	UsageOffset       float64
	CalibrationFactor float64
	LastAlertPercent  int
	SetupStep         int // 0=complete, 1-5=wizard, 6=sync input, 7=calibrate input, 8=calibrate confirm
	LastResetCycle    string
	PendingFactor     float64
	PendingRawBytes   int64
	PendingUsage      float64
}

type DailyTrafficLog struct {
	RecordDate string
	RxBytes    int64
	TxBytes    int64
}

var db *sql.DB
var configMu sync.Mutex

func InitDB(dbPath string) error {
	var err error
	db, err = sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)

	schema := `
	CREATE TABLE IF NOT EXISTS app_config (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		total_bandwidth REAL DEFAULT 0,
		reset_day INTEGER DEFAULT 1,
		daily_push_time VARCHAR DEFAULT '08:00',
		usage_offset REAL DEFAULT 0,
		calibration_factor REAL DEFAULT 1.0,
		last_alert_percentage INTEGER DEFAULT 0,
		setup_step INTEGER DEFAULT 1
	);
	INSERT OR IGNORE INTO app_config (id) VALUES (1);
	CREATE TABLE IF NOT EXISTS daily_traffic_log (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		record_date DATE NOT NULL,
		rx_bytes INTEGER DEFAULT 0,
		tx_bytes INTEGER DEFAULT 0,
		UNIQUE(record_date)
	);`

	_, err = db.Exec(schema)
	if err != nil {
		return err
	}

	// Auto-migrate from older schemas
	migrations := []string{
		"ALTER TABLE app_config ADD COLUMN usage_offset REAL DEFAULT 0",
		"ALTER TABLE app_config ADD COLUMN calibration_factor REAL DEFAULT 1.0",
		"ALTER TABLE app_config ADD COLUMN last_reset_cycle VARCHAR DEFAULT ''",
		"ALTER TABLE app_config ADD COLUMN timezone VARCHAR DEFAULT ''",
		"ALTER TABLE app_config ADD COLUMN push_timezone VARCHAR DEFAULT ''",
		"ALTER TABLE app_config ADD COLUMN billing_timezone VARCHAR DEFAULT ''",
		"ALTER TABLE app_config ADD COLUMN pending_factor REAL DEFAULT 0",
		"ALTER TABLE app_config ADD COLUMN pending_raw_bytes INTEGER DEFAULT 0",
		"ALTER TABLE app_config ADD COLUMN pending_usage REAL DEFAULT 0",
	}
	for _, m := range migrations {
		db.Exec(m)
	}
	return initMonitorDB()
}

func GetConfig() (*AppConfig, error) {
	cfg, _, _, err := GetCycleConfig()
	return cfg, err
}

// Configuration and billing dates share one clock snapshot, including when a
// vnStat refresh crosses midnight just before the caller asks for the totals.
func GetCycleConfig() (*AppConfig, time.Time, time.Time, error) {
	configMu.Lock()
	defer configMu.Unlock()
	cfg, err := readConfig()
	if err != nil {
		return nil, time.Time{}, time.Time{}, err
	}
	now := billingTime(cfg, time.Now())
	if reconcileBillingCycle(cfg, now) {
		if err = saveConfig(cfg); err != nil {
			return nil, time.Time{}, time.Time{}, err
		}
	}
	start, end := cycleDatesAt(now, cfg.ResetDay)
	return cfg, start, end, nil
}

func readConfig() (*AppConfig, error) {
	cfg := &AppConfig{}
	err := db.QueryRow(`SELECT total_bandwidth, reset_day, daily_push_time,
		COALESCE(push_timezone, COALESCE(timezone, '')),
		COALESCE(billing_timezone, COALESCE(timezone, '')),
		usage_offset, calibration_factor, last_alert_percentage, setup_step,
		COALESCE(last_reset_cycle, ''), pending_factor, pending_raw_bytes, pending_usage FROM app_config WHERE id=1`).
		Scan(&cfg.TotalBandwidth, &cfg.ResetDay, &cfg.DailyPushTime,
			&cfg.PushTimezone, &cfg.BillingTimezone,
			&cfg.UsageOffset, &cfg.CalibrationFactor,
			&cfg.LastAlertPercent, &cfg.SetupStep, &cfg.LastResetCycle,
			&cfg.PendingFactor, &cfg.PendingRawBytes, &cfg.PendingUsage)
	return cfg, err
}

func UpdateConfig(fn func(cfg *AppConfig)) error {
	configMu.Lock()
	defer configMu.Unlock()

	cfg, err := readConfig()
	if err != nil {
		return err
	}
	reconcileBillingCycle(cfg, time.Now())
	fn(cfg)
	// Complete first-time setup without throwing away a current-cycle sync.
	if cfg.LastResetCycle == "" {
		reconcileBillingCycle(cfg, time.Now())
	}
	return saveConfig(cfg)
}

func saveConfig(cfg *AppConfig) error {
	if !finiteNumber(cfg.TotalBandwidth) || cfg.TotalBandwidth < 0 ||
		!finiteNumber(cfg.UsageOffset) || !finiteNumber(cfg.CalibrationFactor) || cfg.CalibrationFactor <= 0 ||
		!finiteNumber(cfg.PendingFactor) || cfg.PendingFactor < 0 || cfg.PendingRawBytes < 0 ||
		!finiteNumber(cfg.PendingUsage) || cfg.PendingUsage < 0 {
		return fmt.Errorf("invalid or non-finite traffic configuration")
	}
	_, err := db.Exec(`UPDATE app_config SET total_bandwidth=?, reset_day=?, daily_push_time=?,
		push_timezone=?, billing_timezone=?, usage_offset=?, calibration_factor=?,
		last_alert_percentage=?, setup_step=?, last_reset_cycle=?, pending_factor=?, pending_raw_bytes=?, pending_usage=? WHERE id=1`,
		cfg.TotalBandwidth, cfg.ResetDay, cfg.DailyPushTime,
		cfg.PushTimezone, cfg.BillingTimezone, cfg.UsageOffset, cfg.CalibrationFactor,
		cfg.LastAlertPercent, cfg.SetupStep, cfg.LastResetCycle,
		cfg.PendingFactor, cfg.PendingRawBytes, cfg.PendingUsage)
	return err
}

// Runs under configMu before every config read/write. Calendar changes never
// depend on the daily report being enabled or successfully sent.
func reconcileBillingCycle(cfg *AppConfig, now time.Time) bool {
	if cfg.SetupStep >= 1 && cfg.SetupStep <= 5 || cfg.ResetDay < 1 || cfg.ResetDay > 31 {
		return false
	}
	now = billingTime(cfg, now)
	start, _ := cycleDatesAt(now, cfg.ResetDay)
	cycle := start.Format("2006-01-02")
	if cfg.LastResetCycle == "" {
		cfg.LastResetCycle = cycle
		return true
	}
	if cycle > cfg.LastResetCycle {
		cfg.UsageOffset, cfg.LastAlertPercent = 0, 0
		cfg.LastResetCycle = cycle
		clearPendingCalibration(cfg)
		if cfg.SetupStep == 8 {
			cfg.SetupStep = 0
		}
		return true
	}
	return false
}

func billingTime(cfg *AppConfig, now time.Time) time.Time {
	if cfg.BillingTimezone != "" {
		if loc, err := time.LoadLocation(cfg.BillingTimezone); err == nil {
			return now.In(loc)
		}
	}
	return now
}

func clearPendingCalibration(cfg *AppConfig) {
	cfg.PendingFactor, cfg.PendingRawBytes, cfg.PendingUsage = 0, 0, 0
}

func UpsertDailyTraffic(date string, rx, tx int64) error {
	_, err := db.Exec(`INSERT INTO daily_traffic_log (record_date, rx_bytes, tx_bytes)
		VALUES (?, ?, ?) ON CONFLICT(record_date) DO UPDATE SET rx_bytes=?, tx_bytes=?`,
		date, rx, tx, rx, tx)
	return err
}

func GetCycleTraffic(startDate, endDate string) (int64, error) {
	var total sql.NullInt64
	err := db.QueryRow(`SELECT SUM(rx_bytes + tx_bytes) FROM daily_traffic_log
		WHERE record_date >= ? AND record_date <= ?`, startDate, endDate).Scan(&total)
	if err != nil {
		return 0, err
	}
	if !total.Valid {
		return 0, nil
	}
	return total.Int64, nil
}

func GetDailyLogs(startDate, endDate string) ([]DailyTrafficLog, error) {
	rows, err := db.Query(`SELECT record_date, rx_bytes, tx_bytes FROM daily_traffic_log
		WHERE record_date >= ? AND record_date <= ? ORDER BY record_date ASC`, startDate, endDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var logs []DailyTrafficLog
	for rows.Next() {
		var l DailyTrafficLog
		if err := rows.Scan(&l.RecordDate, &l.RxBytes, &l.TxBytes); err != nil {
			return nil, err
		}
		logs = append(logs, l)
	}
	return logs, rows.Err()
}

func CleanOldData(beforeDate string) error {
	_, err := db.Exec(`DELETE FROM daily_traffic_log WHERE record_date < ?`, beforeDate)
	return err
}

func CloseDB() {
	if db != nil {
		db.Close()
	}
}

func GetYesterdayTraffic(tz string) (int64, error) {
	yesterday := NowInZone(tz).AddDate(0, 0, -1).Format("2006-01-02")
	var total sql.NullInt64
	err := db.QueryRow(`SELECT rx_bytes + tx_bytes FROM daily_traffic_log WHERE record_date = ?`, yesterday).Scan(&total)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !total.Valid {
		return 0, nil
	}
	return total.Int64, nil
}

// CalcTotalUsed: 已用量 = vnStat周期累计 * factor + offset
// offset 是 sync 产生的差值，不参与倍率计算
func CalcTotalUsed(cfg *AppConfig, localCycleBytes int64) float64 {
	factor := cfg.CalibrationFactor
	if factor <= 0 {
		factor = 1.0
	}
	total := float64(localCycleBytes)*factor + cfg.UsageOffset
	if total < 0 {
		total = 0
	}
	return total
}
