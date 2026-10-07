#!/bin/sh
# AutoClaw 旁路 sidecar 入口：把 console 注入的共享密钥转成 bridge.py 认识的
# AC2A_API_KEY，固定回环监听与持久卷路径，再 exec 业务进程。
set -eu

DATA_DIR="${BUDDY_DATA_DIR:-/data}"
mkdir -p "$DATA_DIR/auths"

# 数据面密钥由 compose 从 WB2A_AUTOCLAW_KEY 注入；为空时 bridge 视为免鉴权，
# 但本仓旁路只监听 127.0.0.1，无鉴权不会外泄。
export AC2A_LISTEN="127.0.0.1:7865"
export AC2A_AUTH_DIR="$DATA_DIR/auths"
export AC2A_STATE_FILE="$DATA_DIR/state.json"
if [ -n "${WB2A_AUTOCLAW_KEY:-}" ]; then
    export AC2A_API_KEY="$WB2A_AUTOCLAW_KEY"
fi

cd /app
exec python bridge.py -config /app/config.json
