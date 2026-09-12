#!/bin/bash
set -e

echo "========================================="
echo "  Traffic Bot 一键更新脚本"
echo "========================================="

export PATH=$PATH:/usr/local/go/bin
cd /opt/traffic-bot

echo "[1/4] 拉取最新代码..."
git pull

echo "[2/4] 编译..."
CGO_ENABLED=1 go build -o traffic-bot .

echo "[3/4] 修复 vnStat 数据目录权限并重启服务..."
if id vnstat &>/dev/null && [ -d /var/lib/vnstat ]; then
  sed -i -E 's/^[[:space:]]*UpdateInterval[[:space:]]+[0-9]+/UpdateInterval 5/' /etc/vnstat.conf
  chown -R vnstat:vnstat /var/lib/vnstat
  systemctl restart vnstat 2>/dev/null || systemctl restart vnstatd 2>/dev/null || true
fi
systemctl restart traffic-bot

echo "[4/4] 检查状态..."
sleep 2
systemctl status traffic-bot --no-pager

echo ""
echo "========================================="
echo "  ✅ 更新完成！"
echo "========================================="
