#!/bin/sh
# 在 Linux 网关上安装 / 升级 PortRelay 服务端（systemd）。
#
#   sudo ./install-server.sh ./portrelay-server
#
# 首次安装会生成随机 token 写入 /etc/portrelay-server.env；升级时保留已有配置。
set -eu

BIN="${1:-./portrelay-server}"
HERE="$(cd "$(dirname "$0")" && pwd)"
ENV_FILE=/etc/portrelay-server.env
UNIT=/etc/systemd/system/portrelay-server.service

if [ "$(id -u)" -ne 0 ]; then
    echo "请用 root 运行：sudo $0 $*" >&2
    exit 1
fi
if [ ! -x "$BIN" ]; then
    echo "找不到可执行文件 $BIN" >&2
    exit 1
fi

if [ -f /usr/local/bin/portrelay-server ]; then
    cp /usr/local/bin/portrelay-server "/usr/local/bin/portrelay-server.bak-$(date +%Y%m%d-%H%M%S)"
fi
install -m 755 "$BIN" /usr/local/bin/portrelay-server

if [ ! -f "$ENV_FILE" ]; then
    if command -v openssl >/dev/null 2>&1; then
        TOKEN="$(openssl rand -hex 24)"
    else
        TOKEN="$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"
    fi
    umask 077
    sed "s/^PORTRELAY_TOKEN=.*/PORTRELAY_TOKEN=$TOKEN/" "$HERE/portrelay-server.env.example" > "$ENV_FILE"
    chmod 600 "$ENV_FILE"
    echo "已生成 token 并写入 $ENV_FILE"
fi

install -m 644 "$HERE/portrelay-server.service" "$UNIT"
systemctl daemon-reload
systemctl enable portrelay-server >/dev/null
systemctl restart portrelay-server
sleep 1
systemctl --no-pager --lines=5 status portrelay-server || true

echo
echo "完成。查看 token：sudo grep PORTRELAY_TOKEN $ENV_FILE"
echo "防火墙需放通 TCP 33000-34000（控制端口 + 通道端口池）"
