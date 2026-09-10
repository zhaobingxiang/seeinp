// seeinpc —— seeinp 三件套的 Windows 客户端（C 端）：
// 将 seeinps 运维 HTTP 代理转换为虚拟网卡，提供"内网环境 VPN"体验。
// 纯 Go 实现（wintun + gVisor netstack，替代原 proxytovpn 的 tun2socks.exe 方案）。
package main

import (
	"embed"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"

	"github.com/energye/systray"
	"github.com/seeinp/seeinp/internal/logx"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"golang.org/x/sys/windows"
)

//go:embed all:frontend
var assets embed.FS

//go:embed resource/wintun.dll
var wintunDLL []byte

//go:embed resource/seeinpc.ico
var trayIcon []byte

func main() {
	elevateAndRestart() // 虚拟网卡需管理员权限（清单声明之外的运行时兜底）

	// 日志尽早接管：双开检测、驱动准备等启动期问题才有记录可查
	// （此前 logx.Install 在实例锁校验之后，锁失败直接 exit 会留下零日志）
	restoreLogs := logx.Install(logsDir(), "seeinpc", "info", 10, 7)
	defer restoreLogs()

	// 兜底：panic 时记录堆栈并立即刷盘。
	// logx 经管道异步落盘，进程直接崩溃会丢掉尾部日志（含 panic 堆栈）——
	// 全项目此前只有 version.go/selfupgrade.go/deployer 有 recover，seeinpc 一处都没有。
	defer func() {
		if r := recover(); r != nil {
			logx.Panicf("[PANIC] seeinpc panicked: %v\n%s", r, debug.Stack())
			panic(r)
		}
	}()

	if _, err := acquireInstanceLock(filepath.Join(exeDir(), "seeinpc.lock")); err != nil {
		// 已有实例在运行：弹窗告知（GUI 子系统下 stderr 不可见，静默退出曾让
		// 用户误以为新实例的托盘失效，实为旧实例残留图标）
		logx.Panicf("[APP] another instance is already running, exiting: %v", err)
		title, _ := windows.UTF16PtrFromString("seeinpc")
		text, _ := windows.UTF16PtrFromString("seeinpc 已在运行（请查看任务栏通知区域图标）。\n如托盘图标无响应，将鼠标悬停其上即可清除失效图标。")
		_, _ = windows.MessageBox(0, text, title, windows.MB_OK|windows.MB_ICONINFORMATION)
		os.Exit(0)
	}

	wintunEmbed = wintunDLL
	if err := ensureWintun(); err != nil {
		logx.Errorf("[DRIVER] wintun 准备失败: %v", err)
	} else {
		logx.Infof("[DRIVER] wintun ready admin=%v", isAdmin())
	}

	app := NewApp()
	app.onQuit = systray.Quit

	// 托盘独立协程运行自身消息循环。
	// 关键：energye/systray 的 Windows 实现只在包 init() 里 LockOSThread 锁了
	// main goroutine，并未锁 systray.Run 所在 goroutine——而 Win32 窗口（SystrayClass）
	// 的消息只投递到创建它的线程队列，GetMessage(hWnd=0) 又只取当前线程队列：
	// 一旦 Go 调度器把本 goroutine 迁移到其他线程，托盘消息循环就会在错误的线程上
	// 空转 GetMessage——表现为"图标可见但点击无响应"（曾在本项目复现，属概率性事件，
	// 与 hide/show 等触发 goroutine 调度的操作时序相关）。
	// 因此在 Run 之前显式锁定当前 goroutine 到其 OS 线程，保证建窗与消息循环
	// 全程线程亲和；同时该线程被独占后，Wails runtime 内部的 LockOSThread 调用
	// 也不可能再把托盘循环挤走。Run 返回即托盘消息循环终止（正常退出时 quitFlag
	// 已置位），此处日志用于侦测"图标可见但点击无响应"的循环停摆问题。
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logx.Panicf("[PANIC] tray goroutine panicked: %v\n%s", r, debug.Stack())
			}
		}()
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		systray.Run(func() { setupTray(app) }, func() {})
		if !app.quitFlag.Load() {
			logx.Errorf("[TRAY] 托盘消息循环异常退出，托盘将不可用（请重启应用并反馈日志）")
		}
	}()

	err := wails.Run(&options.App{
		Title:     "seeinpc",
		Width:     1180,
		Height:    780,
		MinWidth:  980,
		MinHeight: 640,
		Frameless: true,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 245, G: 247, B: 250, A: 1},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		OnBeforeClose:    app.beforeClose,
		Bind: []interface{}{
			app,
		},
	})
	if err != nil {
		logx.Errorf("[APP] wails run failed: %v", err)
		os.Exit(1)
	}
}

// setupTray 系统托盘：左键单击/双击恢复主界面，右键弹出菜单。
// 注意：所有回调都运行在 Win32 消息回调的临时 goroutine 上——**禁止**在该栈上
// 直接调用 Wails runtime（其内部 LockOSThread 会与阻塞在 GetMessage 中的托盘
// goroutine 争抢线程锁归属，导致托盘消息循环停摆：图标可见但点击无响应）。
// 应用逻辑必须 go 出去执行。
func setupTray(app *App) {
	systray.SetIcon(trayIcon)
	systray.SetTitle("seeinpc")
	systray.SetTooltip("seeinpc · 运维 VPN 客户端")

	// 左键单击/双击：恢复主界面；右键：显示菜单（ShowMenu 为库内模态循环，留在回调内）
	systray.SetOnClick(func(systray.IMenu) { go app.showWindow() })
	systray.SetOnDClick(func(systray.IMenu) { go app.showWindow() })
	systray.SetOnRClick(func(menu systray.IMenu) {
		if err := menu.ShowMenu(); err != nil {
			logx.Warnf("[TRAY] show menu failed: %v", err)
		}
	})

	mShow := systray.AddMenuItem("显示主窗口", "显示主窗口")
	mDisconn := systray.AddMenuItem("断开 VPN", "断开当前运行的 VPN")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("退出", "清理隧道并退出")

	mShow.Click(func() { go app.showWindow() })
	mDisconn.Click(func() { go app.Disconnect() })
	mQuit.Click(func() { go app.fullQuit() })

	// 提权运行时放行 Explorer → 托盘窗口的点击消息（UIPI），否则真实点击无响应
	allowTrayClicksFromLowIL()
}
