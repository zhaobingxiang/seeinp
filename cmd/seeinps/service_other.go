//go:build !windows

package main

// runAsService 非 Windows 平台无原生服务模式，统一走前台运行。返回 false 由 main 兜底。
func runAsService(confPath string) bool {
	return false
}