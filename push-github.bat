@echo off
rem One-click push of main to GitHub (origin) with automatic retries.
rem Plain-HTTPS GitHub access is often flaky on CN networks;
rem "Connection was reset" usually succeeds on the next attempt.
cd /d "%~dp0"
echo.
echo === push main -^> origin (workbuddy2api-ui-me) ===
echo.
git log --oneline -1
git status -sb | findstr /r "^##"
echo.
git status --porcelain | findstr /r "." >nul
if not errorlevel 1 (
  echo [WARN] Uncommitted changes exist. Only COMMITTED work will be pushed.
  echo        Commit them first if you want them included, then run this again.
  echo.
)
set RETRY=0
:push
set /a RETRY+=1
echo [INFO] git push origin main (attempt %RETRY% of 3)...
git push origin main
if not errorlevel 1 goto ok
if %RETRY% LSS 3 (
  echo [WARN] Push failed. Retrying in 3 seconds...
  timeout /t 3 /nobreak >nul
  goto push
)
echo.
echo [ERROR] Push failed after 3 attempts.
echo If a local proxy is running, push manually through it, for example:
echo   git -c http.proxy=http://127.0.0.1:7890 -c https.proxy=http://127.0.0.1:7890 push origin main
pause
exit /b 1
:ok
echo.
echo [OK] Pushed. Repo: https://github.com/renp666/workbuddy2api-ui-me
pause
