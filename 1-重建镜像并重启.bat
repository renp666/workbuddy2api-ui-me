@echo off
rem [1] Rebuild the local :dev images and redeploy the whole stack. MOST USED.
rem Run this after ANY source change: console Go code, embedded web assets
rem (app.js/index.html are baked into the console binary via go:embed, so a
rem git push alone never changes the running UI), patches, extensions or the
rem Dockerfiles. If you changed nothing, use [2] instead (no build step).
rem
rem Every sidecar (all deploy\compose.*.yml channels) shares console's network
rem network_mode: service:console. Recreating console destroys the old
rem namespace, so every existing sidecar is force-recreated in the SAME
rem compose call and joins the new namespace. Sidecars you removed with [5]
rem stay removed (only containers that exist are recreated).
cd /d "%~dp0"
setlocal EnableDelayedExpansion
echo.
echo === [1] rebuild :dev images + redeploy whole stack ===
echo.
docker info >nul 2>&1
if errorlevel 1 (
  echo [ERROR] Docker is not running. Start Docker Desktop first, then retry.
  pause
  exit /b 1
)

echo [1/3] Building core and console :dev images (a few minutes on cold cache)...
python .build\local-build.py
if errorlevel 1 (
  echo [ERROR] Build failed. Check .build\workbuddy2api_*.log
  pause
  exit /b 1
)

rem Assemble the -f chain to MATCH the currently active deployment exactly.
rem A different chain changes container config and forces extra recreations
rem (e.g. adding the zcode local overlay would rotate its credential secret
rem and require a zcode re-login). Verify with: docker compose ls
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

rem Services: always core + console, plus any sidecar container that EXISTS.
rem Use docker ps -a (all states): when console stops, sidecars that share
rem its network are auto-stopped too, so they show Exited but still need to
rem come back. A FOR-loop body expands %SVCS% only once at parse time, so
rem accumulating it REQUIRES delayed expansion (!SVCS!), not %SVCS%.
set SVCS=core console
for %%n in (zcode-proxy qoder-proxy opencode-proxy autoclaw-proxy trae-proxy traeai-proxy raccoon-proxy freellmapi) do (
  docker ps -a --format "{{.Names}}" | findstr /c:"workbuddy2api-ui-me-%%n-1" >nul
  if not errorlevel 1 set SVCS=!SVCS! %%n
)

echo [2/3] Recreating: %SVCS%
docker compose %FILES% up -d --force-recreate %SVCS%
if errorlevel 1 (
  echo [ERROR] Redeploy failed. Check output above.
  pause
  exit /b 1
)

ping -n 6 127.0.0.1 >nul
echo [3/3] Container status:
docker ps --format "{{.Names}}  {{.Status}}" | findstr /c:"workbuddy2api-ui-me"
echo.
echo [OK] Rebuilt and redeployed. Open http://127.0.0.1:7863 and press Ctrl+F5.
echo Note: zcode proxy auto-starts a few seconds after redeploy; if a channel
echo still shows unreachable, wait ~30s and refresh.
pause
exit /b 0
