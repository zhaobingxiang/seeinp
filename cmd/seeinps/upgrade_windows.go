//go:build windows

package main

import (
	"fmt"
	"os"
)

// checkDiskSpace Windows 开发环境跳过磁盘检查
func checkDiskSpace(need int64) error {
	return nil
}

// applyAndRestart Windows 锁定运行中的可执行文件，无法在线自替换；
// 开发环境仅校验（删除临时文件）不重启，实际自替换在 Linux 上执行
func applyAndRestart(exePath, tmpPath string) error {
	_ = exePath
	if err := os.Remove(tmpPath); err != nil {
		return err
	}
	fmt.Printf("[UPGRADE] package applied (windows dev: restart skipped)\n")
	return nil
}
