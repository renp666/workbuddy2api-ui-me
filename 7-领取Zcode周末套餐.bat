@echo off
rem [7] Claim the z.ai weekend trial package from inside the running
rem zcode-proxy container. Run this on Friday/Saturday once the Start Plan
rem weekend campaign is live. The container auto-solves the captcha
rem in-process; no browser is needed.
rem Safe to re-run: "No claimable plans right now" just means the campaign
rem is not live yet.
rem Why not a console button: the zcode control protocol has no claim command,
rem console holds no Docker privileges, and the zcode-proxy image is third-party.
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
set ZCODE_CID=
for /f %%a in ('docker compose -f docker-compose.build.yaml -f .build\compose.local-build.yaml -f deploy\compose.zcode.yml ps -q zcode-proxy 2^>nul') do set ZCODE_CID=%%a
if "%ZCODE_CID%"=="" (
  echo [ERROR] zcode-proxy container is not running.
  echo         Start the channels first with [4], then retry.
  pause
  exit /b 1
)
echo.
echo Fetching claimable plans... (empty list means the campaign is not live yet)
docker exec %ZCODE_CID% bun run src/index.ts claim list
if errorlevel 1 (
  echo [WARN] claim list failed. See output above.
)
echo.
echo Attempting claim now...
docker exec %ZCODE_CID% bun run src/index.ts claim now
if errorlevel 1 (
  echo [ERROR] claim now failed. See output above.
  pause
  exit /b 1
)
echo.
echo Done. If a package was claimed, the quota is active on this z.ai account immediately.
pause
exit /b 0
