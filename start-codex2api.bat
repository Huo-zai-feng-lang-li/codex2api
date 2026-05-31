@echo off
setlocal

cd /d "%~dp0"
set "CODEX_PORT=18080"
set "CODEX_BIND=127.0.0.1"
set "URL=http://127.0.0.1:%CODEX_PORT%/admin/"

if not exist logs mkdir logs

powershell -NoProfile -ExecutionPolicy Bypass -Command ^
  "$url='http://127.0.0.1:18080/health';" ^
  "$running=$false;" ^
  "try { $r=Invoke-WebRequest -UseBasicParsing -Uri $url -TimeoutSec 2; $running=($r.StatusCode -eq 200) } catch {}" ^
  "if (-not $running) {" ^
  "  $env:CODEX_PORT='18080'; $env:CODEX_BIND='127.0.0.1';" ^
  "  Start-Process -FilePath (Join-Path (Get-Location) 'codex2api.exe') -WorkingDirectory (Get-Location) -WindowStyle Hidden -RedirectStandardOutput (Join-Path (Get-Location) 'logs\double-click-start.out.log') -RedirectStandardError (Join-Path (Get-Location) 'logs\double-click-start.err.log');" ^
  "  for ($i=0; $i -lt 30; $i++) { Start-Sleep -Milliseconds 500; try { $r=Invoke-WebRequest -UseBasicParsing -Uri $url -TimeoutSec 2; if ($r.StatusCode -eq 200) { break } } catch {} }" ^
  "}" ^
  "Start-Process 'http://127.0.0.1:18080/admin/'"

endlocal
