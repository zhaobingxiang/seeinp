//go:build !windows

package main

// isAdmin 在非 Windows 平台上恒返回 true（Linux 系统服务需 root，但部署工具通常以 root 运行）
func isAdmin() bool {
	return true
}

// relaunchAsAdmin 在非 Windows 平台无需提权
func relaunchAsAdmin(args []string) error {
	return nil
}
