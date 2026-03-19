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

支持 CentOS 7/8、Ubuntu 18+、Debian 10+，一键脚本自动处理所有依赖：

```bash
# 方式一：直接远程执行（需要交互输入 Bot Token 和 Chat ID）
curl -sL https://raw.githubusercontent.com/deusaw/traffic-bot/main/install.sh | bash

# 方式二：克隆后执行
git clone https://github.com/deusaw/traffic-bot.git
cd traffic-bot
bash install.sh

# 方式三：预设环境变量免交互
export BOT_TOKEN="your_token"
export CHAT_ID="your_chat_id"
bash install.sh
```

脚本会自动：安装 Go、gcc、vnStat → 编译项目 → 检测网卡 → 注册 Systemd 服务 → 启动

后续更新：
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

- `/start` `/help` - 帮助 / 初始化
- `/status` - 当前流量状态
- `/daily` - 每日流量明细
- `/settings` - 重新配置
- `/calibrate <值> <单位>` - 校准偏移量
