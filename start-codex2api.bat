@echo off
setlocal
cd /d "%~dp0"

if not exist logs mkdir logs

REM Enable Chrome TLS JA3 fingerprint masking and debug logging
set CODEX_TRANSPORT_MODE=utls
set CODEX_FINGERPRINT_DEBUG=true
set CODEX_WS_SEND_USER_AGENT=true
set STABILIZE_DEVICE_PROFILE=true

taskkill /F /IM codex2api.exe >nul 2>&1

start "" /B cmd /c "codex2api.exe >> "%~dp0logs\start.out.log" 2>&1"

powershell -NoProfile -ExecutionPolicy Bypass -Command "$url='http://127.0.0.1:18080/health'; for ($i=0; $i -lt 30; $i++) { Start-Sleep -Milliseconds 300; try { $r=Invoke-WebRequest -UseBasicParsing -Uri $url -TimeoutSec 1; if ($r.StatusCode -eq 200) { break } } catch {} }; Start-Process 'http://127.0.0.1:18080/admin/'"

endlocal
