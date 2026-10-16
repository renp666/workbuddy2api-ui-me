@echo off
rem [4] Start every optional sidecar channel (all deploy\compose.*.yml overlays:
rem zcode, qoder, opencode, autoclaw, trae, traeai, raccoon, freellm).
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
rem Full -f chain: every channel overlay plus the local-image overlays that the
rem network-restricted environment needs (qoder/opencode images are built
rem locally, not pulled).
set FILES=-f docker-compose.build.yaml -f .build\compose.local-build.yaml
if exist deploy\compose.zcode.yml set FILES=%FILES% -f deploy\compose.zcode.yml
if exist deploy\compose.qoder.yml set FILES=%FILES% -f deploy\compose.qoder.yml
if exist .build\compose.qoder-local.yaml set FILES=%FILES% -f .build\compose.qoder-local.yaml
if exist deploy\compose.opencode.yml set FILES=%FILES% -f deploy\compose.opencode.yml
if exist .build\compose.opencode-local.yaml set FILES=%FILES% -f .build\compose.opencode-local.yaml
if exist deploy\compose.autoclaw.yml set FILES=%FILES% -f deploy\compose.autoclaw.yml
if exist deploy\compose.trae.yml set FILES=%FILES% -f deploy\compose.trae.yml
if exist deploy\compose.traeai.yml set FILES=%FILES% -f deploy\compose.traeai.yml
if exist deploy\compose.raccoon.yml set FILES=%FILES% -f deploy\compose.raccoon.yml
if exist deploy\compose.freellm.yml set FILES=%FILES% -f deploy\compose.freellm.yml
docker compose %FILES% up -d
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
