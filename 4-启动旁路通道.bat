@echo off
rem [4] Start the three optional sidecar channels (zcode + qoder + opencode)
rem next to the already-running core/console stack.
rem The first run recreates console once so it picks up the channel routing
rem env vars (brief 7863 downtime; admin sessions drop).
rem Sidecars share console's network namespace (network_mode: service:console).
rem Scripts [1] and [2] re-attach existing sidecars automatically, so you only
rem need this script after [3], after removing channels with [5], or after a
rem bare `docker compose down`/Docker Desktop restart left them out.
cd /d "%~dp0"
docker info >nul 2>&1
if errorlevel 1 (
  echo [ERROR] Docker is not running. Start Docker Desktop first, then retry.
  pause
  exit /b 1
)
if not exist ".build\compose.local-build.yaml" (
  echo [ERROR] Missing .build\compose.local-build.yaml, so compose cannot resolve this stack.
  echo         Run [3] once to regenerate it, then retry.
  pause
  exit /b 1
)
if not exist "runtime\zcode\config.yaml" (
  echo [ERROR] Missing runtime\zcode\config.yaml.
  echo         Copy deploy\zcode.config.yaml to runtime\zcode\config.yaml first.
  pause
  exit /b 1
)
rem Full -f chain: all three channels plus the local-image overlays that the
rem network-restricted environment needs (qoder/opencode images are built
rem locally, not pulled).
docker compose -f docker-compose.build.yaml -f .build\compose.local-build.yaml -f deploy\compose.zcode.yml -f deploy\compose.qoder.yml -f .build\compose.qoder-local.yaml -f deploy\compose.opencode.yml -f .build\compose.opencode-local.yaml up -d
if errorlevel 1 (
  echo [ERROR] Start failed. Check the output above.
  pause
  exit /b 1
)
echo.
echo Channels started. Finish setup in the console: http://localhost:7863
echo   Zcode tab: log in, then enable the channel.
echo   Qoder tab: page login (or WB2A_QODER_PAT for unattended auth).
echo   OpenCode tab: read-only status; first run downloads the opencode runtime.
pause
exit /b 0
