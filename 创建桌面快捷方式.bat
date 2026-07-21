@echo off
setlocal

set "CODEXPROXY_ROOT=%~dp0"

powershell -NoProfile -ExecutionPolicy Bypass -Command ^
  "$root=$env:CODEXPROXY_ROOT.TrimEnd('\');" ^
  "$target=Join-Path $root 'start-codex2api.vbs';" ^
  "$icon=Join-Path $root 'assets\codexproxy.ico';" ^
  "if (-not (Test-Path -LiteralPath $target)) { throw ('Missing start script: ' + $target) }" ^
  "$name=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('Q29kZXhQcm94eSDnrqHnkIblkI7lj7AubG5r'));" ^
  "$desc=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('5ZCv5YqoIENvZGV4UHJveHkg5bm25omT5byA566h55CG5ZCO5Y+w'));" ^
  "$desktop=[Environment]::GetFolderPath('Desktop');" ^
  "$shortcutPath=Join-Path $desktop $name;" ^
  "$wsh=New-Object -ComObject WScript.Shell;" ^
  "$shortcut=$wsh.CreateShortcut($shortcutPath);" ^
  "$shortcut.TargetPath=$target;" ^
  "$shortcut.WorkingDirectory=$root;" ^
  "if (Test-Path -LiteralPath $icon) { $shortcut.IconLocation=$icon + ',0' }" ^
  "$shortcut.Description=$desc;" ^
  "$shortcut.Save();" ^
  "Write-Host ('Created shortcut: ' + $shortcutPath)"

if errorlevel 1 (
  echo.
  echo Failed to create desktop shortcut.
  pause
  exit /b 1
)

echo.
echo Desktop shortcut is ready.
pause

endlocal
