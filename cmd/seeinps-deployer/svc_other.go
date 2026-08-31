//go:build !windows

package main

import "fmt"

// 非 Windows 平台不涉及本机 Windows 服务托管（Linux 远程部署走远端 systemd），
// 这些函数仅用于让 windows.go 的 deployWindows 在任意平台均能编译，实际不会被执行。

func createWindowsService(name, binPath, confPath string) error {
	return fmt.Errorf("Windows 服务注册仅支持 Windows 平台")
}

func startWindowsService(name string) error {
	return fmt.Errorf("Windows 服务启动仅支持 Windows 平台")
}

func stopWindowsService(name string) error {
	return nil
}

func deleteWindowsService(name string) error {
	return nil
}

func svcExists(name string) bool {
	return false
}
