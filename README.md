# Traffic Bot

VPS 双向流量监控与 Telegram 推送助手，专为搬瓦工等按双向流量计费的 VPS 设计。

## 功能

- 三步引导式初始化配置（总流量 / 重置日 / 推送时间）
- 每日定时流量日报推送
- 10% ~ 100% 阶梯式流量告警（每 5 分钟检测）
- 按账单周期统计（支持自定义重置日，非自然月）
- 手动同步面板用量，自动推荐校准倍率
- 校准倍率设置，修正 vnStat 与面板的偏差
- 历史数据自动清理（仅保留当前和上一周期）
- 自动适配 vnStat 1.x 和 2.x

## 支持系统

- CentOS 7/8, Ubuntu 18.04+, Debian 10+
- 架构: x86_64 / ARM64

## 一键部署

```bash
git clone https://github.com/deusaw/traffic-bot.git
cd traffic-bot
bash install.sh
```

也可预设环境变量免交互:

```bash
export BOT_TOKEN="your_token"
export CHAT_ID="your_chat_id"
bash install.sh
```

脚本自动完成: 安装依赖 -> 编译 -> 检测网卡 -> 注册服务 -> 启动

## 后续更新

```bash
bash /opt/traffic-bot/update.sh
```

## 环境变量

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `BOT_TOKEN` | Telegram Bot Token | 必填 |
| `CHAT_ID` | 授权的 Chat ID | 必填 |
| `INTERFACE` | 监控网卡 | eth0 |
| `DB_PATH` | 数据库路径 | /opt/traffic-bot/traffic-bot.db |

## Bot 指令

| 指令 | 说明 |
|------|------|
| `/start` | 首次使用进入配置向导 |
| `/help` | 显示帮助信息 |
| `/status` | 查看当前计费周期流量状态 |
| `/daily` | 查看当前周期每日流量明细 |
| `/sync` | 手动同步面板实际用量 |
| `/calibrate` | 设置校准倍率 |
| `/config` | 查看当前配置 |
| `/report` | 立即发送一次日报 |
| `/settings` | 重新配置 |

## 流量计算

```
已用量 = vnStat周期累计 * 校准倍率 + 同步偏移
```

- `/sync` 输入面板用量后覆盖本地显示值，推荐倍率 = 实际值 / vnStat原始值
- `/calibrate` 设置乘法倍率修正系统性偏差
- 新计费周期自动重置偏移和告警状态

## 运维

```bash
systemctl status traffic-bot    # 查看状态
journalctl -u traffic-bot -f    # 实时日志
systemctl restart traffic-bot   # 重启
```
