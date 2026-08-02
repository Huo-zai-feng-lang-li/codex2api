@echo off
setlocal
cd /d "%~dp0"

echo ============================================
echo   Codex2API - Build and Restart
echo   %date% %time%
echo ============================================

:: [1] Kill old process
echo.
echo [1/4] Stopping old process...
taskkill /F /IM codex2api.exe 2>nul 1>nul
timeout /t 1 /nobreak >nul

:: [2] Build
echo [2/4] Building...
go build -o codex2api.exe .
if %errorlevel% neq 0 (
    echo BUILD FAILED! Check code errors above.
    pause
    exit /b 1
)
echo       Build OK!

:: [3] Ensure log dir
if not exist logs mkdir logs

:: [4] Start service (redirect stdout/stderr to log files)
echo [3/4] Starting service...
start "" /B cmd /c "codex2api.exe 1>logs\start.out.log 2>logs\start.err.log"

:: [5] Health check loop
echo [4/4] Waiting for service...
powershell -NoProfile -ExecutionPolicy Bypass -Command "$url='http://127.0.0.1:18080/health'; $ok=$false; for ($i=0;$i -lt 40;$i++) { Start-Sleep -Milliseconds 500; try { $r=Invoke-WebRequest -UseBasicParsing -Uri $url -TimeoutSec 2; if ($r.StatusCode -eq 200) { $ok=$true; break } } catch {} }; if($ok){Write-Host '      Service ready! Opening admin...'; Start-Process 'http://127.0.0.1:18080/admin/'}else{Write-Host '      Health check timeout. Check logs\ for details.'}"

echo.
echo ============================================
echo   Done!
echo ============================================
timeout /t 3 /nobreak >nul
endlocal
