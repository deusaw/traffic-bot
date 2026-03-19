package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// VnStatDayEntry represents a single day's traffic.
type VnStatDayEntry struct {
	Date    string // "YYYY-MM-DD"
	RxBytes int64
	TxBytes int64
}

// FetchVnStatDays auto-detects vnStat version and parses daily traffic.
func FetchVnStatDays(iface string) ([]VnStatDayEntry, error) {
	ver, err := getVnStatMajorVersion()
	if err != nil {
		return nil, err
	}
	if ver >= 2 {
		return fetchVnStat2(iface)
	}
	return fetchVnStat1(iface)
}

func getVnStatMajorVersion() (int, error) {
	out, err := exec.Command("vnstat", "--version").Output()
	if err != nil {
		return 0, fmt.Errorf("vnstat --version 失败: %v", err)
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		return 1, nil
	}
	parts := strings.Split(fields[1], ".")
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 1, nil
	}
	return major, nil
}

// ==================== vnStat 1.x (--dumpdb) ====================

func fetchVnStat1(iface string) ([]VnStatDayEntry, error) {
	out, err := exec.Command("vnstat", "--dumpdb", "-i", iface).Output()
	if err != nil {
		return nil, fmt.Errorf("vnstat --dumpdb 失败: %v", err)
	}

	var entries []VnStatDayEntry
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "d;") {
			continue
		}
		// Format: d;index;timestamp;rx_MiB;tx_MiB;rx_KiB;tx_KiB;used
		parts := strings.Split(line, ";")
		if len(parts) < 7 {
			continue
		}
		ts, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil || ts == 0 {
			continue
		}
		rxMiB, _ := strconv.ParseInt(parts[3], 10, 64)
		txMiB, _ := strconv.ParseInt(parts[4], 10, 64)
		rxKiB, _ := strconv.ParseInt(parts[5], 10, 64)
		txKiB, _ := strconv.ParseInt(parts[6], 10, 64)

		rxBytes := rxMiB*1024*1024 + rxKiB*1024
		txBytes := txMiB*1024*1024 + txKiB*1024

		if rxBytes == 0 && txBytes == 0 {
			continue
		}

		date := time.Unix(ts, 0).Format("2006-01-02")
		entries = append(entries, VnStatDayEntry{
			Date:    date,
			RxBytes: rxBytes,
			TxBytes: txBytes,
		})
	}

	// vnStat 1.x dumpdb may only have today's cumulative data via currx/curtx
	// If no daily entries found, fall back to current totals
	if len(entries) == 0 {
		entries, err = fetchVnStat1Current(iface, out)
		if err != nil {
			return nil, err
		}
	}

	return entries, nil
}

// fetchVnStat1Current parses currx/curtx from dumpdb output as fallback.
func fetchVnStat1Current(iface string, rawOutput []byte) ([]VnStatDayEntry, error) {
	var currx, curtx int64
	scanner := bufio.NewScanner(strings.NewReader(string(rawOutput)))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "currx;") {
			parts := strings.Split(line, ";")
			if len(parts) >= 2 {
				currx, _ = strconv.ParseInt(parts[1], 10, 64)
			}
		}
		if strings.HasPrefix(line, "curtx;") {
			parts := strings.Split(line, ";")
			if len(parts) >= 2 {
				curtx, _ = strconv.ParseInt(parts[1], 10, 64)
			}
		}
	}
	if currx == 0 && curtx == 0 {
		return nil, nil
	}
	today := time.Now().Format("2006-01-02")
	return []VnStatDayEntry{{
		Date:    today,
		RxBytes: currx,
		TxBytes: curtx,
	}}, nil
}

// ==================== vnStat 2.x (--json) ====================

// vnStat2JSON represents the JSON output of `vnstat --json d`
type vnStat2JSON struct {
	Interfaces []struct {
		Name    string `json:"name"`
		Traffic struct {
			Day []struct {
				Date struct {
					Year  int `json:"year"`
					Month int `json:"month"`
					Day   int `json:"day"`
				} `json:"date"`
				Rx int64 `json:"rx"`
				Tx int64 `json:"tx"`
			} `json:"day"`
		} `json:"traffic"`
	} `json:"interfaces"`
}

func fetchVnStat2(iface string) ([]VnStatDayEntry, error) {
	out, err := exec.Command("vnstat", "-i", iface, "--json", "d").Output()
	if err != nil {
		return nil, fmt.Errorf("vnstat --json d 失败: %v", err)
	}

	var data vnStat2JSON
	if err := json.Unmarshal(out, &data); err != nil {
		return nil, fmt.Errorf("解析 vnStat JSON 失败: %v", err)
	}

	if len(data.Interfaces) == 0 {
		return nil, fmt.Errorf("vnStat JSON 中未找到网卡 %s 的数据", iface)
	}

	var entries []VnStatDayEntry
	for _, d := range data.Interfaces[0].Traffic.Day {
		date := fmt.Sprintf("%04d-%02d-%02d", d.Date.Year, d.Date.Month, d.Date.Day)
		if d.Rx == 0 && d.Tx == 0 {
			continue
		}
		entries = append(entries, VnStatDayEntry{
			Date:    date,
			RxBytes: d.Rx,
			TxBytes: d.Tx,
		})
	}
	return entries, nil
}

// ==================== Sync to DB ====================

func SyncVnStatToDB(iface string) error {
	entries, err := FetchVnStatDays(iface)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := UpsertDailyTraffic(e.Date, e.RxBytes, e.TxBytes); err != nil {
			return fmt.Errorf("写入 %s 数据失败: %v", e.Date, err)
		}
	}
	return nil
}
