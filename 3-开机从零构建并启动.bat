@echo off
rem [3] Build from local source and start core + console (first run of the day).
rem This is the network-restricted variant: images come from .build\local-build.py
rem (git-archive build context, LF+exec bits, locally retagged base images,
rem goproxy.cn), so it works without direct Docker Hub / GitHub access.
rem It starts ONLY core + console; the three optional channels are started
rem afterwards by [4]. If you only need a restart without a rebuild, use [2].
cd /d "%~dp0"
echo.
echo === [3] build :dev images from source + start core and console ===
echo.
docker info >nul 2>&1
if errorlevel 1 (
  echo [ERROR] Docker is not running. Start Docker Desktop first, then retry.
  pause
  exit /b 1
)
python .build\local-build.py
if errorlevel 1 (
  echo [ERROR] Build failed. Check output above and .build\workbuddy2api_*.log
  pause
  exit /b 1
)
docker stop workbuddy2api-ui-console-1 workbuddy2api-ui-core-1 >nul 2>&1
docker compose -f docker-compose.build.yaml -f .build\compose.local-build.yaml up -d
if errorlevel 1 (
  echo [ERROR] Start failed. Check the output above.
  pause
  exit /b 1
)
echo.
echo Started (local build). Console: http://localhost:7863
echo Now run [4] to start the optional sidecar channels.
echo Logs: docker compose -f docker-compose.build.yaml -f .build\compose.local-build.yaml logs -f
pause
exit /b 0
