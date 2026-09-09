//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/svc"
)

// runAsService 尝试以 Windows 服务方式常驻运行。
// 服务模式下由 SCM 驱动 svc.Run 生命周期循环，返回 true（调用方 main 直接返回）；
// 若是交互会话中被误带 --service 运行，则返回 false 由前台模式兜底。
func runAsService(confPath string) bool {
	if interactive, err := svc.IsAnInteractiveSession(); err == nil && interactive {
		return false
	}
	if err := svc.Run("seeinps", &psService{confPath: confPath}); err != nil {
		fmt.Fprintf(os.Stderr, "service run failed: %v\n", err)
		return false
	}
	return true
}

// psService 实现 svc.Handler，向上报状态并驱动 seeinps 工作协程。
type psService struct {
	confPath string
}

func (s *psService) Execute(args []string, r <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}

	// Windows 服务工作目录默认是 System32，而 seeinps 全程使用相对路径（conf/ data/ logs/）。
	// 必须切换到可执行文件所在目录，确保配置、数据、日志都落在安装目录内。
	if exe, err := os.Executable(); err == nil {
		if dir := filepath.Dir(exe); dir != "" {
			if err := os.Chdir(dir); err != nil {
				status <- svc.Status{State: svc.StopPending}
				return false, 1
			}
		}
	}

	ctx, cancel, restore, err := startWorker(s.confPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start worker failed: %v\n", err)
		status <- svc.Status{State: svc.StopPending}
		return false, 1
	}
	// LIFO：先 cancel 触发关停，后 restore 冲刷日志落盘
	defer restore()
	defer cancel()

	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}

	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				return false, 0
			}
		case <-ctx.Done():
			return false, 0
		}
	}
}