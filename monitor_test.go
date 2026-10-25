package main

import (
	"fmt"
	"testing"
	"time"
)

func testMonitorDB(t *testing.T) {
	t.Helper()
	if err := InitDB(":memory:"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(CloseDB)
}

func TestKernelCounterParsingAndMissingRules(t *testing.T) {
	cfg := &MonitorConfig{Counters: []MonitorCounter{{Mark: 0x7a000101, Email: "HOME", Category: "YouTube"}}}
	text := "[1:1400] -A TBOT_U0001 -m mark --mark 0x7a000101 -j RETURN\n[2:2400] -A TBOT_D0001 -m connmark --mark 0x7a000101 -j RETURN\n"
	rows, err := parseCounters(text, "ipv4", cfg)
	if err != nil || len(rows) != 1 || rows[0].Up != 1400 || rows[0].Down != 2400 {
		t.Fatalf("%+v %v", rows, err)
	}
	for _, bad := range []string{"", text + "[1:1] -A TBOT_U0001 -m mark --mark 0x7a000101 -j RETURN\n"} {
		if _, err := parseCounters(bad, "ipv4", cfg); err == nil {
			t.Fatal("incomplete or duplicate counters must not return false zero")
		}
	}
}

func TestAccountingNoDoubleCountIPv6AndReset(t *testing.T) {
	testMonitorDB(t)
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, beijing).Unix()
	c := MonitorCounter{Mark: 0x7a000101, Email: "HOME", Category: "YouTube"}
	apply := func(rows []counterValue) {
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = recordCounters(tx, rows, now-60, now); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	rows := []counterValue{{Key: "v4:1", Counter: c, Up: 100, Down: 200}, {Key: "v6:1", Counter: c, Up: 10, Down: 20}}
	apply(rows)
	apply(rows)
	rows[0].Up = 140
	rows[0].Down = 260
	apply(rows)
	rows[0].Up = 5
	rows[0].Down = 8
	apply(rows)
	start, end := monitorPeriod(time.Unix(now, 0))
	got, err := queryMonitorUsage(start, end, "HOME")
	if err != nil || len(got) != 1 || got[0].Up != 155 || got[0].Down != 288 {
		t.Fatalf("repeated samples/reset/IPv6: %+v %v", got, err)
	}
	var count int
	if err = db.QueryRow("SELECT COUNT(*) FROM monitor_usage").Scan(&count); err != nil || count != 1 {
		t.Fatalf("samples must collapse into a daily row: %d %v", count, err)
	}
}

func TestBeijingBoundaryIndependentOfServerZone(t *testing.T) {
	for _, loc := range []*time.Location{time.UTC, time.FixedZone("EDT", -4*3600), time.FixedZone("PDT", -7*3600)} {
		now := time.Date(2026, 10, 24, 16, 0, 0, 0, time.UTC).In(loc)
		start, end := monitorPeriod(now)
		if start != now.Unix() || end-start != 86400 {
			t.Fatalf("Beijing midnight in %s: %d %d", loc, start, end)
		}
		before, _ := monitorPeriod(now.Add(-time.Second))
		if before != start-86400 {
			t.Fatal("wrong date before Beijing midnight")
		}
	}
	boundary := time.Date(2026, 10, 25, 0, 0, 0, 0, beijing).Unix()
	pieces := dailyPieces(101, 203, boundary-30, boundary+30)
	if len(pieces) != 2 || pieces[0].Up+pieces[1].Up != 101 || pieces[0].Down+pieces[1].Down != 203 || pieces[0].Day != boundary-86400 || pieces[1].Day != boundary {
		t.Fatalf("cross-day attribution lost bytes: %+v", pieces)
	}
}

func TestMonitorRollbackAndRetentionBound(t *testing.T) {
	testMonitorDB(t)
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, beijing).Unix()
	day, _ := monitorPeriod(time.Unix(now, 0))
	c := MonitorCounter{Email: "HOME", Category: "YouTube"}
	tx, _ := db.Begin()
	if _, err := recordCounters(tx, []counterValue{{Key: "a", Counter: c, Up: 100}}, now-60, now); err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	var count int
	db.QueryRow("SELECT COUNT(*) FROM monitor_counters").Scan(&count)
	if count != 0 {
		t.Fatal("rolled-back sample advanced the cursor")
	}
	for i := 0; i < 40; i++ {
		if _, err := db.Exec("INSERT INTO monitor_usage VALUES(?,?,?,?,?)", day-int64(i)*86400, "HOME", fmt.Sprint(i), 1, 2); err != nil {
			t.Fatal(err)
		}
	}
	tx, _ = db.Begin()
	if err := pruneMonitorUsage(tx, day); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	db.QueryRow("SELECT COUNT(*) FROM monitor_usage").Scan(&count)
	if count != 30 {
		t.Fatalf("retained %d days, expected 30", count)
	}
}
