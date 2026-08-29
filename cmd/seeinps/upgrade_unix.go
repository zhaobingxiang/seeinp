//go:build unix

package main

import (
	"fmt"
	"os"
	"syscall"
)

// checkDiskSpace 检查工作目录所在文件系统的剩余空间
func checkDiskSpace(need int64) error {
	var st syscall.Statfs_t
	if err := syscall.Statfs(".", &st); err != nil {
		return err
	}
	avail := int64(st.Bavail) * int64(st.Bsize)
	if avail < need {
		return fmt.Errorf("剩余 %d MB，需要 %d MB", avail>>20, need>>20)
	}
	return nil
}

// applyAndRestart 原子换位二进制并 exec 重启：把运行中的文件 rename 为 .old（运行中进程
// 持有旧 inode 不受影响，规避 cp 覆盖运行中二进制的 ETXTBSY），新文件顶替原路径后
// syscall.Exec 原地重启，无需 systemd/supervisor。
func applyAndRestart(exePath, tmpPath string) error {
	oldPath := exePath + ".old"
	if err := os.Remove(oldPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("清理旧备份: %w", err)
	}
	if err := os.Rename(exePath, oldPath); err != nil {
		return fmt.Errorf("备份当前二进制: %w", err)
	}
	if err := os.Rename(tmpPath, exePath); err != nil {
		_ = os.Rename(oldPath, exePath) // 换位失败回滚备份
		return fmt.Errorf("替换二进制: %w", err)
	}
	if err := os.Chmod(exePath, 0755); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}
	fmt.Printf("[UPGRADE] exec new binary %s\n", exePath)
	return syscall.Exec(exePath, os.Args, os.Environ())
}
