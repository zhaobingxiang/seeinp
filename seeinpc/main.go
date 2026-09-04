// seeinpc —— seeinp 三件套的 Windows 客户端（C 端）：
// 将 seeinps 运维 HTTP 代理转换为虚拟网卡，提供"内网环境 VPN"体验。
// 纯 Go 实现（wintun + gVisor netstack，替代原 proxytovpn 的 tun2socks.exe 方案）。
package main

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/energye/systray"
	"github.com/seeinp/seeinp/internal/logx"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend
var assets embed.FS

//go:embed resource/wintun.dll
var wintunDLL []byte

//go:embed resource/seeinpc.ico
var trayIcon []byte

func main() {
	elevateAndRestart() // 虚拟网卡需管理员权限（清单声明之外的运行时兜底）

	if _, err := acquireInstanceLock(filepath.Join(exeDir(), "seeinpc.lock")); err != nil {
		fmt.Fprintln(os.Stderr, "seeinpc 已在运行")
		os.Exit(0)
	}

	restoreLogs := logx.Install(logsDir(), "seeinpc", "info", 10, 7)
	defer restoreLogs()

	wintunEmbed = wintunDLL
	if err := ensureWintun(); err != nil {
		logx.Errorf("[DRIVER] wintun 准备失败: %v", err)
	}

	app := NewApp()
	app.onQuit = systray.Quit

	// 托盘独立协程运行自身消息循环（energye/systray 内部已 LockOSThread）
	go systray.Run(func() { setupTray(app) }, func() {})

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

// setupTray 系统托盘：左键单击/双击恢复主界面，右键弹出菜单
func setupTray(app *App) {
	systray.SetIcon(trayIcon)
	systray.SetTitle("seeinpc")
	systray.SetTooltip("seeinpc · 运维 VPN 客户端")

	// 左键单击/双击：恢复主界面；右键：显示菜单
	systray.SetOnClick(func(systray.IMenu) { app.showWindow() })
	systray.SetOnDClick(func(systray.IMenu) { app.showWindow() })
	systray.SetOnRClick(func(menu systray.IMenu) {
		if err := menu.ShowMenu(); err != nil {
			logx.Warnf("[TRAY] show menu failed: %v", err)
		}
	})

	mShow := systray.AddMenuItem("显示主窗口", "显示主窗口")
	mDisconn := systray.AddMenuItem("断开 VPN", "断开当前运行的 VPN")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("退出", "清理隧道并退出")

	mShow.Click(func() { app.showWindow() })
	mDisconn.Click(func() { app.Disconnect() })
	mQuit.Click(func() { app.fullQuit() })
}
