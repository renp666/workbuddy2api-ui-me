#!/bin/sh
# 同时启动 qoder-proxy（3000）与登录控制进程（3001），均只监听 127.0.0.1。
# 任一进程退出即终止另一个并以非零码退出，交由 compose restart 策略统一拉起。
set -eu

node /app/clean/server.js &
proxy_pid=$!
node /opt/qoder-login-ctl.cjs &
ctl_pid=$!

trap 'kill "$proxy_pid" "$ctl_pid" 2>/dev/null || true' TERM INT

wait -n "$proxy_pid" "$ctl_pid"
status=$?
kill "$proxy_pid" "$ctl_pid" 2>/dev/null || true
exit "$status"
