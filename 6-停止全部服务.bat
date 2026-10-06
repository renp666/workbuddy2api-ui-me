@echo off
rem [6] Stop and remove the ENTIRE local stack: core + console + all three
rem sidecars. Containers and the compose network are removed; both :dev
rem images and everything under runtime\ stay untouched, so the next start
rem restores accounts, keys and data as-is.
rem (Stopping console also takes down the sidecars that share its network,
rem but they are removed explicitly here so nothing is left Exited.)
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
docker compose %FILES% down
if errorlevel 1 (
  echo [ERROR] Stop failed. Check the output above.
  pause
  exit /b 1
)
echo.
echo Stopped. Port 7863 is released; start again with [2] or [3].
pause
exit /b 0
