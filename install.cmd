@echo off
rem ccqc installer for Windows (cmd). Runs install.ps1 next to this file with the same options:
rem   install.cmd [-Version vX.Y.Z] [-Dir DIR] [-FromSource | -FromRelease] [-NoPath] [-NoRestart] [-Uninstall]
setlocal
set "PS=powershell"
where pwsh >nul 2>nul && set "PS=pwsh"
%PS% -NoProfile -ExecutionPolicy Bypass -File "%~dp0install.ps1" %*
exit /b %ERRORLEVEL%
