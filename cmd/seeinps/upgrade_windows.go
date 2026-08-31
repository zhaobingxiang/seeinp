//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// checkDiskSpace Windows 跳过磁盘检查（无 statfs 等价 API）
func checkDiskSpace(need int64) error {
	return nil
}

// applyAndRestart Windows 自替换并重启。
// 策略：暂存新二进制为 .new，启动外部 PowerShell 脚本完成等待/替换/重启，
// 当前进程随后退出。注意脚本变量不能用 $pid（PowerShell 保留只读变量）。
func applyAndRestart(exePath, tmpPath string) error {
	serviceMode := isWindowsService()

	// 1. 暂存新二进制
	stagePath := exePath + ".new"
	_ = os.Remove(stagePath)
	if err := os.Rename(tmpPath, stagePath); err != nil {
		if err := copyFile(tmpPath, stagePath); err != nil {
			return fmt.Errorf("暂存新二进制: %w", err)
		}
		_ = os.Remove(tmpPath)
	}

	// 2. 启动 PowerShell 辅助脚本
	if err := launchRestarter(exePath, stagePath, serviceMode); err != nil {
		_ = os.Remove(stagePath)
		return fmt.Errorf("启动重启脚本: %w", err)
	}

	fmt.Printf("[UPGRADE] staged %s, exiting (service=%v, pid=%d)\n", stagePath, serviceMode, os.Getpid())

	// 3. 等待 PM 侧完成流收尾与状态记录，再退出当前进程
	time.Sleep(2 * time.Second)
	os.Exit(0)
	return nil
}

func isWindowsService() bool {
	for _, arg := range os.Args[1:] {
		if arg == "--service" || arg == "-service" {
			return true
		}
	}
	return false
}

func copyFile(src, dst string) error {
	in, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, in, 0755)
}

// launchRestarter 写 .ps1 辅助脚本并用 PowerShell 隐藏窗口启动（独立进程，不随父退出）。
// 关键点：
//   - $pid 是 PowerShell 保留只读自动变量（=powershell 自身 PID），必须用 $watchPid；
//   - Rename-Item -NewName 不接受完整路径，统一用 Move-Item（接受完整目标路径）。
func launchRestarter(exePath, stagePath string, serviceMode bool) error {
	watchPid := os.Getpid()
	exeDir := filepath.Dir(exePath)
	oldPath := exePath + ".old"
	logPath := filepath.Join(exeDir, "_upgrade.log")
	psPath := filepath.Join(exeDir, "_upgrade_restart.ps1")

	var stopBlock, restartBlock string
	if serviceMode {
		stopBlock = "Start-Process sc.exe -ArgumentList 'stop','seeinps' -WindowStyle Hidden -Wait; Log 'sc stop sent'"
		restartBlock = "Start-Process sc.exe -ArgumentList 'start','seeinps' -WindowStyle Hidden -Wait"
	} else {
		stopBlock = "Log 'foreground mode, no service stop needed'"
		restartBlock = fmt.Sprintf("Start-Process -FilePath '%s' -ArgumentList '-conf','conf\\seeinps.toml' -WindowStyle Hidden", exePath)
	}

	psScript := fmt.Sprintf(`$ErrorActionPreference = 'Continue'
$log = '%s'
$watchPid = %d
$exe = '%s'
$stage = '%s'
$old = '%s'

function Log($msg) { Add-Content -Path $log -Value ("[{0}] {1}" -f (Get-Date -Format 'yyyy-MM-dd HH:mm:ss'), $msg) }

Log "upgrade started, watching pid $watchPid"

# Phase 1: wait for the old process to exit (max 60s)
$timeout = 60
$elapsed = 0
while ($elapsed -lt $timeout) {
    $proc = Get-Process -Id $watchPid -ErrorAction SilentlyContinue
    if (-not $proc) { break }
    Start-Sleep -Seconds 1
    $elapsed++
}
if ($elapsed -ge $timeout) { Log "TIMEOUT waiting for pid $watchPid" } else { Log "pid $watchPid exited after ${elapsed}s" }

# Phase 2: extra stop for service mode
%s
Start-Sleep -Seconds 2

# Phase 3: replace binary (Move-Item: full destination paths are valid)
if (Test-Path $old) { Remove-Item $old -Force -ErrorAction SilentlyContinue; Log "removed old backup" }

if (Test-Path $exe) {
    try {
        Move-Item $exe $old -Force -ErrorAction Stop
        Log "moved exe -> old"
    } catch {
        Log "move exe failed: $_"
    }
}

if (Test-Path $stage) {
    try {
        Move-Item $stage $exe -Force -ErrorAction Stop
        Log "moved stage -> exe"
    } catch {
        Log "ERROR: move stage failed: $_"
        exit 1
    }
} else {
    Log "ERROR: stage file $stage not found"
    exit 1
}

if (-not (Test-Path $exe)) { Log "ERROR: exe missing after replace"; exit 1 }
Log "binary replaced successfully"

if (Test-Path $old) { Remove-Item $old -Force -ErrorAction SilentlyContinue }

# Phase 4: restart
Start-Sleep -Seconds 1
Log "restarting..."
%s
Log "restart done"
`,
		logPath,
		watchPid,
		exePath,
		stagePath,
		oldPath,
		stopBlock,
		restartBlock,
	)

	if err := os.WriteFile(psPath, []byte(psScript), 0644); err != nil {
		return err
	}

	// 中转 PowerShell 立即退出，实际脚本经 Start-Process 在独立进程运行
	cmd := exec.Command("powershell.exe",
		"-ExecutionPolicy", "Bypass",
		"-WindowStyle", "Hidden",
		"-Command",
		fmt.Sprintf("Start-Process powershell.exe -ArgumentList '-ExecutionPolicy','Bypass','-WindowStyle','Hidden','-File','%s' -WindowStyle Hidden", psPath),
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x08000000, // CREATE_NO_WINDOW
	}
	cmd.Dir = exeDir
	return cmd.Start()
}
