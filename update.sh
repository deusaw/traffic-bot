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

echo "[3/4] 重启服务..."
systemctl restart traffic-bot

echo "[4/4] 检查状态..."
sleep 2
systemctl status traffic-bot --no-pager

echo ""
echo "========================================="
echo "  ✅ 更新完成！"
echo "========================================="
