#!/bin/sh
# OW Bridge sidecar 入口：预写数据面密钥（与 console 的 WB2A_OPENCODE_KEY 一致），
# 清理容器重启后残留的 service.pid 锁，再 exec 业务进程。
set -eu

mkdir -p "${BUDDY_DATA_DIR:-/data}"
# 容器重启后旧 PID 已失效；残留锁可能误撞新进程 PID，导致启动被拒。
rm -f "${BUDDY_DATA_DIR:-/data}/service.pid"

if [ -n "${WB2A_OPENCODE_KEY:-}" ]; then
    printf '%s' "$WB2A_OPENCODE_KEY" > "${BUDDY_DATA_DIR:-/data}/api-key"
    chmod 600 "${BUDDY_DATA_DIR:-/data}/api-key"
fi

cd /app
exec node src/main.js
