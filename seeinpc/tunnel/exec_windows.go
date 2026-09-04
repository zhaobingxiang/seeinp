//go:build windows

package tunnel

import (
	"os/exec"
	"syscall"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// noWindow 隐藏子进程控制台窗口：netsh/route/powershell 均为控制台程序，
// 不加 CREATE_NO_WINDOW 时每次调用都会在前台短暂弹出 cmd 黑窗。
func noWindow(cmd *exec.Cmd) *exec.Cmd {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
	return cmd
}

// decodeGBK 将 netsh/route/powershell 的 GBK 输出转为 UTF-8，避免日志/提示乱码。
// 解码失败时回退为原字符串。
func decodeGBK(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	out, err := simplifiedchinese.GBK.NewDecoder().Bytes(b)
	if err != nil {
		return string(b)
	}
	return string(out)
}
