package main

import (
	"database/sql"

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
	SetupStep         int    // 0=complete, 1-5=wizard, 6=sync input, 7=calibrate input, 8=calibrate confirm
	LastResetCycle    string
}

type DailyTrafficLog struct {
	RecordDate string
	RxBytes    int64
	TxBytes    int64
}

var db *sql.DB

func InitDB(dbPath string) error {
	var err error
	db, err = sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	if err != nil {
		return err
	}

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
	}
	for _, m := range migrations {
		db.Exec(m)
	}
	return nil
}

func GetConfig() (*AppConfig, error) {
	cfg := &AppConfig{}
	err := db.QueryRow(`SELECT total_bandwidth, reset_day, daily_push_time,
		COALESCE(push_timezone, COALESCE(timezone, '')),
		COALESCE(billing_timezone, COALESCE(timezone, '')),
		usage_offset, calibration_factor, last_alert_percentage, setup_step,
		COALESCE(last_reset_cycle, '') FROM app_config WHERE id=1`).
		Scan(&cfg.TotalBandwidth, &cfg.ResetDay, &cfg.DailyPushTime,
			&cfg.PushTimezone, &cfg.BillingTimezone,
			&cfg.UsageOffset, &cfg.CalibrationFactor,
			&cfg.LastAlertPercent, &cfg.SetupStep, &cfg.LastResetCycle)
	return cfg, err
}

func UpdateConfig(fn func(cfg *AppConfig)) error {
	cfg, err := GetConfig()
	if err != nil {
		return err
	}
	fn(cfg)
	_, err = db.Exec(`UPDATE app_config SET total_bandwidth=?, reset_day=?, daily_push_time=?,
		push_timezone=?, billing_timezone=?, usage_offset=?, calibration_factor=?,
		last_alert_percentage=?, setup_step=?, last_reset_cycle=? WHERE id=1`,
		cfg.TotalBandwidth, cfg.ResetDay, cfg.DailyPushTime,
		cfg.PushTimezone, cfg.BillingTimezone, cfg.UsageOffset, cfg.CalibrationFactor,
		cfg.LastAlertPercent, cfg.SetupStep, cfg.LastResetCycle)
	return err
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
