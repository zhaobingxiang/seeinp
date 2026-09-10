package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/seeinp/seeinp/internal/auth"
	"github.com/seeinp/seeinp/internal/guard"
	"github.com/seeinp/seeinp/internal/gzhttp"
	"github.com/seeinp/seeinp/internal/logx"
	"github.com/seeinp/seeinp/internal/protocol"
	"github.com/seeinp/seeinp/internal/store"
	"github.com/seeinp/seeinp/internal/version"
	"github.com/seeinp/seeinp/internal/webui"
)

const legacyLocalUserPath = "data/local-user.json"

var proxyIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,31}$`)

type apiResponse struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

func writeOK(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apiResponse{Code: 0, Message: "ok", Data: data})
}

func writeErr(w http.ResponseWriter, httpStatus, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	json.NewEncoder(w).Encode(apiResponse{Code: code, Message: message})
}

func writeErrData(w http.ResponseWriter, httpStatus, code int, message string, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	json.NewEncoder(w).Encode(apiResponse{Code: code, Message: message, Data: data})
}

// migrateLegacyLocalUser 导入旧版 data/local-user.json，升级后本地账号不丢失
func (c *Client) migrateLegacyLocalUser() {
	if _, err := c.store.GetLocalUser(); err == nil {
		return
	}
	data, err := os.ReadFile(legacyLocalUserPath)
	if err != nil {
		return
	}
	legacy := struct {
		Username     string `json:"username"`
		PasswordHash string `json:"passwordHash"`
		AuthCode     string `json:"authCode"`
		SeeinpmUser  string `json:"seeinpmUser"`
		Initialized  bool   `json:"initialized"`
	}{}
	if err := json.Unmarshal(data, &legacy); err != nil || !legacy.Initialized {
		return
	}
	if err := c.store.UpsertLocalUser(&store.PSLocalUser{
		Username:     legacy.Username,
		PasswordHash: legacy.PasswordHash,
		SeeinpmUser:  legacy.SeeinpmUser,
		AuthCode:     legacy.AuthCode,
	}); err != nil {
		logx.Warnf("[INIT] migrate legacy user error: %v", err)
		return
	}
	os.Rename(legacyLocalUserPath, legacyLocalUserPath+".bak")
	logx.Infof("[INIT] migrated legacy local user: user=%s", legacy.Username)
}

func (c *Client) startLocalServer() error {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/v1/auth/status", c.handleAuthStatus)
	mux.HandleFunc("POST /api/v1/auth/init", c.handleAuthInit)
	mux.HandleFunc("POST /api/v1/auth/login", c.handleAuthLogin)
	mux.HandleFunc("GET /api/v1/auth/captcha", c.handleAuthCaptcha)
	mux.HandleFunc("POST /api/v1/auth/rebind", c.requireAuth(c.handleAuthRebind))
	mux.HandleFunc("GET /health", c.handleHealth)

	mux.HandleFunc("GET /api/v1/proxies", c.requireAuth(c.handleProxyList))
	mux.HandleFunc("POST /api/v1/proxies", c.requireAuth(c.handleProxyCreate))
	mux.HandleFunc("PATCH /api/v1/proxies/{id}", c.requireAuth(c.handleProxyUpdate))
	mux.HandleFunc("DELETE /api/v1/proxies/{id}", c.requireAuth(c.handleProxyDelete))
	mux.HandleFunc("GET /api/v1/status", c.requireAuth(c.handleStatus))
	mux.HandleFunc("GET /api/v1/stats", c.requireAuth(c.handleStats))
	mux.HandleFunc("GET /api/v1/quota", c.requireAuth(c.handleQuota))
	mux.HandleFunc("GET /api/v1/audit-logs", c.requireAuth(c.handleAuditLogs))
	mux.HandleFunc("GET /api/v1/audit-logs/export", c.requireAuth(c.handleAuditLogsExport))
	mux.HandleFunc("GET /api/v1/logs", c.requireAuth(c.handleListLogFiles))
	mux.HandleFunc("GET /api/v1/logs/content", c.requireAuth(c.handleLogFileContent))

	// 版本管理（B 端自主升级）：状态 / 自上传 / 从 seeinpm 查询版本 / 从 seeinpm 拉取升级
	mux.HandleFunc("GET /api/v1/self-upgrade/status", c.requireAuth(c.handleSelfUpgradeStatus))
	mux.HandleFunc("POST /api/v1/self-upgrade", c.requireAuth(c.handleSelfUpgrade))
	mux.HandleFunc("GET /api/v1/pm-versions", c.requireAuth(c.handlePMVersions))
	mux.HandleFunc("POST /api/v1/pm-upgrade", c.requireAuth(c.handlePMUpgrade))

	// 系统日志：运行时查询/修改日志级别（持久化到 toml，异步上报审计 log_level_update）
	mux.HandleFunc("GET /api/v1/logging", c.requireAuth(c.handleLoggingGet))
	mux.HandleFunc("PUT /api/v1/logging", c.requireAuth(c.handleLoggingSet))

	// 前端内嵌于二进制（internal/webui），与后端版本严格一致，随一键升级同步更新
	mux.HandleFunc("/", webui.SPAHandler(webui.PS()))
	logx.Infof("[WEB] serving embedded web: version=%s", version.Version)

	addr := c.config.Local.BendAddr

	server := &http.Server{
		Addr: addr,
		// gzip：前端产物与 API 响应压缩（窄带链路下体积 -60% 以上）
		// 外层再套访问日志中间件：所有 4xx/5xx 落盘，panic 兜底
		Handler: c.accessLog(gzhttp.Handler(mux)),
		// 超时按慢链路适配：B 端要承载 200MB 升级包上传（Read）与文件/日志下载（Write）
		ReadTimeout:  30 * time.Minute,
		WriteTimeout: 10 * time.Minute,
		IdleTimeout:  120 * time.Second,
	}

	// 绑定失败（如端口被残留实例占用）不退出进程：代理与控制通道不受影响，
	// 每 5s 重试绑定，端口释放后管理页自动恢复
	for {
		logx.Infof("[WEB] listening on %s", addr)
		err := server.ListenAndServe()
		if err == http.ErrServerClosed {
			return err
		}
		logx.Warnf("[WEB] listen failed, retry in 5s: %v", err)
		time.Sleep(5 * time.Second)
	}
}

type ctxKey string

const operatorKey ctxKey = "operator"

// ---------------- HTTP 访问/错误日志中间件 ----------------
//
// 级别约定（与 seeinpm 一致）：5xx -> ERROR，4xx -> WARN，其余 -> DEBUG。
// 此前所有 writeErr 只回前端、不落盘，用户报"创建代理报错"时服务端零线索。

// accessInfoKey 承载本次请求的访问日志附加信息（指针，便于内层处理器回填）。
const accessInfoKey ctxKey = "access-info"

// accessInfo 由外层中间件创建、内层 requireAuth 回填操作者。
type accessInfo struct{ operator string }

// statusRecorder 捕获响应状态码与写出字节数。
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Flush / Hijack / Unwrap 透传，避免破坏 gzip 中间件、升级包上传与大文件下载。
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := r.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("hijack not supported")
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// ReadFrom 透传底层 writer 的 ReadFrom（sendfile），否则大文件下载会退化为纯用户态拷贝。
// 注意必须委托给 r.ResponseWriter 而不是自己，避免 io.Copy 回调自身造成无限递归。
func (r *statusRecorder) ReadFrom(src io.Reader) (int64, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if rf, ok := r.ResponseWriter.(io.ReaderFrom); ok {
		n, err := rf.ReadFrom(src)
		r.bytes += int(n)
		return n, err
	}
	n, err := io.Copy(r.ResponseWriter, src)
	r.bytes += int(n)
	return n, err
}

// skipAccessLog 静态资源与健康检查不进访问日志，避免刷屏。
func skipAccessLog(path string) bool {
	return path == "/health" || !strings.HasPrefix(path, "/api/")
}

// accessLog 统一 HTTP 访问/错误日志中间件：按状态码分级记录并兜住 handler panic。
func (c *Client) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if skipAccessLog(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		info := &accessInfo{}
		rec := &statusRecorder{ResponseWriter: w}
		start := time.Now()
		r = r.WithContext(context.WithValue(r.Context(), accessInfoKey, info))

		defer func() {
			if rv := recover(); rv != nil {
				if rec.status == 0 {
					rec.status = http.StatusInternalServerError
					rec.ResponseWriter.WriteHeader(rec.status)
					_, _ = rec.ResponseWriter.Write([]byte(`{"code":5000,"message":"internal error"}`))
				}
				logx.Errorf("[HTTP] panic recovered method=%s path=%s ip=%s err=%v\n%s",
					r.Method, r.URL.Path, clientIP(r), rv, debug.Stack())
			}
			dur := time.Since(start)
			op := info.operator
			if op == "" {
				op = "-"
			}
			switch {
			case rec.status >= 500:
				logx.Errorf("[HTTP] %s %s status=%d dur=%s bytes=%d ip=%s operator=%s",
					r.Method, r.URL.Path, rec.status, dur, rec.bytes, clientIP(r), op)
			case rec.status >= 400:
				logx.Warnf("[HTTP] %s %s status=%d dur=%s bytes=%d ip=%s operator=%s",
					r.Method, r.URL.Path, rec.status, dur, rec.bytes, clientIP(r), op)
			default:
				logx.Debugf("[HTTP] %s %s status=%d dur=%s bytes=%d ip=%s operator=%s",
					r.Method, r.URL.Path, rec.status, dur, rec.bytes, clientIP(r), op)
			}
		}()

		next.ServeHTTP(rec, r)
	})
}

// requireAuth 校验 JWT 并把操作者用户名放入 context，供审计埋点取用
func (c *Client) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" {
			writeErr(w, http.StatusUnauthorized, 1003, "未登录")
			return
		}
		claims, err := c.jwt.ValidateToken(token)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, 1003, "登录已过期，请重新登录")
			return
		}
		// 回填操作者给外层访问日志（context 里是指针，外层可读到）
		if info, ok := r.Context().Value(accessInfoKey).(*accessInfo); ok {
			info.operator = claims.Username
		}
		next(w, r.WithContext(context.WithValue(r.Context(), operatorKey, claims.Username)))
	}
}

// operatorFrom 从 context 取当前操作者（审计埋点用）
func operatorFrom(r *http.Request) string {
	if v, ok := r.Context().Value(operatorKey).(string); ok {
		return v
	}
	return "unknown"
}

// audit 写一条本地操作审计记录（自动附加来源 IP）并触发上报 seeinpm（混合架构）。
// best-effort：写失败记 ERROR（不再静默），业务继续；需要 fail-closed 的关键动作用 auditBefore。
func (c *Client) audit(r *http.Request, action, target, detail string) {
	c.auditRecord(operatorFrom(r), action, target, withIP(detail, clientIP(r)))
}

// auditPlain 无 JWT 场景（登录/初始化）的审计记录，操作者取自请求参数（detail 需自带 ip=）
func (c *Client) auditPlain(username, action, detail string, r *http.Request) {
	c.auditRecord(username, action, "", detail)
}

// auditBefore 先落库审计、再执行关键操作（fail-closed）。
// 仅在配置 [audit] fail_closed = true 时生效：审计写不进去就拒绝执行，
// 避免出现"用户已删除但审计表里查不到"这种不可追溯的状态。返回 false 表示已写出响应、调用方应直接返回。
func (c *Client) auditBefore(w http.ResponseWriter, r *http.Request, action, target, detail string) bool {
	if !c.config.Audit.FailClosed {
		c.audit(r, action, target, detail)
		return true
	}
	if err := c.auditRecord(operatorFrom(r), action, target, withIP(detail, clientIP(r))); err != nil {
		writeErr(w, http.StatusInternalServerError, 5000, "审计日志写入失败，已拒绝该操作（fail_closed 已开启）")
		return false
	}
	return true
}

// withIP 把来源 IP 追加到 detail 末尾（保持既有 "k=v; k=v" 风格）。
func withIP(detail, ip string) string {
	if detail != "" {
		detail += "; "
	}
	return detail + "ip=" + ip
}

// clientIP 从请求取客户端 IP（去掉端口）
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// auditLoginFailed 记录登录失败，并按 IP 节流：
// guard 的锁定是按 username 的，攻击者换 username 喷洒可无限写审计表；同一 IP 60s 内只记一条。
// 返回是否真的落库（供调用方在需要时提示）。
func (c *Client) auditLoginFailed(username, ip string) {
	now := time.Now()
	c.loginFailMu.Lock()
	if last, ok := c.loginFailAt[ip]; ok && now.Sub(last) < time.Minute {
		c.loginFailMu.Unlock()
		return
	}
	c.loginFailAt[ip] = now
	if len(c.loginFailAt) > 1024 { // 简单清理，防长期运行下无界增长
		for k, t := range c.loginFailAt {
			if now.Sub(t) > time.Minute {
				delete(c.loginFailAt, k)
			}
		}
	}
	c.loginFailMu.Unlock()
	c.auditRecord(username, "login_failed", "", "ip="+ip)
}

// auditRecord 落库本地审计（synced=0），随后 pokes 补传协程把未同步记录推给 seeinpm。
// 返回写库错误，供 fail-closed 场景判定。
func (c *Client) auditRecord(username, action, target, detail string) error {
	id, err := c.store.InsertAuditLog(username, action, target, detail)
	if err != nil {
		// 审计写入失败必须显式可见（此前只 Warnf，且业务照常成功返回）
		logx.Errorf("[AUDIT] insert failed action=%s user=%s err=%v", action, username, err)
		return err
	}
	logx.Debugf("[AUDIT] recorded id=%d action=%s user=%s target=%q", id, action, username, target)
	c.pokeAuditSync()
	return nil
}

// pokeAuditSync 非阻塞唤醒补传协程。
func (c *Client) pokeAuditSync() {
	select {
	case c.auditPoke <- struct{}{}:
	default:
	}
}

// startAuditSyncLoop 启动（幂等）审计补传协程：收到 poke 或每 60s 兜底推送一次未同步记录。
func (c *Client) startAuditSyncLoop() {
	c.auditLoopOnce.Do(func() {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					logx.Errorf("[PANIC] audit sync loop panicked: %v\n%s", r, debug.Stack())
				}
			}()
			for {
				select {
				case <-c.auditPoke:
				case <-time.After(time.Minute):
				case <-rootDone(c):
					return
				}
				c.flushAudit()
			}
		}()
	})
}

// rootDone 返回进程退出信号；rootCtx 未注入时返回一个永不触发的 channel。
func rootDone(c *Client) <-chan struct{} {
	if c.rootCtx == nil {
		return make(chan struct{})
	}
	return c.rootCtx.Done()
}

// flushAudit 把本地未同步的审计批量推给 seeinpm；成功后标记 synced=1。
// PM 侧以 (source, ext_id) 唯一索引幂等去重，因此重放不会产生重复记录。
func (c *Client) flushAudit() {
	link := c.getLink()
	if link == nil {
		return // 离线：记录留在本地，连接恢复后由 setLink 再次触发
	}
	for {
		rows, err := c.store.PendingAuditLogs(200)
		if err != nil {
			logx.Errorf("[AUDIT] read pending audit failed: %v", err)
			return
		}
		if len(rows) == 0 {
			return
		}
		items := make([]protocol.AuditSyncItem, 0, len(rows))
		ids := make([]int64, 0, len(rows))
		for _, r := range rows {
			items = append(items, protocol.AuditSyncItem{
				LocalID: r.ID, Username: r.Username, Action: r.Action,
				Target: r.Target, Detail: r.Detail, CreatedAt: r.CreatedAt,
			})
			ids = append(ids, r.ID)
		}
		msg := protocol.NewMessage(protocol.TypeAuditSync, &protocol.AuditSyncData{Items: items})
		if err := link.send(msg); err != nil {
			logx.Warnf("[AUDIT] sync %d records to seeinpm failed, will retry after reconnect: %v", len(ids), err)
			return
		}
		if err := c.store.MarkAuditSynced(ids); err != nil {
			logx.Errorf("[AUDIT] mark synced failed (records may be re-sent, PM dedups by ext_id): %v", err)
			return
		}
		logx.Infof("[AUDIT] synced %d audit records to seeinpm", len(ids))
		if len(rows) < 200 {
			return
		}
	}
}

func (c *Client) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	c.proxiesMu.RLock()
	n := len(c.proxies)
	c.proxiesMu.RUnlock()
	fmt.Fprintf(w, `{"status":"ok","version":"%s","proxies":%d}`, version.Version, n)
}

func (c *Client) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	u, err := c.store.GetLocalUser()
	if err != nil {
		writeOK(w, map[string]interface{}{"initialized": false, "authCodeReset": c.revoked.Load()})
		return
	}
	writeOK(w, map[string]interface{}{
		"initialized":   true,
		"username":      u.Username,
		"authCodeReset": c.revoked.Load(),
	})
}

func (c *Client) handleAuthInit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		AuthCode string `json:"authCode"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, 2001, "请求格式错误")
		return
	}
	if _, err := c.store.GetLocalUser(); err == nil {
		writeErr(w, http.StatusConflict, 3001, "已初始化，请直接登录")
		return
	}
	if len(req.Username) < 3 || len(req.Password) < 6 {
		writeErr(w, http.StatusBadRequest, 2002, "用户名至少3位，密码至少6位")
		return
	}
	if req.AuthCode == "" {
		writeErr(w, http.StatusBadRequest, 2002, "请输入 seeinpm 下发的授权码")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, 5000, "内部错误")
		return
	}
	if err := c.store.UpsertLocalUser(&store.PSLocalUser{
		Username:     req.Username,
		PasswordHash: hash,
		SeeinpmUser:  req.Username,
		AuthCode:     req.AuthCode,
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, 5000, "保存账号失败")
		return
	}
	logx.Infof("[INIT] local user initialized: user=%s", req.Username)
	c.auditPlain(req.Username, "auth_init", "ip="+r.RemoteAddr, r)
	token, _ := c.jwt.GenerateAccessToken(req.Username, "ps")
	// 授权码可能是在离线/被吊销状态下更新的：立即重启控制循环用新凭证注册
	c.restartControl()
	writeOK(w, map[string]interface{}{"token": token})
}

// handleAuthRebind 重新绑定授权码：seeinpm 侧重置授权码后，B端输入新码恢复连接；
// 只更新凭证不动代理数据，重连成功后端口复用自动恢复全部代理
func (c *Client) handleAuthRebind(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AuthCode string `json:"authCode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, 2001, "请求格式错误")
		return
	}
	if strings.TrimSpace(req.AuthCode) == "" {
		writeErr(w, http.StatusBadRequest, 2002, "请输入新授权码")
		return
	}
	if _, err := c.store.GetLocalUser(); err != nil {
		writeErr(w, http.StatusConflict, 3001, "本地账号未初始化，请先完成初始化")
		return
	}
	if err := c.store.UpdateLocalAuthCode(strings.TrimSpace(req.AuthCode)); err != nil {
		writeErr(w, http.StatusInternalServerError, 5000, "保存授权码失败")
		return
	}
	logx.Infof("[AUTH] auth code updated via web console; restarting control loop")
	c.audit(r, "auth_rebind", "", "ip="+r.RemoteAddr)
	c.restartControl()
	writeOK(w, map[string]interface{}{"restarted": true})
}

func (c *Client) handleAuthCaptcha(w http.ResponseWriter, r *http.Request) {
	id, image := c.guard.CreateCaptcha()
	writeOK(w, map[string]interface{}{"captcha_id": id, "image_base64": image})
}

func (c *Client) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username    string `json:"username"`
		Password    string `json:"password"`
		CaptchaID   string `json:"captcha_id"`
		CaptchaText string `json:"captcha_text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, 2001, "请求格式错误")
		return
	}

	// 先判锁定：锁定期间即使密码/验证码正确也拒绝
	if locked, remaining, failed := c.guard.Locked(req.Username); locked {
		c.auditPlain(req.Username, "login_locked", "ip="+clientIP(r), r)
		writeErrData(w, http.StatusForbidden, 1103, "账号已锁定", map[string]interface{}{
			"lock_remaining": remaining,
			"failed_count":   failed,
		})
		return
	}
	if c.guard.NeedsCaptcha(req.Username) {
		if !c.guard.VerifyCaptcha(req.CaptchaID, req.CaptchaText) {
			writeErr(w, http.StatusUnauthorized, 1104, "验证码错误或已失效，请重新输入")
			return
		}
	}

	u, err := c.store.GetLocalUser()
	if err != nil {
		writeErr(w, http.StatusBadRequest, 3002, "尚未初始化")
		return
	}
	if req.Username != u.Username || !auth.CheckPassword(req.Password, u.PasswordHash) {
		failed, lockSec := c.guard.OnFailure(req.Username)
		c.auditLoginFailed(req.Username, clientIP(r)) // 按 IP 节流，防喷洒刷表
		if lockSec > 0 {
			writeErrData(w, http.StatusForbidden, 1103, "密码错误次数过多，账号已锁定", map[string]interface{}{
				"lock_remaining": lockSec,
				"failed_count":   failed,
			})
			return
		}
		writeErrData(w, http.StatusUnauthorized, 1001, "用户名或密码错误", map[string]interface{}{
			"need_captcha": failed >= guard.CaptchaThreshold,
			"failed_count": failed,
		})
		return
	}

	c.guard.Reset(req.Username)
	token, err := c.jwt.GenerateAccessToken(u.Username, "ps")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, 5000, "生成令牌失败")
		return
	}
	c.auditPlain(u.Username, "login", "ip="+clientIP(r), r)
	logx.Infof("[AUTH] login ok user=%s ip=%s", u.Username, clientIP(r))
	writeOK(w, map[string]interface{}{"token": token})
}

func proxyJSON(p *Proxy) map[string]interface{} {
	m := map[string]interface{}{
		"id":          p.ID,
		"type":        p.Type,
		"localAddr":   p.LocalAddr,
		"localPort":   p.LocalPort,
		"forwardPort": p.ForwardPort,
	}
	if p.Type == "ops_http" {
		m["opsId"] = p.ForwardPort // 运维ID = 对外端口（F-P4）
		m["proxyUsername"] = p.ProxyUsername
		acl := DefaultOpsACL
		if p.ACLRaw != "" {
			json.Unmarshal([]byte(p.ACLRaw), &acl)
		}
		m["acl"] = acl
	}
	return m
}

func (c *Client) handleProxyList(w http.ResponseWriter, r *http.Request) {
	dbList, err := c.store.ListProxies()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, 5000, "读取代理列表失败")
		return
	}
	c.proxiesMu.RLock()
	list := make([]map[string]interface{}, 0, len(dbList))
	for _, dp := range dbList {
		if p, ok := c.proxies[dp.ProxyID]; ok {
			list = append(list, proxyJSON(p))
		}
	}
	c.proxiesMu.RUnlock()
	writeOK(w, list)
}

type proxyReq struct {
	ID            string   `json:"id"`
	Type          string   `json:"type"`
	LocalAddr     string   `json:"localAddr"`
	LocalPort     int      `json:"localPort"`
	ProxyUsername string   `json:"proxyUsername"`
	ProxyPassword string   `json:"proxyPassword"`
	ACL           []string `json:"acl"`
}

// validateProxyPassword 代理密码强度：≥10 位，含大小写/数字/特殊字符（§10.3）
func validateProxyPassword(pwd string) error {
	if len(pwd) < 10 {
		return fmt.Errorf("代理密码至少 10 位")
	}
	var hasUpper, hasLower, hasDigit, hasSpecial bool
	for _, r := range pwd {
		switch {
		case unicode.IsUpper(r):
			hasUpper = true
		case unicode.IsLower(r):
			hasLower = true
		case unicode.IsDigit(r):
			hasDigit = true
		default:
			hasSpecial = true
		}
	}
	if !hasUpper || !hasLower || !hasDigit || !hasSpecial {
		return fmt.Errorf("代理密码需包含大写、小写、数字和特殊字符")
	}
	return nil
}

// validateACL 校验 ACL 网段列表（空则用默认内网段）
func validateACL(acl []string) ([]string, error) {
	if len(acl) == 0 {
		return DefaultOpsACL, nil
	}
	for _, cidr := range acl {
		cidr = strings.TrimSpace(cidr)
		if cidr == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return nil, fmt.Errorf("ACL 网段 %q 格式错误", cidr)
		}
	}
	return acl, nil
}

func decodeProxyReq(r *http.Request) (*proxyReq, error) {
	req := &proxyReq{}
	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		return nil, fmt.Errorf("请求格式错误")
	}
	req.ID = strings.TrimSpace(req.ID)
	req.Type = strings.ToLower(strings.TrimSpace(req.Type))
	req.LocalAddr = strings.TrimSpace(req.LocalAddr)
	req.ProxyUsername = strings.TrimSpace(req.ProxyUsername)
	if req.LocalAddr == "" {
		req.LocalAddr = "127.0.0.1"
	}
	switch req.Type {
	case "tcp", "udp":
		if _, err := validatePort(req.LocalPort); err != nil {
			return nil, err
		}
	case "ops_http":
		if req.ProxyUsername == "" {
			return nil, fmt.Errorf("运维代理必须设置代理账号")
		}
		acl, err := validateACL(req.ACL)
		if err != nil {
			return nil, err
		}
		req.ACL = acl
	case "ops_socks":
		return nil, fmt.Errorf("SOCKS5 运维代理为二期功能")
	default:
		return nil, fmt.Errorf("代理类型仅支持 tcp / udp / ops_http")
	}
	return req, nil
}

// buildStoreProxy 将请求转为存储对象；password 为空时沿用旧哈希（编辑场景）
func (c *Client) buildStoreProxy(req *proxyReq, old *store.PSProxy) (*store.PSProxy, error) {
	p := &store.PSProxy{
		ProxyID:       req.ID,
		Type:          req.Type,
		LocalAddr:     req.LocalAddr,
		LocalPort:     req.LocalPort,
		ProxyUsername: req.ProxyUsername,
		Status:        1,
	}
	if req.Type == "ops_http" {
		// ops 代理无本地映射目标，清零避免残留表单值误导显示
		p.LocalAddr = ""
		p.LocalPort = 0
		aclJSON, _ := json.Marshal(req.ACL)
		p.ACL = string(aclJSON)
		if req.ProxyPassword != "" {
			if err := validateProxyPassword(req.ProxyPassword); err != nil {
				return nil, err
			}
			hash, err := auth.HashPassword(req.ProxyPassword)
			if err != nil {
				return nil, fmt.Errorf("密码处理失败")
			}
			p.ProxyPassword = hash
		} else if old != nil && old.ProxyPassword != "" {
			p.ProxyPassword = old.ProxyPassword // 编辑时留空=不改密码
		} else {
			return nil, fmt.Errorf("运维代理必须设置代理密码")
		}
	}
	return p, nil
}

func mustACLJSON(acl []string) string {
	if len(acl) == 0 {
		acl = DefaultOpsACL
	}
	b, _ := json.Marshal(acl)
	return string(b)
}

func validatePort(port int) (int, error) {
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("本地端口必须在 1-65535 之间")
	}
	return port, nil
}

func (c *Client) handleProxyCreate(w http.ResponseWriter, r *http.Request) {
	req, err := decodeProxyReq(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, 2002, err.Error())
		return
	}
	if !proxyIDPattern.MatchString(req.ID) {
		writeErr(w, http.StatusBadRequest, 2002, "代理名称仅支持字母、数字、下划线、连字符（1-32位，字母或数字开头）")
		return
	}
	sp, err := c.buildStoreProxy(req, nil)
	if err != nil {
		writeErr(w, http.StatusBadRequest, 2002, err.Error())
		return
	}

	p := &Proxy{
		ID:                req.ID,
		Type:              req.Type,
		LocalAddr:         req.LocalAddr,
		LocalPort:         req.LocalPort,
		ProxyUsername:     sp.ProxyUsername,
		ProxyPasswordHash: sp.ProxyPassword,
		ACLRaw:            sp.ACL,
	}

	c.proxiesMu.Lock()
	if _, exists := c.proxies[req.ID]; exists {
		c.proxiesMu.Unlock()
		writeErr(w, http.StatusConflict, 3001, "代理名称已存在")
		return
	}
	if req.Type == "ops_http" {
		nets, err := parseOpsACL(sp.ACL)
		if err == nil {
			p.aclNets = nets
		}
	}
	c.proxies[req.ID] = p
	c.proxiesMu.Unlock()

	if err := c.store.CreateProxy(sp); err != nil {
		c.proxiesMu.Lock()
		delete(c.proxies, req.ID)
		c.proxiesMu.Unlock()
		writeErr(w, http.StatusInternalServerError, 5000, "保存代理失败")
		return
	}

	// 已连接 seeinpm 时立即申请转发端口；失败则回滚本次添加
	if link := c.getLink(); link != nil {
		if _, err := c.allocProxy(link, p); err != nil {
			c.store.DeleteProxy(req.ID)
			c.proxiesMu.Lock()
			delete(c.proxies, req.ID)
			c.proxiesMu.Unlock()
			if errors.Is(err, errPortPoolExhausted) || errors.Is(err, errPortQuotaExhausted) {
				writeErr(w, http.StatusBadGateway, 5002, err.Error())
			} else {
				writeErr(w, http.StatusBadGateway, 5002, "端口分配失败，代理未保存: "+err.Error())
			}
			return
		}
	}
	c.audit(r, "proxy_create", req.ID, fmt.Sprintf("%s -> %s:%d", req.Type, req.LocalAddr, req.LocalPort))
	writeOK(w, proxyJSON(p))
}

func (c *Client) handleProxyUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	req, err := decodeProxyReq(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, 2002, err.Error())
		return
	}

	c.proxiesMu.Lock()
	p, ok := c.proxies[id]
	if !ok {
		c.proxiesMu.Unlock()
		writeErr(w, http.StatusNotFound, 2004, "代理不存在")
		return
	}
	if req.Type != p.Type {
		c.proxiesMu.Unlock()
		writeErr(w, http.StatusBadRequest, 2002, "代理类型不可修改，请删除后重建")
		return
	}
	old := p.snapshot()
	p.LocalAddr = req.LocalAddr
	p.LocalPort = req.LocalPort
	p.ProxyUsername = req.ProxyUsername
	if req.Type == "ops_http" {
		p.ACLRaw = mustACLJSON(req.ACL)
		if nets, err := parseOpsACL(p.ACLRaw); err == nil {
			p.aclNets = nets
		}
	}
	c.proxiesMu.Unlock()

	oldDB, _ := c.store.GetProxy(id)
	sp, err := c.buildStoreProxy(req, oldDB)
	if err != nil {
		c.proxiesMu.Lock()
		if p, ok := c.proxies[id]; ok {
			p.restore(old)
		}
		c.proxiesMu.Unlock()
		writeErr(w, http.StatusBadRequest, 2002, err.Error())
		return
	}
	sp.ProxyID = id
	if p.Type == "ops_http" {
		p.ProxyPasswordHash = sp.ProxyPassword
	}

	if err := c.store.UpdateProxyMeta(sp); err != nil {
		c.proxiesMu.Lock()
		if p, ok := c.proxies[id]; ok {
			p.restore(old)
		}
		c.proxiesMu.Unlock()
		writeErr(w, http.StatusInternalServerError, 5000, "更新代理失败")
		return
	}
	c.audit(r, "proxy_update", id, fmt.Sprintf("from %s:%d to %s:%d", old.LocalAddr, old.LocalPort, req.LocalAddr, req.LocalPort))
	writeOK(w, proxyJSON(p))
}

func (c *Client) handleProxyDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	c.proxiesMu.Lock()
	p, ok := c.proxies[id]
	if !ok {
		c.proxiesMu.Unlock()
		writeErr(w, http.StatusNotFound, 2004, "代理不存在")
		return
	}
	c.proxiesMu.Unlock()

	detail := ""
	if p.ForwardPort > 0 {
		detail = fmt.Sprintf("type=%s forward_port=%d", p.Type, p.ForwardPort)
	}
	// 删除不可逆：fail_closed 开启时先落审计，写不进去就拒绝执行
	if !c.auditBefore(w, r, "proxy_delete", id, detail) {
		return
	}

	c.proxiesMu.Lock()
	delete(c.proxies, id)
	c.proxiesMu.Unlock()

	if link := c.getLink(); link != nil {
		c.releaseProxy(link, p)
	}
	if err := c.store.DeleteProxy(id); err != nil {
		logx.Errorf("[PROXY] delete failed: proxy=%s err=%v", id, err)
		writeErr(w, http.StatusInternalServerError, 5000, "删除代理失败")
		return
	}
	logx.Infof("[PROXY] deleted: proxy=%s", id)
	writeOK(w, nil)
}

func (c *Client) handleStatus(w http.ResponseWriter, r *http.Request) {
	status := "disconnected"
	if c.getLink() != nil {
		status = "connected"
	}
	username := c.config.Auth.Username
	if u, err := c.store.GetLocalUser(); err == nil && u.SeeinpmUser != "" {
		username = u.SeeinpmUser
	}
	c.linkMu.Lock()
	sessionID := c.sessionID
	c.linkMu.Unlock()
	writeOK(w, map[string]interface{}{
		"status":        status,
		"sessionId":     sessionID,
		"username":      username,
		"serverAddr":    c.config.Server.ServerAddr,
		"lastPing":      c.lastPing.Load(),
		"authCodeReset": c.revoked.Load(),
		"version":       version.Version,
		"platform":      runtime.GOOS + "/" + runtime.GOARCH,
	})
}

func (c *Client) handleStats(w http.ResponseWriter, r *http.Request) {
	c.proxiesMu.RLock()
	n := len(c.proxies)
	c.proxiesMu.RUnlock()
	writeOK(w, map[string]interface{}{"connections": 0, "traffic": 0, "proxies": n})
}

// handleQuota 返回 A 端同步的周期流量配额状态缓存（进度/重置日期/超额标志）。
// 数据随 B 端心跳响应（约 10s）与 A 端状态跃迁推送刷新；从未同步过时 enabled=false。
func (c *Client) handleQuota(w http.ResponseWriter, r *http.Request) {
	q, syncedAt := c.quotaSnapshot()
	writeOK(w, map[string]interface{}{
		"enabled": q.Enabled, "period": q.Period, "used": q.Used, "limit": q.Limit,
		"periodStart": q.PeriodStart, "periodEnd": q.PeriodEnd, "exceeded": q.Exceeded,
		"syncedAt": syncedAt,
	})
}

// handleAuditLogs 分页查询本地操作审计日志（?username=&action=&keyword=&start_time=&end_time=&page=&page_size=）
func (c *Client) handleAuditLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	pageSize, _ := strconv.Atoi(q.Get("page_size"))
	start, _ := strconv.ParseInt(q.Get("start_time"), 10, 64)
	end, _ := strconv.ParseInt(q.Get("end_time"), 10, 64)
	list, total, err := c.store.ListAuditLogs(store.PSAuditFilter{
		Username:  q.Get("username"),
		Action:    q.Get("action"),
		Keyword:   q.Get("keyword"),
		StartTime: start,
		EndTime:   end,
		Page:      page,
		PageSize:  pageSize,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, 5000, "查询审计日志失败")
		return
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 20
	}
	type auditResp struct {
		ID        int64  `json:"id"`
		Username  string `json:"username"`
		Action    string `json:"action"`
		Target    string `json:"target"`
		Detail    string `json:"detail"`
		CreatedAt int64  `json:"createdAt"`
	}
	items := make([]auditResp, 0, len(list))
	for _, a := range list {
		items = append(items, auditResp{ID: a.ID, Username: a.Username, Action: a.Action, Target: a.Target, Detail: a.Detail, CreatedAt: a.CreatedAt})
	}
	writeOK(w, map[string]interface{}{"list": items, "total": total, "page": page, "pageSize": pageSize})
}

// handleAuditLogsExport GET /api/v1/audit-logs/export：按筛选条件导出完整 CSV。
// 独立于分页接口的原因：ListAuditLogs 会把 PageSize 压到 200，
// 前端导出曾传 page_size=10000 被静默重置为 20 —— 导出的 CSV 永远只有 20 行。
// 导出行为本身也记入审计（敏感数据外带需留痕）。
func (c *Client) handleAuditLogsExport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	start, _ := strconv.ParseInt(q.Get("start_time"), 10, 64)
	end, _ := strconv.ParseInt(q.Get("end_time"), 10, 64)
	f := store.PSAuditFilter{
		Username:  q.Get("username"),
		Action:    q.Get("action"),
		Keyword:   q.Get("keyword"),
		StartTime: start,
		EndTime:   end,
	}
	list, err := c.store.ExportAuditLogs(f, 200000)
	if err != nil {
		logx.Errorf("[AUDIT] export failed: %v", err)
		writeErr(w, http.StatusInternalServerError, 5000, "导出失败")
		return
	}
	c.audit(r, "audit_log_export", fmt.Sprintf("%d rows", len(list)),
		fmt.Sprintf("action=%s keyword=%s start=%d end=%d", f.Action, f.Keyword, f.StartTime, f.EndTime))

	name := "audit-logs-" + time.Now().Format("20060102-150405") + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
	_, _ = w.Write([]byte("\ufeff")) // BOM：Excel 直接打开不乱码
	bw := bufio.NewWriter(w)
	_, _ = bw.WriteString("时间,操作者,动作,对象,详情\n")
	for _, a := range list {
		row := []string{
			time.Unix(a.CreatedAt, 0).Format("2006-01-02 15:04:05"),
			a.Username, a.Action, a.Target, a.Detail,
		}
		for i, col := range row {
			if i > 0 {
				_ = bw.WriteByte(',')
			}
			_, _ = bw.WriteString(csvQuote(col))
		}
		_ = bw.WriteByte('\n')
	}
	_ = bw.Flush()
	logx.Infof("[AUDIT] exported %d rows to CSV by %s", len(list), operatorFrom(r))
}

// csvQuote 按 RFC 4180 转义 CSV 字段（含逗号/引号/换行时加引号并把 " 翻倍）。
func csvQuote(s string) string {
	if !strings.ContainsAny(s, ",\"\n\r") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

const psLogsDir = "logs"

// psListLogFiles 列出 logs 目录下的 .log 文件
func (c *Client) psListLogFiles() ([]protocol.LogFileInfo, error) {
	entries, err := os.ReadDir(psLogsDir)
	if err != nil {
		return nil, err
	}
	list := make([]protocol.LogFileInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		list = append(list, protocol.LogFileInfo{Name: e.Name(), Size: info.Size(), Modified: info.ModTime().Unix()})
	}
	return list, nil
}

// handleListLogFiles 列出 logs 目录下的 .log 文件
func (c *Client) handleListLogFiles(w http.ResponseWriter, r *http.Request) {
	list, err := c.psListLogFiles()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, 5000, "读取日志目录失败")
		return
	}
	writeOK(w, list)
}

// tailLines 读文件尾部至多 maxLines 行（大文件只读末尾 2MB 避免整读）
func tailLines(path string, maxLines int, tailBytes int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	size := info.Size()
	offset := int64(0)
	if size > tailBytes {
		offset = size - tailBytes
	}
	buf := make([]byte, size-offset)
	if _, err := f.ReadAt(buf, offset); err != nil && err.Error() != "EOF" {
		return "", err
	}
	content := string(buf)
	if offset > 0 {
		if idx := strings.Index(content, "\n"); idx >= 0 {
			content = content[idx+1:]
		}
	}
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n"), nil
}

// psReadLogContent 读取日志文件内容：keyword 非空时只返回匹配行；download=true 返回全文（限制 50MB）
func (c *Client) psReadLogContent(name string, lines int, keyword string, download bool) (string, error) {
	// 只允许 logs 目录下的纯 .log 文件名，防目录穿越
	if name == "" || name != filepath.Base(name) || !strings.HasSuffix(name, ".log") || strings.Contains(name, "..") {
		return "", fmt.Errorf("非法文件名")
	}
	if lines < 1 {
		lines = 200
	}
	if lines > 2000 {
		lines = 2000
	}
	if download {
		info, err := os.Stat(filepath.Join(psLogsDir, name))
		if err != nil {
			return "", err
		}
		if info.Size() > 50*1024*1024 {
			return "", fmt.Errorf("文件过大，不支持整文件下载")
		}
		b, err := os.ReadFile(filepath.Join(psLogsDir, name))
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	content, err := tailLines(filepath.Join(psLogsDir, name), lines, 2*1024*1024)
	if err != nil {
		return "", err
	}
	if keyword != "" {
		rows := strings.Split(content, "\n")
		kept := make([]string, 0, len(rows))
		for _, ln := range rows {
			if strings.Contains(ln, keyword) {
				kept = append(kept, ln)
			}
		}
		content = strings.Join(kept, "\n")
	}
	return content, nil
}

// handleLogFileContent 尾部读取运行日志（?file=seeinps.log&lines=500&keyword=&download=1）
// download=1 时以附件形式返回整文件（此前即便传 download 也只返回 JSON 字符串，
// 前端"下载"实际拿不到文件，与 seeinpm 的行为不一致）。
func (c *Client) handleLogFileContent(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	name := q.Get("file")
	lines, _ := strconv.Atoi(q.Get("lines"))
	download := q.Get("download") == "1"
	content, err := c.psReadLogContent(name, lines, q.Get("keyword"), download)
	if err != nil {
		logx.Warnf("[LOG] read log failed file=%q download=%v user=%s err=%v",
			name, download, operatorFrom(r), err)
		writeErr(w, http.StatusNotFound, 2004, "文件不存在或参数非法")
		return
	}
	if download {
		// 运行日志外带需留痕（同 PM 侧 log_download）
		c.audit(r, "log_download", name, fmt.Sprintf("size=%d", len(content)))
		logx.Infof("[LOG] downloaded runtime log file=%s size=%d by=%s", name, len(content), operatorFrom(r))
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
		_, _ = w.Write([]byte(content))
		return
	}
	writeOK(w, map[string]string{"file": name, "content": content})
}

// handleLogListReq 控制通道：A 端请求 B 端日志文件列表（LOG_LIST_REQ -> LOG_LIST_RESP）
func (c *Client) handleLogListReq(msg *protocol.Message) *protocol.Message {
	files, err := c.psListLogFiles()
	code := int(protocol.CodeOK)
	if err != nil {
		code = int(protocol.CodeInternal)
	}
	// 注意：显式用 LOG_LIST_RESP 类型（NewResponse 会生成 REQ_RESP 后缀，与 A 端期待不符）
	return &protocol.Message{Type: protocol.TypeLogListResp, ID: msg.ID, Ts: time.Now().Unix(),
		Code: &code, Data: &protocol.LogListRespData{Files: files}}
}

// handleLogContentReq 控制通道：A 端请求 B 端日志文件内容（LOG_CONTENT_REQ -> LOG_CONTENT_RESP）
func (c *Client) handleLogContentReq(msg *protocol.Message) *protocol.Message {
	b, _ := json.Marshal(msg.Data)
	req := &protocol.LogContentReqData{}
	json.Unmarshal(b, req)
	content, err := c.psReadLogContent(req.File, req.Lines, req.Keyword, req.Download)
	code := int(protocol.CodeOK)
	if err != nil {
		code = int(protocol.CodeBadRequest)
	}
	return &protocol.Message{Type: protocol.TypeLogContentResp, ID: msg.ID, Ts: time.Now().Unix(),
		Code: &code, Data: &protocol.LogContentRespData{File: req.File, Content: content}}
}
