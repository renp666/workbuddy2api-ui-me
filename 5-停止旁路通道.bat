@echo off
rem [5] Stop and remove every optional sidecar channel
rem (zcode/qoder/opencode/autoclaw/trae/traeai/raccoon + freellmapi).
rem Core and console keep running; runtime\zcode, runtime\qoder and
rem runtime\opencode data stay on disk. Console keeps the channel env vars
rem until the next restart, so the tabs simply report the channels as
rem unreachable until you re-run [4].
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
docker compose %FILES% stop zcode-proxy qoder-proxy opencode-proxy autoclaw-proxy trae-proxy traeai-proxy raccoon-proxy freellmapi
if errorlevel 1 (
  echo [ERROR] Stop failed. Check the output above.
  pause
  exit /b 1
)
docker compose %FILES% rm -f zcode-proxy qoder-proxy opencode-proxy autoclaw-proxy trae-proxy traeai-proxy raccoon-proxy freellmapi
if errorlevel 1 (
  echo [ERROR] Remove failed. Check the output above.
  pause
  exit /b 1
)
echo.
echo All channels stopped and removed. Start them again with [4].
pause
exit /b 0
