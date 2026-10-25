<p align="center">
  <h1 align="center">🚦 Traffic Bot</h1>
  <p align="center">Lightweight VPS bilateral traffic monitoring Telegram Bot</p>
  <p align="center">Designed for VPS providers like BandwagonHost that bill by <b>inbound + outbound</b> combined traffic</p>
  <p align="center">🇬🇧 English | <a href="./README_CN.md">🇨🇳 中文</a></p>
</p>

### Optional device × website accounting

For compatible X-UI Linux hosts with a direct Freedom default outbound, run
`python /opt/traffic-bot/monitor_setup.py --plan`, then `--apply` to enable the
optional integration (backs up the template and briefly restarts X-UI/Xray).
Each device needs a unique VLESS ID/email; all accounts can share port 443.

Use `/devices`, `/sites`, `/sites HOME`, or append a `YYYY-MM-DD` date.
New accounting uses **Asia/Shanghai midnight boundaries**, samples once per minute
and refreshes on queries. Only daily device/category byte aggregates are retained,
automatically expiring after 30 days. No access logs, browsing history, source IP
history or packet captures are collected. YouTube (including the installed
geosite rule), Netflix, OpenAI, Telegram, Bilibili, TikTok, Douyin, Instagram,
Facebook, Steam, EpicGames, Roblox, Leigod, iCloud, OneDrive, Dropbox, MEGA,
AppleUpdate, Microsoft and Other are supported (20 categories).
Roblox also matches its ARIN-registered 128.116.0.0/17 network. Shared cloud
networks and broad UDP port ranges are not labelled as games. Only connections
passing through this VPS are measurable; an accelerator tunnel can hide Roblox
behind Leigod or Other. Each connection belongs to one category only.

Existing integrations: after updating source, validate with
`python /opt/traffic-bot/monitor_setup.py --plan-upgrade`, then `--upgrade`.
This preserves existing counter identities/history and Telegram settings, appends
missing rules without resetting counts, and briefly restarts X-UI/Xray. Manual
template edits or changed accounts abort the upgrade. A single bounded recovery
snapshot replaces `monitor-backup/upgrade.json`; the original install backup is
retained. New categories accrue prospectively; old Other totals are not reclassified.

Counters measure the site's IP-layer bytes, including connection overhead, not
the VPS's combined interface accounting. Cross-midnight samples are apportioned
by elapsed time; resets or collection outages can limit completeness. Unknown or
unobservable domains remain Other. The collector never resets X-UI API counters.
The helper adds counter-only chains and socket marks, preserves credentials and
existing blocking rules, and refuses conflicting fwmark policy routes.
New/renamed users require reconciliation; mismatches fail explicitly.
The original template is retained in `monitor-backup/`; `--rollback` restores it
unless it has subsequently been edited. Existing Telegram credentials and Bot
data are preserved. See [Chinese operational details](README_CN.md).

### Query freshness and daily dates

On vnStat 1.x, `/status`, `/daily`, and `/sync` overlay unsaved kernel interface
increments on persisted daily records. Repeated queries do not double-count;
traffic keeps accumulating after `/sync`. vnStat 2.x still uses saved data.
Counter resets and unfinished day rollover return an explicit refresh error.
Daily reports select yesterday in the **push timezone**, but daily buckets remain
in the **VPS timezone**, matching `/daily`. An unfinished bucket is marked
provisional; missing data is not reported as zero.
Read-only byte-level diagnostics: `traffic-bot --check-traffic --interface eth0`.
Updates support Git and archive installations and preserve the database and secrets.

---

## ✨ Features

| Feature | Description |
|---------|-------------|
| 📊 Daily Report | Auto-push daily digest at your chosen time — yesterday's usage, cycle total, days remaining |
| ⚠️ Threshold Alerts | Real-time alerts at 10%, 20% … 100% usage (checked every 5 minutes) |
| 🔄 Manual Sync | Sync actual usage from your VPS panel, overriding local statistics |
| 📐 Calibration Factor | Set a multiplier to correct systematic drift between vnStat and your panel |
| 📅 Custom Billing Cycle | Supports any reset day (e.g. the 15th), not limited to calendar months |
| 🕐 Dual Timezone | Separate timezones for push notifications (your local time) and billing cycle (provider's reset time) — system clock untouched |
| 🧹 Auto Cleanup | Only retains current and previous cycle data — database stays lean |
| 🔐 Auth Protection | Responds only to your authorized Chat ID |

## 📋 Supported Environments

| Item | Support |
|------|---------|
| OS | CentOS 7/8, Ubuntu 18.04/20.04/22.04, Debian 10/11/12 |
| Arch | x86_64 (AMD64), ARM64 (AArch64) |
| vnStat | Auto-adapts to both 1.x (older systems) and 2.x (newer systems) |

---

## 🚀 Quick Start

### Prerequisites

You'll need two things before deploying:

1. **Telegram Bot Token** — Message [@BotFather](https://t.me/BotFather) on Telegram, send `/newbot`, and follow the prompts. You'll receive a token like `123456789:ABCdefGHIjklMNOpqrsTUVwxyz`
2. **Your Chat ID** — Message [@userinfobot](https://t.me/userinfobot) on Telegram, it will reply with your numeric ID

### One-Click Deploy

SSH into your VPS as root and run:

```bash
git clone https://github.com/deusaw/traffic-bot.git
cd traffic-bot
bash install.sh
```

On a fresh system without Git, install it first:

```bash
yum install -y git || apt update && apt install -y git
```

The script will prompt you for Bot Token and Chat ID, then handle everything else:

> Install curl/gcc/git → Install Go compiler → Install & configure vnStat → Compile → Register systemd service → Start

To skip interactive prompts, set environment variables beforehand:

```bash
export BOT_TOKEN="your_token"
export CHAT_ID="your_chat_id"
bash install.sh
```

Once deployed, send `/start` to your Bot on Telegram and follow the 5-step setup wizard.

### Updating

When a new version is available, run one command on your VPS:

```bash
bash /opt/traffic-bot/update.sh
```

Pulls latest code → Recompiles → Restarts service. Fully automatic.

---

## 🤖 Bot Commands

### Basic Commands

| Command | Description |
|---------|-------------|
| `/start` | First-time setup wizard (bandwidth → reset day → push time → push timezone → billing timezone) |
| `/help` | Show all available commands |
| `/status` | View current billing cycle status with progress bar and days remaining |
| `/daily` | View daily upload/download breakdown for the current cycle |
| `/config` | View current settings (bandwidth, reset day, push time, timezones, calibration factor) |
| `/report` | Trigger an immediate daily report (don't wait for scheduled time) |
| `/settings` | Re-enter setup wizard to change bandwidth, reset day, push time, or timezones |

### Advanced Commands

| Command | Description |
|---------|-------------|
| `/sync` | Manually sync panel usage. Enter the actual usage shown on your VPS panel (e.g. `6.81 GB`). Bot overwrites local stats and may suggest a calibration factor based on the difference. Reply "yes" to apply |
| `/sync 6.81 GB` | Same as above, inline shorthand |
| `/calibrate` | Interactive calibration factor setup, showing current factor and raw vnStat data |
| `/calibrate 1.25` | Directly set calibration factor to 1.25 |
| `/cancel` | Cancel an ongoing `/sync` or `/calibrate` interaction |

> **When do I need `/sync` and `/calibrate`?**
>
> vnStat and your VPS panel may report different numbers — this is common. For example, your panel shows 100 GB used, but vnStat only reports 80 GB.
>
> - Use `/sync 100 GB` to tell the Bot the real value. It will immediately show 100 GB and suggest a factor of 1.25 (= 100 ÷ 80)
> - Reply "yes" to apply the factor. From then on, vnStat data is automatically multiplied by 1.25 — no more manual syncing needed

---

## 🕐 Timezone Configuration

The bot uses two separate timezones (neither changes your system clock):

| Timezone | Purpose | Example |
|----------|---------|---------|
| Push Timezone | When you receive daily reports and alerts (your local time) | `Asia/Shanghai` |
| Billing Timezone | When the billing cycle resets (your VPS provider's time) | `America/New_York` |

For example, if you're in China using a BandwagonHost DC9 server (US East Coast):
- Push Timezone: `Asia/Shanghai` — so `10:00` means Beijing time 10 AM
- Billing Timezone: `America/New_York` — traffic resets at midnight Eastern Time

Common timezone values: `UTC`, `Asia/Shanghai`, `Asia/Tokyo`, `America/New_York`, `America/Los_Angeles`, `Europe/London`

---

## ⚙️ Environment Variables

Passed via a mode-600 environment file. The install script configures it automatically. To modify manually:

```bash
# Edit the service file
nano /etc/traffic-bot.env

# Reload and restart after changes
systemctl daemon-reload
systemctl restart traffic-bot
```

| Variable | Description | Default |
|----------|-------------|---------|
| `BOT_TOKEN` | Telegram Bot Token | **Required** |
| `CHAT_ID` | Authorized Telegram Chat ID (only this ID can operate the Bot) | **Required** |
| `INTERFACE` | Network interface to monitor | `eth0` (auto-detected by install script) |
| `DB_PATH` | SQLite database file path | `/opt/traffic-bot/traffic-bot.db` |

---

## 🛠️ Operations

```bash
# Check service status
systemctl status traffic-bot

# View live logs (Ctrl+C to exit)
journalctl -u traffic-bot -f

# Restart service
systemctl restart traffic-bot

# Stop service
systemctl stop traffic-bot

# Disable auto-start on boot
systemctl disable traffic-bot
```

---

## 📁 Project Structure

```
traffic-bot/
├── main.go          # Entry point
├── config.go        # Environment variable loading
├── db.go            # SQLite database operations
├── vnstat.go        # vnStat data collection (auto-adapts 1.x / 2.x)
├── bot.go           # Telegram Bot command handling
├── scheduler.go     # Scheduled tasks (daily report, alert checks, data cleanup)
├── utils.go         # Utilities (formatting, cycle calculation, progress bar)
├── install.sh       # One-click deploy script
├── update.sh        # One-click update script
├── traffic-bot.service  # Systemd service template
├── go.mod           # Go module dependencies
└── README.md
```

## 📄 License

MIT
