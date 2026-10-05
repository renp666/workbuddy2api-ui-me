@echo off
rem [2] Restart the whole stack from the CURRENT :dev images, NO rebuild.
rem Use this when the source did not change: config tweaks, a stuck service,
rem or morning startup. After code changes use [1] instead, because web
rem assets and Go code are baked into the images and --force-recreate only
rem reuses whatever is already built.
rem
rem core + console are recreated and every EXISTING sidecar is recreated in
rem the same compose call, so sidecars always land in console's new network
rem namespace (network_mode: service:console). Sidecars removed with [5]
rem stay removed; start channels again with [4].
cd /d "%~dp0"
setlocal EnableDelayedExpansion
echo.
echo === [2] restart whole stack from current images ===
echo.
docker info >nul 2>&1
if errorlevel 1 (
  echo [ERROR] Docker is not running. Start Docker Desktop first, then retry.
  pause
  exit /b 1
)
if not exist ".build\compose.local-build.yaml" (
  echo [ERROR] Missing .build\compose.local-build.yaml, so compose cannot resolve this stack.
  echo         Run [3] once to generate it, then retry.
  pause
  exit /b 1
)

rem Assemble the -f chain to MATCH the currently active deployment exactly
rem (verify with: docker compose ls). A different chain forces extra
rem recreations and can rotate the zcode credential secret.
set FILES=-f docker-compose.build.yaml -f .build\compose.local-build.yaml
if exist deploy\compose.zcode.yml set FILES=%FILES% -f deploy\compose.zcode.yml
if exist deploy\compose.qoder.yml set FILES=%FILES% -f deploy\compose.qoder.yml
if exist .build\compose.qoder-local.yaml set FILES=%FILES% -f .build\compose.qoder-local.yaml
if exist deploy\compose.opencode.yml set FILES=%FILES% -f deploy\compose.opencode.yml
if exist .build\compose.opencode-local.yaml set FILES=%FILES% -f .build\compose.opencode-local.yaml

rem core + console always; sidecars only if the container exists (docker ps
rem -a: console being stopped also stops its network-mode dependents, so an
rem Exited sidecar still belongs to the stack). Loop accumulation needs the
rem delayed expansion !SVCS!; %SVCS% inside the block is a parse-time bug.
set SVCS=core console
for %%n in (zcode-proxy qoder-proxy opencode-proxy) do (
  docker ps -a --format "{{.Names}}" | findstr /c:"workbuddy2api-ui-me-%%n-1" >nul
  if not errorlevel 1 set SVCS=!SVCS! %%n
)

echo [INFO] Recreating: %SVCS%
docker compose %FILES% up -d --force-recreate %SVCS%
if errorlevel 1 (
  echo [ERROR] Restart failed. Check the output above.
  pause
  exit /b 1
)

rem The console only answers after core has published the shared key, so probe instead of assuming.
rem ping sleeps because timeout.exe loses to GNU coreutils when this runs from a Git Bash PATH.
set "CODE=000"
set /a LEFT=15
:probe
ping -n 2 127.0.0.1 >nul
for /f "delims=" %%C in ('curl -s -o nul -w "%%{http_code}" http://127.0.0.1:7863/healthz') do set "CODE=%%C"
if not "%CODE%"=="000" goto ready
set /a LEFT-=1
if %LEFT% gtr 0 goto probe
echo [WARN] Port 7863 did not answer within 15s.
echo        docker compose %FILES% logs
pause
exit /b 1
:ready
echo.
echo Console: http://localhost:7863   healthz=%CODE%
docker ps --format "{{.Names}}  {{.Status}}" | findstr /c:"workbuddy2api-ui-me"
echo.
echo [OK] Done. Press Ctrl+F5 in the browser.
echo Note: zcode proxy auto-starts a few seconds after restart; if a channel
echo still shows unreachable, wait ~30s and refresh.
pause
exit /b 0
