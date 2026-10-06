@echo off
rem [5] Stop and remove the three optional sidecar channels
rem (zcode-proxy + qoder-proxy + opencode-proxy).
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
set FILES=-f docker-compose.build.yaml -f .build\compose.local-build.yaml -f deploy\compose.zcode.yml -f deploy\compose.qoder.yml -f .build\compose.qoder-local.yaml -f deploy\compose.opencode.yml -f .build\compose.opencode-local.yaml
docker compose %FILES% stop zcode-proxy qoder-proxy opencode-proxy
if errorlevel 1 (
  echo [ERROR] Stop failed. Check the output above.
  pause
  exit /b 1
)
docker compose %FILES% rm -f zcode-proxy qoder-proxy opencode-proxy
if errorlevel 1 (
  echo [ERROR] Remove failed. Check the output above.
  pause
  exit /b 1
)
echo.
echo All three channels stopped and removed. Start them again with [4].
pause
exit /b 0
