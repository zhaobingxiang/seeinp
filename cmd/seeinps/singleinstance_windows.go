//go:build windows

package main

import (
	"os"
	"syscall"
	"unsafe"
)

// acquireInstanceLock 进程级单实例锁：LockFileEx 的锁随进程句柄关闭自动释放
func acquireInstanceLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	lockFileEx := kernel32.NewProc("LockFileEx")
	// LOCKFILE_FAIL_IMMEDIATELY(1) | LOCKFILE_EXCLUSIVE_LOCK(2)
	const flags = uintptr(1 | 2)
	var overlapped syscall.Overlapped
	r1, _, callErr := lockFileEx.Call(f.Fd(), flags, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if r1 == 0 {
		f.Close()
		return nil, callErr
	}
	return f, nil
}
