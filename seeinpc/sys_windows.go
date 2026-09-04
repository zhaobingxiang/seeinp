//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// wintunDLLHash 内置 wintun.dll（0.14.1）的 SHA256，释放时校验防替换（§9.3）
const wintunDLLHash = "e5da8447dc2c320edc0fc52fa01885c103de8c118481f683643cacc3220dafce"

// wintunEmbed 由 main.go 的 go:embed 提供 DLL 字节
var wintunEmbed []byte

func fileSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// ensureWintun 释放并校验 wintun.dll：
// 安装目录 resource/wintun.dll（§9.3 驱动存放位）+ 可执行文件同目录（wintun 加载规则）。
func ensureWintun() error {
	targets := []string{
		filepath.Join(resourceDir(), "wintun.dll"),
		filepath.Join(exeDir(), "wintun.dll"),
	}
	for _, t := range targets {
		if h, err := fileSHA256(t); err == nil && h == wintunDLLHash {
			continue // 已就位且哈希匹配
		}
		if err := os.MkdirAll(filepath.Dir(t), 0o755); err != nil {
			return fmt.Errorf("创建目录失败: %w", err)
		}
		if err := os.WriteFile(t, wintunEmbed, 0o755); err != nil {
			return fmt.Errorf("释放 wintun.dll 失败: %w", err)
		}
		if h, err := fileSHA256(t); err != nil || h != wintunDLLHash {
			return fmt.Errorf("wintun.dll 哈希校验失败: %s", t)
		}
	}
	return nil
}

// driverReady 驱动文件是否就位（供状态栏显示）
func driverReady() bool {
	h, err := fileSHA256(filepath.Join(exeDir(), "wintun.dll"))
	return err == nil && h == wintunDLLHash
}

// isAdmin 当前进程是否具备管理员权限
func isAdmin() bool {
	var token windows.Token
	err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token)
	if err != nil {
		return false
	}
	defer token.Close()
	var elevation uint32
	var retLen uint32
	err = windows.GetTokenInformation(token, windows.TokenElevation,
		(*byte)(unsafe.Pointer(&elevation)), uint32(unsafe.Sizeof(elevation)), &retLen)
	return err == nil && elevation != 0
}

// elevateAndRestart 非管理员时以 runas 重启自身（双重保障②，清单声明为①）。
// 设置环境变量 SEEINPC_NOELEVATE=1 可跳过提权（调试/安装器已提权场景）。
func elevateAndRestart() {
	if isAdmin() {
		return
	}
	if os.Getenv("SEEINPC_NOELEVATE") != "" {
		return
	}
	exe, err := syscall.UTF16PtrFromString(os.Args[0])
	if err != nil {
		return
	}
	verb, _ := syscall.UTF16PtrFromString("runas")
	_ = windows.ShellExecute(0, verb, exe, nil, nil, windows.SW_SHOWNORMAL)
	os.Exit(0)
}

// acquireInstanceLock 单实例锁（复用 seeinps 的 LockFileEx 方案）
func acquireInstanceLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	lockFileEx := kernel32.NewProc("LockFileEx")
	const flags = uintptr(1 | 2) // LOCKFILE_FAIL_IMMEDIATELY | LOCKFILE_EXCLUSIVE_LOCK
	var overlapped syscall.Overlapped
	r1, _, callErr := lockFileEx.Call(f.Fd(), flags, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if r1 == 0 {
		f.Close()
		return nil, callErr
	}
	return f, nil
}

// openExplorer 打开资源管理器目录（日志/配置入口）
func openExplorer(path string) {
	_ = exec.Command("explorer", path).Start()
}
