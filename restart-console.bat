@echo off
rem One-click restart of console AND its sidecar proxies.
rem Why: zcode/qoder/opencode share console network namespace
rem (network_mode: service:console). Restarting console alone
rem leaves sidecars attached to the OLD namespace, so all three
rem channels show unreachable until they are restarted too.
rem Sidecars are restarted only if currently running;
rem a manually-stopped sidecar stays stopped.
cd /d "%~dp0"
echo.
echo === restart console + sidecar proxies ===
echo.
echo [INFO] restarting console (brief web downtime, a few seconds)...
docker restart workbuddy2api-ui-me-console-1
if errorlevel 1 goto fail
ping -n 6 127.0.0.1 >nul
echo [INFO] restarting sidecars that are currently running...
docker ps --format "{{.Names}}" | findstr /c:"zcode-proxy" >nul
if not errorlevel 1 docker restart workbuddy2api-ui-me-zcode-proxy-1
docker ps --format "{{.Names}}" | findstr /c:"qoder-proxy" >nul
if not errorlevel 1 docker restart workbuddy2api-ui-me-qoder-proxy-1
docker ps --format "{{.Names}}" | findstr /c:"opencode-proxy" >nul
if not errorlevel 1 docker restart workbuddy2api-ui-me-opencode-proxy-1
ping -n 6 127.0.0.1 >nul
echo.
echo === container status ===
docker ps --format "{{.Names}}  {{.Status}}" | findstr /c:"workbuddy2api-ui-me"
echo.
echo [OK] Done. Open http://127.0.0.1:7863 and press Ctrl+F5.
echo Note: zcode proxy auto-starts a few seconds after restart;
echo if a channel still shows unreachable, wait ~30s and refresh.
pause
exit /b 0
:fail
echo [ERROR] console container not found or restart failed.
echo Check: docker ps -a ^| findstr workbuddy2api
pause
exit /b 1
