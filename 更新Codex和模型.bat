@echo off
chcp 65001 >nul
cd /d "%~dp0"
echo ============================================
echo   Update Codex and refresh model lists
echo ============================================
echo.
python update_codex_and_models.py %*
echo.
pause
