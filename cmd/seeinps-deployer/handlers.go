package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
)

// ---- 请求/响应结构 ----

type validateAuthReq struct {
	ServerAddr string `json:"serverAddr"`
	Username   string `json:"username"`
	AuthCode   string `json:"authCode"`
}

// handleValidateAuth 用 seeinpm 用户名+授权码做校验，回显 seeinpm 控制地址
func (a *API) handleValidateAuth(w http.ResponseWriter, r *http.Request) {
	var req validateAuthReq
	if err := decodeJSONBody(r.Body, &req); err != nil {
		writeErr(w, http.StatusBadRequest, 2001, "请求格式错误")
		return
	}
	host, port := parseServerAddr(req.ServerAddr)
	pc := newPMClient(buildPMBase(host, port), req.Username, req.AuthCode)
	serverAddr, err := pc.validate()
	if err != nil {
		writeErr(w, http.StatusUnauthorized, 1001, err.Error())
		return
	}
	writeOK(w, map[string]interface{}{"serverAddr": serverAddr, "validated": true})
}

type versionsReq struct {
	ServerAddr string `json:"serverAddr"`
	Username   string `json:"username"`
	AuthCode   string `json:"authCode"`
	GoOS       string `json:"goos"`
	GoArch     string `json:"goarch"`
}

// handleVersions 返回某平台可用的 seeinps 版本列表
func (a *API) handleVersions(w http.ResponseWriter, r *http.Request) {
	var req versionsReq
	if err := decodeJSONBody(r.Body, &req); err != nil {
		writeErr(w, http.StatusBadRequest, 2001, "请求格式错误")
		return
	}
	host, port := parseServerAddr(req.ServerAddr)
	pc := newPMClient(buildPMBase(host, port), req.Username, req.AuthCode)
	list, err := pc.listReleases(normGOOS(req.GoOS), normGOARCH(req.GoArch))
	if err != nil {
		writeErr(w, http.StatusBadRequest, 1001, err.Error())
		return
	}
	writeOK(w, list)
}

// handleServerConfig 读取/重置 seeinpm 服务器参数（"服务器参数配置"折叠区）
func (a *API) handleServerConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeOK(w, defaultServerConfig())
	case http.MethodPost:
		var req struct {
			ServerAddr string `json:"serverAddr"`
			Port       int    `json:"port"`
		}
		if err := decodeJSONBody(r.Body, &req); err != nil {
			writeErr(w, http.StatusBadRequest, 2001, "请求格式错误")
			return
		}
		writeOK(w, serverConfig{ServerAddr: req.ServerAddr, Port: req.Port, ControlPort: defaultPMControlPort})
	default:
		writeErr(w, http.StatusMethodNotAllowed, 1000, "方法不允许")
	}
}

type installReq struct {
	ServerAddr       string     `json:"serverAddr"`
	Username         string     `json:"username"`
	AuthCode         string     `json:"authCode"`
	Password         string     `json:"password"`         // 初始化网页账号的登录密码
	Target           targetInfo `json:"target"`           // 部署目标
	InstallDir       string     `json:"installDir"`       // Windows 本机安装目录（可选）
	CreateWebMapping bool       `json:"createWebMapping"` // 部署成功后为管理页创建 TCP 映射
	AfterUninstall   bool       `json:"afterUninstall"`   // 本次为“卸载后重试”：若仍检测到已安装则直接报错而非再次提示
}

// handleInstall 执行完整部署流程，以 SSE 流回推进度/结果。
// 事件格式（约定）：
//
//	event: log      data:{level,message}
//	event: confirm  data:{type:"uninstall", ...}   需要用户确认卸载
//	event: success  data:{message,url}             部署成功，url 为 seeinps 网页地址
//	event: error    data:{message}
func (a *API) handleInstall(w http.ResponseWriter, r *http.Request) {
	var req installReq
	if err := decodeJSONBody(r.Body, &req); err != nil {
		writeErr(w, http.StatusBadRequest, 2001, "请求格式错误")
		return
	}
	if req.Username == "" || req.AuthCode == "" {
		writeErr(w, http.StatusBadRequest, 2002, "请先填写 seeinps 用户名与授权码")
		return
	}
	if len(req.Password) < 6 {
		writeErr(w, http.StatusBadRequest, 2002, "初始化密码至少 6 位")
		return
	}
	if err := req.Target.validate(); err != nil {
		writeErr(w, http.StatusBadRequest, 2002, err.Error())
		return
	}

	// 校验通过后切换至 SSE 流式输出
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	f, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, 5000, "环境不支持流式输出")
		return
	}
	stream := &sseWriter{w: w, f: f}
	stream.level("info", "已连接 seeinpm，正在校验授权…")

	host, port := parseServerAddr(req.ServerAddr)
	pc := newPMClient(buildPMBase(host, port), req.Username, req.AuthCode)
	result, err := a.runDeploy(stream, &req, pc)

	// 已安装需要先卸载：后端已发出 confirmUninstall 事件，前端会弹窗让用户选择
	// “卸载并继续”或“退出安装”。这里不做 error 结束（避免误弹错误提示），
	// 仅结束本次 SSE 流，后续由前端决定重发 /install 或终止。
	if errors.Is(err, errNeedUninstall) {
		return
	}
	if err != nil {
		stream.error(err.Error())
		return
	}
	stream.success(result.message, result.url)

	// 部署成功后本工具使命完成：延迟数秒自动退出后台进程。
	// 成功页与“访问 seeinps”按钮不依赖本进程——管理页由已部署的
	// seeinps 服务提供；如需重新部署，重新打开本工具即可（会自动提权）。
	go func() {
		stream.level("info", "部署流程结束，部署工具将在 15 秒后自动退出（不影响 seeinps 服务与管理页）")
		time.Sleep(15 * time.Second)
		os.Exit(0)
	}()
}

// handleTestSSH 测试目标 Linux 服务器的 SSH 连通性（握手+认证），
// 供前端"测试连接"按钮使用；返回 {ok, arch, elapsedMs} 或 {ok:false, error}
func (a *API) handleTestSSH(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Target targetInfo `json:"target"`
	}
	if err := decodeJSONBody(r.Body, &req); err != nil {
		writeErr(w, http.StatusBadRequest, 2001, "请求格式错误")
		return
	}
	if err := req.Target.validate(); err != nil {
		writeOK(w, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	remote := &remoteSSH{
		Host:     req.Target.Host,
		Port:     req.Target.SSHPort,
		User:     req.Target.Username,
		Pass:     req.Target.Password,
		RootPass: req.Target.RootPass,
	}
	start := time.Now()
	if err := remote.connect(); err != nil {
		writeOK(w, map[string]interface{}{"ok": false, "error": friendlySSHError(err)})
		return
	}
	defer remote.close()
	arch := ""
	if out, err := remote.run("uname -m"); err == nil {
		arch = strings.TrimSpace(string(out))
	}
	writeOK(w, map[string]interface{}{
		"ok":        true,
		"arch":      arch,
		"elapsedMs": time.Since(start).Milliseconds(),
	})
}

// sseWriter 封装 SSE 事件写入
type sseWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func (s *sseWriter) event(ev string, payload interface{}) {
	data, _ := json.Marshal(payload)
	fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", ev, data)
	s.f.Flush()
	// 部署流程的每一条事件（操作/告警/错误/成功）都记录到日志；progress 节流事件太密不落盘
	if ev != "progress" {
		appLog("info", "[deploy][%s] %s", ev, string(data))
	}
}
func (s *sseWriter) level(level, msg string) {
	s.event("log", map[string]string{"level": level, "message": msg})
}
func (s *sseWriter) info(msg string) { s.level("info", msg) }
func (s *sseWriter) warn(msg string) { s.level("warn", msg) }
func (s *sseWriter) confirmUninstall(msg string) {
	s.event("confirm", map[string]string{"type": "uninstall", "message": msg})
}
func (s *sseWriter) success(msg, url string) {
	s.event("success", map[string]string{"message": msg, "url": url})
}
func (s *sseWriter) error(msg string) { s.event("error", map[string]string{"message": msg}) }

// handleDeployStatus 检测部署目标当前是否已安装 seeinps（供卸载前提示）
func (a *API) handleDeployStatus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Target targetInfo `json:"target"`
	}
	if err := decodeJSONBody(r.Body, &req); err != nil {
		writeErr(w, http.StatusBadRequest, 2001, "请求格式错误")
		return
	}
	installed, detail := a.detectInstalled(&req.Target)
	writeOK(w, map[string]interface{}{"installed": installed, "detail": detail})
}

// handlePrivilege 返回当前进程是否具备管理员权限，以及是否需要提权。
// Windows 上注册/启动系统服务需要管理员权限；Linux 部署走远端 SSH，无需本机提权。
func (a *API) handlePrivilege(w http.ResponseWriter, r *http.Request) {
	writeOK(w, map[string]interface{}{
		"admin":       isAdmin(),
		"kind":        runtime.GOOS,
		"needElevate": runtime.GOOS == "windows" && !isAdmin(),
	})
}

// handleElevate 触发 Windows UAC 提权重启当前部署工具。
// 调用后前端应提示用户在 UAC 弹窗中选择“是”，并等待新进程打开。
func (a *API) handleElevate(w http.ResponseWriter, r *http.Request) {
	if isAdmin() {
		writeOK(w, map[string]bool{"elevated": true})
		return
	}
	if err := relaunchAsAdmin(os.Args[1:]); err != nil {
		writeErr(w, http.StatusInternalServerError, 5000, err.Error())
		return
	}
	writeOK(w, map[string]bool{"elevated": false})
}

// handleLinks 返回 seeinps 网页访问链接（部署成功后前端跳转）
func (a *API) handleLinks(w http.ResponseWriter, r *http.Request) {
	writeOK(w, map[string]interface{}{"url": seeinpsBendURL()})
}

// seeinpsBendURL 返回 seeinps B 端网页地址（本机回环）
func seeinpsBendURL() string {
	return "http://127.0.0.1:65443"
}

// handleUninstall 卸载：路径与 seeinps-deployer 部署的安装目录绑定，前端传 target
func (a *API) handleUninstall(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Target targetInfo `json:"target"`
	}
	if err := decodeJSONBody(r.Body, &req); err != nil {
		writeErr(w, http.StatusBadRequest, 2001, "请求格式错误")
		return
	}
	if err := a.runUninstall(&req.Target); err != nil {
		writeErr(w, http.StatusInternalServerError, 5000, err.Error())
		return
	}
	writeOK(w, map[string]string{"message": "seeinps 已卸载"})
}
