package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestDailyReportUsesPushDate(t *testing.T) {
	vps, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	// 10:01 Shanghai on Sep 13 is still Sep 12 on the VPS and in LA.
	now := time.Date(2026, 9, 12, 22, 1, 0, 0, vps)
	date, note := dailyReportDate(now, "Asia/Shanghai")
	if date != "2026-09-12" || !strings.Contains(note, "Provisional") {
		t.Fatalf("wrong date or missing partial-day warning: %s %s", date, note)
	}
	date, note = dailyReportDate(now.Add(3*time.Hour), "Asia/Shanghai")
	if date != "2026-09-12" || strings.Contains(note, "Provisional") {
		t.Fatalf("closed day: %s %s", date, note)
	}
}

func TestLiveV1NoDoubleCounting(t *testing.T) {
	now := time.Date(2026, 9, 13, 14, 0, 0, 0, time.Local)
	raw := []byte(fmt.Sprintf("updated;%d\nbtime;100\ncurrx;10000\ncurtx;20000\nd;0;%d;1;2;0;0;1\n", now.Unix(), now.Unix()))
	for _, extra := range []int64{100, 100, 300} {
		entries, err := parseVnStat1(raw)
		if err != nil {
			t.Fatal(err)
		}
		got, err := addLiveV1(entries, raw, 10000+extra, 20000+extra, 100, now)
		if err != nil {
			t.Fatal(err)
		}
		if got[0].RxBytes != 1048576+extra || got[0].TxBytes != 2097152+extra {
			t.Fatalf("bad overlay: %+v", got)
		}
	}
}

func TestSyncBaselineContinuesGrowing(t *testing.T) {
	now := time.Date(2026, 9, 13, 14, 0, 0, 0, time.Local)
	raw := []byte(fmt.Sprintf("updated;%d\nbtime;100\ncurrx;10000\ncurtx;20000\nd;0;%d;1;2;0;0;1\n", now.Unix(), now.Unix()))
	read := func(extra int64) int64 {
		rows, _ := parseVnStat1(raw)
		rows, err := addLiveV1(rows, raw, 10000+extra, 20000+extra, 100, now)
		if err != nil {
			t.Fatal(err)
		}
		return rows[0].RxBytes + rows[0].TxBytes
	}
	baseline := read(100)
	actual := float64(808.94 * 1024 * 1024 * 1024)
	cfg := &AppConfig{CalibrationFactor: 1, UsageOffset: actual - float64(baseline)}
	if got := CalcTotalUsed(cfg, read(300)); got != actual+400 {
		t.Fatalf("sync baseline froze or double counted: %.2f", got)
	}
}

func TestLiveV1RejectsResetAndMidnight(t *testing.T) {
	now := time.Date(2026, 9, 13, 14, 0, 0, 0, time.Local)
	raw := []byte(fmt.Sprintf("updated;%d\nbtime;100\ncurrx;10000\ncurtx;20000\n", now.Unix()))
	for _, tc := range []struct {
		rx, tx, boot int64
		now          time.Time
	}{{9999, 20000, 100, now}, {10000, 20000, 101, now}, {10000, 20000, 100, now.AddDate(0, 0, 1)}} {
		if _, err := addLiveV1(nil, raw, tc.rx, tc.tx, tc.boot, tc.now); err == nil {
			t.Fatal("expected unsafe baseline error")
		}
	}
}

func TestParseV1ZeroIsRecordNotKernelCounter(t *testing.T) {
	raw := []byte("currx;999999999\ncurtx;999999999\nd;0;1789300000;0;0;0;0;1\nd;1;0;0;0;0;0;0\n")
	rows, err := parseVnStat1(raw)
	if err != nil || len(rows) != 1 || rows[0].RxBytes != 0 {
		t.Fatalf("%+v %v", rows, err)
	}
}

func TestDailyMissingAndZero(t *testing.T) {
	if err := InitDB(":memory:"); err != nil {
		t.Fatal(err)
	}
	defer CloseDB()
	db.SetMaxOpenConns(1)
	missing, err := dailyUsageText("2026-09-12", 1)
	if err != nil || missing != "No recorded data (not zero usage)" {
		t.Fatalf("%s %v", missing, err)
	}
	if err := UpsertDailyTraffic("2026-09-12", 0, 0); err != nil {
		t.Fatal(err)
	}
	zero, err := dailyUsageText("2026-09-12", 1)
	if err != nil || zero != "0 B" {
		t.Fatalf("%s %v", zero, err)
	}
	if err := UpsertDailyTraffic("2026-09-12", 1048576, 1048576); err != nil {
		t.Fatal(err)
	}
	usage, err := dailyUsageText("2026-09-12", 0)
	if err != nil || usage != "2.00 MB" {
		t.Fatalf("%s %v", usage, err)
	}
}
