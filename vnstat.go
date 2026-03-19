package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

// VnStatJSON models the relevant parts of `vnstat --json` output.
type VnStatJSON struct {
	Interfaces []struct {
		Name    string `json:"name"`
		Traffic struct {
			Day []struct {
				Date struct {
					Year  int `json:"year"`
					Month int `json:"month"`
					Day   int `json:"day"`
				} `json:"date"`
				Rx uint64 `json:"rx"`
				Tx uint64 `json:"tx"`
			} `json:"day"`
		} `json:"traffic"`
	} `json:"interfaces"`
}

// VnStatDayEntry represents a single day's traffic from vnStat.
type VnStatDayEntry struct {
	Date    string // "YYYY-MM-DD"
	RxBytes int64
	TxBytes int64
}

// FetchVnStatDays calls vnstat and returns per-day traffic data.
func FetchVnStatDays(iface string) ([]VnStatDayEntry, error) {
	cmd := exec.Command("vnstat", "-i", iface, "--json", "d")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("vnstat 执行失败: %v", err)
	}

	var data VnStatJSON
	if err := json.Unmarshal(output, &data); err != nil {
		return nil, fmt.Errorf("vnstat JSON 解析失败: %v", err)
	}

	if len(data.Interfaces) == 0 {
		return nil, fmt.Errorf("未找到网卡 %s 的数据", iface)
	}

	var entries []VnStatDayEntry
	for _, d := range data.Interfaces[0].Traffic.Day {
		dateStr := fmt.Sprintf("%04d-%02d-%02d", d.Date.Year, d.Date.Month, d.Date.Day)
		entries = append(entries, VnStatDayEntry{
			Date:    dateStr,
			RxBytes: int64(d.Rx),
			TxBytes: int64(d.Tx),
		})
	}
	return entries, nil
}

// SyncVnStatToDB fetches vnStat data and upserts into daily_traffic_log.
func SyncVnStatToDB(iface string) error {
	entries, err := FetchVnStatDays(iface)
	if err != nil {
		return err
	}
	today := time.Now().Format("2006-01-02")
	for _, e := range entries {
		// Only sync up to today
		if e.Date > today {
			continue
		}
		if err := UpsertDailyTraffic(e.Date, e.RxBytes, e.TxBytes); err != nil {
			return fmt.Errorf("写入 %s 数据失败: %v", e.Date, err)
		}
	}
	return nil
}
