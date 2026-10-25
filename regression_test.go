package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func captureTestMessages(t *testing.T) func() string {
	t.Helper()
	var mu sync.Mutex
	last := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/getMe") {
			fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"Test","username":"test_bot"}}`)
			return
		}
		r.ParseForm()
		mu.Lock()
		last = r.Form.Get("text")
		mu.Unlock()
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":1,"date":0,"chat":{"id":123,"type":"private"},"text":"ok"}}`)
	}))
	t.Cleanup(srv.Close)
	original := bot
	var err error
	bot, err = tgbotapi.NewBotAPIWithClient("synthetic", srv.URL+"/bot%s/%s", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bot = original })
	return func() string { mu.Lock(); defer mu.Unlock(); return last }
}

func TestNonfiniteInputCannotCorruptConfig(t *testing.T) {
	testMonitorDB(t)
	for _, input := range []string{"NaN GB", "+Inf TB", "-Inf GB", "1e308 TB", "1e999 GB"} {
		if _, err := parseBandwidthAllUnits(input); err == nil {
			t.Errorf("sync accepted %q", input)
		}
		if _, err := parseBandwidth(input); err == nil {
			t.Errorf("quota accepted %q", input)
		}
	}
	for _, mutate := range []func(*AppConfig){
		func(c *AppConfig) { c.UsageOffset = math.NaN() },
		func(c *AppConfig) { c.CalibrationFactor = math.Inf(1) },
		func(c *AppConfig) { c.PendingUsage = math.NaN() },
	} {
		if err := UpdateConfig(mutate); err == nil {
			t.Fatal("non-finite database update succeeded")
		}
		cfg, err := GetConfig()
		if err != nil || !finiteNumber(cfg.UsageOffset) || cfg.CalibrationFactor != 1 {
			t.Fatalf("rejected input changed the saved configuration: %+v %v", cfg, err)
		}
	}
	message := captureTestMessages(t)
	handleCalibrateInput(&AppEnv{}, 123, "NaN")
	if !strings.Contains(message(), "positive number") {
		t.Fatal("calibration did not reject NaN")
	}
}

func TestBillingCycleReconcilesBeforeStatusAndAlerts(t *testing.T) {
	testMonitorDB(t)
	now := time.Now().UTC()
	if err := UpdateConfig(func(c *AppConfig) {
		c.SetupStep, c.ResetDay = 0, now.Day()
		c.BillingTimezone = "UTC"
		c.TotalBandwidth, c.UsageOffset = 1024*bytesPerGB, 800*bytesPerGB
		c.LastAlertPercent = 90
		c.LastResetCycle = now.Format("2006-01-02")
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE app_config SET last_reset_cycle='2000-01-01'"); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in --version) echo 'vnStat 2.13';; *) echo '{\"interfaces\":[{\"name\":\"eth0\",\"traffic\":{\"day\":[{\"date\":{\"year\":%d,\"month\":%d,\"day\":%d},\"rx\":1048576,\"tx\":0}]}}]}' ;; esac\n", now.Year(), now.Month(), now.Day())
	if err := os.WriteFile(filepath.Join(bin, "vnstat"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	message := captureTestMessages(t)
	handleStatus(&AppEnv{InterfaceName: "eth0"}, 123)
	if !strings.Contains(message(), "Used: 1.00 MB") || strings.Contains(message(), "800.") {
		t.Fatalf("status retained the previous cycle offset: %s", message())
	}
	cfg, err := GetConfig()
	if err != nil || cfg.UsageOffset != 0 || cfg.LastAlertPercent != 0 || cfg.LastResetCycle != now.Format("2006-01-02") {
		t.Fatalf("cycle state was not persisted: %+v %v", cfg, err)
	}
	if err = UpdateConfig(func(c *AppConfig) { c.UsageOffset = 42 }); err != nil {
		t.Fatal(err)
	}
	cfg, _ = GetConfig()
	if cfg.UsageOffset != 42 {
		t.Fatal("same-cycle manual sync was reset twice")
	}
}

func TestBillingCycleBoundaryAndInitialBaseline(t *testing.T) {
	before := time.Date(2026, 10, 23, 23, 59, 59, 0, beijing)
	cfg := &AppConfig{ResetDay: 24, BillingTimezone: "Asia/Shanghai", UsageOffset: 800 * bytesPerGB, CalibrationFactor: 1.25, LastResetCycle: "2026-09-24", LastAlertPercent: 80}
	if reconcileBillingCycle(cfg, before) || cfg.UsageOffset == 0 {
		t.Fatal("reset before the billing boundary")
	}
	if !reconcileBillingCycle(cfg, before.Add(time.Second)) || cfg.UsageOffset != 0 || cfg.CalibrationFactor != 1.25 {
		t.Fatal("did not reset exactly at the configured boundary")
	}
	if reconcileBillingCycle(cfg, before) {
		t.Fatal("a backward clock change reset the baseline")
	}
	cfg.LastResetCycle, cfg.UsageOffset = "", 123
	if !reconcileBillingCycle(cfg, before) || cfg.UsageOffset != 123 {
		t.Fatal("migration must preserve a baseline of unknown age")
	}
}

func TestCalibrationConfirmationSurvivesRestartWithoutDoubleCorrection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bot.db")
	if err := InitDB(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(CloseDB)
	if err := UpdateConfig(func(c *AppConfig) {
		c.SetupStep = 8
		c.PendingFactor, c.PendingRawBytes, c.PendingUsage = 2, bytesPerGB, 2*bytesPerGB
		c.UsageOffset = bytesPerGB
	}); err != nil {
		t.Fatal(err)
	}
	CloseDB()
	if err := InitDB(path); err != nil {
		t.Fatal(err)
	}
	if factor, err := confirmCalibration(0); err != nil || factor != 2 {
		t.Fatalf("persisted confirmation was lost: %v %v", factor, err)
	}
	cfg, _ := GetConfig()
	if cfg.CalibrationFactor != 2 || cfg.UsageOffset != 0 || cfg.PendingFactor != 0 || cfg.SetupStep != 0 {
		t.Fatalf("invalid confirmation result: %+v", cfg)
	}
	if got := CalcTotalUsed(cfg, bytesPerGB+bytesPerMB); got != 2*bytesPerGB+2*bytesPerMB {
		t.Fatalf("factor double-corrected the manual sync or lost new bytes: %v", got)
	}
	if err := UpdateConfig(func(c *AppConfig) { c.SetupStep = 8 }); err != nil {
		t.Fatal(err)
	}
	if _, err := confirmCalibration(0); err == nil {
		t.Fatal("legacy confirmation without a candidate was accepted")
	}
	cfg, _ = GetConfig()
	if cfg.CalibrationFactor != 2 || cfg.SetupStep != 0 {
		t.Fatal("expired confirmation overwrote the valid factor")
	}
}

func TestDeletedAccountsRemainQueryableAndIncludedInDeviceTotals(t *testing.T) {
	testMonitorDB(t)
	start, end := monitorPeriod(time.Now())
	for _, email := range []string{"DRnc.love@xray.com", "HOME.love@xray.com"} {
		if _, err := db.Exec("INSERT INTO monitor_usage VALUES(?,?,?,?,?)", start, email, "Other", 100, 200); err != nil {
			t.Fatal(err)
		}
	}
	conf := &MonitorConfig{Counters: []MonitorCounter{{Email: "HOME.love@xray.com"}, {Email: "NEW.love@xray.com"}}}
	emails, err := monitorEmails(conf, start, end)
	if err != nil || len(emails) != 3 {
		t.Fatalf("historical or zero-usage account omitted: %v %v", emails, err)
	}
	filter, err := resolveMonitorEmail("DRnc", emails)
	if err != nil || filter != "DRnc.love@xray.com" {
		t.Fatalf("deleted account cannot be selected: %q %v", filter, err)
	}
	rows, err := queryMonitorUsage(start, end, "")
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	writeDeviceUsage(&text, conf, rows, emails, "")
	if !strings.Contains(text.String(), "DRnc（历史账号）") || strings.Count(text.String(), "合计 300 B") != 2 {
		t.Fatalf("device totals omit historical traffic: %s", text.String())
	}
	filtered, err := queryMonitorUsage(start, end, filter)
	if err != nil || len(filtered) != 1 || filtered[0].Up+filtered[0].Down != 300 {
		t.Fatalf("deleted account's site usage is unavailable: %+v %v", filtered, err)
	}
}

func TestInterruptedMonitorStateRequestsRecoveryAndLockPreventsRaces(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MONITOR_LOCK_PATH", filepath.Join(dir, "lock"))
	t.Setenv("MONITOR_TRANSACTION_PATH", filepath.Join(dir, "transaction.json"))
	if err := syncMonitorMode(true); err == nil || !strings.Contains(err.Error(), "requires the reconciliation lock") {
		t.Fatalf("internal helper sample was allowed without the parent's lock: %v", err)
	}
	lock, err := acquireMonitorLock()
	if err != nil {
		t.Fatal(err)
	}
	if err = syncMonitor(); !errors.Is(err, errMonitorBusy) {
		t.Fatalf("concurrent sampler entered an active reconciliation: %v", err)
	}
	lock.Close()
	if err = os.WriteFile(filepath.Join(dir, "transaction.json"), []byte(`{"phase":"prepared"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err = syncMonitor(); !errors.Is(err, errMonitorAccountsChanged) {
		t.Fatalf("durable interrupted transaction did not request recovery: %v", err)
	}
	os.Remove(filepath.Join(dir, "transaction.json"))
	conf := MonitorConfig{Version: 1, Created: 1, PendingEmails: []string{"NEW"}}
	encoded, _ := json.Marshal(conf)
	path := filepath.Join(dir, "manifest.json")
	if err = os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONITOR_CONFIG", path)
	if err = syncMonitor(); !errors.Is(err, errMonitorAccountsChanged) {
		t.Fatalf("legacy pending accounts were silently accepted: %v", err)
	}
}
