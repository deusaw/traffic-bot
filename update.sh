#!/bin/bash
set -e

echo "========================================="
echo "  Traffic Bot 一键更新脚本"
echo "========================================="

cd /opt/traffic-bot

echo "[1/4] 拉取最新代码..."
git pull

echo "[2/4] 编译..."
CGO_ENABLED=1 go build -o traffic-bot .

echo "[3/4] 重启服务..."

# Handle DB schema migration: if old schema detected, remove DB to rebuild
if [ -f /opt/traffic-bot/traffic-bot.db ]; then
  HAS_OLD=$(sqlite3 /opt/traffic-bot/traffic-bot.db ".schema app_config" 2>/dev/null | grep -c "sync_usage" || true)
  HAS_NEW=$(sqlite3 /opt/traffic-bot/traffic-bot.db ".schema app_config" 2>/dev/null | grep -c "usage_offset" || true)
  if [ "$HAS_OLD" -gt 0 ] && [ "$HAS_NEW" -eq 0 ]; then
    echo "检测到旧数据库 schema，重建数据库..."
    rm -f /opt/traffic-bot/traffic-bot.db
  fi
fi

systemctl restart traffic-bot

echo "[4/4] 检查状态..."
sleep 2
systemctl status traffic-bot --no-pager

echo ""
echo "========================================="
echo "  ✅ 更新完成！"
echo "========================================="
