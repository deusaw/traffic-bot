<p align="center">
  <h1 align="center">🚦 Traffic Bot</h1>
  <p align="center">轻量级 VPS 双向流量监控 Telegram Bot</p>
  <p align="center">专为搬瓦工 (BandwagonHost) 等按 <b>入站 + 出站</b> 双向计费的 VPS 设计</p>
  <p align="center"><a href="./README.md">🇬🇧 English</a> | 🇨🇳 中文</p>
</p>

---

## ✨ 功能特性

| 功能 | 说明 |
|------|------|
| 📊 每日日报 | 在你设定的时间自动推送昨日消耗、周期累计、剩余天数 |
| ⚠️ 阶梯告警 | 用量达到 10%、20% … 100% 时实时推送告警（每 5 分钟检测） |
| 🔄 手动同步 | 将 VPS 面板的实际用量同步到 Bot，覆盖本地统计 |
| 📐 校准倍率 | 设置乘法系数修正 vnStat 与面板之间的系统性偏差 |
| 📅 自定义周期 | 支持任意重置日（如每月 15 日），不局限于自然月 |
| 🧹 自动清理 | 仅保留当前和上一计费周期数据，数据库不会无限膨胀 |
| 🔐 鉴权保护 | 仅响应预设 Chat ID，其他人无法操作你的 Bot |

## 📋 支持环境

| 项目 | 支持范围 |
|------|----------|
| 操作系统 | CentOS 7/8、Ubuntu 18.04/20.04/22.04、Debian 10/11/12 |
| 架构 | x86_64 (AMD64)、ARM64 (AArch64) |
| vnStat | 1.x（CentOS 7 等老系统）和 2.x（Ubuntu 20+ 等新系统）均自动适配 |

---

## 🚀 快速开始

### 前置准备

在部署之前，你需要准备两样东西：

1. **Telegram Bot Token** — 在 Telegram 中找 [@BotFather](https://t.me/BotFather)，发送 `/newbot`，按提示创建后会给你一串 Token（格式类似 `123456789:ABCdefGHIjklMNOpqrsTUVwxyz`）
2. **你的 Chat ID** — 在 Telegram 中找 [@userinfobot](https://t.me/userinfobot)，发送任意消息，它会回复你的数字 ID

### 一键部署

SSH 登录你的 VPS，以 root 身份执行：

```bash
git clone https://github.com/deusaw/traffic-bot.git
cd traffic-bot
bash install.sh
```

脚本会引导你输入 Bot Token 和 Chat ID，其余全部自动处理：

> 安装 curl/gcc/git → 安装 Go 编译器 → 安装并配置 vnStat → 编译项目 → 注册开机自启服务 → 启动

如果你想跳过交互提示，可以提前设置环境变量：

```bash
export BOT_TOKEN="你的Token"
export CHAT_ID="你的ChatID"
bash install.sh
```

部署完成后，打开 Telegram 向你的 Bot 发送 `/start`，按引导完成三步配置即可。

### 后续更新

当项目有新版本时，在 VPS 上执行一条命令即可：

```bash
bash /opt/traffic-bot/update.sh
```

自动拉取最新代码 → 重新编译 → 重启服务，全程无需手动操作。

---

## 🤖 Bot 指令

### 基础指令

| 指令 | 说明 |
|------|------|
| `/start` | 首次使用时进入三步配置向导（总流量 → 重置日 → 推送时间） |
| `/help` | 显示所有可用指令 |
| `/status` | 查看当前计费周期的流量状态、进度条、剩余天数 |
| `/daily` | 查看当前周期内每一天的上行/下行流量明细 |
| `/config` | 查看当前配置（总流量配额、重置日、推送时间、校准倍率） |
| `/report` | 立即触发一次日报推送（不用等到设定时间） |
| `/settings` | 重新进入配置向导，修改总流量、重置日或推送时间 |

### 进阶指令

| 指令 | 说明 |
|------|------|
| `/sync` | 手动同步面板用量。输入面板上显示的实际已用流量（如 `6.81 GB`），Bot 会用这个值覆盖本地统计，并根据差异推荐一个校准倍率，回复「是」即可应用 |
| `/sync 6.81 GB` | 同上，一步到位的写法 |
| `/calibrate` | 交互式设置校准倍率，Bot 会显示当前倍率和 vnStat 原始数据供参考 |
| `/calibrate 1.25` | 直接设置校准倍率为 1.25（一步到位） |
| `/cancel` | 在 `/sync` 或 `/calibrate` 的交互过程中取消操作 |

> **什么时候需要用 `/sync` 和 `/calibrate`？**
>
> vnStat 统计的流量和 VPS 面板显示的可能有偏差（这很常见）。比如面板显示已用 100 GB，但 vnStat 只统计到 80 GB。
>
> - 用 `/sync 100 GB` 把面板的真实值告诉 Bot，Bot 会立刻显示 100 GB，并推荐倍率 1.25（= 100 ÷ 80）
> - 回复「是」应用倍率后，以后 vnStat 的数据会自动乘以 1.25，不用每次手动同步了

---

## ⚙️ 环境变量

通过 Systemd 服务文件传入，部署脚本会自动配置。如需手动修改：

```bash
# 编辑服务文件
nano /etc/systemd/system/traffic-bot.service

# 修改后重新加载并重启
systemctl daemon-reload
systemctl restart traffic-bot
```

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `BOT_TOKEN` | Telegram Bot Token | **必填** |
| `CHAT_ID` | 授权的 Telegram Chat ID（只有这个 ID 能操作 Bot） | **必填** |
| `INTERFACE` | 要监控的网卡名称 | `eth0`（部署脚本会自动检测） |
| `DB_PATH` | SQLite 数据库文件路径 | `/opt/traffic-bot/traffic-bot.db` |

---

## 🛠️ 常用运维命令

```bash
# 查看服务运行状态
systemctl status traffic-bot

# 查看实时日志（Ctrl+C 退出）
journalctl -u traffic-bot -f

# 重启服务
systemctl restart traffic-bot

# 停止服务
systemctl stop traffic-bot

# 禁用开机自启
systemctl disable traffic-bot
```

---

## 📁 项目结构

```
traffic-bot/
├── main.go          # 程序入口
├── config.go        # 环境变量加载
├── db.go            # SQLite 数据库操作
├── vnstat.go        # vnStat 数据采集（自动适配 1.x / 2.x）
├── bot.go           # Telegram Bot 指令处理
├── scheduler.go     # 定时任务（日报推送、告警检测、数据清理）
├── utils.go         # 工具函数（格式化、周期计算、进度条）
├── install.sh       # 一键部署脚本
├── update.sh        # 一键更新脚本
├── traffic-bot.service  # Systemd 服务模板
├── go.mod           # Go 模块依赖
└── README.md
```

## 📄 License

MIT
