package main

// Optional accounting: authenticated email x website category, IP-layer bytes.
// X-UI resets its gRPC counters, so independently marked site sockets are counted
// by isolated, non-blocking firewall chains. No connection/access logs are kept.
import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type MonitorCounter struct {
	Mark     uint32 `json:"mark"`
	Email    string `json:"email"`
	Category string `json:"category"`
	Retired  bool   `json:"retired,omitempty"`
}
type MonitorCategory struct {
	Name    string   `json:"name"`
	Domains []string `json:"domains"`
}
type MonitorConfig struct {
	Version       int               `json:"version"`
	Created       int64             `json:"created"`
	Counters      []MonitorCounter  `json:"counters"`
	Categories    []MonitorCategory `json:"categories"`
	PendingEmails []string          `json:"pending_emails,omitempty"`
}
type usageRow struct {
	Email, Category string
	Up, Down        int64
}
type counterValue struct {
	Key      string
	Counter  MonitorCounter
	Up, Down int64
}

var monitorMu sync.Mutex
var reconcileMu sync.Mutex
var lastReconcileRequest time.Time
var errMonitorAccountsChanged = errors.New("Xray accounts/accounting template changed")
var beijing = time.FixedZone("Asia/Shanghai", 8*60*60)
var counterLine = regexp.MustCompile(`^\[\d+:(\d+)\] -A (TBOT_U[0-9a-f]{4}|TBOT_D[0-9a-f]{4}) .* --mark (0x[0-9a-fA-F]+|[0-9]+)(?:/\S+)? -j RETURN$`)

func loadMonitorConfig() (*MonitorConfig, error) {
	path := os.Getenv("MONITOR_CONFIG")
	if path == "" {
		path = "/etc/traffic-bot-monitor.json"
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c MonitorConfig
	if err = json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if c.Version != 1 || c.Created <= 0 {
		return nil, fmt.Errorf("unsupported or empty monitor configuration")
	}
	marks := map[uint32]bool{}
	for _, x := range c.Counters {
		if x.Mark&0xff000000 != 0x7a000000 || x.Email == "" || marks[x.Mark] {
			return nil, fmt.Errorf("invalid accounting mark or duplicate")
		}
		marks[x.Mark] = true
	}
	return &c, nil
}

func initMonitorDB() error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS monitor_counters (counter_key TEXT PRIMARY KEY, up INTEGER NOT NULL, down INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS monitor_usage (at INTEGER NOT NULL, email TEXT NOT NULL, category TEXT NOT NULL, up INTEGER NOT NULL, down INTEGER NOT NULL, PRIMARY KEY(at,email,category));
CREATE TABLE IF NOT EXISTS monitor_state (name TEXT PRIMARY KEY, value TEXT NOT NULL);`)
	return err
}

func parseCounters(text, family string, cfg *MonitorConfig) ([]counterValue, error) {
	byMark := map[uint32]MonitorCounter{}
	for _, c := range cfg.Counters {
		byMark[c.Mark] = c
	}
	values := map[uint32]*counterValue{}
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		m := counterLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		mark, err := strconv.ParseUint(m[3], 0, 32)
		if err != nil {
			return nil, err
		}
		c, ok := byMark[uint32(mark)]
		if !ok {
			continue
		}
		up := strings.HasPrefix(m[2], "TBOT_U")
		key := fmt.Sprintf("%t:%d", up, mark)
		if seen[key] {
			return nil, fmt.Errorf("duplicate accounting rule for %s", key)
		}
		seen[key] = true
		n, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return nil, err
		}
		v := values[c.Mark]
		if v == nil {
			v = &counterValue{Key: fmt.Sprintf("%s:%d", family, c.Mark), Counter: c}
			values[c.Mark] = v
		}
		if up {
			v.Up = n
		} else {
			v.Down = n
		}
	}
	if len(seen) != 2*len(cfg.Counters) {
		return nil, fmt.Errorf("%s accounting rules incomplete; run monitor_setup.py --restore-firewall", family)
	}
	result := make([]counterValue, 0, len(values))
	for _, v := range values {
		result = append(result, *v)
	}
	return result, nil
}

func readMonitorCounters(cfg *MonitorConfig) ([]counterValue, error) {
	var all []counterValue
	for _, bin := range []string{"iptables-save", "ip6tables-save"} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		b, err := exec.CommandContext(ctx, bin, "-c", "-t", "mangle").Output()
		cancel()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", bin, err)
		}
		rows, err := parseCounters(string(b), bin, cfg)
		if err != nil {
			return nil, err
		}
		all = append(all, rows...)
	}
	return all, nil
}

func counterDelta(now, previous int64) (int64, bool) {
	if now < previous {
		return now, true
	}
	return now - previous, false
}

type dailyPiece struct{ Day, Up, Down int64 }

// Only a boundary-spanning sample needs proportional attribution. Totals remain
// byte-exact; the split between two days is an estimate within the sample window.
func dailyPieces(up, down, from, to int64) []dailyPiece {
	if to <= from {
		day, _ := monitorPeriod(time.Unix(to, 0))
		return []dailyPiece{{day, up, down}}
	}
	var out []dailyPiece
	var assignedUp, assignedDown int64
	for pos := from; pos < to; {
		day, end := monitorPeriod(time.Unix(pos, 0))
		next := end
		if next > to {
			next = to
		}
		u, d := up-assignedUp, down-assignedDown
		if next < to {
			u = int64(float64(up) * float64(next-pos) / float64(to-from))
			d = int64(float64(down) * float64(next-pos) / float64(to-from))
		}
		out = append(out, dailyPiece{day, u, d})
		assignedUp += u
		assignedDown += d
		pos = next
	}
	return out
}

func recordCounters(tx *sql.Tx, rows []counterValue, from, to int64) (int, error) {
	resets := 0
	for _, v := range rows {
		var oldUp, oldDown int64
		err := tx.QueryRow("SELECT up,down FROM monitor_counters WHERE counter_key=?", v.Key).Scan(&oldUp, &oldDown)
		if err != nil && err != sql.ErrNoRows {
			return 0, err
		}
		up, r1 := counterDelta(v.Up, oldUp)
		down, r2 := counterDelta(v.Down, oldDown)
		if r1 || r2 {
			resets++
		}
		if up > 0 || down > 0 {
			begin := from
			if r1 || r2 {
				begin = to
			}
			for _, p := range dailyPieces(up, down, begin, to) {
				if _, err = tx.Exec(`INSERT INTO monitor_usage(at,email,category,up,down) VALUES(?,?,?,?,?) ON CONFLICT(at,email,category) DO UPDATE SET up=up+excluded.up,down=down+excluded.down`, p.Day, v.Counter.Email, v.Counter.Category, p.Up, p.Down); err != nil {
					return 0, err
				}
			}
		}
		if _, err = tx.Exec("INSERT INTO monitor_counters(counter_key,up,down) VALUES(?,?,?) ON CONFLICT(counter_key) DO UPDATE SET up=excluded.up,down=excluded.down", v.Key, v.Up, v.Down); err != nil {
			return 0, err
		}
	}
	return resets, nil
}

func syncMonitor() error {
	monitorMu.Lock()
	defer monitorMu.Unlock()
	conf, err := loadMonitorConfig()
	if err != nil {
		return err
	}
	rows, err := readMonitorCounters(conf)
	if err != nil {
		return err
	}
	// Reject a changed account/template instead of silently showing a flat counter.
	if err = validateMonitorAccounts(conf); err != nil {
		return err
	}
	day, _ := monitorPeriod(time.Now())
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	previous := conf.Created
	err = tx.QueryRow("SELECT value FROM monitor_state WHERE name='last_sample'").Scan(&previous)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if previous < conf.Created {
		previous = conf.Created
	}
	resets, err := recordCounters(tx, rows, previous, now)
	if err != nil {
		return err
	}
	if resets > 0 {
		if _, err = tx.Exec("INSERT INTO monitor_state(name,value) VALUES('last_reset',?) ON CONFLICT(name) DO UPDATE SET value=excluded.value", strconv.FormatInt(now, 10)); err != nil {
			return err
		}
		log.Printf("accounting counters reset (%d); pre-reset unsampled bytes cannot be recovered", resets)
	}
	if _, err = tx.Exec("INSERT INTO monitor_state(name,value) VALUES('last_sample',?) ON CONFLICT(name) DO UPDATE SET value=excluded.value", strconv.FormatInt(now, 10)); err != nil {
		return err
	}
	// Only daily aggregates survive; one row per device/category/day, capped at 30 days.
	err = pruneMonitorUsage(tx, day)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func pruneMonitorUsage(tx *sql.Tx, day int64) error {
	_, err := tx.Exec("DELETE FROM monitor_usage WHERE at < ?", time.Unix(day, 0).In(beijing).AddDate(0, 0, -29).Unix())
	return err
}

func startMonitor() {
	if _, err := loadMonitorConfig(); err != nil {
		if !os.IsNotExist(err) {
			log.Printf("monitor disabled: %v", err)
		}
		return
	}
	go func() {
		var lastError time.Time
		for {
			if err := syncMonitor(); err != nil {
				if errors.Is(err, errMonitorAccountsChanged) {
					if requestErr := requestMonitorReconcile(); requestErr != nil {
						err = fmt.Errorf("%v; automatic sync: %w", err, requestErr)
					}
				}
				if time.Since(lastError) > 15*time.Minute {
					log.Printf("monitor collection: %v", err)
					lastError = time.Now()
				}
			}
			time.Sleep(time.Until(time.Now().Truncate(time.Minute).Add(time.Minute)))
		}
	}()
}

func requestMonitorReconcile() error {
	reconcileMu.Lock()
	defer reconcileMu.Unlock()
	if time.Since(lastReconcileRequest) < time.Minute {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// An independent unit survives stopping the Bot during its final sample.
	if err := exec.CommandContext(ctx, "systemctl", "start", "--no-block", "traffic-bot-reconcile.service").Run(); err != nil {
		return err
	}
	lastReconcileRequest = time.Now()
	return nil
}

func monitorPeriod(now time.Time) (int64, int64) {
	n := now.In(beijing)
	start := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, beijing)
	return start.Unix(), start.AddDate(0, 0, 1).Unix()
}

func queryMonitorUsage(start, end int64, email string) ([]usageRow, error) {
	rows, err := db.Query(`SELECT email,category,SUM(up),SUM(down) FROM monitor_usage WHERE at>=? AND at<? AND (?='' OR email=?) GROUP BY email,category ORDER BY SUM(up+down) DESC`, start, end, email, email)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []usageRow
	for rows.Next() {
		var r usageRow
		if err := rows.Scan(&r.Email, &r.Category, &r.Up, &r.Down); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

func handleMonitor(env *AppEnv, chatID int64, command string) {
	conf, err := loadMonitorConfig()
	if err != nil {
		SendPlainMessage(chatID, "设备监控尚未启用：需要先运行 monitor_setup.py。现有 /status 总流量监控不受影响。")
		return
	}
	if err = syncMonitor(); err != nil {
		log.Printf("monitor query: %v", err)
		if errors.Is(err, errMonitorAccountsChanged) {
			if requestMonitorReconcile() == nil {
				SendPlainMessage(chatID, "检测到账号变化，正在自动同步监控，请稍后重试。已有历史用量会保留。")
				return
			}
		}
		SendPlainMessage(chatID, "设备监控刷新失败，未返回旧值。请检查专用计数规则。")
		return
	}
	start, end := monitorPeriod(time.Now())
	parts := strings.Fields(command)
	if len(parts) > 1 {
		if date, e := time.ParseInLocation("2006-01-02", parts[len(parts)-1], beijing); e == nil {
			start, end = monitorPeriod(date)
			parts = parts[:len(parts)-1]
		}
	}
	cutoff, _ := monitorPeriod(time.Now().In(beijing).AddDate(0, 0, -29))
	if start < cutoff || end <= conf.Created || start > time.Now().Unix() {
		SendPlainMessage(chatID, "该日期没有可用历史数据。只保留启用后的最近 30 天，北京时间日期格式为 YYYY-MM-DD。")
		return
	}
	filter := ""
	if len(parts) > 1 {
		wanted := strings.Join(parts[1:], " ")
		for _, c := range conf.Counters {
			if strings.EqualFold(c.Email, wanted) || strings.EqualFold(deviceLabel(c.Email), wanted) {
				if filter != "" && filter != c.Email {
					SendPlainMessage(chatID, "标签不唯一，请填写完整 email。")
					return
				}
				filter = c.Email
			}
		}
		if filter == "" {
			SendPlainMessage(chatID, "未找到该设备，请使用面板设置的 email 或 /devices 中的标签。")
			return
		}
	}
	rows, err := queryMonitorUsage(start, end, filter)
	if err != nil {
		SendPlainMessage(chatID, "设备用量查询失败。")
		return
	}
	var b strings.Builder
	if parts[0] == "/devices" {
		fmt.Fprintf(&b, "设备用量 · %s（北京时间）\n", time.Unix(start, 0).In(beijing).Format("2006-01-02"))
		seen := map[string]bool{}
		totals := map[string]usageRow{}
		for _, r := range rows {
			v := totals[r.Email]
			v.Up += r.Up
			v.Down += r.Down
			totals[r.Email] = v
		}
		for _, c := range conf.Counters {
			if seen[c.Email] || filter != "" && filter != c.Email {
				continue
			}
			seen[c.Email] = true
			r := totals[c.Email]
			fmt.Fprintf(&b, "\n%s\n↑ %s  ↓ %s  合计 %s\n", deviceLabel(c.Email), FormatBytes(float64(r.Up)), FormatBytes(float64(r.Down)), FormatBytes(float64(r.Up+r.Down)))
		}
	} else {
		fmt.Fprintf(&b, "网站分类用量 · %s（北京时间）\n", time.Unix(start, 0).In(beijing).Format("2006-01-02"))
		if filter != "" {
			fmt.Fprintf(&b, "设备：%s\n", deviceLabel(filter))
		}
		for _, c := range conf.Categories {
			var up, down int64
			for _, r := range rows {
				if r.Category == c.Name {
					up += r.Up
					down += r.Down
				}
			}
			fmt.Fprintf(&b, "\n%s：%s（↑%s / ↓%s）", c.Name, FormatBytes(float64(up+down)), FormatBytes(float64(up)), FormatBytes(float64(down)))
		}
	}
	fmt.Fprintf(&b, "\n\n开始记录：%s（北京时间）\n用量为站点侧 IP 层收发字节，不等同于 VPS 网卡双向总量。未识别流量计入 Other。每分钟采样，跨日样本按时间比例分摊；查询时刷新。保留 30 天日汇总，不保存访问日志。", time.Unix(conf.Created, 0).In(beijing).Format("01-02 15:04"))
	var reset int64
	if e := db.QueryRow("SELECT value FROM monitor_state WHERE name='last_reset'").Scan(&reset); e == nil && reset >= start && reset < end {
		b.WriteString("\n注意：今天计数器曾重置，重置前未采样部分可能缺失。")
	}
	SendPlainMessage(chatID, b.String())
}

func validateMonitorAccounts(conf *MonitorConfig) error {
	b, err := os.ReadFile("/usr/local/x-ui/bin/config.json")
	if err != nil {
		return err
	}
	return validateMonitorAccountData(conf, b)
}

func validateMonitorAccountData(conf *MonitorConfig, b []byte) error {
	var live struct {
		Inbounds []struct {
			Settings struct {
				Clients []struct {
					Email string `json:"email"`
				} `json:"clients"`
			} `json:"settings"`
		} `json:"inbounds"`
		Outbounds []struct {
			StreamSettings struct {
				Sockopt struct {
					Mark uint32 `json:"mark"`
				} `json:"sockopt"`
			} `json:"streamSettings"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(b, &live); err != nil {
		return err
	}
	emails := map[string]bool{}
	marks := map[uint32]bool{}
	for _, i := range live.Inbounds {
		for _, u := range i.Settings.Clients {
			if u.Email != "" {
				emails[u.Email] = true
			}
		}
	}
	for _, o := range live.Outbounds {
		marks[o.StreamSettings.Sockopt.Mark] = true
	}
	expected := map[string]bool{}
	for _, email := range conf.PendingEmails {
		if !emails[email] {
			return fmt.Errorf("pending accounting account disappeared")
		}
		expected[email] = true
	}
	for _, c := range conf.Counters {
		// Only the explicit reconciliation helper may retire a missing account
		// for its final sample. Active/unknown accounts still fail closed.
		if c.Retired {
			if emails[c.Email] {
				return fmt.Errorf("retired accounting account is still active")
			}
			continue
		}
		expected[c.Email] = true
		if !emails[c.Email] || !marks[c.Mark] {
			return fmt.Errorf("%w; accounting rules need reconciliation", errMonitorAccountsChanged)
		}
	}
	if len(expected) != len(emails) {
		return fmt.Errorf("%w; new accounts need reconciliation", errMonitorAccountsChanged)
	}
	return nil
}

func deviceLabel(email string) string {
	return strings.TrimSuffix(strings.SplitN(email, "@", 2)[0], ".love")
}
