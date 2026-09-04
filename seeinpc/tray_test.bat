@echo off
REM seeinpc 托盘两轮实验总控（由 schtasks 以最高权限运行，与 seeinpc 同完整级）
taskkill /F /IM seeinpc.exe >nul 2>&1
timeout /t 1 /nobreak >nul
start "" "D:\ide\seeinp\release\seeinpc\dbg\seeinpc.exe"
timeout /t 11 /nobreak >nul
"C:\Users\zhaobingxiang\.workbuddy\binaries\python\envs\default\Scripts\python.exe" "D:\ide\seeinp\seeinpc\debug-tray7.py" > "D:\ide\seeinp\seeinpc\debug-tray7-out.txt" 2>&1
taskkill /F /IM seeinpc.exe >nul 2>&1
