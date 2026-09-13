package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

func runVnStat(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "vnstat", args...).Output()
}

func getVnStatMajorVersion() (int, error) {
	out, err := runVnStat("--version")
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
	out, err := runVnStat("--dumpdb", "-i", iface)
	if err != nil {
		return nil, fmt.Errorf("vnstat --dumpdb 失败: %v", err)
	}

	entries, err := parseVnStat1(out)
	if err != nil {
		return nil, err
	}
	if iface == "" || filepath.Base(iface) != iface {
		return nil, fmt.Errorf("invalid interface")
	}
	rx, err := os.ReadFile("/sys/class/net/" + iface + "/statistics/rx_bytes")
	if err != nil {
		return nil, err
	}
	tx, err := os.ReadFile("/sys/class/net/" + iface + "/statistics/tx_bytes")
	if err != nil {
		return nil, err
	}
	stat, err := os.ReadFile("/proc/stat")
	if err != nil {
		return nil, err
	}
	var boot int64
	for _, line := range strings.Split(string(stat), "\n") {
		if strings.HasPrefix(line, "btime ") {
			boot, _ = strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "btime ")), 10, 64)
		}
	}
	rxNow, err := strconv.ParseInt(strings.TrimSpace(string(rx)), 10, 64)
	if err != nil {
		return nil, err
	}
	txNow, err := strconv.ParseInt(strings.TrimSpace(string(tx)), 10, 64)
	if err != nil {
		return nil, err
	}
	return addLiveV1(entries, out, rxNow, txNow, boot, time.Now())
}

// Query-only overlay: never run vnstat -u as root while vnstatd owns its DB.
func addLiveV1(entries []VnStatDayEntry, raw []byte, rx, tx, boot int64, now time.Time) ([]VnStatDayEntry, error) {
	meta := map[string]int64{}
	for _, line := range strings.Split(string(raw), "\n") {
		p := strings.Split(line, ";")
		if len(p) == 2 {
			if n, err := strconv.ParseInt(p[1], 10, 64); err == nil {
				meta[p[0]] = n
			}
		}
	}
	for _, key := range []string{"currx", "curtx", "btime", "updated"} {
		if _, ok := meta[key]; !ok {
			return nil, fmt.Errorf("vnStat missing %s", key)
		}
	}
	if boot == 0 || meta["btime"] != boot || rx < meta["currx"] || tx < meta["curtx"] {
		return nil, fmt.Errorf("network counter reset: waiting for vnStat to establish a new baseline")
	}
	today := now.Format("2006-01-02")
	if time.Unix(meta["updated"], 0).In(now.Location()).Format("2006-01-02") != today {
		return nil, fmt.Errorf("vnStat has not closed the previous day; check daemon write permissions")
	}
	for i := range entries {
		if entries[i].Date == today {
			entries[i].RxBytes += rx - meta["currx"]
			entries[i].TxBytes += tx - meta["curtx"]
			return entries, nil
		}
	}
	return nil, fmt.Errorf("vnStat has no current-day baseline")
}

func parseVnStat1(out []byte) ([]VnStatDayEntry, error) {
	var entries []VnStatDayEntry
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "d;") {
			continue
		}
		// Format: d;index;timestamp;rx_MiB;tx_MiB;rx_KiB;tx_KiB;used
		parts := strings.Split(line, ";")
		if len(parts) < 8 || parts[7] != "1" {
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

		date := time.Unix(ts, 0).Format("2006-01-02")
		entries = append(entries, VnStatDayEntry{
			Date:    date,
			RxBytes: rxBytes,
			TxBytes: txBytes,
		})
	}

	return entries, scanner.Err()
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
	out, err := runVnStat("-i", iface, "--json", "d")
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
		entries = append(entries, VnStatDayEntry{
			Date:    date,
			RxBytes: d.Rx,
			TxBytes: d.Tx,
		})
	}
	return entries, nil
}

// ==================== Sync to DB ====================

var trafficSyncMu sync.Mutex

func SyncVnStatToDB(iface string) error {
	trafficSyncMu.Lock()
	defer trafficSyncMu.Unlock()
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
