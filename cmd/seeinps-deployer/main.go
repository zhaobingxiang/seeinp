// seeinps-deployer：seeinps 一键部署工具（单 exe / 内嵌前端 / 自动开浏览器）。
//
// 支持两种部署目标：
//  1. 本机 Windows —— 下载对应 goos/goarch 的包，部署到安装目录，注册为 Win32 服务托管。
//  2. 远程 Linux 服务器 —— 经 SSH/SFTP 推送包与配置，systemd 托管服务。
//
// 界面风格与 seeinps / seeinpm 一致（白+蓝极简、Element Plus），
// 前端构建产物内嵌于本二进制（web-deployer/），启动后自动拉起默认浏览器。
// 所有操作、HTTP 访问、部署流程、报错与成功均记录到用户目录 seeinps-deployer-logs/（按天轮转）。
package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/seeinp/seeinp/internal/webui"
)

// staticFiles 内嵌 web-deployer 前端构建产物
//
//go:embed all:web/build
var staticFS embed.FS

func staticSub() fs.FS {
	s, err := fs.Sub(staticFS, "web/build")
	if err != nil {
		panic(err)
	}
	return s
}

var (
	appVersion = "1.0.0.0001.01" // 部署工具自身版本（可被 -ldflags 覆盖）
)

func main() {
	port := flag.Int("port", 0, "本地 Web 服务端口（0=自动选择空闲端口）")
	flag.Parse()

	initAppLog()
	appLog("info", "=== seeinps-deployer 启动 === 版本=%s PID=%d 参数=%v", appVersion, os.Getpid(), strings.Join(os.Args[1:], " "))
	defer func() {
		if r := recover(); r != nil {
			appLog("error", "main panic: %v", r)
		}
	}()

	// Windows：未以管理员运行则自动触发 UAC 提权重启，本实例随即退出
	// （注册/启动 Windows 服务需要管理员权限）；用户拒绝 UAC 时降级为
	// 普通权限继续运行，部署本机服务时前端会提示手动提权。
	if runtime.GOOS == "windows" && !isAdmin() {
		appLog("info", "当前非管理员权限，触发 UAC 提权重启")
		if err := relaunchAsAdmin(os.Args[1:]); err == nil {
			appLog("info", "提权重启已发起，本实例退出")
			return
		}
		appLog("warn", "提权未成功（用户取消或失败），以普通权限继续运行")
	}

	// 结束历史残留的部署工具实例，保证同一时间只有一个后台进程在运行
	killPreviousInstances()

	// 起一个仅绑回环的本地服务：只对部署机本身开放，前端经同源访问
	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		appLog("error", "无法绑定本地端口 %s: %v", addr, err)
		return
	}
	boundPort := ln.Addr().(*net.TCPAddr).Port
	appLog("info", "本地服务已启动: http://127.0.0.1:%d", boundPort)

	mux := http.NewServeMux()

	// 后端 JSON API
	apiRoot := newAPIRoot()
	apiRoot.register(mux)
	// REST 别名（前端统一用 /api 前缀）
	apiRoot.registerRatePostFix(mux)

	// 前端 SPA
	mux.Handle("/", webui.SPAHandler(staticSub()))

	srv := &http.Server{
		Addr:    ln.Addr().String(),
		Handler: logMiddleware(mux),
	}

	// 优雅关闭：收到 Ctrl+C / 终止信号时先停本地服务再退出
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		<-ch
		appLog("info", "收到退出信号，正在关闭本地服务")
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	// 自动打开发布页面
	if err := openBrowser(fmt.Sprintf("http://127.0.0.1:%d", boundPort)); err != nil {
		appLog("warn", "打开浏览器失败（可手动访问 http://127.0.0.1:%d）: %v", boundPort, err)
	} else {
		appLog("info", "已打开浏览器")
	}

	// 阻塞直到服务退出
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		appLog("error", "本地服务异常退出: %v", err)
	} else {
		appLog("info", "本地服务已退出，seeinps-deployer 结束运行")
	}
}

// killPreviousInstances 结束历史残留的部署工具进程（排除自身），避免多个后台实例并存。
// 必须在提权之后调用：旧实例可能是管理员权限，普通权限无法结束它们。
func killPreviousInstances() {
	if runtime.GOOS != "windows" {
		return
	}
	self := os.Getpid()
	out, err := exec.Command("tasklist", "/FO", "CSV", "/NH", "/FI", "IMAGENAME eq seeinps-deployer.exe").Output()
	if err != nil {
		appLog("warn", "枚举历史部署工具进程失败: %v", err)
		return
	}
	killed := 0
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(strings.TrimSpace(line), `","`)
		if len(f) < 2 || !strings.HasPrefix(strings.Trim(f[0], `"`), "seeinps-deployer") {
			continue
		}
		pid, err := strconv.Atoi(strings.Trim(f[1], `" `))
		if err != nil || pid == self || pid == 0 {
			continue
		}
		if err := exec.Command("taskkill", "/F", "/PID", strconv.Itoa(pid)).Run(); err == nil {
			killed++
			appLog("info", "已结束历史部署工具进程 PID=%d", pid)
		} else {
			appLog("warn", "结束历史进程 PID=%d 失败: %v", pid, err)
		}
	}
	if killed == 0 {
		appLog("info", "无历史部署工具实例需要结束")
	}
}

// logMiddleware HTTP 访问日志：所有 API 调用（校验/测试连接/下载/安装/卸载等）均落日志
func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: 200}
		next.ServeHTTP(sw, r)
		appLog("info", "[http] %s %s -> %d (%dms) 来源:%s", r.Method, r.URL.Path, sw.code, time.Since(start).Milliseconds(), r.RemoteAddr)
	})
}

// statusWriter 记录响应状态码并透传 Flusher（SSE 依赖）
type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// openBrowser 用默认浏览器打开 URL（跨平台）
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Start()
}
