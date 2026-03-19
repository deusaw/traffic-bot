# Traffic Bot

VPS 双向流量监控与 Telegram 推送助手，专为搬瓦工等按双向流量计费的 VPS 设计。

## 功能

- 三步引导式初始化配置
- 每日定时流量日报推送
- 10%~100% 阶梯式流量告警
- 按账单周期统计（非自然月）
- 流量校准偏移量支持
- 历史数据自动清理

## 部署

```bash
# 编译
CGO_ENABLED=1 go build -o traffic-bot .

# 一键部署（CentOS 7）
sudo bash install.sh
```

## 环境变量

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `BOT_TOKEN` | Telegram Bot Token | 必填 |
| `CHAT_ID` | 授权的 Chat ID | 必填 |
| `INTERFACE` | 监控网卡 | eth0 |
| `DB_PATH` | 数据库路径 | /opt/traffic-bot/traffic-bot.db |

## Bot 指令

- `/start` `/help` - 帮助 / 初始化
- `/status` - 当前流量状态
- `/daily` - 每日流量明细
- `/settings` - 重新配置
- `/calibrate <值> <单位>` - 校准偏移量
