#!/bin/bash
set -e

echo "========================================="
echo "  Traffic Bot 一键部署脚本"
echo "  支持 CentOS 7/8, Ubuntu 18+, Debian 10+"
echo "========================================="

if [ "$EUID" -ne 0 ]; then
  echo "❌ 请使用 root 权限运行此脚本"
  exit 1
fi

# ---- 检测发行版 ----
detect_distro() {
  if [ -f /etc/os-release ]; then
    . /etc/os-release
    DISTRO=$ID
  elif command -v yum &> /dev/null; then
    DISTRO="centos"
  elif command -v apt &> /dev/null; then
    DISTRO="ubuntu"
  else
    DISTRO="unknown"
  fi
  echo "检测到系统: $DISTRO"
}

# ---- 安装 vnStat ----
install_vnstat() {
  echo "[1/6] 安装 vnStat..."
  if command -v vnstat &> /dev/null; then
    echo "✅ vnStat 已存在，跳过"
    return
  fi

  case "$DISTRO" in
    centos|rhel|rocky|alma|fedora)
      yum install -y epel-release 2>/dev/null || true
      yum install -y vnstat
      ;;
    ubuntu|debian|linuxmint|pop)
      apt update -y
      apt install -y vnstat
      ;;
    *)
      echo "❌ 不支持的系统: $DISTRO，请手动安装 vnstat"
      exit 1
      ;;
  esac

  systemctl enable vnstat 2>/dev/null || systemctl enable vnstatd 2>/dev/null || true
  systemctl start vnstat 2>/dev/null || systemctl start vnstatd 2>/dev/null || true
  echo "✅ vnStat 已安装并启动"
}

# ---- 安装编译依赖 ----
install_build_deps() {
  echo "[2/6] 安装编译依赖..."
  case "$DISTRO" in
    centos|rhel|rocky|alma|fedora)
      yum install -y gcc git sqlite 2>/dev/null || true
      ;;
    ubuntu|debian|linuxmint|pop)
      apt install -y gcc git sqlite3 2>/dev/null || true
      ;;
  esac
}

# ---- 安装 Go ----
install_go() {
  echo "[3/6] 检查 Go 环境..."
  if command -v go &> /dev/null; then
    echo "✅ Go 已存在: $(go version)"
    return
  fi

  echo "安装 Go 1.21..."
  ARCH=$(uname -m)
  case "$ARCH" in
    x86_64)  GOARCH="amd64" ;;
    aarch64) GOARCH="arm64" ;;
    armv7l)  GOARCH="armv6l" ;;
    *)       echo "❌ 不支持的架构: $ARCH"; exit 1 ;;
  esac

  curl -sL "https://go.dev/dl/go1.21.13.linux-${GOARCH}.tar.gz" -o /tmp/go.tar.gz
  rm -rf /usr/local/go
  tar -C /usr/local -xzf /tmp/go.tar.gz
  rm -f /tmp/go.tar.gz

  export PATH=$PATH:/usr/local/go/bin
  grep -q '/usr/local/go/bin' /etc/profile || echo 'export PATH=$PATH:/usr/local/go/bin' >> /etc/profile

  echo "✅ Go 已安装: $(go version)"
}

# ---- 拉取代码并编译 ----
build_bot() {
  echo "[4/6] 拉取代码并编译..."
  mkdir -p /opt/traffic-bot

  if [ -d /opt/traffic-bot/.git ]; then
    cd /opt/traffic-bot
    git pull
  elif [ -f ./go.mod ] && grep -q "traffic-bot" ./go.mod 2>/dev/null; then
    # Running from source directory
    cp -r ./* /opt/traffic-bot/
    cd /opt/traffic-bot
  else
    cd /opt/traffic-bot
    if [ ! -f go.mod ]; then
      git clone https://github.com/deusaw/traffic-bot.git .
    fi
  fi

  cd /opt/traffic-bot
  export PATH=$PATH:/usr/local/go/bin
  go mod tidy
  CGO_ENABLED=1 go build -o traffic-bot .
  chmod +x traffic-bot
  echo "✅ 编译完成"
}

# ---- 检测网卡 ----
detect_interface() {
  IFACE=$(ip route | grep default | awk '{print $5}' | head -1)
  if [ -z "$IFACE" ]; then
    IFACE="eth0"
  fi
  echo "检测到主网卡: $IFACE"

  # 初始化 vnStat 数据库
  vnstat -u -i "$IFACE" 2>/dev/null || true
}

# ---- 配置 Systemd 服务 ----
setup_service() {
  echo "[5/6] 配置 Systemd 服务..."

  if [ -z "$BOT_TOKEN" ]; then
    read -p "请输入 Telegram Bot Token: " BOT_TOKEN
  fi
  if [ -z "$CHAT_ID" ]; then
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

  systemctl daemon-reload
  systemctl enable traffic-bot
}

# ---- 启动 ----
start_bot() {
  echo "[6/6] 启动服务..."
  systemctl restart traffic-bot
  sleep 2

  echo ""
  echo "========================================="
  echo "  ✅ Traffic Bot 部署完成！"
  echo "========================================="
  echo "  系统: $DISTRO"
  echo "  网卡: $IFACE"
  echo "  服务状态: systemctl status traffic-bot"
  echo "  查看日志: journalctl -u traffic-bot -f"
  echo "  请在 Telegram 中向 Bot 发送 /start 开始配置"
  echo "========================================="
}

# ---- 主流程 ----
detect_distro
install_vnstat
install_build_deps
install_go
build_bot
detect_interface
setup_service
start_bot
