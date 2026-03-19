package main

import (
	"database/sql"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// AppConfig maps to the app_config table (single-row).
type AppConfig struct {
	TotalBandwidth     float64 // Total quota in bytes
	ResetDay           int
	DailyPushTime      string // "HH:MM"
	CalibrationOffset  float64 // Offset in bytes, can be negative
	LastAlertPercent   int     // Highest alert threshold sent this cycle
	SetupStep          int     // 0=complete, 1=awaiting bandwidth, 2=awaiting reset day, 3=awaiting push time
}

// DailyTrafficLog maps to the daily_traffic_log table.
type DailyTrafficLog struct {
	RecordDate string // "YYYY-MM-DD"
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
		calibration_offset REAL DEFAULT 0,
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
	return err
}

// GetConfig reads the single-row app_config.
func GetConfig() (*AppConfig, error) {
	cfg := &AppConfig{}
	err := db.QueryRow(`SELECT total_bandwidth, reset_day, daily_push_time,
		calibration_offset, last_alert_percentage, setup_step FROM app_config WHERE id=1`).
		Scan(&cfg.TotalBandwidth, &cfg.ResetDay, &cfg.DailyPushTime,
			&cfg.CalibrationOffset, &cfg.LastAlertPercent, &cfg.SetupStep)
	return cfg, err
}

// UpdateConfig updates specific fields. Pass a function that modifies the config.
func UpdateConfig(fn func(cfg *AppConfig)) error {
	cfg, err := GetConfig()
	if err != nil {
		return err
	}
	fn(cfg)
	_, err = db.Exec(`UPDATE app_config SET total_bandwidth=?, reset_day=?, daily_push_time=?,
		calibration_offset=?, last_alert_percentage=?, setup_step=? WHERE id=1`,
		cfg.TotalBandwidth, cfg.ResetDay, cfg.DailyPushTime,
		cfg.CalibrationOffset, cfg.LastAlertPercent, cfg.SetupStep)
	return err
}

// UpsertDailyTraffic inserts or replaces today's traffic snapshot.
func UpsertDailyTraffic(date string, rx, tx int64) error {
	_, err := db.Exec(`INSERT INTO daily_traffic_log (record_date, rx_bytes, tx_bytes)
		VALUES (?, ?, ?) ON CONFLICT(record_date) DO UPDATE SET rx_bytes=?, tx_bytes=?`,
		date, rx, tx, rx, tx)
	return err
}

// GetCycleTraffic returns total RX+TX bytes within a billing cycle date range.
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

// GetDailyLogs returns per-day traffic records within a date range.
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

// CleanOldData deletes traffic logs older than the previous billing cycle start.
func CleanOldData(beforeDate string) error {
	_, err := db.Exec(`DELETE FROM daily_traffic_log WHERE record_date < ?`, beforeDate)
	return err
}

// GetYesterdayTraffic returns RX+TX for yesterday.
func GetYesterdayTraffic() (int64, error) {
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
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
