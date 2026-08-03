@echo off
setlocal
cd /d "%~dp0"

set "CODEX_ROOT=%CD%"
set "NEW_EXE=codex2api.new.exe"
set "PREVIOUS_EXE=codex2api.previous.exe"
set "OLD_PID_FILE=%CD%\.codex2api.old.pid"
set "NEW_PID_FILE=%CD%\.codex2api.new.pid"
set "OLD_PID="
set "NEW_PID="

del /Q "%OLD_PID_FILE%" "%NEW_PID_FILE%" 2>nul

echo ============================================
echo   Codex2API - Build and Restart
echo   %date% %time%
echo ============================================

echo.
echo [1/6] Building frontend while the current service stays online...
pushd frontend
call npm run build
set "FRONTEND_EXIT=%ERRORLEVEL%"
popd
if not "%FRONTEND_EXIT%"=="0" (
    echo FRONTEND BUILD FAILED. Current service was not stopped.
    exit /b 1
)

echo [2/6] Building replacement backend...
if exist "%NEW_EXE%" del /Q "%NEW_EXE%"
go build -o "%NEW_EXE%" .
if errorlevel 1 (
    echo BACKEND BUILD FAILED. Current service was not stopped.
    del /Q "%NEW_EXE%" 2>nul
    exit /b 1
)
if not exist "%NEW_EXE%" (
    echo BACKEND BUILD FAILED: replacement binary is missing.
    exit /b 1
)

echo [3/6] Waiting for in-flight requests to drain...
powershell -NoProfile -ExecutionPolicy Bypass -Command "$target=[IO.Path]::GetFullPath((Join-Path $env:CODEX_ROOT 'codex2api.exe')); $deadline=(Get-Date).AddSeconds(60); do { $p=Get-Process codex2api -ErrorAction SilentlyContinue | Where-Object { try { [IO.Path]::GetFullPath($_.Path) -eq $target } catch { $false } } | Select-Object -First 1; if(-not $p){exit 0}; try { $ownerPid=$null; foreach($line in (& netstat -ano -p TCP)){if($line -match '^\s*TCP\s+(127\.0\.0\.1|\[::1\]):18080\s+\S+\s+LISTENING\s+(\d+)\s*$'){$ownerPid=[int]$Matches[2];break}}; if($ownerPid -ne $p.Id){throw 'port 18080 is not owned by the target process'}; $h=Invoke-RestMethod -Uri 'http://127.0.0.1:18080/health' -TimeoutSec 2; if($h.status -ne 'ok'){throw 'health status is not ok'}; $raw=$h.responses_memory.inflight_requests; if($null -eq $raw){throw 'missing inflight_requests'}; $n=[int]$raw; if($n -eq 0){Set-Content -LiteralPath $env:OLD_PID_FILE -Value $p.Id -NoNewline; exit 0}; Write-Host ('      in-flight requests: '+$n) } catch { Write-Host ('      precheck waiting: '+$_.Exception.Message) }; Start-Sleep -Milliseconds 500 } while((Get-Date) -lt $deadline); exit 1"
if errorlevel 1 (
    echo PRECHECK FAILED. Current service was not stopped.
    del /Q "%NEW_EXE%" "%OLD_PID_FILE%" 2>nul
    exit /b 1
)
if exist "%OLD_PID_FILE%" set /P OLD_PID=<"%OLD_PID_FILE%"
del /Q "%OLD_PID_FILE%" 2>nul

echo [4/6] Stopping the exact old process and replacing the binary...
if defined OLD_PID (
    set "TARGET_PID=%OLD_PID%"
    call :stop_exact_pid
    if errorlevel 1 (
        echo FAILED TO STOP OLD PID %OLD_PID%. Replacement cancelled.
        del /Q "%NEW_EXE%" 2>nul
        exit /b 1
    )
)

powershell -NoProfile -ExecutionPolicy Bypass -Command "$new=Join-Path $env:CODEX_ROOT $env:NEW_EXE; $current=Join-Path $env:CODEX_ROOT 'codex2api.exe'; $previous=Join-Path $env:CODEX_ROOT $env:PREVIOUS_EXE; try { if(Test-Path -LiteralPath $previous){Remove-Item -LiteralPath $previous -Force}; if(Test-Path -LiteralPath $current){[IO.File]::Replace($new,$current,$previous,$true)}else{[IO.File]::Move($new,$current)}; exit 0 } catch { Write-Host $_.Exception.Message; exit 1 }"
if errorlevel 1 goto replace_failed

if not exist logs mkdir logs
set CODEX_TRANSPORT_MODE=utls
set CODEX_FINGERPRINT_DEBUG=true
set CODEX_WS_SEND_USER_AGENT=true
set STABILIZE_DEVICE_PROFILE=true

echo [5/6] Starting new service...
call :start_service
if errorlevel 1 (
    echo NEW SERVICE FAILED TO START. Rolling back.
    goto rollback
)

echo [6/6] Verifying new service health...
call :wait_health
if errorlevel 1 (
    echo HEALTH CHECK FAILED. Rolling back to the previous binary.
    goto rollback
)

del /Q "%PREVIOUS_EXE%" "%NEW_PID_FILE%" 2>nul
echo.
echo ============================================
echo   Build, replacement, and health check OK.
echo ============================================
exit /b 0

:replace_failed
echo BINARY REPLACEMENT FAILED.
if exist "%PREVIOUS_EXE%" goto rollback
del /Q "%NEW_EXE%" 2>nul
exit /b 1

:rollback
if exist "%NEW_PID_FILE%" set /P NEW_PID=<"%NEW_PID_FILE%"
del /Q "%NEW_PID_FILE%" 2>nul
if defined NEW_PID (
    set "TARGET_PID=%NEW_PID%"
    call :stop_exact_pid
)
powershell -NoProfile -ExecutionPolicy Bypass -Command "$current=Join-Path $env:CODEX_ROOT 'codex2api.exe'; $previous=Join-Path $env:CODEX_ROOT $env:PREVIOUS_EXE; try { if(-not (Test-Path -LiteralPath $previous)){exit 1}; if(Test-Path -LiteralPath $current){Remove-Item -LiteralPath $current -Force}; Move-Item -LiteralPath $previous -Destination $current -Force; exit 0 } catch { Write-Host $_.Exception.Message; exit 1 }"
if errorlevel 1 (
    echo ROLLBACK FAILED: previous binary is unavailable.
    exit /b 1
)
call :start_service
if errorlevel 1 (
    echo ROLLBACK FAILED: previous service did not start.
    exit /b 1
)
call :wait_health
if errorlevel 1 echo ROLLBACK SERVICE HEALTH CHECK FAILED. Check logs\start.err.log.
if not errorlevel 1 echo Previous service restored successfully.
del /Q "%NEW_PID_FILE%" 2>nul
exit /b 1

:stop_exact_pid
powershell -NoProfile -ExecutionPolicy Bypass -Command "$target=[IO.Path]::GetFullPath((Join-Path $env:CODEX_ROOT 'codex2api.exe')); try { $p=Get-Process -Id ([int]$env:TARGET_PID) -ErrorAction Stop; if([IO.Path]::GetFullPath($p.Path) -ne $target){exit 1}; $p.Kill(); if(-not $p.WaitForExit(5000)){exit 1}; exit 0 } catch { exit 1 }"
exit /b %ERRORLEVEL%

:start_service
del /Q "%NEW_PID_FILE%" 2>nul
powershell -NoProfile -ExecutionPolicy Bypass -Command "$exe=Join-Path $env:CODEX_ROOT 'codex2api.exe'; $stdout=Join-Path $env:CODEX_ROOT 'logs\start.out.log'; $stderr=Join-Path $env:CODEX_ROOT 'logs\start.err.log'; try { $p=Start-Process -FilePath $exe -WorkingDirectory $env:CODEX_ROOT -WindowStyle Hidden -RedirectStandardOutput $stdout -RedirectStandardError $stderr -PassThru; Set-Content -LiteralPath $env:NEW_PID_FILE -Value $p.Id -NoNewline; exit 0 } catch { Write-Host $_.Exception.Message; exit 1 }"
exit /b %ERRORLEVEL%

:wait_health
powershell -NoProfile -ExecutionPolicy Bypass -Command "try{$servicePid=[int](Get-Content -LiteralPath $env:NEW_PID_FILE -Raw)}catch{exit 1}; $url='http://127.0.0.1:18080/health'; for($i=0;$i -lt 40;$i++){Start-Sleep -Milliseconds 500; if(-not (Get-Process -Id $servicePid -ErrorAction SilentlyContinue)){exit 1}; try{$ownerPid=$null; foreach($line in (& netstat -ano -p TCP)){if($line -match '^\s*TCP\s+(127\.0\.0\.1|\[::1\]):18080\s+\S+\s+LISTENING\s+(\d+)\s*$'){$ownerPid=[int]$Matches[2];break}}; if($ownerPid -ne $servicePid){continue}; $h=Invoke-RestMethod -Uri $url -TimeoutSec 2; if($h.status -eq 'ok'){exit 0}}catch{}}; exit 1"
exit /b %ERRORLEVEL%
