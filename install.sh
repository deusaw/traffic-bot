#!/bin/bash
set -e

echo "========================================="
echo "  Traffic Bot 一键部署脚本"
echo "========================================="

# 检查 root 权限
if [ "$EUID" -ne 0 ]; then
  echo "❌ 请使用 root 权限运行此脚本"
  exit 1
fi

# 1. 安装 vnStat
echo "[1/5] 安装 vnStat..."
if ! command -v vnstat &> /dev/null; then
  yum install -y epel-release
  yum install -y vnstat
  systemctl enable vnstat
  systemctl start vnstat
  echo "✅ vnStat 已安装并启动"
else
  echo "✅ vnStat 已存在，跳过安装"
fi

# 2. 检测主网卡
IFACE=$(ip route | grep default | awk '{print $5}' | head -1)
if [ -z "$IFACE" ]; then
  IFACE="eth0"
fi
echo "检测到主网卡: $IFACE"

# 3. 创建安装目录
echo "[2/5] 创建安装目录..."
mkdir -p /opt/traffic-bot

# 4. 编译（如果有 Go 环境）或复制二进制
echo "[3/5] 部署二进制文件..."
if [ -f "./traffic-bot" ]; then
  cp ./traffic-bot /opt/traffic-bot/traffic-bot
  chmod +x /opt/traffic-bot/traffic-bot
  echo "✅ 二进制文件已部署"
else
  echo "⚠️  未找到编译好的 traffic-bot 二进制文件"
  echo "    请先执行: CGO_ENABLED=1 go build -o traffic-bot ."
  echo "    然后重新运行此脚本"
  exit 1
fi

# 5. 配置环境变量
echo "[4/5] 配置 Systemd 服务..."
if [ -z "$BOT_TOKEN" ] || [ -z "$CHAT_ID" ]; then
  read -p "请输入 Telegram Bot Token: " BOT_TOKEN
  read -p "请输入授权的 Chat ID: " CHAT_ID
fi

cat > /etc/systemd/system/traffic-bot.service << EOF
[Unit]
Description=Traffic Bot - VPS 流量监控 Telegram 助手
After=network.target

[Service]
Type=simple
ExecStart=/opt/traffic-bot/traffic-bot
Restart=always
RestartSec=5
Environment=BOT_TOKEN=${BOT_TOKEN}
Environment=CHAT_ID=${CHAT_ID}
Environment=INTERFACE=${IFACE}
Environment=DB_PATH=/opt/traffic-bot/traffic-bot.db
WorkingDirectory=/opt/traffic-bot

[Install]
WantedBy=multi-user.target
EOF

# 6. 启动服务
echo "[5/5] 启动服务..."
systemctl daemon-reload
systemctl enable traffic-bot
systemctl start traffic-bot

echo ""
echo "========================================="
echo "  ✅ Traffic Bot 部署完成！"
echo "========================================="
echo "  网卡: $IFACE"
echo "  服务状态: systemctl status traffic-bot"
echo "  查看日志: journalctl -u traffic-bot -f"
echo "  请在 Telegram 中向 Bot 发送 /start 开始配置"
echo "========================================="
