@echo off
chcp 65001 >nul
cd /d "%~dp0"
echo ============================================
echo   codex2api model list sync
echo   official catalog + upstream pool
echo ============================================
echo.
python update_models.py %*
echo.
pause
