//go:build linux || darwin || unix

package main

import (
	"os"
	"syscall"
)

// acquireInstanceLock 进程级单实例锁：文件锁随进程退出自动释放（含被 kill），
// 第二个实例启动时立即得到占用错误，从根源避免双实例互踢（1002）与 B 端端口抢占
func acquireInstanceLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
