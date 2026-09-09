package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/seeinp/seeinp/internal/logx"
	"github.com/seeinp/seeinp/internal/version"
	"github.com/seeinp/seeinp/seeinpc/tunnel"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// App 应用编排：配置、VPN 会话、探测循环与前端事件推送。
// 暴露方法均被 Wails 绑定到前端（window.go.main.App.*）。
type App struct {
	ctx context.Context

	mu      sync.Mutex
	cfg     *Config
	session *vpnSession
	probes  map[string]*probeInfo
	probing bool

	updateInfo    *UpdateInfo // 最新检查到的升级信息
	installerPath string      // 已下载校验的安装包本地路径
	downloading   atomic.Bool
	updateCancel  context.CancelFunc

	ring      *logRing
	quitFlag  atomic.Bool
	probeOnce chan struct{}
	onQuit    func() // 由 main 注入：退出托盘等收尾
}

// fullQuit 完整退出：置退出标记 → 清理隧道 → 保存配置 → 退出托盘与窗口
func (a *App) fullQuit() {
	if !a.quitFlag.CompareAndSwap(false, true) {
		return
	}
	a.stopVPN("程序退出")
	a.mu.Lock()
	if err := saveConfig(a.cfg); err != nil {
		a.logErrorf("[APP] save config failed: %v", err)
	}
	a.mu.Unlock()
	if a.onQuit != nil {
		a.onQuit()
	}
	if a.ctx != nil {
		wruntime.Quit(a.ctx)
	}
}

// OpenLogsDir 打开日志目录
func (a *App) OpenLogsDir() { openExplorer(logsDir()) }

// OpenConfDir 打开配置目录
func (a *App) OpenConfDir() { openExplorer(filepath.Dir(confPath())) }

type vpnSession struct {
	entryID     string
	entryName   string
	adapterIP   string
	addedRoutes []string
	engine      *tunnel.Engine
	startedAt   time.Time
}

type probeInfo struct {
	Probe string // ok / 407 / 403 / unreachable / dialfail / unknown
	Msg   string
	RTT   string
	Last  time.Time
}

func NewApp() *App {
	return &App{
		probes:    make(map[string]*probeInfo),
		ring:      newLogRing(600),
		probeOnce: make(chan struct{}, 1),
	}
}

// logf 统一日志出口：logx 落盘 + 内存环形缓冲（前端日志页读取）
func (a *App) logf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	logx.Infof("%s", msg)
	a.ring.append(time.Now().Format("2006-01-02 15:04:05.000") + " [INFO] " + msg)
}

func (a *App) logWarnf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	logx.Warnf("%s", msg)
	a.ring.append(time.Now().Format("2006-01-02 15:04:05.000") + " [WARN] " + msg)
}

func (a *App) logErrorf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	logx.Errorf("%s", msg)
	a.ring.append(time.Now().Format("2006-01-02 15:04:05.000") + " [ERROR] " + msg)
}

// ---------------- 生命周期 ----------------

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	cfg, err := loadConfig()
	if err != nil {
		a.logErrorf("[APP] load config failed: %v", err)
		cfg = &Config{Settings: defaultSettings()}
	}
	a.mu.Lock()
	a.cfg = cfg
	a.mu.Unlock()
	if cfg.Settings.LogLevel != "" {
		_ = logx.SetLevel(cfg.Settings.LogLevel)
	}
	a.logf("[APP] version=%s banner=%s pid_entries=%d admin=%v", version.Version, version.Banner, len(cfg.Entries), isAdmin())
	go a.probeLoop()
	go func() {
		time.Sleep(3 * time.Second) // 延迟检查，不阻塞启动
		if a.quitFlag.Load() {
			return
		}
		res := a.CheckUpdate()
		if res.Message != "" {
			a.logf("[UPDATE] startup check: %s", res.Message)
			return
		}
		if res.Available && (!res.Ignored || res.Forced) {
			a.logf("[UPDATE] new version available current=%s new=%s forced=%v", res.Current, res.Version, res.Forced)
			wruntime.EventsEmit(a.ctx, "update-available", res)
		}
	}()
	a.emitState()
}

func (a *App) shutdown(_ context.Context) {
	a.stopVPN("退出清理")
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cfg == nil {
		return
	}
	if err := saveConfig(a.cfg); err != nil {
		a.logErrorf("[APP] save config failed: %v", err)
	}
}

// beforeClose 窗口关闭拦截：最小化到托盘（除非真正退出）
func (a *App) beforeClose(ctx context.Context) bool {
	a.mu.Lock()
	minimize := true
	if a.cfg != nil {
		minimize = a.cfg.Settings.MinimizeToTray
	}
	a.mu.Unlock()
	if a.quitFlag.Load() || !minimize {
		return false // 允许关闭
	}
	wruntime.WindowHide(ctx)
	return true // 取消关闭
}

// emitState 推送全量状态到前端
func (a *App) emitState() {
	if a.ctx == nil {
		return
	}
	wruntime.EventsEmit(a.ctx, "state", a.GetState())
}

// ---------------- 前端绑定方法 ----------------

// GetState 返回全量状态（卡片+设置+运行信息）
func (a *App) GetState() StateView {
	a.mu.Lock()
	defer a.mu.Unlock()
	view := StateView{
		Settings: a.cfg.Settings,
		Version:  version.Version,
		LogLevel: logx.GetLevel(),
		Admin:    isAdmin(),
		DriverOK: driverReady(),
		Probing:  a.probing,
		LogPath:  filepath.Join(logsDir(), "seeinpc.log"),
		Cards:    make([]CardView, 0, len(a.cfg.Entries)),
	}
	if a.session != nil && a.session.engine != nil {
		view.RunningID = a.session.entryID
		view.RunningName = a.session.entryName
		view.ConnCount = a.session.engine.Conns()
	}
	for _, e := range a.cfg.Entries {
		view.Cards = append(view.Cards, a.cardViewLocked(e))
	}
	return view
}

func (a *App) cardViewLocked(e *Entry) CardView {
	cv := CardView{
		ID:            e.ID,
		Name:          e.Name,
		Server:        e.Server,
		OpsID:         e.OpsID,
		ProxyUser:     e.ProxyUser,
		HasPwd:        e.ProxyPass != "",
		ACL:           e.ACL,
		ProbeTarget:   e.ProbeTarget,
		ProbeIntranet: e.ProbeIntranet,
		SourcePS:      e.SourcePS,
		Manual:        e.Manual,
		Probe:         "unknown",
	}
	if p, ok := a.probes[e.ID]; ok {
		cv.Probe = p.Probe
		cv.ProbeMsg = p.Msg
		cv.RTT = p.RTT
		cv.LastProbe = p.Last.Format("15:04:05")
	}
	switch {
	case a.session != nil && a.session.entryID == e.ID:
		cv.Status = "running"
	case !cv.HasPwd:
		cv.Status = "waitpwd"
	case cv.Probe == "407" || cv.Probe == "403" || cv.Probe == "unreachable" || cv.Probe == "dialfail":
		cv.Status = "fail"
	default:
		cv.Status = "ready"
	}
	return cv
}

// FetchSeeinps 「登录 seeinps 获取」第一步：登录并拉取运维 HTTP 代理列表，不落库。
// 返回的每个代理带服务端 ACL（预填给用户编辑）；服务端 ACL 为空或仅 0.0.0.0/0 时标记
// NeedACL，前端必须让用户手动填写网段后才能生成卡片。
// 服务器地址来自设置中的服务器池（默认 see.timemsee.cn 主 / see.timesee.cn 备，顺序回退）。
func (a *App) FetchSeeinps(port int, user, pwd, captchaID, captchaText string) FetchSeeinpsResult {
	if port <= 0 || port > 65535 {
		return FetchSeeinpsResult{OpResult: OpResult{Message: "请填写有效的 web-ui 外部端口（1-65535）"}}
	}
	c, lr, err := loginFallback(a.serverPool(), port, user, pwd, captchaID, captchaText)
	if err != nil {
		a.logErrorf("[AUTH] login failed port=%d err=%v", port, err)
		return FetchSeeinpsResult{OpResult: OpResult{Message: err.Error()}}
	}
	src := strings.TrimPrefix(c.base, "http://")
	if lr.NeedCaptcha && !lr.OK {
		cid, b64, cerr := c.captcha(context.Background())
		if cerr != nil {
			return FetchSeeinpsResult{OpResult: OpResult{Message: lr.Message}}
		}
		msg := lr.Message
		if msg == "" {
			msg = "请输入验证码"
		}
		return FetchSeeinpsResult{
			OpResult:    OpResult{Message: msg},
			NeedCaptcha: true, CaptchaID: cid, CaptchaB64: b64,
		}
	}
	if !lr.OK {
		return FetchSeeinpsResult{OpResult: OpResult{Message: lr.Message}}
	}
	a.logf("[AUTH] login ok src=%s user=%s", src, user)

	proxies, err := c.opsProxies(context.Background(), lr.Token)
	if err != nil {
		return FetchSeeinpsResult{OpResult: OpResult{Message: err.Error()}}
	}
	if len(proxies) == 0 {
		return FetchSeeinpsResult{OpResult: OpResult{Message: "该 seeinps 暂无运维 HTTP 代理（type=ops_http）"}}
	}
	items := make([]FetchOpsItem, 0, len(proxies))
	for _, p := range proxies {
		acl := sanitizeACL(p.ACL)
		items = append(items, FetchOpsItem{
			OpsID: p.OpsID, ProxyUser: p.ProxyUser,
			ACL: acl, NeedACL: len(acl) == 0,
		})
	}
	return FetchSeeinpsResult{
		OpResult: OpResult{OK: true, Message: fmt.Sprintf("已获取 %d 个运维代理，请确认网段后生成卡片", len(items))},
		SourcePS: src, Items: items,
	}
}

// ConfirmSeeinps 「登录 seeinps 获取」第二步：按用户编辑后的列表生成/更新卡片。
// 名称留空用默认「账号 · 运维ID」；网段必填且经安全过滤（0.0.0.0/0 一律拒绝）。
func (a *App) ConfirmSeeinps(sourcePS string, items []ConfirmOpsItem) OpResult {
	if sourcePS == "" || len(items) == 0 {
		return OpResult{Message: "没有需要生成的卡片"}
	}
	for _, it := range items {
		if it.OpsID <= 0 || it.OpsID > 65535 {
			return OpResult{Message: "存在无效的运维代理ID（应为 1-65535）"}
		}
		if containsFullRoute(it.ACL) {
			return OpResult{Message: fmt.Sprintf("代理 %d 的目标网段不支持 0.0.0.0/0：会把本机全部流量导入 VPN 导致断网", it.OpsID)}
		}
		if len(sanitizeACL(it.ACL)) == 0 {
			return OpResult{Message: fmt.Sprintf("代理 %d 的目标网段为必填项：该 seeinps 未提供可用 ACL（或为全网段），请手动填写至少一个网段", it.OpsID)}
		}
	}
	a.mu.Lock()
	added, updated := 0, 0
	for _, it := range items {
		acl := sanitizeACL(it.ACL)
		name := strings.TrimSpace(it.Name)
		if name == "" {
			name = fmt.Sprintf("%s · %d", it.ProxyUser, it.OpsID)
		}
		if existing := a.findByOpsLocked(sourcePS, it.OpsID, it.ProxyUser); existing != nil {
			existing.ACL = acl
			existing.SourcePS = sourcePS
			if strings.TrimSpace(it.Name) != "" {
				existing.Name = name
			}
			if existing.ProxyUser == "" {
				existing.ProxyUser = it.ProxyUser
			}
			updated++
			continue
		}
		a.cfg.Entries = append(a.cfg.Entries, &Entry{
			ID: newID(), Name: name, OpsID: it.OpsID,
			ProxyUser: it.ProxyUser, ACL: acl, SourcePS: sourcePS,
		})
		added++
	}
	err := saveConfig(a.cfg)
	a.mu.Unlock()
	if err != nil {
		return OpResult{Message: "保存配置失败: " + err.Error()}
	}
	a.logf("[SYNC] confirm from=%s items=%d added=%d updated=%d", sourcePS, len(items), added, updated)
	a.triggerProbe()
	a.emitState()
	msg := fmt.Sprintf("新增 %d 个运维入口", added)
	if updated > 0 {
		msg += fmt.Sprintf("，更新 %d 个已存在入口", updated)
	}
	return OpResult{OK: true, Message: msg}
}

// AddOpsDirect 直接填写运维代理（名称可选+ID+账号+密码+目标网段）生成单张卡片，无需登录 seeinps。
// name 留空时使用默认名「代理账号 · 运维ID」。
func (a *App) AddOpsDirect(name string, opsID int, proxyUser, proxyPass string, acl []string) OpResult {
	if opsID <= 0 || opsID > 65535 {
		return OpResult{Message: "请填写有效的运维代理ID（端口 1-65535）"}
	}
	if containsFullRoute(acl) {
		return OpResult{Message: "目标网段不支持 0.0.0.0/0：会把本机全部流量导入 VPN 导致断网"}
	}
	acl = sanitizeACL(acl)
	if len(acl) == 0 {
		return OpResult{Message: "目标网段为必填项，请至少添加一个网段"}
	}
	a.mu.Lock()
	var existing *Entry
	for _, e := range a.cfg.Entries {
		if e.OpsID == opsID && e.ProxyUser == proxyUser && e.SourcePS == "" {
			existing = e
			break
		}
	}
	if existing != nil {
		if proxyPass != "" {
			existing.ProxyPass = proxyPass
		}
		if strings.TrimSpace(name) != "" {
			existing.Name = strings.TrimSpace(name)
		}
		existing.ACL = acl
	} else {
		displayName := strings.TrimSpace(name)
		if displayName == "" {
			displayName = fmt.Sprintf("%s · %d", proxyUser, opsID)
		}
		a.cfg.Entries = append(a.cfg.Entries, &Entry{
			ID:        newID(),
			Name:      displayName,
			OpsID:     opsID,
			ProxyUser: proxyUser,
			ProxyPass: proxyPass,
			ACL:       acl,
		})
	}
	err := saveConfig(a.cfg)
	a.mu.Unlock()
	if err != nil {
		return OpResult{Message: "保存配置失败: " + err.Error()}
	}
	a.logf("[APP] ops direct added opsId=%d user=%s", opsID, proxyUser)
	a.triggerProbe()
	a.emitState()
	return OpResult{OK: true, Message: "已添加运维入口"}
}

// ManualAdd 手动添加 VPN（备选入口，等价原需求文档 §9.2 手工模式）
func (a *App) ManualAdd(name, server string, opsID int, proxyUser, proxyPass string, acl []string, probeTarget string) OpResult {
	if name == "" || opsID <= 0 || opsID > 65535 {
		return OpResult{Message: "名称、端口号（1-65535）为必填项"}
	}
	if containsFullRoute(acl) {
		return OpResult{Message: "目标网段不支持 0.0.0.0/0：会把本机全部流量导入 VPN 导致断网"}
	}
	acl = sanitizeACL(acl)
	if len(acl) == 0 {
		return OpResult{Message: "目标网段为必填项，请至少添加一个网段"}
	}
	probeTarget, probeErr := validateProbeTarget(probeTarget)
	if probeErr != "" {
		return OpResult{Message: probeErr}
	}
	a.mu.Lock()
	a.cfg.Entries = append(a.cfg.Entries, &Entry{
		ID: newID(), Name: name, Server: server, OpsID: opsID,
		ProxyUser: proxyUser, ProxyPass: proxyPass, ACL: acl,
		ProbeTarget: probeTarget, Manual: true,
	})
	err := saveConfig(a.cfg)
	a.mu.Unlock()
	if err != nil {
		return OpResult{Message: "保存失败: " + err.Error()}
	}
	a.logf("[APP] manual add name=%s opsId=%d", name, opsID)
	a.triggerProbe()
	a.emitState()
	return OpResult{OK: true, Message: "已添加"}
}

// FillPassword 为卡片一次性填写代理密码（DPAPI 加密保存）
func (a *App) FillPassword(id, pwd string) OpResult {
	if pwd == "" {
		return OpResult{Message: "密码不能为空"}
	}
	a.mu.Lock()
	e := a.findEntryLocked(id)
	if e == nil {
		a.mu.Unlock()
		return OpResult{Message: "入口不存在"}
	}
	e.ProxyPass = pwd
	delete(a.probes, id)
	err := saveConfig(a.cfg)
	a.mu.Unlock()
	if err != nil {
		return OpResult{Message: "保存失败: " + err.Error()}
	}
	a.logf("[APP] password saved entry=%s", id)
	delete(a.probes, id)
	a.triggerProbe()
	a.emitState()
	return OpResult{OK: true, Message: "代理密码已保存"}
}

// UpdateEntry 编辑卡片（名称/代理服务器/探测目标/目标网段）
func (a *App) UpdateEntry(id, name, server, probeTarget string, probeIntranet bool, acl []string) OpResult {
	if containsFullRoute(acl) {
		return OpResult{Message: "目标网段不支持 0.0.0.0/0：会把本机全部流量导入 VPN 导致断网"}
	}
	cleaned := sanitizeACL(acl)
	if len(cleaned) == 0 {
		return OpResult{Message: "目标网段为必填项，请至少添加一个网段"}
	}
	probeTarget, probeErr := validateProbeTarget(probeTarget)
	if probeErr != "" {
		return OpResult{Message: probeErr}
	}
	server = strings.TrimSpace(server)
	if server != "" {
		inPool := false
		for _, s := range a.serverPool() {
			if s == server {
				inPool = true
				break
			}
		}
		if !inPool {
			return OpResult{Message: "指定的代理服务器不在设置的服务器列表中，请先在设置中添加，或改回“自动（按列表顺序回退）”"}
		}
	}
	a.mu.Lock()
	e := a.findEntryLocked(id)
	if e == nil {
		a.mu.Unlock()
		return OpResult{Message: "入口不存在"}
	}
	if name != "" {
		e.Name = name
	}
	e.Server = server
	e.ProbeIntranet = probeIntranet
	e.ProbeTarget = probeTarget
	e.ACL = cleaned
	err := saveConfig(a.cfg)
	a.mu.Unlock()
	if err != nil {
		return OpResult{Message: "保存失败: " + err.Error()}
	}
	a.emitState()
	return OpResult{OK: true, Message: "已保存"}
}

// RemoveEntry 删除卡片（若正在运行先断开）
func (a *App) RemoveEntry(id string) OpResult {
	a.mu.Lock()
	if a.session != nil && a.session.entryID == id {
		a.mu.Unlock()
		a.stopVPN("删除入口")
		a.mu.Lock()
	}
	idx := -1
	for i, e := range a.cfg.Entries {
		if e.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		a.mu.Unlock()
		return OpResult{Message: "入口不存在"}
	}
	name := a.cfg.Entries[idx].Name
	a.cfg.Entries = append(a.cfg.Entries[:idx], a.cfg.Entries[idx+1:]...)
	delete(a.probes, id)
	err := saveConfig(a.cfg)
	a.mu.Unlock()
	if err != nil {
		return OpResult{Message: "保存失败: " + err.Error()}
	}
	a.logf("[APP] entry removed name=%s", name)
	a.emitState()
	return OpResult{OK: true, Message: "已移除"}
}

// Connect 连接指定入口（同一时刻仅一个 VPN 运行，先停旧再启新）
func (a *App) Connect(id string) OpResult {
	a.mu.Lock()
	e := a.findEntryLocked(id)
	if e == nil {
		a.mu.Unlock()
		return OpResult{Message: "入口不存在"}
	}
	if e.ProxyPass == "" {
		a.mu.Unlock()
		return OpResult{Message: "请先填写代理密码"}
	}
	if len(e.ACL) == 0 {
		a.mu.Unlock()
		return OpResult{Message: "目标网段为空，请先编辑该入口填写目标网段"}
	}
	entry := *e // 复制一份，避免长事务持锁
	a.mu.Unlock()

	if err := a.startVPN(&entry); err != nil {
		return OpResult{Message: err.Error()}
	}
	a.emitState()
	return OpResult{OK: true, Message: "已连接 " + entry.Name}
}

// Disconnect 断开当前 VPN
func (a *App) Disconnect() OpResult {
	a.stopVPN("手动断开")
	a.emitState()
	return OpResult{OK: true, Message: "已断开"}
}

// RefreshProbes 立即执行一轮探测
func (a *App) RefreshProbes() OpResult {
	a.mu.Lock()
	a.probing = true
	a.mu.Unlock()
	a.emitState()
	a.triggerProbe()
	return OpResult{OK: true, Message: "探测中…"}
}

// GetLogs 读取最近 lines 行日志（内存环形缓冲）
func (a *App) GetLogs(lines int) []string {
	return a.ring.tail(lines)
}

// SetLogLevel 修改日志级别（即时生效并持久化）
func (a *App) SetLogLevel(level string) OpResult {
	if err := logx.SetLevel(level); err != nil {
		return OpResult{Message: err.Error()}
	}
	a.mu.Lock()
	a.cfg.Settings.LogLevel = logx.GetLevel()
	_ = saveConfig(a.cfg)
	a.mu.Unlock()
	a.logf("[APP] log level changed level=%s", logx.GetLevel())
	return OpResult{OK: true, Message: "已生效"}
}

// SaveSettings 保存全局设置
func (a *App) SaveSettings(s Settings) OpResult {
	a.mu.Lock()
	if s.ProbeIntervalSec < 5 {
		s.ProbeIntervalSec = 30
	}
	if s.LogLevel == "" {
		s.LogLevel = a.cfg.Settings.LogLevel
	}
	// 代理服务器池清洗：去空白、丢弃空地址、无名用地址兜底、按地址去重；全空则回种内置默认
	seen := map[string]bool{}
	clean := make([]ServerConf, 0, len(s.Servers))
	for _, sv := range s.Servers {
		addr := strings.TrimSpace(sv.Addr)
		if addr == "" || seen[addr] {
			continue
		}
		seen[addr] = true
		name := strings.TrimSpace(sv.Name)
		if name == "" {
			name = addr
		}
		clean = append(clean, ServerConf{Name: name, Addr: addr})
	}
	if len(clean) == 0 {
		clean = defaultServers()
	}
	s.Servers = clean
	a.cfg.Settings = s
	err := saveConfig(a.cfg)
	a.mu.Unlock()
	if err != nil {
		return OpResult{Message: "保存失败: " + err.Error()}
	}
	_ = logx.SetLevel(s.LogLevel)
	a.logf("[APP] settings saved interval=%d", s.ProbeIntervalSec)
	a.emitState()
	return OpResult{OK: true, Message: "设置已保存"}
}

// HideWindow 隐藏主窗口到托盘（自定义关闭按钮调用）
func (a *App) HideWindow() {
	a.logf("[TRAY] hide requested")
	if a.ctx != nil {
		wruntime.WindowHide(a.ctx)
		a.logf("[TRAY] hide done normal=%v minimised=%v", wruntime.WindowIsNormal(a.ctx), wruntime.WindowIsMinimised(a.ctx))
	}
}

// showWindow 恢复并前置主窗口（托盘左键单击/双击、菜单"显示主窗口"）
func (a *App) showWindow() {
	a.logf("[TRAY] show requested")
	if a.ctx == nil {
		return
	}
	wruntime.WindowShow(a.ctx)
	wruntime.WindowUnminimise(a.ctx)
	a.logf("[TRAY] show done normal=%v minimised=%v", wruntime.WindowIsNormal(a.ctx), wruntime.WindowIsMinimised(a.ctx))
}

// QuitApp 完整退出（清理隧道 → 退出）
func (a *App) QuitApp() {
	a.fullQuit()
}

// ---------------- VPN 会话 ----------------

// startVPN 启动隧道（失败按步骤回滚：引擎→路由→网卡地址）
func (a *App) startVPN(e *Entry) error {
	a.stopVPN("切换 VPN")

	a.logf("[TUNNEL] connecting entry=%s opsId=%d server=%s", e.Name, e.OpsID, e.Server)
	emitConnecting := func() {
		a.mu.Lock()
		a.probes[e.ID] = &probeInfo{Probe: "unknown", Msg: "连接中…", Last: time.Now()}
		a.mu.Unlock()
		a.emitState()
	}
	emitConnecting()

	// 1. 创建虚拟网卡
	dev, err := tunnel.CreateDevice(tunnel.DefaultMTU)
	if err != nil {
		a.logErrorf("[TUNNEL] create adapter failed: %v", err)
		a.markProbe(e.ID, "dialfail", "创建虚拟网卡失败（请确认管理员权限与 wintun 驱动）")
		return err
	}
	rollback := func(step string, rerr error) error {
		a.logErrorf("[TUNNEL] rollback at %s: %v", step, rerr)
		a.markProbe(e.ID, "dialfail", "连接失败（"+step+"）: "+rerr.Error())
		return fmt.Errorf("%s失败: %w", step, rerr)
	}

	// 2. 配置网卡地址
	subnet := pickTunSubnet(e.ACL)
	adapterIP := fmt.Sprintf("10.0.%d.1", subnet)
	if err := tunnel.ConfigureAddress(adapterIP, "255.255.255.0"); err != nil {
		dev.Close()
		return rollback("配置网卡地址", err)
	}
	a.logf("[TUNNEL] adapter=%s ip=%s/24", tunnel.AdapterName, adapterIP)

	// 3. 写路由
	ifIdx, err := tunnel.AdapterIndex()
	if err != nil {
		tunnel.RemoveAddress(adapterIP)
		dev.Close()
		return rollback("查询接口索引", err)
	}
	added, err := tunnel.AddRoutes(e.ACL, adapterIP, ifIdx)
	if err != nil {
		tunnel.DeleteRoutes(added)
		tunnel.RemoveAddress(adapterIP)
		dev.Close()
		return rollback("添加路由", err)
	}
	a.logf("[ROUTE] added=%v via=%s if=%d", added, adapterIP, ifIdx)

	// 4. 启动协议栈引擎
	dialer := &tunnel.Dialer{
		Servers: a.entryServers(e), OpsID: e.OpsID,
		Username: e.ProxyUser, Password: e.ProxyPass,
		Timeout: 15 * time.Second,
	}
	engine, err := tunnel.StartEngine(dev, tunnel.DefaultMTU, dialer.DialContext, a.logf)
	if err != nil {
		tunnel.DeleteRoutes(added)
		tunnel.RemoveAddress(adapterIP)
		dev.Close()
		return rollback("启动隧道引擎", err)
	}

	a.mu.Lock()
	a.session = &vpnSession{
		entryID: e.ID, entryName: e.Name, adapterIP: adapterIP,
		addedRoutes: added, engine: engine, startedAt: time.Now(),
	}
	a.mu.Unlock()
	a.logf("[TUNNEL] connected entry=%s adapter=%s routes=%d", e.Name, tunnel.AdapterName, len(added))

	// 5. 连接后探测：仅当勾选"探测内网目标"时才经虚拟网卡探测，否则仅确认代理已连接
	go func() {
		time.Sleep(800 * time.Millisecond) // 等待路由生效
		if !e.ProbeIntranet {
			a.markProbe(e.ID, "ok", "VPN 已连接（代理正常）")
			a.emitState()
			return
		}
		target := a.probeTargetOf(e)
		if target == "" {
			a.markProbe(e.ID, "ok", "VPN 已连接（未配置内网探测目标）")
			a.emitState()
			return
		}
		start := time.Now()
		conn, derr := net.DialTimeout("tcp", target, 8*time.Second)
		if derr != nil {
			a.logWarnf("[PROBE] tunnel probe fail entry=%s target=%s err=%v", e.Name, target, derr)
			a.markProbe(e.ID, "dialfail", "VPN 已连接，但内网目标 "+target+" 不可达")
		} else {
			conn.Close()
			a.markProbe(e.ID, "ok", "内网目标可达", time.Since(start))
		}
		a.emitState()
	}()
	return nil
}

// stopVPN 停止当前会话并清理（路由→引擎→网卡地址；适配器保留复用）
func (a *App) stopVPN(reason string) {
	a.mu.Lock()
	s := a.session
	a.session = nil
	a.mu.Unlock()
	if s == nil {
		return
	}
	tunnel.DeleteRoutes(s.addedRoutes)
	if s.engine != nil {
		s.engine.Stop() // 内部关闭 dev
	}
	tunnel.RemoveAddress(s.adapterIP)
	a.logf("[TUNNEL] disconnected entry=%s reason=%s routes_removed=%d", s.entryName, reason, len(s.addedRoutes))
	a.emitState()
}

func (a *App) markProbe(id, probe, msg string, rtt ...time.Duration) {
	a.mu.Lock()
	p := &probeInfo{Probe: probe, Msg: msg, Last: time.Now()}
	if len(rtt) > 0 {
		p.RTT = fmt.Sprintf("%dms", rtt[0].Milliseconds())
	}
	a.probes[id] = p
	a.mu.Unlock()
}

// ---------------- 探测循环 ----------------

func (a *App) triggerProbe() {
	select {
	case a.probeOnce <- struct{}{}:
	default:
	}
}

// probeLoop 周期探测所有已填密码的入口（运行中的走隧道路径，其余走代理预检）
func (a *App) probeLoop() {
	for {
		a.runProbeCycle()
		a.mu.Lock()
		interval := a.cfg.Settings.ProbeIntervalSec
		a.mu.Unlock()
		if interval < 5 {
			interval = 30
		}
		select {
		case <-time.After(time.Duration(interval) * time.Second):
		case <-a.probeOnce:
		}
		if a.quitFlag.Load() {
			return
		}
	}
}

func (a *App) runProbeCycle() {
	defer func() {
		a.mu.Lock()
		a.probing = false
		a.mu.Unlock()
		a.emitState()
	}()
	a.mu.Lock()
	type task struct {
		entry   Entry
		running bool
	}
	tasks := make([]task, 0, len(a.cfg.Entries))
	for _, e := range a.cfg.Entries {
		if e.ProxyPass == "" {
			continue
		}
		running := a.session != nil && a.session.entryID == e.ID
		tasks = append(tasks, task{entry: *e, running: running})
	}
	a.mu.Unlock()

	for _, t := range tasks {
		if a.quitFlag.Load() {
			return
		}
		e := t.entry
		target := a.probeTargetOf(&e)
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		start := time.Now()
		dialer := &tunnel.Dialer{
			Servers: a.entryServers(&e), OpsID: e.OpsID,
			Username: e.ProxyUser, Password: e.ProxyPass, Timeout: 10 * time.Second,
		}

		if e.ProbeIntranet && t.running {
			// 勾选内网探测且运行中：经虚拟网卡直连内网目标
			conn, err := net.DialTimeout("tcp", target, 8*time.Second)
			cancel()
			if err != nil {
				a.markProbe(e.ID, "dialfail", "内网目标 "+target+" 不可达")
			} else {
				conn.Close()
				a.markProbe(e.ID, "ok", "内网目标可达", time.Since(start))
			}
			continue
		}

		// 默认：探测 HTTP 代理是否可用（CONNECT 认证预检），rtt 仅为到代理握手耗时
		rtt, err := dialer.Probe(ctx, target)
		cancel()
		switch {
		case err == nil:
			a.markProbe(e.ID, "ok", "代理可用", rtt)
		case errors.Is(err, tunnel.ErrTargetUnreachable):
			// 认证/ACL 通过但目标不可达：代理本身是可用的
			if e.ProbeIntranet {
				a.markProbe(e.ID, "dialfail", "认证通过，但内网目标 "+target+" 不可达")
			} else {
				a.markProbe(e.ID, "ok", "代理可用（认证通过）", rtt)
			}
		case errors.Is(err, tunnel.ErrProxyAuthFailed):
			a.markProbe(e.ID, "407", "认证失败：请核对代理用户名与密码")
		case errors.Is(err, tunnel.ErrACLDenied):
			a.markProbe(e.ID, "403", "探测目标被 ACL 拒绝（可修改探测目标）")
		case errors.Is(err, tunnel.ErrProxyUnreachable):
			a.markProbe(e.ID, "unreachable", "无法连接代理服务器 "+dialer.Addr())
		default:
			a.markProbe(e.ID, "dialfail", err.Error())
		}
	}
}

// probeTargetOf 探测目标：显式配置优先，否则取首个网段的首个可用地址:443
func (a *App) probeTargetOf(e *Entry) string {
	if e.ProbeTarget != "" {
		return e.ProbeTarget
	}
	for _, cidr := range e.ACL {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		ip := ipNet.IP.To4()
		if ip == nil {
			continue
		}
		ip = append(net.IP{}, ip...)
		ip[3]++
		if ip[3] == 0 {
			ip[2]++
			ip[3] = 1
		}
		return net.JoinHostPort(ip.String(), "443")
	}
	return ""
}

// ---------------- 工具 ----------------

func (a *App) findEntryLocked(id string) *Entry {
	for _, e := range a.cfg.Entries {
		if e.ID == id {
			return e
		}
	}
	return nil
}

func (a *App) findByOpsLocked(sourcePS string, opsID int, user string) *Entry {
	for _, e := range a.cfg.Entries {
		if e.SourcePS == sourcePS && e.OpsID == opsID && e.ProxyUser == user {
			return e
		}
	}
	return nil
}

// defaultServers 见 model.go：内置主/备地址在设置被清空时兜底

// serverPool 返回设置中的代理服务器地址列表（按顺序回退）；被清空时兜底内置主/备
func (a *App) serverPool() []string {
	a.mu.Lock()
	servers := a.cfg.Settings.Servers
	a.mu.Unlock()
	out := make([]string, 0, len(servers))
	for _, s := range servers {
		if addr := strings.TrimSpace(s.Addr); addr != "" {
			out = append(out, addr)
		}
	}
	if len(out) == 0 {
		for _, s := range defaultServers() {
			out = append(out, s.Addr)
		}
	}
	return out
}

// entryServers 返回入口实际使用的代理服务器列表：指定了某台则只连该台（失败不回退，
// 语义明确便于排查），未指定按设置中的服务器池顺序回退
func (a *App) entryServers(e *Entry) []string {
	if e.Server != "" {
		return []string{e.Server}
	}
	return a.serverPool()
}

// validateProbeTarget 校验内网探测目标：留空合法（自动取首个网段地址:443）；
// 填写时必须是 IPv4:端口。用户最常犯的错误是只填 IP 漏填端口，这里给出明确提示。
func validateProbeTarget(s string) (string, string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ""
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		if net.ParseIP(s) != nil {
			return s, "探测目标缺少端口：请填写 IP:端口（如 " + s + ":443）"
		}
		return s, "探测目标格式错误：应为 IP:端口（如 192.168.10.20:443）"
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() == nil {
		return s, "探测目标的主机部分必须是 IPv4 地址（如 192.168.10.20:443）"
	}
	var p int
	if _, cerr := fmt.Sscanf(port, "%d", &p); cerr != nil || p <= 0 || p > 65535 || fmt.Sprintf("%d", p) != port {
		return s, "探测目标的端口无效：应为 1-65535 的数字（如 192.168.10.20:443）"
	}
	return s, ""
}

// isFullRoute 判断是否为 0.0.0.0/0 全路由网段
func isFullRoute(ipNet *net.IPNet) bool {
	if ipNet.IP.To4() == nil {
		return false
	}
	ones, bits := ipNet.Mask.Size()
	return bits == 32 && ones == 0
}

// containsFullRoute 网段列表中是否包含 0.0.0.0/0（手填场景据此明确报错）
func containsFullRoute(acl []string) bool {
	for _, cidr := range acl {
		if _, ipNet, err := net.ParseCIDR(cidr); err == nil && isFullRoute(ipNet) {
			return true
		}
	}
	return false
}

// sanitizeACL 过滤危险/非法目标网段：
// 0.0.0.0/0 会把本机全部流量（含代理连接自身）导入虚拟网卡导致立即断网，
// 即使 seeinps 的 ACL 配置了 0.0.0.0/0，客户端也一律剔除；同时丢弃非法与 IPv6 网段。
func sanitizeACL(acl []string) []string {
	out := make([]string, 0, len(acl))
	for _, cidr := range acl {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil || ipNet.IP.To4() == nil || isFullRoute(ipNet) {
			continue
		}
		out = append(out, cidr)
	}
	return out
}

// pickTunSubnet 选择 TUN 网段 10.0.X.0/24，尽量避开 ACL 中显式声明的 /24
func pickTunSubnet(acl []string) int {
	explicit := make(map[int]bool)
	for _, cidr := range acl {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		if ones, bits := ipNet.Mask.Size(); bits == 32 && ones == 24 {
			if ip4 := ipNet.IP.To4(); ip4 != nil && ip4[0] == 10 && ip4[1] == 0 {
				explicit[int(ip4[2])] = true
			}
		}
	}
	for x := 1; x < 255; x++ {
		if !explicit[x] {
			return x
		}
	}
	return 1
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---------------- 日志环形缓冲 ----------------

type logRing struct {
	mu    sync.Mutex
	max   int
	lines []string
}

func newLogRing(max int) *logRing { return &logRing{max: max} }

func (r *logRing) append(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, line)
	if len(r.lines) > r.max {
		r.lines = r.lines[len(r.lines)-r.max:]
	}
}

func (r *logRing) tail(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n <= 0 || n > len(r.lines) {
		n = len(r.lines)
	}
	out := make([]string, n)
	copy(out, r.lines[len(r.lines)-n:])
	return out
}

// ---------------- 升级 ----------------

// CheckUpdate 检查官网最新版本（设置页"立即检查"与启动检查共用）。
// 手动检查不受"跳过此版本"影响；启动检查由调用方过滤。
func (a *App) CheckUpdate() UpdateCheckResult {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	info, err := fetchUpdateInfo(ctx)
	if err != nil {
		a.logWarnf("[UPDATE] check failed: %v", err)
		return UpdateCheckResult{Current: version.Version, Message: err.Error()}
	}
	a.mu.Lock()
	if info.Available {
		a.updateInfo = info
		a.installerPath = "" // 有新包，旧的下载结果作废
	}
	ignored := false
	for _, v := range a.cfg.IgnoredVersions {
		if v == info.Version {
			ignored = true
			break
		}
	}
	a.mu.Unlock()
	res := UpdateCheckResult{UpdateInfo: *info, Current: version.Version, Ignored: ignored}
	if !info.Available {
		res.Message = "已是最新版本"
	}
	a.logf("[UPDATE] checked current=%s latest=%s available=%v forced=%v", version.Version, info.Version, info.Available, info.Forced)
	return res
}

// StartDownload 开始后台下载升级包，进度经 "update-progress"、结果经
// "update-download-done" 事件推送。
func (a *App) StartDownload() OpResult {
	a.mu.Lock()
	info := a.updateInfo
	a.mu.Unlock()
	if info == nil {
		return OpResult{Message: "请先检查更新"}
	}
	if !a.downloading.CompareAndSwap(false, true) {
		return OpResult{OK: true, Message: "下载进行中"}
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.updateCancel = cancel
	a.mu.Unlock()
	a.logf("[UPDATE] download start version=%s url=%s", info.Version, info.URL)
	go func() {
		defer a.downloading.Store(false)
		defer cancel()
		path, err := a.downloadUpdate(ctx, info)
		if err != nil {
			if ctx.Err() != nil {
				a.logf("[UPDATE] download canceled")
				wruntime.EventsEmit(a.ctx, "update-download-done", map[string]any{"ok": false, "canceled": true, "message": "已取消下载"})
				return
			}
			a.logErrorf("[UPDATE] download failed: %v", err)
			wruntime.EventsEmit(a.ctx, "update-download-done", map[string]any{"ok": false, "message": err.Error()})
			return
		}
		a.mu.Lock()
		a.installerPath = path
		a.mu.Unlock()
		wruntime.EventsEmit(a.ctx, "update-download-done", map[string]any{"ok": true, "path": path})
	}()
	return OpResult{OK: true, Message: "开始下载"}
}

// CancelDownload 取消进行中的下载
func (a *App) CancelDownload() {
	a.mu.Lock()
	cancel := a.updateCancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// ApplyUpdate 启动静默安装并退出本程序。
// 安装器（/SILENT）会自动结束残留进程、覆盖安装并启动新版本。
func (a *App) ApplyUpdate() OpResult {
	a.mu.Lock()
	path := a.installerPath
	a.mu.Unlock()
	if path == "" {
		return OpResult{Message: "升级包尚未就绪，请先下载"}
	}
	a.logf("[UPDATE] apply installer=%s", path)
	if err := applyUpdateSilently(path); err != nil {
		a.logErrorf("[UPDATE] launch installer failed: %v", err)
		return OpResult{Message: "启动安装程序失败: " + err.Error()}
	}
	go func() {
		time.Sleep(800 * time.Millisecond) // 给安装器拉起留出时间
		a.fullQuit()
	}()
	return OpResult{OK: true, Message: "正在启动升级"}
}

// IgnoreVersion 跳过指定版本（同版本不再自动提醒；手动检查仍会提示）
func (a *App) IgnoreVersion(ver string) OpResult {
	if ver == "" {
		return OpResult{Message: "版本号为空"}
	}
	a.mu.Lock()
	for _, v := range a.cfg.IgnoredVersions {
		if v == ver {
			a.mu.Unlock()
			return OpResult{OK: true, Message: "已跳过"}
		}
	}
	a.cfg.IgnoredVersions = append(a.cfg.IgnoredVersions, ver)
	err := saveConfig(a.cfg)
	a.mu.Unlock()
	if err != nil {
		return OpResult{Message: "保存失败: " + err.Error()}
	}
	a.logf("[UPDATE] version ignored ver=%s", ver)
	return OpResult{OK: true, Message: "已跳过该版本"}
}

// GetIgnoredVersions 当前跳过的版本列表（设置页展示）
func (a *App) GetIgnoredVersions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, len(a.cfg.IgnoredVersions))
	copy(out, a.cfg.IgnoredVersions)
	return out
}

// ClearIgnoredVersions 清除全部跳过记录（恢复提醒）
func (a *App) ClearIgnoredVersions() OpResult {
	a.mu.Lock()
	a.cfg.IgnoredVersions = nil
	err := saveConfig(a.cfg)
	a.mu.Unlock()
	if err != nil {
		return OpResult{Message: "保存失败: " + err.Error()}
	}
	a.logf("[UPDATE] ignored versions cleared")
	return OpResult{OK: true, Message: "已清除"}
}
