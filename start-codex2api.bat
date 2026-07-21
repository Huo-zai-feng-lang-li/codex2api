@echo off
setlocal
cd /d "%~dp0"

if not exist logs mkdir logs

taskkill /F /IM codex2api.exe >nul 2>&1

start "" /B "%~dp0codex2api.exe" > "%~dp0logs\start.out.log" 2> "%~dp0logs\start.err.log"

powershell -NoProfile -ExecutionPolicy Bypass -Command "$url='http://127.0.0.1:18080/health'; for ($i=0; $i -lt 30; $i++) { Start-Sleep -Milliseconds 300; try { $r=Invoke-WebRequest -UseBasicParsing -Uri $url -TimeoutSec 1; if ($r.StatusCode -eq 200) { break } } catch {} }; Start-Process 'http://127.0.0.1:18080/admin/'"

endlocal
