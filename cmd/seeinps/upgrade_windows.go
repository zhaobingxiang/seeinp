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
// 策略：暂存新二进制为 .new，启动外部 PowerShell 脚本完成替换和重启。
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

	// 3. 退出当前进程
	time.Sleep(300 * time.Millisecond)
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

// launchRestarter 写一个 .ps1 脚本并用 PowerShell 隐藏窗口启动。
// 脚本会在独立进程中运行，不随父进程退出。
func launchRestarter(exePath, stagePath string, serviceMode bool) error {
	pid := os.Getpid()
	exeDir := filepath.Dir(exePath)
	oldPath := exePath + ".old"
	logPath := filepath.Join(exeDir, "_upgrade.log")
	psPath := filepath.Join(exeDir, "_upgrade_restart.ps1")

	var restartBlock string
	if serviceMode {
		restartBlock = "Start-Process -FilePath 'sc.exe' -ArgumentList 'start','seeinps' -WindowStyle Hidden -Wait"
	} else {
		restartBlock = fmt.Sprintf("Start-Process -FilePath '%s' -ArgumentList '-conf','conf\\seeinps.toml' -WindowStyle Hidden", exePath)
	}

	// PowerShell 脚本内容
	psScript := fmt.Sprintf(`
$ErrorActionPreference = 'Continue'
$log = '%s'
$pid = %d
$exe = '%s'
$stage = '%s'
$old = '%s'

function Log($msg) { Add-Content -Path $log -Value ("[{0}] {1}" -f (Get-Date -Format 'yyyy-MM-dd HH:mm:ss'), $msg) }

Log "upgrade started, waiting for pid $pid to exit"

# Phase 1: wait for old process to exit
$timeout = 60
$elapsed = 0
while ($elapsed -lt $timeout) {
    $proc = Get-Process -Id $pid -ErrorAction SilentlyContinue
    if (-not $proc) { break }
    Start-Sleep -Seconds 1
    $elapsed++
}
if ($elapsed -ge $timeout) { Log "TIMEOUT waiting for pid $pid" } else { Log "pid $pid exited after ${elapsed}s" }

# Phase 2: extra stop for service mode
%s
Start-Sleep -Seconds 2

# Phase 3: replace binary
try {
    if (Test-Path $old) { Remove-Item $old -Force -ErrorAction SilentlyContinue; Log "removed old backup" }
} catch { Log "remove old failed: $_" }

try {
    if (Test-Path $exe) { Rename-Item $exe $old -Force -ErrorAction Stop; Log "renamed exe -> old" }
} catch {
    Log "rename failed: $_, trying remove"
    try { Remove-Item $exe -Force -ErrorAction Stop } catch { Log "remove exe also failed: $_" }
}

try {
    if (Test-Path $stage) { Rename-Item $stage $exe -Force -ErrorAction Stop; Log "renamed stage -> exe" }
    else { Log "ERROR: stage file $stage not found"; exit 1 }
} catch { Log "rename stage failed: $_"; exit 1 }

# Verify
if (-not (Test-Path $exe)) { Log "ERROR: exe not found after replace"; exit 1 }
Log "binary replaced successfully"

# Cleanup
try { if (Test-Path $old) { Remove-Item $old -Force -ErrorAction SilentlyContinue } } catch {}

# Phase 4: restart
Start-Sleep -Seconds 1
Log "restarting..."
%s
Log "restart done"
`,
		logPath,
		pid,
		exePath,
		stagePath,
		oldPath,
		// service stop block
		func() string {
			if serviceMode {
				return "try { Start-Process -FilePath 'sc.exe' -ArgumentList 'stop','seeinps' -WindowStyle Hidden -Wait -ErrorAction SilentlyContinue; Log 'sc stop sent' } catch { Log \"sc stop failed: $_\" }"
			}
			return "Log 'foreground mode, no service stop needed'"
		}(),
		restartBlock,
	)

	if err := os.WriteFile(psPath, []byte(psScript), 0644); err != nil {
		return err
	}

	// 用 PowerShell 启动脚本：-ExecutionPolicy Bypass 绕过策略，-WindowStyle Hidden 无窗口
	// Start-Process 让脚本在独立进程运行，不随父进程退出
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
