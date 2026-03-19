package main

import (
	"bufio"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// VnStatDayEntry represents a single day's traffic from vnStat.
type VnStatDayEntry struct {
	Date    string // "YYYY-MM-DD"
	RxBytes int64
	TxBytes int64
}

// FetchVnStatDays calls `vnstat --dumpdb` (v1.x) and parses daily traffic entries.
// dumpdb day format: d;index;timestamp;rx_mib;tx_mib;rxk_kib;txk_kib;used
// rx total bytes = rx_mib * 1024 * 1024 + rxk_kib * 1024
func FetchVnStatDays(iface string) ([]VnStatDayEntry, error) {
	cmd := exec.Command("vnstat", "--dumpdb", "-i", iface)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("vnstat --dumpdb 执行失败: %v", err)
	}

	var entries []VnStatDayEntry
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "d;") {
			continue
		}
		parts := strings.Split(line, ";")
		// d;index;timestamp;rx_mib;tx_mib;rxk_kib;txk_kib
		if len(parts) < 7 {
			continue
		}

		ts, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil || ts == 0 {
			continue // unused slot
		}

		rxMiB, _ := strconv.ParseInt(parts[3], 10, 64)
		txMiB, _ := strconv.ParseInt(parts[4], 10, 64)
		rxKiB, _ := strconv.ParseInt(parts[5], 10, 64)
		txKiB, _ := strconv.ParseInt(parts[6], 10, 64)

		// Skip entries with zero traffic and no real data
		if rxMiB == 0 && txMiB == 0 && rxKiB == 0 && txKiB == 0 {
			continue
		}

		rxBytes := rxMiB*1024*1024 + rxKiB*1024
		txBytes := txMiB*1024*1024 + txKiB*1024

		t := time.Unix(ts, 0)
		dateStr := t.Format("2006-01-02")

		entries = append(entries, VnStatDayEntry{
			Date:    dateStr,
			RxBytes: rxBytes,
			TxBytes: txBytes,
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
		if e.Date > today {
			continue
		}
		if err := UpsertDailyTraffic(e.Date, e.RxBytes, e.TxBytes); err != nil {
			return fmt.Errorf("写入 %s 数据失败: %v", e.Date, err)
		}
	}
	return nil
}
