//go:build windows

package main

import (
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// isAdmin 检测当前进程是否以提升的管理员权限运行。
// 创建/启动/删除 Windows 服务等操作需要管理员权限，部署本机 seeinps 前应提示用户提权。
func isAdmin() bool {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return false
	}
	defer token.Close()

	var elevation uint32
	var sz uint32
	err = windows.GetTokenInformation(
		token,
		windows.TokenElevation,
		(*byte)(unsafe.Pointer(&elevation)),
		uint32(unsafe.Sizeof(elevation)),
		&sz,
	)
	if err != nil {
		// 无法判定时保守返回 true，避免因误报阻断合法安装路径
		return true
	}
	return elevation != 0
}

var (
	modShell32   = windows.NewLazySystemDLL("shell32.dll")
	procShellExW = modShell32.NewProc("ShellExecuteW")
)

// relaunchAsAdmin 通过 ShellExecute("runas") 以管理员权限重新启动当前程序（触发 UAC 弹窗）。
// args 为透传给新实例的命令行参数；返回是否成功发起提权流程。
func relaunchAsAdmin(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return err
	}
	exePtr, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	cwdPtr, err := windows.UTF16PtrFromString(cwd)
	if err != nil {
		return err
	}
	paramsPtr, err := windows.UTF16PtrFromString(strings.Join(args, " "))
	if err != nil {
		return err
	}

	r, _, callErr := procShellExW.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(exePtr)),
		uintptr(unsafe.Pointer(paramsPtr)),
		uintptr(unsafe.Pointer(cwdPtr)),
		windows.SW_SHOWNORMAL,
	)
	// ShellExecuteW 返回值 <= 32 表示失败（枚举见 shellapi.h）
	if uintptr(r) <= 32 {
		return fmt.Errorf("启动管理员提权失败: %v", callErr)
	}
	return nil
}
