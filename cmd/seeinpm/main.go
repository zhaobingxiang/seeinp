package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/hashicorp/yamux"
	"github.com/seeinp/seeinp/internal/auth"
	"github.com/seeinp/seeinp/internal/config"
	"github.com/seeinp/seeinp/internal/gzhttp"
	"github.com/seeinp/seeinp/internal/guard"
	"github.com/seeinp/seeinp/internal/logx"
	"github.com/seeinp/seeinp/internal/mux"
	"github.com/seeinp/seeinp/internal/portpool"
	"github.com/seeinp/seeinp/internal/protocol"
	"github.com/seeinp/seeinp/internal/shaper"
	"github.com/seeinp/seeinp/internal/store"
	"github.com/seeinp/seeinp/internal/tlsutil"
	"github.com/seeinp/seeinp/internal/udpframing"
	"github.com/seeinp/seeinp/internal/version"
	"github.com/seeinp/seeinp/internal/webui"
)

const (
	// connWriteTimeout 转发连接单次写的上限（每次写前刷新，滑动语义）：
	// 对端窗口僵死时最迟在此时间后写失败、连接回收。
	connWriteTimeout = 300 * time.Second
	// connKeepAlivePeriod 转发连接 TCP keepalive 探测间隔：空闲长连接不再被固定寿命掐断，
	// 死连接（客户端掉线无 FIN）由 keepalive 探测失败回收。
	connKeepAlivePeriod = 60 * time.Second
	// udpSessionIdle 会话无任何双向活动超过此时间则回收（与 seeinps 联动）
	udpSessionIdle = 120 * time.Second
)

type Server struct {
	config             *config.PMConfig
	configPath         string // 配置文件路径，用于日志级别等设置持久化写回
	tlsConfig          *tls.Config
	store              *store.Store
	jwt                *auth.JWTManager
	guard              *guard.LoginGuard
	portPool           *portpool.Pool
	clients            map[string]*Client
	clientsMu          sync.RWMutex
	sessions           map[string]*Session
	sessionsMu         sync.RWMutex
	publicConns        map[int]*PublicListener
	publicConnsByProxy map[string]*PublicListener
	publicMu           sync.RWMutex
	// traffic 为代理流量内存计数器，key 为 username + "/" + proxyID
	traffic          map[string]*proxyTraffic
	trafficMu        sync.Mutex
	lastProxyCleanup time.Time
	// shapers：每用户一个共享令牌桶（所有代理双向合计），key=username。
	// quotaEnforced：用户当前周期流量已超额并执行过断开存量，防重复触发/用于恢复检测
	shapers       map[string]*shaper.Limiter
	shapersMu     sync.Mutex
	quotaEnforced map[string]bool
	quotaMu       sync.Mutex
	// fatal 上抛致命错误（如控制监听失败），Start 收到后优雅关停并以错误退出
	fatal chan error
	// loginFailAt 登录失败审计的按 IP 节流（防 spraying 刷爆审计表）
	loginFailMu sync.Mutex
	loginFailAt map[string]time.Time
}

// proxyTraffic 代理流量内存计数：in/out 为进程启动以来累计（含未落库部分），
// flushedIn/flushedOut 为已写入 DB 的水位，rateIn/rateOut 为采样速率（字节/秒）。
// DB 中的 bytes_in/bytes_out 为历史累计，接口返回总量 = DB值 + (内存累计 - 已落库水位)。
type proxyTraffic struct {
	in         atomic.Int64
	out        atomic.Int64
	flushedIn  atomic.Int64
	flushedOut atomic.Int64
	rateIn     atomic.Int64
	rateOut    atomic.Int64
}

type Client struct {
	username      string
	version       string // seeinps 上报的版本号（HELLO 时获取）
	goos          string // seeinps 运行平台（HELLO 时获取，升级包平台匹配用）
	goarch        string
	sessionID     string
	connected     time.Time
	lastPing      time.Time
	remoteAddr    string
	session       *yamux.Session
	controlStream net.Conn
	// ctrlWriteMu 保护控制流写入（心跳应答与 SESSION_REVOKE 推送可能并发）
	ctrlWriteMu sync.Mutex
	// pendingMu/pending 关联控制通道请求与响应（拉取 B 端日志等）
	pendingMu sync.Mutex
	pending   map[string]chan *protocol.Message
}

type Session struct {
	username  string
	sessionID string
	created   time.Time
}

type PublicListener struct {
	Port    int
	ProxyID string
	Type    string
	Ln      net.Listener
	// UdpConn 仅 UDP 映射使用（type=="udp"）；TCP 类代理为 nil
	UdpConn net.PacketConn
	Client  *Client
	tr      *proxyTraffic
	// lim 用户级共享令牌桶（所有代理双向合计）；web-ui 管理代理恒为 nil（豁免限速）
	lim *shaper.Limiter
	// activeConns 跟踪该监听器上的活动转发连接（公网连接与 mux 流），禁用时强制断开
	activeConns map[net.Conn]struct{}
	connsMu     sync.Mutex
	// udpSessions 按外部客户端源地址索引的 UDP 会话（仅 UDP 代理使用）
	udpSessions map[string]*udpPMSession
	udpMu       sync.Mutex
}

// udpPMSession 一个外部 UDP 客户端会话：公网侧共享 UdpConn，back 侧独占一条 yamux 流。
type udpPMSession struct {
	clientAddr net.Addr
	stream     net.Conn
}

// mapKey 返回该监听器在 publicConnsByProxy 中的复合键（user:proxy）。
// 监听器因端口被复用而停止时，按端口从 publicConns 取回 pl 再据此删除自身条目。
func (pl *PublicListener) mapKey() string {
	u := ""
	if pl.Client != nil {
		u = pl.Client.username
	}
	return u + ":" + pl.ProxyID
}

func NewServer(config *config.PMConfig) (*Server, error) {
	s, err := store.New(config.Database.Path)
	if err != nil {
		return nil, fmt.Errorf("init store: %w", err)
	}

	jwtMgr := auth.NewJWTManager(
		config.Auth.JWTSecret,
		time.Duration(config.Auth.JWTExpire)*time.Second,
		time.Duration(config.Auth.JWTRefreshExpire)*time.Second,
	)

	// 端口池：toml 为首次初始种子；DB 中有历史配置则优先（web 配置持久化）
	ranges := make([]portpool.Range, 0, len(config.PortPool.Ranges))
	for _, r := range config.PortPool.Ranges {
		ranges = append(ranges, portpool.Range{Start: r.Start, End: r.End})
	}
	if dbRanges, err := s.GetPortPoolConfig(); err == nil && len(dbRanges) > 0 {
		ranges = ranges[:0]
		for _, r := range dbRanges {
			ranges = append(ranges, portpool.Range{Start: r.Start, End: r.End})
		}
		logx.Infof("[POOL] loaded config from DB: %v", dbRanges)
	} else if err != nil {
		logx.Warnf("[POOL] read db config error (fallback to toml): %v", err)
	}

	return &Server{
		config:             config,
		store:              s,
		jwt:                jwtMgr,
		guard:              guard.New(),
		portPool:           portpool.New(ranges, s),
		clients:            make(map[string]*Client),
		sessions:           make(map[string]*Session),
		publicConns:        make(map[int]*PublicListener),
		publicConnsByProxy: make(map[string]*PublicListener),
		traffic:            make(map[string]*proxyTraffic),
		shapers:            make(map[string]*shaper.Limiter),
		quotaEnforced:      make(map[string]bool),
		fatal:              make(chan error, 2),
		loginFailAt:        make(map[string]time.Time),
	}, nil
}

func (s *Server) Start(ctx context.Context) error {
	defer s.store.Close()

	certFile := "data/server.crt"
	keyFile := "data/server.key"

	if s.config.TLS.CertFile != "" && s.config.TLS.KeyFile != "" {
		certFile = s.config.TLS.CertFile
		keyFile = s.config.TLS.KeyFile
	} else {
		fp, err := tlsutil.GenerateCert(certFile, keyFile)
		if err != nil {
			return fmt.Errorf("generate cert: %w", err)
		}
		logx.Infof("[TLS] fingerprint: %s", fp)
	}

	cert, err := tlsutil.LoadCert(certFile, keyFile)
	if err != nil {
		return fmt.Errorf("load cert: %w", err)
	}

	s.tlsConfig = &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}

	go s.startControlListener(ctx)
	go s.startHTTPListener()
	go s.startCleanupRoutine(ctx)
	go s.startExpiryTicker(ctx)
	go s.trafficSampler(ctx)
	go s.trafficFlusher(ctx)
	go s.quotaWatcher(ctx)

	httpAddr := s.config.Server.HTTPAddr
	if httpAddr == "" {
		httpAddr = ":90"
	}
	logx.Infof("[SYS] started: control %s, api %s", s.config.Server.Addr, httpAddr)

	// 致命错误（控制监听失败）立即关停并上抛；信号/ctx 取消走正常关停
	var fatalErr error
	select {
	case <-ctx.Done():
	case fatalErr = <-s.fatal:
	}
	s.shutdown()
	return fatalErr
}

func (s *Server) startControlListener(ctx context.Context) {
	ln, err := tls.Listen("tcp", s.config.Server.Addr, s.tlsConfig)
	if err != nil {
		logx.Errorf("[CTRL] control listener failed: %v", err)
		s.fatal <- fmt.Errorf("control listener: %w", err)
		return
	}
	defer ln.Close()
	logx.Infof("[CTRL] listening on %s", s.config.Server.Addr)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		conn, err := ln.Accept()
		if err != nil {
			logx.Warnf("[CTRL] accept error: %v", err)
			continue
		}
		go s.handleControlConnection(conn)
	}
}

func (s *Server) startHTTPListener() {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/api/v1/auth/init", s.handleAuthInit)
	mux.HandleFunc("/api/v1/auth/login", s.handleAuthLogin)
	mux.HandleFunc("GET /api/v1/auth/captcha", s.handleAuthCaptcha)
	mux.HandleFunc("/api/v1/auth/refresh", s.handleAuthRefresh)
	mux.HandleFunc("/api/v1/users", s.authMiddleware(s.handleUsers))
	mux.HandleFunc("PUT /api/v1/users/{username}", s.authMiddleware(s.handleUpdateUser))
	mux.HandleFunc("POST /api/v1/users/batch-move-group", s.authMiddleware(s.handleUsersBatchMoveGroup))
	mux.HandleFunc("POST /api/v1/users/{username}/disable", s.authMiddleware(s.handleUserDisable))
	mux.HandleFunc("POST /api/v1/users/{username}/enable", s.authMiddleware(s.handleUserEnable))
	mux.HandleFunc("POST /api/v1/users/{username}/reset-code", s.authMiddleware(s.handleUserResetCode))
	// 用户分组（树状，根分组不可删可改名，最多 5 层）
	mux.HandleFunc("/api/v1/user-groups", s.authMiddleware(s.handleUserGroups))
	mux.HandleFunc("PUT /api/v1/user-groups/{id}", s.authMiddleware(s.handleUserGroupRename))
	mux.HandleFunc("DELETE /api/v1/user-groups/{id}", s.authMiddleware(s.handleUserGroupDelete))
	mux.HandleFunc("/api/v1/clients", s.authMiddleware(s.handleClientsAPI))
	mux.HandleFunc("POST /api/v1/clients/{username}/upgrade", s.authMiddleware(s.handleClientUpgrade))
	mux.HandleFunc("GET /api/v1/clients/{username}/upgrade-status", s.authMiddleware(s.handleUpgradeStatus))
	// 批量升级（内存任务编排，金丝雀+限并发）
	mux.HandleFunc("POST /api/v1/clients/upgrade-batch", s.authMiddleware(s.handleUpgradeBatchCreate))
	mux.HandleFunc("GET /api/v1/upgrade-batches", s.authMiddleware(s.handleUpgradeBatchList))
	mux.HandleFunc("GET /api/v1/upgrade-batch/{id}", s.authMiddleware(s.handleUpgradeBatchGet))
	mux.HandleFunc("POST /api/v1/upgrade-batch/{id}/stop", s.authMiddleware(s.handleUpgradeBatchStop))
	mux.HandleFunc("GET /api/v1/versions", s.authMiddleware(s.handleVersionsList))
	mux.HandleFunc("POST /api/v1/versions", s.authMiddleware(s.handleVersionUpload))
	mux.HandleFunc("DELETE /api/v1/versions/{id}", s.authMiddleware(s.handleVersionDelete))
	mux.HandleFunc("/api/v1/auth/verify-code", s.handleVerifyCode)
	// seeinps 一键部署（免登录，走账号+授权码校验）：版本列表 / 安装包下载
	mux.HandleFunc("GET /api/v1/ps-release/versions", s.handlePSReleaseVersions)
	mux.HandleFunc("GET /api/v1/ps-release/download", s.handlePSReleaseDownload)
	mux.HandleFunc("/api/v1/port-pool", s.authMiddleware(s.handlePortPool))
	mux.HandleFunc("GET /api/v1/proxies", s.authMiddleware(s.handleListProxies))
	mux.HandleFunc("POST /api/v1/proxies/cleanup-offline", s.authMiddleware(s.handleCleanupOfflineProxies))
	mux.HandleFunc("GET /api/v1/proxies/{username}/{proxyId}/sessions", s.authMiddleware(s.handleProxySessions))
	mux.HandleFunc("POST /api/v1/proxies/{username}/{proxyId}/disable", s.authMiddleware(s.handleProxyDisable))
	mux.HandleFunc("POST /api/v1/proxies/{username}/{proxyId}/enable", s.authMiddleware(s.handleProxyEnable))
	mux.HandleFunc("GET /api/v1/audit-logs", s.authMiddleware(s.handleAuditLogs))
	mux.HandleFunc("GET /api/v1/audit-logs/export", s.authMiddleware(s.handleAuditLogsExport))
	mux.HandleFunc("GET /api/v1/logs", s.authMiddleware(s.handleListLogFiles))
	mux.HandleFunc("GET /api/v1/logs/content", s.authMiddleware(s.handleLogFileContent))
	// 系统日志：运行时查询/修改日志级别（持久化到 toml，审计记录 log_level_update）
	mux.HandleFunc("GET /api/v1/logging", s.authMiddleware(s.handleLoggingGet))
	mux.HandleFunc("PUT /api/v1/logging", s.authMiddleware(s.handleLoggingSet))
	// 混合架构：A 端按需拉取在线 B 端（seeinps）运行日志（经控制通道转发）
	mux.HandleFunc("GET /api/v1/ps-logs", s.authMiddleware(s.handlePSLogList))
	mux.HandleFunc("GET /api/v1/ps-logs/content", s.authMiddleware(s.handlePSLogContent))
	mux.HandleFunc("PUT /api/v1/ps-logging", s.authMiddleware(s.handlePSLoggingSet))
	mux.HandleFunc("GET /api/v1/ps-logging", s.authMiddleware(s.handlePSLoggingGet))

	// 前端内嵌于二进制（internal/webui），与后端版本严格一致，随版本包同步更新
	mux.HandleFunc("/", webui.SPAHandler(webui.PM()))

	addr := s.config.Server.HTTPAddr
	if addr == "" {
		addr = ":90"
	}
	logx.Infof("[HTTP] listening on %s", addr)
	// 文件下载接口用独立 mux 注册，完全不经过 gzip 中间件（io.Copy + 大文件与 gzip 管道有交互问题）。
	// 其余请求（前端/JSON API）走 gzip 压缩。
	downloadMux := http.NewServeMux()
	downloadMux.HandleFunc("/api/v1/ps-release/download", s.handlePSReleaseDownload)
	topMux := http.NewServeMux()
	topMux.Handle("/api/v1/ps-release/download", s.accessLog(downloadMux))
	topMux.Handle("/", s.accessLog(gzhttp.Handler(mux)))
	if err := http.ListenAndServe(addr, topMux); err != nil {
		logx.Errorf("[HTTP] listener error: %v", err)
	}
}

type ctxKey string

const operatorKey ctxKey = "operator"

// authMiddleware 校验 JWT 并把操作者用户名放入 context，供审计埋点取用
func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" ||
			r.URL.Path == "/api/v1/auth/login" ||
			r.URL.Path == "/api/v1/auth/captcha" ||
			r.URL.Path == "/api/v1/auth/init" ||
			r.URL.Path == "/api/v1/auth/refresh" ||
			r.URL.Path == "/api/v1/auth/verify-code" {
			next(w, r)
			return
		}

		token := extractToken(r)
		if token == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintf(w, `{"code":1003,"message":"unauthorized"}`)
			return
		}

		claims, err := s.jwt.ValidateToken(token)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintf(w, `{"code":1003,"message":"invalid token"}`)
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

// audit 写一条操作审计记录（自动附加来源 IP）。
// best-effort：写失败记 ERROR（此前只 Warnf，业务照常成功返回，等于"动作成功但无审计"且不显眼）。
// 需要 fail-closed 的关键动作用 auditBefore。
func (s *Server) audit(r *http.Request, action, target, detail string) {
	s.auditRecord(operatorFrom(r), action, target, withIP(detail, clientIP(r)))
}

// auditBefore 先落库审计、再执行关键操作（fail-closed）。
// 仅在配置 [audit] fail_closed = true 时生效：审计写不进去就拒绝执行，
// 避免出现"用户已删除但审计表里查不到"这类不可追溯的状态。返回 false 表示已写出响应。
func (s *Server) auditBefore(w http.ResponseWriter, r *http.Request, action, target, detail string) bool {
	if !s.config.Audit.FailClosed {
		s.audit(r, action, target, detail)
		return true
	}
	if err := s.auditRecord(operatorFrom(r), action, target, withIP(detail, clientIP(r))); err != nil {
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

// auditRecord 落库一条 PM 本地审计；返回错误供 fail-closed 场景判定。
func (s *Server) auditRecord(username, action, target, detail string) error {
	if err := s.store.InsertAuditLog(username, action, target, detail); err != nil {
		logx.Errorf("[AUDIT] insert failed action=%s user=%s err=%v", action, username, err)
		return err
	}
	logx.Debugf("[AUDIT] recorded action=%s user=%s target=%q", action, username, target)
	return nil
}

// auditLogin 登录成败审计（无 JWT，操作者取自请求体）
func (s *Server) auditLogin(username, action string, r *http.Request) {
	s.auditRecord(username, action, "", "ip="+clientIP(r))
}

// auditLoginFailed 登录失败审计的按 IP 节流：guard 的锁定按 username，
// 攻击者换 username 喷洒可无限写审计表；同一 IP 60s 内只记一条。
func (s *Server) auditLoginFailed(username, ip string) {
	now := time.Now()
	s.loginFailMu.Lock()
	if last, ok := s.loginFailAt[ip]; ok && now.Sub(last) < time.Minute {
		s.loginFailMu.Unlock()
		return
	}
	s.loginFailAt[ip] = now
	if len(s.loginFailAt) > 1024 {
		for k, t := range s.loginFailAt {
			if now.Sub(t) > time.Minute {
				delete(s.loginFailAt, k)
			}
		}
	}
	s.loginFailMu.Unlock()
	s.auditRecord(username, "login_failed", "", "ip="+ip)
}

// clientIP 从请求取客户端 IP（去掉端口）
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---------------- HTTP 访问/错误日志中间件 ----------------
//
// 级别约定（PM 与 PS 一致）：
//   - 5xx -> ERROR（服务端故障，必须能事后定位）
//   - 4xx -> WARN （客户端/权限问题，如 401 未登录、403 无权限、400 参数错）
//   - 其余（2xx/3xx）-> DEBUG（默认关闭，排查时开启即可看到完整调用轨迹）
//
// 此前所有 writeErr 只把错误回给前端、不落盘，用户报"操作报错了"时服务端零线索。

// accessInfoKey 承载本次请求的访问日志附加信息（指针，便于内层处理器回填）。
const accessInfoKey ctxKey = "access-info"

// accessInfo 由外层中间件创建、内层 authMiddleware 回填操作者。
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

// Flush / Unwrap 透传，避免破坏 gzip 中间件与大文件流式下载。
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// ReadFrom 透传底层 writer 的 ReadFrom（sendfile），否则 200MB 安装包下载会退化为纯用户态拷贝。
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

// Unwrap 之外还需支持 Hijacker（yamux 数据流不经过 HTTP，此处为通用兼容）
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := r.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("hijack not supported")
}

// skipAccessLog 静态资源与健康检查不进访问日志，避免刷屏。
func skipAccessLog(path string) bool {
	return path == "/health" || !strings.HasPrefix(path, "/api/")
}

// accessLog 统一 HTTP 访问/错误日志中间件：按状态码分级记录 method/path/status/耗时/字节/IP/操作者。
// 同时兜住 handler 内的 panic —— 此前没有 recover，一次 panic 会打挂整个 HTTP 服务。
func (s *Server) accessLog(next http.Handler) http.Handler {
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

func (s *Server) startCleanupRoutine(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.cleanupStaleAllocations()
		}
	}
}

// startExpiryTicker 每 30s 检查一次有效期：过期用户的 seeinps 控制连接与全部代理被断开；
// 有效期改回今天或以后后，seeinps 重连注册成功并自动恢复全部代理
func (s *Server) startExpiryTicker(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.checkExpiredUsers()
		}
	}
}

func (s *Server) checkExpiredUsers() {
	users, err := s.store.ListUsers()
	if err != nil {
		return
	}
	for _, u := range users {
		if u.Status != 1 || !userExpired(u) {
			continue
		}
		if _, active := s.isClientActive(u.Username); active {
			logx.Infof("[USER] expired, kicking: user=%s reason=user_expired", u.Username)
			s.kickClient(u.Username, "user_expired")
			s.store.InsertAuditLog(u.Username, "user_expired", "", "account expired, active connection kicked")
		}
	}
}

func (s *Server) cleanupStaleAllocations() {
	stale, err := s.store.GetStaleAllocations()
	if err != nil {
		logx.Warnf("[CLEANUP] get stale allocations error: %v", err)
		return
	}

	for _, alloc := range stale {
		s.stopPublicListener(alloc.Port)
		s.flushTraffic(alloc.UserID, alloc.ProxyID)
		if err := s.store.MarkProxyOffline(alloc.UserID, alloc.ProxyID); err != nil {
			logx.Warnf("[CLEANUP] mark proxy offline error: %v", err)
		}
		// 方案B：离线只停公网转发，端口仍预留给该代理；只有代理或用户被删除才放回公共池，
		// 避免重连前端口被其他用户（如新部署的 web-ui 映射）抢占。
		logx.Infof("[CLEANUP] proxy offline, port kept reserved: proxy=%s user=%s port=%d", alloc.ProxyID, alloc.UserID, alloc.Port)
	}

	if len(stale) > 0 {
		logx.Infof("[CLEANUP] cleaned up %d stale allocations", len(stale))
	}

	// 代理记录清理每小时执行一次：删除离线超 7 天且未禁用的代理（含会话历史），
	// 并修剪每个代理的会话记录到最近 50 条。手动禁用的代理永不自动清理。
	// 同时清理 90 天前的操作审计日志。
	if time.Since(s.lastProxyCleanup) > time.Hour {
		s.lastProxyCleanup = time.Now()
		cutoff := time.Now().Add(-7 * 24 * time.Hour).Unix()
		if n, err := s.store.CleanupOfflineProxies(cutoff); err != nil {
			logx.Warnf("[CLEANUP] cleanup offline proxies error: %v", err)
		} else if n > 0 {
			logx.Infof("[CLEANUP] removed %d proxies offline over 7 days", n)
		}
		if proxies, err := s.store.ListProxies(); err == nil {
			for _, p := range proxies {
				if err := s.store.TrimProxySessions(p.Username, p.ProxyID, 50); err != nil {
					logx.Warnf("[CLEANUP] trim sessions error: %v", err)
				}
			}
		}
		if n, err := s.store.CleanupAuditLogs(time.Now().Add(-90 * 24 * time.Hour).Unix()); err != nil {
			logx.Warnf("[CLEANUP] cleanup audit logs error: %v", err)
		} else if n > 0 {
			logx.Infof("[CLEANUP] removed %d audit logs over 90 days", n)
		}
	}
}

// Public listener functions
func (s *Server) startPublicListener(port int, proxyID, proxyType string, client *Client) error {
	pl := &PublicListener{
		Port:        port,
		ProxyID:     proxyID,
		Type:        proxyType,
		Client:      client,
		tr:          s.trafficFor(client.username, proxyID),
		activeConns: make(map[net.Conn]struct{}),
	}
	// 带宽限速取用户级共享令牌桶（ALLOC 前已 sync 过速率，一般非 nil）；web-ui 恒豁免
	if proxyID != webUIProxyID {
		pl.lim = s.userLimiter(client.username)
	}

	if proxyType == "udp" {
		pc, err := net.ListenUDP("udp", &net.UDPAddr{Port: port})
		if err != nil {
			return fmt.Errorf("listen udp on port %d: %w", port, err)
		}
		pl.UdpConn = pc
		pl.udpSessions = make(map[string]*udpPMSession)
		logx.Infof("[PUBLIC] listening udp on :%d for proxy %s", port, proxyID)
	} else {
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			return fmt.Errorf("listen on port %d: %w", port, err)
		}
		pl.Ln = ln
		logx.Infof("[PUBLIC] listening on :%d for proxy %s", port, proxyID)
	}

	s.publicMu.Lock()
	s.publicConns[port] = pl
	// 复合键：proxyID 全局不唯一，不同用户的同名代理需各自独立监听器
	s.publicConnsByProxy[client.username+":"+proxyID] = pl
	s.publicMu.Unlock()

	if pl.UdpConn != nil {
		go s.acceptPublicUDPConnections(pl)
	} else {
		go s.acceptPublicConnections(pl)
	}
	return nil
}

func (s *Server) stopPublicListener(port int) {
	s.publicMu.Lock()
	pl, ok := s.publicConns[port]
	if ok {
		delete(s.publicConns, port)
		delete(s.publicConnsByProxy, pl.mapKey())
	}
	s.publicMu.Unlock()

	if !ok {
		return
	}
	if pl.Ln != nil {
		pl.Ln.Close()
	}
	if pl.UdpConn != nil {
		pl.udpCloseAllSessions()
		pl.UdpConn.Close()
	}
	logx.Infof("[PUBLIC] stopped listening on :%d", port)
}

func (s *Server) acceptPublicConnections(pl *PublicListener) {
	for {
		conn, err := pl.Ln.Accept()
		if err != nil {
			return
		}
		go s.handlePublicConnection(pl, conn)
	}
}

// countingConn 统计从底层连接读取的字节数（onRead 每次 Read 后回调）
type countingConn struct {
	net.Conn
	onRead func(int)
}

func (c *countingConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 && c.onRead != nil {
		c.onRead(n)
	}
	return n, err
}

func (pl *PublicListener) trackConns(conns ...net.Conn) {
	pl.connsMu.Lock()
	for _, c := range conns {
		pl.activeConns[c] = struct{}{}
	}
	pl.connsMu.Unlock()
}

func (pl *PublicListener) untrackConns(conns ...net.Conn) {
	pl.connsMu.Lock()
	for _, c := range conns {
		delete(pl.activeConns, c)
	}
	pl.connsMu.Unlock()
}

// killActiveConns 强制关闭该监听器上的全部活动转发连接（禁用代理时调用）
func (pl *PublicListener) killActiveConns() {
	pl.connsMu.Lock()
	for c := range pl.activeConns {
		c.Close()
	}
	pl.activeConns = make(map[net.Conn]struct{})
	pl.connsMu.Unlock()
	pl.udpCloseAllSessions()
}

// udpCloseAllSessions 关闭该 UDP 监听器上的全部会话流（禁用/端口复用/停止时调用）
func (pl *PublicListener) udpCloseAllSessions() {
	pl.udpMu.Lock()
	defer pl.udpMu.Unlock()
	for _, sess := range pl.udpSessions {
		if sess.stream != nil {
			sess.stream.Close()
		}
	}
}

// acceptPublicUDPConnections UDP 代理共享一个公网 UDP socket（ReadFrom 得到源地址），
// 每个源地址映射一条 yamux 会话流，按会话分帧转发。
func (s *Server) acceptPublicUDPConnections(pl *PublicListener) {
	pc := pl.UdpConn
	buf := make([]byte, udpframing.MaxDatagram)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		payload := make([]byte, n)
		copy(payload, buf[:n])
		s.routeUDPPacket(pl, addr, payload)
	}
}

// routeUDPPacket 命中或新建外部源地址对应的会话并转发一个数据报。
func (s *Server) routeUDPPacket(pl *PublicListener, addr net.Addr, payload []byte) {
	key := addr.String()
	pl.udpMu.Lock()
	sess, ok := pl.udpSessions[key]
	if !ok {
		stream, err := pl.Client.session.Open()
		if err != nil {
			logx.Errorf("[UDP] open stream error: %v", err)
			pl.udpMu.Unlock()
			return
		}
		// 会话建立流头：stype(0x02) + proxyIdLen + proxyId + clientLen + clientAddr
		ca := addr.String()
		header := make([]byte, 3+len(pl.ProxyID)+len(ca))
		header[0] = protocol.StreamTypeUDP
		header[1] = byte(len(pl.ProxyID))
		copy(header[2:], pl.ProxyID)
		h := 2 + len(pl.ProxyID)
		header[h] = byte(len(ca))
		copy(header[h+1:], ca)
		if _, err := stream.Write(header); err != nil {
			stream.Close()
			logx.Errorf("[UDP] write header error: %v", err)
			pl.udpMu.Unlock()
			return
		}
		sess = &udpPMSession{clientAddr: addr, stream: stream}
		pl.udpSessions[key] = sess
		go s.udpResponsePump(pl, key, sess)
	}
	stream := sess.stream
	pl.udpMu.Unlock()

	// 外网→内网：写一帧到会话流，并刷新空闲计时（双方共享同一流）；限速走用户共享令牌桶
	if pl.lim != nil && pl.lim.Active() {
		pl.lim.Wait(context.Background(), int64(len(payload)))
	}
	stream.SetReadDeadline(time.Now().Add(udpSessionIdle))
	udpframing.WriteFrame(stream, payload)
	pl.tr.in.Add(int64(len(payload)))
}

// udpResponsePump 内网→外网：读会话流帧，写回公网 socket 对应源地址；流关闭/空闲超时回收会话。
func (s *Server) udpResponsePump(pl *PublicListener, key string, sess *udpPMSession) {
	defer sess.stream.Close()
	defer func() {
		pl.udpMu.Lock()
		delete(pl.udpSessions, key)
		pl.udpMu.Unlock()
	}()
	buf := make([]byte, udpframing.MaxDatagram)
	for {
		sess.stream.SetReadDeadline(time.Now().Add(udpSessionIdle))
		payload, err := udpframing.ReadFrame(sess.stream, buf)
		if err != nil {
			return
		}
		buf = payload
		if pl.lim != nil && pl.lim.Active() {
			pl.lim.Wait(context.Background(), int64(len(payload)))
		}
		if _, err := pl.UdpConn.WriteTo(payload, sess.clientAddr); err != nil {
			return
		}
		pl.tr.out.Add(int64(len(payload)))
	}
}

func (s *Server) handlePublicConnection(pl *PublicListener, conn net.Conn) {
	defer conn.Close()
	// 不在连接建立时设置一次性绝对 deadline：那会让所有转发连接固定寿命
	// （读 60s / 整体 300s），SSH 等交互式长连接空闲即被误杀。
	// 死连接由 TCP keepalive 探测回收；写方向由 writeDeadlineConn 逐次刷新滑动超时。
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(connKeepAlivePeriod)
	}

	logx.Debugf("[PUBLIC] new connection on :%d from %s", pl.Port, conn.RemoteAddr())

	stream, err := pl.Client.session.Open()
	if err != nil {
		logx.Errorf("[PUBLIC] open stream error: %v", err)
		return
	}
	defer stream.Close()

	pl.trackConns(conn, stream)
	defer pl.untrackConns(conn, stream)

	// 流头: stype(1B) + proxyIdLen(1B) + proxyId，读方按长度精确解析，避免与载荷粘连
	stype := protocol.StreamTypeTCP
	switch pl.Type {
	case "ops_http", "ops_socks":
		stype = protocol.StreamTypeOps
	}
	header := make([]byte, 2+len(pl.ProxyID))
	header[0] = stype
	header[1] = byte(len(pl.ProxyID))
	copy(header[2:], pl.ProxyID)
	if _, err := stream.Write(header); err != nil {
		logx.Errorf("[PUBLIC] write header error: %v", err)
		return
	}

	// 流量统计：入站 = 外部客户端 -> 内网（从公网连接读到的字节）；
	// 出站 = 内网 -> 外部（从 mux 流读到的字节）
	inConn := &countingConn{Conn: conn, onRead: func(n int) { pl.tr.in.Add(int64(n)) }}
	outStream := &countingConn{Conn: stream, onRead: func(n int) { pl.tr.out.Add(int64(n)) }}

	// 带宽限制：用户所有代理共享令牌桶（双向合计）；web-ui 与未配置限速走直通拷贝
	lim := pl.lim
	if lim != nil && !lim.Active() {
		lim = nil
	}

	wStream := &writeDeadlineConn{Conn: stream, d: connWriteTimeout}
	wConn := &writeDeadlineConn{Conn: conn, d: connWriteTimeout}
	done := make(chan struct{}, 2)
	go func() {
		if lim != nil {
			shapedCopy(wStream, inConn, lim)
		} else {
			io.Copy(wStream, inConn)
		}
		done <- struct{}{}
	}()
	go func() {
		if lim != nil {
			shapedCopy(wConn, outStream, lim)
		} else {
			io.Copy(wConn, outStream)
		}
		done <- struct{}{}
	}()
	<-done
	<-done
}

// writeDeadlineConn 在每次写之前刷新写 deadline（滑动超时），
// 既保证对端窗口僵死时写不会永久阻塞，又不给空闲连接设固定寿命。
type writeDeadlineConn struct {
	net.Conn
	d time.Duration
}

func (c *writeDeadlineConn) Write(p []byte) (int, error) {
	_ = c.Conn.SetWriteDeadline(time.Now().Add(c.d))
	return c.Conn.Write(p)
}

// trafficFor 获取（或创建）指定代理的流量计数器
func (s *Server) trafficFor(username, proxyID string) *proxyTraffic {
	key := username + "/" + proxyID
	s.trafficMu.Lock()
	defer s.trafficMu.Unlock()
	tr, ok := s.traffic[key]
	if !ok {
		tr = &proxyTraffic{}
		s.traffic[key] = tr
	}
	return tr
}

// webUIProxyID 为 B 端管理页代理 ID：豁免带宽限速，也是周期流量超额后唯一仍允许新建的代理
const webUIProxyID = "web-ui"

// syncUserLimiter 按最新 maxMbps 更新用户共享令牌桶（Mbps→字节/秒；0=不限）。
// 桶对象跨监听器共享，进行中的转发即时生效。
func (s *Server) syncUserLimiter(username string, maxMbps int64) {
	bps := maxMbps * 1000000 / 8
	s.shapersMu.Lock()
	defer s.shapersMu.Unlock()
	lim, ok := s.shapers[username]
	if !ok {
		s.shapers[username] = shaper.New(bps)
		return
	}
	lim.SetRate(bps)
}

// userLimiter 返回用户共享限速器对象（用户从未分配过代理时为 nil）
func (s *Server) userLimiter(username string) *shaper.Limiter {
	s.shapersMu.Lock()
	defer s.shapersMu.Unlock()
	return s.shapers[username]
}

// shapedCopy 是 io.Copy 的限速等价实现：每块读完后按真实字节数向共享桶取令牌再写出；
// src 侧计数由调用方包装（countingConn）完成，此处只负责节奏。
func shapedCopy(dst io.Writer, src io.Reader, lim *shaper.Limiter) {
	buf := make([]byte, shaper.ChunkSize)
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if werr := lim.Wait(context.Background(), int64(n)); werr != nil {
				return
			}
			for off := 0; off < n; {
				wn, werr := dst.Write(buf[off:n])
				off += wn
				if werr != nil {
					return
				}
			}
		}
		if rerr != nil {
			return
		}
	}
}

// flushTraffic 将指定代理的未落库流量增量写入 DB（离线/定时/关停时调用）
func (s *Server) flushTraffic(username, proxyID string) {
	key := username + "/" + proxyID
	s.trafficMu.Lock()
	tr, ok := s.traffic[key]
	s.trafficMu.Unlock()
	if !ok {
		return
	}
	in := tr.in.Load()
	out := tr.out.Load()
	dIn := in - tr.flushedIn.Load()
	dOut := out - tr.flushedOut.Load()
	if dIn <= 0 && dOut <= 0 {
		return
	}
	if err := s.store.AddProxyTraffic(username, proxyID, dIn, dOut); err != nil {
		logx.Warnf("[TRAFFIC] flush error: key=%s err=%v", key, err)
		return
	}
	tr.flushedIn.Store(in)
	tr.flushedOut.Store(out)
	// 周期流量配额：本次增量计入该用户当期用量（in+out 合计；web-ui 流量也计入用量，只是永不因超额被拒）
	if err := s.store.AddQuotaUsage(username, dIn+dOut, time.Now()); err != nil {
		logx.Warnf("[QUOTA] usage add error: user=%s err=%v", username, err)
	}
}

// flushUserTraffic 落库某用户全部代理的流量
func (s *Server) flushUserTraffic(username string) {
	if list, err := s.store.ListProxies(); err == nil {
		for _, p := range list {
			if p.Username == username {
				s.flushTraffic(p.Username, p.ProxyID)
			}
		}
	}
}

// flushAllTraffic 落库全部代理流量（进程关停时调用）
func (s *Server) flushAllTraffic() {
	if list, err := s.store.ListProxies(); err == nil {
		for _, p := range list {
			s.flushTraffic(p.Username, p.ProxyID)
		}
	}
}

// trafficSampler 每 5s 采样一次计数器增量，计算实时速率（字节/秒）
func (s *Server) trafficSampler(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	prev := make(map[string][2]int64)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.trafficMu.Lock()
			for key, tr := range s.traffic {
				in := tr.in.Load()
				out := tr.out.Load()
				p := prev[key]
				tr.rateIn.Store((in - p[0]) / 5)
				tr.rateOut.Store((out - p[1]) / 5)
				prev[key] = [2]int64{in, out}
			}
			s.trafficMu.Unlock()
		}
	}
}

// trafficFlusher 每 60s 将全部代理的未落库流量增量写入 DB，崩溃最多丢失 60s 数据
func (s *Server) trafficFlusher(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.flushAllTraffic()
		}
	}
}

// HTTP handlers
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	s.clientsMu.RLock()
	n := len(s.clients)
	s.clientsMu.RUnlock()
	total, used := s.portPool.GetStats()
	fmt.Fprintf(w, `{"status":"ok","version":"%s","clients":%d,"ports":{"total":%d,"used":%d}}`, version.Version, n, total, used)
}

func (s *Server) handleAuthInit(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "POST" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		fmt.Fprintf(w, `{"code":405,"message":"method not allowed"}`)
		return
	}
	if s.store.HasAdminUser() {
		w.WriteHeader(http.StatusConflict)
		fmt.Fprintf(w, `{"code":3001,"message":"admin user already exists"}`)
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"code":2001,"message":"invalid request body"}`)
		return
	}
	if len(req.Username) < 3 || len(req.Password) < 8 {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"code":2002,"message":"username min 3, password min 8"}`)
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"code":5000,"message":"internal error"}`)
		return
	}
	if err := s.store.CreateAdminUser(req.Username, hash); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"code":5000,"message":"%s"}`, err.Error())
		return
	}
	s.auditLogin(req.Username, "admin_init", r)
	fmt.Fprintf(w, `{"code":0,"message":"ok"}`)
}

func (s *Server) handleAuthCaptcha(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "GET" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		fmt.Fprintf(w, `{"code":405,"message":"method not allowed"}`)
		return
	}
	id, image := s.guard.CreateCaptcha()
	fmt.Fprintf(w, `{"code":0,"message":"ok","data":{"captcha_id":"%s","image_base64":"%s"}}`, id, image)
}

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "POST" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		fmt.Fprintf(w, `{"code":405,"message":"method not allowed"}`)
		return
	}
	var req struct {
		Username    string `json:"username"`
		Password    string `json:"password"`
		CaptchaID   string `json:"captcha_id"`
		CaptchaText string `json:"captcha_text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"code":2001,"message":"invalid request body"}`)
		return
	}

	// 先判锁定：锁定期间即使密码/验证码正确也拒绝
	if locked, remaining, failed := s.guard.Locked(req.Username); locked {
		s.auditLogin(req.Username, "login_locked", r)
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintf(w, `{"code":1103,"message":"账号已锁定","data":{"lock_remaining":%d,"failed_count":%d}}`, remaining, failed)
		return
	}
	if s.guard.NeedsCaptcha(req.Username) {
		if !s.guard.VerifyCaptcha(req.CaptchaID, req.CaptchaText) {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintf(w, `{"code":1104,"message":"验证码错误或已失效，请重新输入"}`)
			return
		}
	}

	user, err := s.store.GetAdminUser(req.Username)
	if err != nil {
		failed, _ := s.guard.OnFailure(req.Username)
		s.auditLoginFailed(req.Username, clientIP(r)) // 按 IP 节流，防喷洒刷表
		s.writeLoginFailure(w, req.Username, failed, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	if !auth.CheckPassword(req.Password, user.PasswordHash) {
		failed, lockSec := s.guard.OnFailure(req.Username)
		s.auditLoginFailed(req.Username, clientIP(r))
		if lockSec > 0 {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintf(w, `{"code":1103,"message":"密码错误次数过多，账号已锁定","data":{"lock_remaining":%d,"failed_count":%d}}`, lockSec, failed)
			return
		}
		s.writeLoginFailure(w, req.Username, failed, http.StatusUnauthorized, "用户名或密码错误")
		return
	}

	s.guard.Reset(req.Username)
	accessToken, _ := s.jwt.GenerateAccessToken(req.Username, "admin")
	refreshToken, _ := s.jwt.GenerateRefreshToken(req.Username, "admin")
	s.auditLogin(req.Username, "login", r)
	fmt.Fprintf(w, `{"code":0,"message":"ok","data":{"access_token":"%s","refresh_token":"%s"}}`, accessToken, refreshToken)
}

// writeLoginFailure 输出带防护信息的登录失败响应，供前端决定是否展示验证码 / 剩余次数
func (s *Server) writeLoginFailure(w http.ResponseWriter, username string, failed int, status int, msg string) {
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"code":1001,"message":"%s","data":{"need_captcha":%t,"failed_count":%d}}`,
		msg, failed >= guard.CaptchaThreshold, failed)
}

// handleAuthRefresh 目前是空实现（不签发新令牌，仅返回 ok）。
// 因此不产生审计记录，也不会泄漏任何凭据；调用它只是可见性事件，记 DEBUG 供排障。
func (s *Server) handleAuthRefresh(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	logx.Debugf("[AUTH] refresh called (no-op endpoint) ip=%s", clientIP(r))
	fmt.Fprintf(w, `{"code":0,"message":"ok"}`)
}

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case "GET":
		s.handleListUsers(w)
	case "POST":
		s.handleCreateUser(w, r)
	case "DELETE":
		s.handleDeleteUser(w, r)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleListUsers(w http.ResponseWriter) {
	users, err := s.store.ListUsers()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	type quotaResp struct {
		Enabled  bool   `json:"enabled"`
		Period   string `json:"period,omitempty"`
		Used     int64  `json:"used"`
		Limit    int64  `json:"limit"`
		StartAt  int64  `json:"startAt"`
		ResetAt  int64  `json:"resetAt"`
		Exceeded bool   `json:"exceeded"`
	}
	type userResp struct {
		ID         int64             `json:"id"`
		Username   string            `json:"username"`
		Remark     string            `json:"remark,omitempty"`
		Status     int               `json:"status"`
		Online     bool              `json:"online"`
		ExpireDate string            `json:"expireDate,omitempty"`
		Expired    bool              `json:"expired"`
		MaxPorts   int               `json:"maxPorts"`
		PortRanges []store.PortRange `json:"portRanges"`
		UsedPorts  int               `json:"usedPorts"`
		GroupID    int64             `json:"groupId"`
		MaxMbps    int64             `json:"maxMbps"`
		Quota      *quotaResp        `json:"quota,omitempty"`
	}
	list := make([]userResp, 0, len(users))
	for _, u := range users {
		_, active := s.isClientActive(u.Username)
		used, _ := s.store.CountActiveAllocationsByUser(u.Username)
		item := userResp{ID: u.ID, Username: u.Username, Remark: u.Remark, Status: u.Status, Online: active,
			MaxPorts: u.MaxPorts, PortRanges: u.PortRanges, UsedPorts: used, GroupID: u.GroupID, MaxMbps: u.MaxMbps}
		if u.ExpiresAt > 0 {
			item.ExpireDate = time.Unix(u.ExpiresAt, 0).Format("2006-01-02")
			item.Expired = userExpired(u)
		}
		if st, err := s.store.GetQuotaStatus(u.Username, time.Now()); err == nil && st.Enabled {
			item.Quota = &quotaResp{Enabled: true, Period: st.Period, Used: st.Used, Limit: st.Limit, StartAt: u.QuotaStart, ResetAt: st.PeriodEnd, Exceeded: st.Exceeded}
		}
		list = append(list, item)
	}
	data, _ := json.Marshal(map[string]interface{}{"code": 0, "data": list})
	w.Write(data)
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username    string            `json:"username"`
		Remark      string            `json:"remark"`
		ExpireDate  string            `json:"expireDate"`
		MaxPorts    int               `json:"maxPorts"`
		PortRanges  []store.PortRange `json:"portRanges"`
		GroupID     int64             `json:"groupId"`
		MaxMbps     int64             `json:"maxMbps"`
		QuotaBytes  int64             `json:"quotaBytes"`
		QuotaPeriod string            `json:"quotaPeriod"`
		QuotaStart  string            `json:"quotaStart"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"code":2001,"message":"invalid request"}`)
		return
	}
	if len(req.Username) < 3 {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"code":2002,"message":"username min 3 chars"}`)
		return
	}
	limits, err := s.parseUserLimits(userLimitsReq{
		ExpireDate: req.ExpireDate, MaxPorts: req.MaxPorts, PortRanges: req.PortRanges,
		MaxMbps: req.MaxMbps, QuotaBytes: req.QuotaBytes, QuotaPeriod: req.QuotaPeriod, QuotaStart: req.QuotaStart,
	})
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"code":2002,"message":%q}`, err.Error())
		return
	}
	// 分组：未指定时归根分组；指定了必须存在
	limits.GroupID = req.GroupID
	if limits.GroupID == 0 {
		if limits.GroupID, err = s.store.RootGroupID(); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, `{"code":5000,"message":"resolve root group failed"}`)
			return
		}
	} else if _, err := s.store.GetUserGroup(limits.GroupID); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"code":2002,"message":"分组不存在"}`)
		return
	}
	code, salt, _ := auth.GenerateAuthCode()
	authCodeHash := auth.HashAuthCode(code, salt)
	if err := s.store.CreateUser(req.Username, authCodeHash, salt, limits); err != nil {
		w.WriteHeader(http.StatusConflict)
		fmt.Fprintf(w, `{"code":3001,"message":"username already exists"}`)
		return
	}
	s.audit(r, "user_create", req.Username, fmt.Sprintf("remark=%s expire=%s maxPorts=%d portRanges=%v group=%d maxMbps=%d quota=%d/%s",
		req.Remark, req.ExpireDate, limits.MaxPorts, limits.PortRanges, limits.GroupID, limits.MaxMbps, limits.QuotaBytes, limits.QuotaPeriod))
	fmt.Fprintf(w, `{"code":0,"message":"ok","data":{"username":"%s","authCode":"%s","remark":"%s"}}`, req.Username, code, req.Remark)
}

// handleUpdateUser PUT /api/v1/users/{username}：修改有效期/端口配额/用户端口池/带宽限制/周期流量配额/所属分组（groupId=0 表示不改分组）。
// 配置收紧导致现有分配违规（池外或超配额）时踢线重连；配额下调/周期变更后若当期已超额，立即断除 web-ui 外存量。
func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	username := r.PathValue("username")
	user, err := s.store.GetUserByUsername(username)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"code":2004,"message":"user not found"}`)
		return
	}
	var req struct {
		ExpireDate  string            `json:"expireDate"`
		MaxPorts    int               `json:"maxPorts"`
		PortRanges  []store.PortRange `json:"portRanges"`
		GroupID     int64             `json:"groupId"`
		MaxMbps     int64             `json:"maxMbps"`
		QuotaBytes  int64             `json:"quotaBytes"`
		QuotaPeriod string            `json:"quotaPeriod"`
		QuotaStart  string            `json:"quotaStart"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"code":2001,"message":"invalid request"}`)
		return
	}
	limits, err := s.parseUserLimits(userLimitsReq{
		ExpireDate: req.ExpireDate, MaxPorts: req.MaxPorts, PortRanges: req.PortRanges,
		MaxMbps: req.MaxMbps, QuotaBytes: req.QuotaBytes, QuotaPeriod: req.QuotaPeriod, QuotaStart: req.QuotaStart,
	})
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"code":2002,"message":%q}`, err.Error())
		return
	}
	groupChanged := false
	if req.GroupID != 0 && req.GroupID != user.GroupID {
		if _, err := s.store.GetUserGroup(req.GroupID); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"code":2002,"message":"分组不存在"}`)
			return
		}
		if _, err := s.store.SetUsersGroup([]string{username}, req.GroupID); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, `{"code":5000,"message":"update group failed"}`)
			return
		}
		groupChanged = true
	}
	// 配额统计窗口（周期类型/起始日）变化 → 当期用量清零重计；仅改上下限则保留已用量
	quotaWindowChanged := limits.QuotaPeriod != user.QuotaPeriod || (limits.QuotaPeriod != "" && limits.QuotaStart != user.QuotaStart)
	if err := s.store.UpdateUserLimits(username, limits); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"code":5000,"message":"update failed"}`)
		return
	}
	if quotaWindowChanged {
		s.store.ResetQuotaUsage(username)
		s.markQuotaEnforced(username, false)
	}
	// 带宽即时生效（在线共享桶 SetRate；离线用户在下次 ALLOC 时同步）
	s.syncUserLimiter(username, limits.MaxMbps)
	// 配额收紧/窗口清零后即时判定超额（新上限已被超过 → 立即断存量并推送提醒）
	s.refreshQuotaState(username)
	allocs, _ := s.store.GetAllocationsByUser(username)
	overQuota := limits.MaxPorts > 0 && len(allocs) > limits.MaxPorts
	outsidePool := len(limits.PortRanges) > 0 && !allocsInRanges(allocs, limits.PortRanges)
	kicked := false
	if overQuota || outsidePool {
		s.kickClient(username, "user_config_changed")
		kicked = true
	}
	logx.Infof("[USER] updated: user=%s expire=%s maxPorts=%d ranges=%v group=%d maxMbps=%d quota=%d/%s windowReset=%v kicked=%v", username, req.ExpireDate, limits.MaxPorts, limits.PortRanges, req.GroupID, limits.MaxMbps, limits.QuotaBytes, limits.QuotaPeriod, quotaWindowChanged, kicked)
	s.audit(r, "user_update", username, fmt.Sprintf("expire=%s maxPorts=%d portRanges=%v group=%d groupChanged=%v maxMbps=%d quotaBytes=%d quotaPeriod=%s quotaStart=%s windowReset=%v kicked=%v",
		req.ExpireDate, limits.MaxPorts, limits.PortRanges, req.GroupID, groupChanged, limits.MaxMbps, limits.QuotaBytes, limits.QuotaPeriod, req.QuotaStart, quotaWindowChanged, kicked))
	fmt.Fprintf(w, `{"code":0,"message":"ok","data":{"kicked":%v}}`, kicked)
}

// validateGroupName 分组名校验：去首尾空白后非空且不超过 30 个字符；返回归一化后的名称
func validateGroupName(raw string) (string, string) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", "分组名称不能为空"
	}
	if utf8.RuneCountInString(name) > 30 {
		return "", "分组名称不能超过 30 个字符"
	}
	return name, ""
}

// handleUserGroups /api/v1/user-groups：GET 返回全部分组（平铺+直属人数/子组数），POST 在 parentId 下新建分组（parentId=0 归根分组）
func (s *Server) handleUserGroups(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case "GET":
		groups, err := s.store.ListUserGroups()
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		type groupResp struct {
			ID         int64  `json:"id"`
			ParentID   int64  `json:"parentId"`
			Name       string `json:"name"`
			IsRoot     bool   `json:"isRoot"`
			UserCount  int    `json:"userCount"`
			ChildCount int    `json:"childCount"`
		}
		list := make([]groupResp, 0, len(groups))
		for _, g := range groups {
			list = append(list, groupResp{ID: g.ID, ParentID: g.ParentID, Name: g.Name, IsRoot: g.IsRoot, UserCount: g.UserCount, ChildCount: g.ChildCount})
		}
		data, _ := json.Marshal(map[string]interface{}{"code": 0, "data": list})
		w.Write(data)
	case "POST":
		var req struct {
			ParentID int64  `json:"parentId"`
			Name     string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"code":2001,"message":"invalid request"}`)
			return
		}
		name, msg := validateGroupName(req.Name)
		if msg != "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"code":2002,"message":%q}`, msg)
			return
		}
		parentID := req.ParentID
		if parentID == 0 {
			rid, rerr := s.store.RootGroupID()
			if rerr != nil {
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprintf(w, `{"code":5000,"message":"resolve root group failed"}`)
				return
			}
			parentID = rid
		}
		g, err := s.store.CreateUserGroup(parentID, name)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"code":2002,"message":%q}`, err.Error())
			return
		}
		s.audit(r, "user_group_create", g.Name, fmt.Sprintf("groupId=%d parentId=%d", g.ID, parentID))
		data, _ := json.Marshal(map[string]interface{}{"code": 0, "data": map[string]interface{}{"id": g.ID, "parentId": g.ParentID, "name": g.Name, "isRoot": false}})
		w.Write(data)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleUserGroupRename PUT /api/v1/user-groups/{id}：重命名分组（根分组也可改名）
func (s *Server) handleUserGroupRename(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"code":2002,"message":"invalid group id"}`)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"code":2001,"message":"invalid request"}`)
		return
	}
	name, msg := validateGroupName(req.Name)
	if msg != "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"code":2002,"message":%q}`, msg)
		return
	}
	g, err := s.store.GetUserGroup(id)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"code":2004,"message":"分组不存在"}`)
		return
	}
	if err := s.store.RenameUserGroup(id, name); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"code":2002,"message":%q}`, err.Error())
		return
	}
	s.audit(r, "user_group_rename", g.Name, fmt.Sprintf("groupId=%d to=%s", id, name))
	fmt.Fprintf(w, `{"code":0,"message":"ok"}`)
}

// handleUserGroupDelete DELETE /api/v1/user-groups/{id}：删除分组；根分组与仍有子分组/用户的分组禁止删除
func (s *Server) handleUserGroupDelete(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"code":2002,"message":"invalid group id"}`)
		return
	}
	g, err := s.store.GetUserGroup(id)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"code":2004,"message":"分组不存在"}`)
		return
	}
	if err := s.store.DeleteUserGroup(id); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, store.ErrRootGroupUndeletable) {
			status = http.StatusForbidden
		}
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"code":2002,"message":%q}`, err.Error())
		return
	}
	s.audit(r, "user_group_delete", g.Name, fmt.Sprintf("groupId=%d", id))
	fmt.Fprintf(w, `{"code":0,"message":"ok"}`)
}

// handleUsersBatchMoveGroup POST /api/v1/users/batch-move-group：批量把用户移动到指定分组
func (s *Server) handleUsersBatchMoveGroup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var req struct {
		Usernames []string `json:"usernames"`
		GroupID   int64    `json:"groupId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"code":2001,"message":"invalid request"}`)
		return
	}
	if len(req.Usernames) == 0 || req.GroupID == 0 {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"code":2002,"message":"请先勾选用户并选择目标分组"}`)
		return
	}
	moved, err := s.store.SetUsersGroup(req.Usernames, req.GroupID)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"code":2002,"message":%q}`, err.Error())
		return
	}
	s.audit(r, "user_batch_move_group", fmt.Sprintf("%d users", moved), fmt.Sprintf("groupId=%d moved=%d", req.GroupID, moved))
	fmt.Fprintf(w, `{"code":0,"message":"ok","data":{"moved":%d}}`, moved)
}

// userLimitsReq 用户创建/编辑请求中的限制相关字段（带宽与配额均为可选：0/空=不限）。
// quotaBytes 单位字节（前端由 GB 换算）；quotaStart 为 YYYY-MM-DD（空=配额启用时取本月 1 号）。
type userLimitsReq struct {
	ExpireDate  string            `json:"expireDate"`
	MaxPorts    int               `json:"maxPorts"`
	PortRanges  []store.PortRange `json:"portRanges"`
	MaxMbps     int64             `json:"maxMbps"`
	QuotaBytes  int64             `json:"quotaBytes"`
	QuotaPeriod string            `json:"quotaPeriod"`
	QuotaStart  string            `json:"quotaStart"`
}

// parseUserLimits 校验并归一化全部用户限制配置，返回存储层 UserLimits（不含 GroupID，由调用方补充）
func (s *Server) parseUserLimits(req userLimitsReq) (store.UserLimits, error) {
	var limits store.UserLimits
	if req.ExpireDate != "" {
		t, err := time.ParseInLocation("2006-01-02", req.ExpireDate, time.Local)
		if err != nil {
			return limits, fmt.Errorf("有效期格式应为 YYYY-MM-DD")
		}
		limits.ExpiresAt = t.Add(23*time.Hour + 59*time.Minute + 59*time.Second).Unix()
	}
	if req.MaxPorts < 0 {
		return limits, fmt.Errorf("端口数量不能为负数")
	}
	limits.MaxPorts = req.MaxPorts
	if len(req.PortRanges) > 0 {
		merged, err := normalizeRanges(req.PortRanges)
		if err != nil {
			return limits, err
		}
		global := s.portPool.GetRanges()
		for _, rg := range merged {
			covered := false
			for _, g := range global {
				if rg.Start >= g.Start && rg.End <= g.End {
					covered = true
					break
				}
			}
			if !covered {
				return limits, fmt.Errorf("用户端口池 %d-%d 不在总端口池范围内", rg.Start, rg.End)
			}
		}
		if req.MaxPorts > 0 {
			span := 0
			for _, rg := range merged {
				span += rg.End - rg.Start + 1
			}
			if span > req.MaxPorts {
				return limits, fmt.Errorf("用户端口池共 %d 个端口，超过端口数量配额 %d", span, req.MaxPorts)
			}
		}
		limits.PortRanges = merged
	}
	// 带宽限制（Mbps，0=不限；上限 100Gbps 防呆）
	if req.MaxMbps < 0 || req.MaxMbps > 100000 {
		return limits, fmt.Errorf("带宽限制需为 0~100000 Mbps（0=不限）")
	}
	limits.MaxMbps = req.MaxMbps
	// 周期流量配额
	switch req.QuotaPeriod {
	case "", "month", "quarter", "year":
	default:
		return limits, fmt.Errorf("流量周期仅支持 月/季/年")
	}
	if req.QuotaBytes < 0 {
		return limits, fmt.Errorf("总流量上限不能为负数")
	}
	if req.QuotaBytes > 0 {
		if req.QuotaPeriod == "" {
			return limits, fmt.Errorf("设置总流量上限时必须选择统计周期")
		}
		if req.QuotaStart != "" {
			t, err := time.ParseInLocation("2006-01-02", req.QuotaStart, time.Local)
			if err != nil {
				return limits, fmt.Errorf("流量起始日期格式应为 YYYY-MM-DD")
			}
			limits.QuotaStart = t.Unix()
		} else {
			// 默认：本月 1 号 0 点（周期建议口径）
			now := time.Now()
			limits.QuotaStart = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local).Unix()
		}
		limits.QuotaBytes = req.QuotaBytes
		limits.QuotaPeriod = req.QuotaPeriod
	}
	return limits, nil
}

// allocsInRanges 判断全部分配是否都在给定范围集合内
func allocsInRanges(allocs []*store.PortAllocation, ranges []store.PortRange) bool {
	for _, a := range allocs {
		if !portInRanges(a.Port, ranges) {
			return false
		}
	}
	return true
}

// userExpired 有效期是否已过（ExpiresAt=0 表示永不过期）
func userExpired(u *store.User) bool {
	return u.ExpiresAt > 0 && time.Now().Unix() > u.ExpiresAt
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	username := r.URL.Query().Get("username")
	if username == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	// 删除用户不可逆：fail_closed 开启时先落审计，写不进去就拒绝执行
	if !s.auditBefore(w, r, "user_delete", username, "") {
		return
	}
	// 删除用户：先断开在线会话，再把其全部端口释放回公共池，最后级联清理数据
	s.kickClient(username, "user_deleted")
	s.releaseUserPorts(username)
	if err := s.store.DeleteUser(username); err != nil {
		logx.Errorf("[USER] delete failed user=%s err=%v", username, err)
		w.WriteHeader(http.StatusNotFound)
		return
	}
	// 清理内存态：共享令牌桶与超额标记（DB 侧 user_quota_usage 已随 DeleteUser 级联删除）
	s.shapersMu.Lock()
	delete(s.shapers, username)
	s.shapersMu.Unlock()
	s.markQuotaEnforced(username, false)
	logx.Infof("[USER] deleted user=%s by=%s", username, operatorFrom(r))
	fmt.Fprintf(w, `{"code":0,"message":"ok"}`)
}

// handleUserDisable 禁用用户（F-U2/F-U4）：可选 disconnectNow 立即断开已建立的全部连接
func (s *Server) handleUserDisable(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	username := r.PathValue("username")
	var req struct {
		DisconnectNow bool `json:"disconnectNow"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	if _, err := s.store.GetUserByUsername(username); err != nil {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"code":2004,"message":"user not found"}`)
		return
	}
	if _, err := s.store.SetUserStatus(username, 0); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"code":5000,"message":"update failed"}`)
		return
	}
	if req.DisconnectNow {
		s.kickClient(username, "user_disabled")
	}
	logx.Infof("[USER] disabled: user=%s disconnectNow=%v", username, req.DisconnectNow)
	s.audit(r, "user_disable", username, fmt.Sprintf("disconnectNow=%v", req.DisconnectNow))
	fmt.Fprintf(w, `{"code":0,"message":"ok"}`)
}

func (s *Server) handleUserEnable(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	username := r.PathValue("username")
	found, err := s.store.SetUserStatus(username, 1)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"code":5000,"message":"update failed"}`)
		return
	}
	if !found {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"code":2004,"message":"user not found"}`)
		return
	}
	logx.Infof("[USER] enabled: user=%s", username)
	s.audit(r, "user_enable", username, "status=1")
	fmt.Fprintf(w, `{"code":0,"message":"ok"}`)
}

// handleUserResetCode 重置授权码（F-U2）：旧码立即失效，在线 seeinps 被断开，需重新部署
func (s *Server) handleUserResetCode(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	username := r.PathValue("username")
	if _, err := s.store.GetUserByUsername(username); err != nil {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"code":2004,"message":"user not found"}`)
		return
	}
	code, salt, err := auth.GenerateAuthCode()
	if err != nil {
		logx.Errorf("[USER] generate auth code failed user=%s err=%v", username, err)
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"code":5000,"message":"generate auth code failed"}`)
		return
	}
	// 重置授权码会让 B 端断线重绑，属高敏感不可逆操作：fail_closed 时先落审计
	if !s.auditBefore(w, r, "user_reset_code", username, "新授权码已下发，旧码吊销，在线 seeinps 已断开等待重新绑定") {
		return
	}
	if err := s.store.ResetUserAuthCode(username, auth.HashAuthCode(code, salt), salt); err != nil {
		logx.Errorf("[USER] reset auth code failed user=%s err=%v", username, err)
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"code":5000,"message":"update failed"}`)
		return
	}
	s.kickClient(username, "auth_code_reset")
	logx.Infof("[USER] auth code reset: user=%s by=%s", username, operatorFrom(r))
	fmt.Fprintf(w, `{"code":0,"message":"ok","data":{"username":"%s","authCode":"%s"}}`, username, code)
}

func (s *Server) handleVerifyCode(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var req struct {
		Username string `json:"username"`
		AuthCode string `json:"authCode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"code":2001,"message":"invalid request"}`)
		return
	}
	user, err := s.verifyUserAuth(req.Username, req.AuthCode)
	if err != nil {
		// 区分禁用与其它（账号/授权码无效）
		if u, uerr := s.store.GetUserByUsername(req.Username); uerr == nil && u.Status != 1 {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintf(w, `{"code":1003,"message":"user disabled"}`)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"code":1001,"message":"invalid auth code or user"}`)
		return
	}
	if user.OnlineSession != nil {
		if _, active := s.isClientActive(req.Username); active {
			w.WriteHeader(http.StatusConflict)
			fmt.Fprintf(w, `{"code":1002,"message":"auth code already in use"}`)
			return
		}
	}
	fmt.Fprintf(w, `{"code":0,"message":"ok","data":{"serverAddr":"%s"}}`, s.config.Server.Addr)
}

func (s *Server) handleClientsAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	s.clientsMu.RLock()
	type clientInfo struct {
		Username   string `json:"username"`
		SessionID  string `json:"sessionId"`
		Connected  int64  `json:"connected"`
		LastPing   int64  `json:"lastPing"`
		Version    string `json:"version"`
		GoOS       string `json:"goos"`
		GoArch     string `json:"goarch"`
		Upgradable bool   `json:"upgradable"`
		Upgrading  bool   `json:"upgrading"`
	}
	list := make([]clientInfo, 0, len(s.clients))
	for _, c := range s.clients {
		// 心跳超 90s 的陈旧连接视为离线不展示（链路半死时避免"假在线/假升级中"误导）
		if time.Since(c.lastPing) > 90*time.Second || c.session == nil || c.session.IsClosed() {
			continue
		}
		// 可升级判断按节点自身平台取最新包，避免跨平台误判
		latest, _ := s.store.GetLatestVersion("seeinps", c.goos, c.goarch)
		upgradable := latest != nil && compareVersions(latest.Version, c.version) > 0
		list = append(list, clientInfo{Username: c.username, SessionID: c.sessionID, Connected: c.connected.Unix(), LastPing: c.lastPing.Unix(),
			Version: c.version, GoOS: c.goos, GoArch: c.goarch, Upgradable: upgradable, Upgrading: isUpgrading(c.username)})
	}
	s.clientsMu.RUnlock()
	data, _ := json.Marshal(map[string]interface{}{"code": 0, "data": list})
	w.Write(data)
}

func (s *Server) handlePortPool(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodPut {
		s.handlePortPoolUpdate(w, r)
		return
	}
	total, used := s.portPool.GetStats()
	cur := s.portPool.GetRanges()
	ranges := make([]store.PortRange, 0, len(cur))
	for _, rg := range cur {
		ranges = append(ranges, store.PortRange{Start: rg.Start, End: rg.End})
	}
	// 已分配端口明细（含池外标记）
	allocs, _ := s.store.GetAllocatedPorts()
	type allocResp struct {
		Username string `json:"username"`
		ProxyID  string `json:"proxyId"`
		Port     int    `json:"port"`
		Type     string `json:"type"`
		InPool   bool   `json:"inPool"`
	}
	allocList := make([]allocResp, 0, len(allocs))
	for _, a := range allocs {
		allocList = append(allocList, allocResp{Username: a.UserID, ProxyID: a.ProxyID, Port: a.Port, Type: a.ProxyType, InPool: s.portPool.Contains(a.Port)})
	}
	data, _ := json.Marshal(map[string]interface{}{"code": 0, "data": map[string]interface{}{
		"ranges": ranges, "total": total, "used": used, "allocations": allocList,
	}})
	w.Write(data)
}

// handlePortPoolUpdate PUT /api/v1/port-pool：保存新范围并处理池外分配
// body: {"ranges":[{"start":..,"end":..}], "action":"recycle"|"keep"}
//   - recycle（默认）：池外在线代理踢线重连（30s 内自动回池内），池外分配记录释放
//   - keep：不踢线，仅提示哪些代理在池外（新分配只用池内）
func (s *Server) handlePortPoolUpdate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var req struct {
		Ranges []store.PortRange `json:"ranges"`
		Action string            `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"code":2001,"message":"invalid request"}`)
		return
	}
	if req.Action == "" {
		req.Action = "recycle"
	}
	if req.Action != "recycle" && req.Action != "keep" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"code":2002,"message":"action must be recycle or keep"}`)
		return
	}
	merged, err := normalizeRanges(req.Ranges)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"code":2002,"message":%q}`, err.Error())
		return
	}

	// 找出新池外的活跃分配
	allocs, _ := s.store.GetAllocatedPorts()
	type outsideItem struct {
		username, proxyID, ptype string
		port                     int
	}
	var outside []outsideItem
	for _, a := range allocs {
		if !portInRanges(a.Port, merged) {
			outside = append(outside, outsideItem{a.UserID, a.ProxyID, a.ProxyType, a.Port})
		}
	}

	// 持久化 + 热生效
	if err := s.store.SavePortPoolConfig(merged); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"code":5000,"message":"save config failed"}`)
		return
	}
	newRanges := make([]portpool.Range, len(merged))
	for i, rg := range merged {
		newRanges[i] = portpool.Range{Start: rg.Start, End: rg.End}
	}
	s.portPool.SetRanges(newRanges)

	recycled := make([]map[string]interface{}, 0, len(outside))
	kept := make([]map[string]interface{}, 0, len(outside))
	// 记录旧范围用于审计对比
	oldRanges := s.portPool.GetRanges()
	if len(outside) > 0 && req.Action == "recycle" {
		for _, it := range outside {
			// 端口池收缩回收是管理员的显式治理动作：无论在线离线，都把池外代理端口
			// 停转发并释放回公共池，重连后按池内重新分配；在线代理同时踢线强制造应新模式
			if _, active := s.isClientActive(it.username); active {
				s.kickClient(it.username, "port_pool_shrunk")
			}
			if alloc, err := s.store.GetPortByUserProxy(it.username, it.proxyID); err == nil {
				s.stopPublicListener(alloc.Port)
			}
			s.portPool.Release(it.username, it.proxyID)
			s.flushTraffic(it.username, it.proxyID)
			_ = s.store.MarkProxyOffline(it.username, it.proxyID)
			recycled = append(recycled, map[string]interface{}{"username": it.username, "proxyId": it.proxyID, "port": it.port})
		}
	} else {
		for _, it := range outside {
			kept = append(kept, map[string]interface{}{"username": it.username, "proxyId": it.proxyID, "port": it.port})
		}
	}
	s.audit(r, "port_pool_update", fmt.Sprintf("%v", merged), fmt.Sprintf("old=%v new=%v action=%s outside=%d recycled=%d", oldRanges, merged, req.Action, len(outside), len(recycled)))

	total, used := s.portPool.GetStats()
	data, _ := json.Marshal(map[string]interface{}{"code": 0, "data": map[string]interface{}{
		"ranges": merged, "total": total, "used": used, "recycled": recycled, "kept": kept,
	}})
	w.Write(data)
}

// normalizeRanges 校验端口池范围并自动合并重叠/相邻段
func normalizeRanges(input []store.PortRange) ([]store.PortRange, error) {
	if len(input) == 0 {
		return nil, fmt.Errorf("端口池至少需要一段范围")
	}
	for _, rg := range input {
		if rg.Start < 1 || rg.End > 65535 || rg.Start > rg.End {
			return nil, fmt.Errorf("非法范围 %d-%d（需满足 1 <= start <= end <= 65535）", rg.Start, rg.End)
		}
	}
	sorted := append([]store.PortRange(nil), input...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Start != sorted[j].Start {
			return sorted[i].Start < sorted[j].Start
		}
		return sorted[i].End < sorted[j].End
	})
	merged := []store.PortRange{sorted[0]}
	for _, rg := range sorted[1:] {
		last := &merged[len(merged)-1]
		if rg.Start <= last.End+1 { // 重叠或相邻 → 合并
			if rg.End > last.End {
				last.End = rg.End
			}
		} else {
			merged = append(merged, rg)
		}
	}
	return merged, nil
}

// portInRanges 判断端口是否在给定范围集合内
func portInRanges(port int, ranges []store.PortRange) bool {
	for _, rg := range ranges {
		if port >= rg.Start && port <= rg.End {
			return true
		}
	}
	return false
}

// handleListProxies 代理管理列表：所有连接过的代理（含离线/禁用），
// 附累计流量（DB 累计 + 未落库增量）与实时速率（在线代理）
func (s *Server) handleListProxies(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	proxies, err := s.store.ListProxies()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"code":5000,"message":"query failed"}`)
		return
	}
	type proxyResp struct {
		Username      string `json:"username"`
		ProxyID       string `json:"proxyId"`
		Name          string `json:"name"` // 显示名：seeinps用户名.代理ID
		Type          string `json:"type"`
		Status        int    `json:"status"` // 1 启用 / 0 禁用
		Online        bool   `json:"online"`
		Port          int    `json:"port,omitempty"`
		LastOnlineAt  *int64 `json:"lastOnlineAt,omitempty"`
		LastOfflineAt *int64 `json:"lastOfflineAt,omitempty"`
		BytesIn       int64  `json:"bytesIn"`
		BytesOut      int64  `json:"bytesOut"`
		RateIn        int64  `json:"rateIn"`
		RateOut       int64  `json:"rateOut"`
	}
	list := make([]proxyResp, 0, len(proxies))
	for _, p := range proxies {
		resp := proxyResp{
			Username: p.Username, ProxyID: p.ProxyID,
			Name: p.Username + "." + p.ProxyID, Type: p.ProxyType,
			Status: p.Status, Online: p.Online,
			LastOnlineAt: p.LastOnlineAt, LastOfflineAt: p.LastOfflineAt,
			BytesIn: p.BytesIn, BytesOut: p.BytesOut,
		}
		if p.Online {
			s.publicMu.RLock()
			if pl, ok := s.publicConnsByProxy[p.Username+":"+p.ProxyID]; ok {
				resp.Port = pl.Port
			}
			s.publicMu.RUnlock()
		} else if pa, err := s.store.GetLastPortByUserProxy(p.Username, p.ProxyID); err == nil {
			// 离线/禁用代理端口保留（方案B），返回保留端口便于按端口筛选与排序
			resp.Port = pa.Port
		}
		tr := s.trafficFor(p.Username, p.ProxyID)
		resp.BytesIn += tr.in.Load() - tr.flushedIn.Load()
		resp.BytesOut += tr.out.Load() - tr.flushedOut.Load()
		if p.Online {
			resp.RateIn = tr.rateIn.Load()
			resp.RateOut = tr.rateOut.Load()
		}
		list = append(list, resp)
	}
	data, _ := json.Marshal(map[string]interface{}{"code": 0, "data": list})
	w.Write(data)
}

// handleCleanupOfflineProxies 手动清理全部离线（未禁用）代理：
// 复用 CleanupOfflineProxies 清理逻辑，cutoff 取当前时间使所有离线代理（status=1, online=0）都命中。
// 被清理代理的会话记录与预留端口一并删除（端口放回公共池），重新连接时会自动重建记录。
func (s *Server) handleCleanupOfflineProxies(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	n, err := s.store.CleanupOfflineProxies(time.Now().Unix())
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"code":5000,"message":"cleanup failed"}`)
		return
	}
	s.audit(r, "proxy_cleanup_offline", "all", fmt.Sprintf("removed=%d offline proxies, ports returned to pool", n))
	fmt.Fprintf(w, `{"code":0,"message":"ok","data":{"removed":%d}}`, n)
}

// handleProxySessions 返回代理最近 10 次连接/离线时间
func (s *Server) handleProxySessions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	username := r.PathValue("username")
	proxyID := r.PathValue("proxyId")
	if _, err := s.store.GetProxy(username, proxyID); err != nil {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"code":2004,"message":"proxy not found"}`)
		return
	}
	sessions, err := s.store.GetProxySessions(username, proxyID, 10)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"code":5000,"message":"query failed"}`)
		return
	}
	type sessionResp struct {
		OnlineAt   int64  `json:"onlineAt"`
		OfflineAt  *int64 `json:"offlineAt,omitempty"`
		RemoteAddr string `json:"remoteAddr"`
	}
	list := make([]sessionResp, 0, len(sessions))
	for _, ps := range sessions {
		list = append(list, sessionResp{OnlineAt: ps.OnlineAt, OfflineAt: ps.OfflineAt, RemoteAddr: ps.RemoteAddr})
	}
	data, _ := json.Marshal(map[string]interface{}{"code": 0, "data": list})
	w.Write(data)
}

// handleProxyDisable 禁用代理：置状态、停公网监听并断开活动连接、释放端口、
// 标记离线，在线 seeinps 推送 PROXY_REVOKE 使其转入低速重试（启用后自动恢复）。
// 禁用的代理不参与 7 天自动清理。
func (s *Server) handleProxyDisable(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	username := r.PathValue("username")
	proxyID := r.PathValue("proxyId")
	if _, err := s.store.GetProxy(username, proxyID); err != nil {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"code":2004,"message":"proxy not found"}`)
		return
	}
	if _, err := s.store.SetProxyStatus(username, proxyID, 0); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"code":5000,"message":"update failed"}`)
		return
	}
	s.publicMu.RLock()
	pl, online := s.publicConnsByProxy[username+":"+proxyID]
	s.publicMu.RUnlock()
	if online && pl.Client != nil && pl.Client.username == username {
		s.stopPublicListener(pl.Port)
		pl.killActiveConns()
		// 方案B：禁用只停转发，端口仍预留给该代理；启用量会恢复原端口，避免被他人抢占
		// 通知 seeinps 该代理已被禁用，使其清除转发端口并转入重试
		pl.Client.writeControl(protocol.NewMessage(protocol.TypeProxyRevoke, &protocol.ProxyRevokeData{ProxyID: proxyID, Reason: "proxy_disabled"}))
	}
	s.flushTraffic(username, proxyID)
	if err := s.store.MarkProxyOffline(username, proxyID); err != nil {
		logx.Warnf("[PROXY] mark offline error: %v", err)
	}
	logx.Infof("[PROXY] disabled: proxy=%s.%s", username, proxyID)
	s.audit(r, "proxy_disable", username+"."+proxyID, "status=0; 已断开连接，端口保留")
	fmt.Fprintf(w, `{"code":0,"message":"ok"}`)
}

// handleProxyEnable 启用代理：恢复状态；seeinps 周期重试 ALLOC_PORT 后自动上线
func (s *Server) handleProxyEnable(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	username := r.PathValue("username")
	proxyID := r.PathValue("proxyId")
	found, err := s.store.SetProxyStatus(username, proxyID, 1)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"code":5000,"message":"update failed"}`)
		return
	}
	if !found {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"code":2004,"message":"proxy not found"}`)
		return
	}
	logx.Infof("[PROXY] enabled: proxy=%s.%s", username, proxyID)
	s.audit(r, "proxy_enable", username+"."+proxyID, "status=1; 等待 seeinps 重试分配端口后自动上线")
	fmt.Fprintf(w, `{"code":0,"message":"ok"}`)
}

// handleAuditLogs 分页查询操作审计日志（?username=&action=&keyword=&source=&page=&page_size=）
func (s *Server) handleAuditLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	pageSize, _ := strconv.Atoi(q.Get("page_size"))
	start, _ := strconv.ParseInt(q.Get("start_time"), 10, 64)
	end, _ := strconv.ParseInt(q.Get("end_time"), 10, 64)
	list, total, err := s.store.ListAuditLogs(store.AuditFilter{
		Username:  q.Get("username"),
		Action:    q.Get("action"),
		Keyword:   q.Get("keyword"),
		Source:    q.Get("source"), // pm / ps / 空=全部
		StartTime: start,
		EndTime:   end,
		Page:      page,
		PageSize:  pageSize,
	})
	if err != nil {
		logx.Errorf("[AUDIT] query failed: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"code":5000,"message":"query failed"}`)
		return
	}
	type auditResp struct {
		ID        int64  `json:"id"`
		Username  string `json:"username"`
		Action    string `json:"action"`
		Target    string `json:"target"`
		Detail    string `json:"detail"`
		CreatedAt int64  `json:"createdAt"`
		Source    string `json:"source"`
	}
	items := make([]auditResp, 0, len(list))
	for _, a := range list {
		items = append(items, auditResp{ID: a.ID, Username: a.Username, Action: a.Action, Target: a.Target, Detail: a.Detail, CreatedAt: a.CreatedAt, Source: a.Source})
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 20
	}
	data, _ := json.Marshal(map[string]interface{}{"code": 0, "data": map[string]interface{}{
		"list": items, "total": total, "page": page, "pageSize": pageSize,
	}})
	w.Write(data)
}

// handleAuditLogsExport GET /api/v1/audit-logs/export：按筛选条件导出完整 CSV。
//
// 独立于分页接口的原因：ListAuditLogs 会把 PageSize 压到 200（防误查全表），
// 前端导出曾传 page_size=10000 被静默重置为 20 —— 导出的 CSV 永远只有 20 行。
// 导出走 ExportAuditLogs，并显式记录一条 audit_log_export（敏感数据外带需留痕）。
func (s *Server) handleAuditLogsExport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	start, _ := strconv.ParseInt(q.Get("start_time"), 10, 64)
	end, _ := strconv.ParseInt(q.Get("end_time"), 10, 64)
	f := store.AuditFilter{
		Username:  q.Get("username"),
		Action:    q.Get("action"),
		Keyword:   q.Get("keyword"),
		Source:    q.Get("source"),
		StartTime: start,
		EndTime:   end,
	}
	list, err := s.store.ExportAuditLogs(f, 200000)
	if err != nil {
		logx.Errorf("[AUDIT] export failed: %v", err)
		writeErr(w, http.StatusInternalServerError, 5000, "导出失败")
		return
	}
	// 记录导出行为本身（含条数与筛选条件），保证"审计数据被带走"可追溯
	s.audit(r, "audit_log_export", fmt.Sprintf("%d rows", len(list)),
		fmt.Sprintf("source=%s action=%s username=%s keyword=%s start=%d end=%d",
			f.Source, f.Action, f.Username, f.Keyword, f.StartTime, f.EndTime))

	name := "audit-logs-" + time.Now().Format("20060102-150405") + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
	_, _ = w.Write([]byte("\ufeff")) // BOM：Excel 直接打开不乱码
	bw := bufio.NewWriter(w)
	_, _ = bw.WriteString("时间,来源,操作者,动作,对象,详情\n")
	for _, a := range list {
		src := "seeinpm（中心端）"
		if a.Source == "ps" {
			src = "seeinps（服务端）"
		}
		row := []string{
			time.Unix(a.CreatedAt, 0).Format("2006-01-02 15:04:05"),
			src, a.Username, a.Action, a.Target, a.Detail,
		}
		for i, c := range row {
			if i > 0 {
				_ = bw.WriteByte(',')
			}
			_, _ = bw.WriteString(csvQuote(c))
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

// logsDir 运行日志目录（相对 CWD，与部署布局一致）
const logsDir = "logs"

// handleListLogFiles 列出 logs 目录下的 .log 文件（名称/大小/修改时间）
func (s *Server) handleListLogFiles(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	entries, err := os.ReadDir(logsDir)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"code":5000,"message":"read logs dir failed"}`)
		return
	}
	type logFile struct {
		Name     string `json:"name"`
		Size     int64  `json:"size"`
		Modified int64  `json:"modified"`
	}
	list := make([]logFile, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		list = append(list, logFile{Name: e.Name(), Size: info.Size(), Modified: info.ModTime().Unix()})
	}
	data, _ := json.Marshal(map[string]interface{}{"code": 0, "data": list})
	w.Write(data)
}

// tailLines 读文件尾部至多 maxLines 行（大文件只读末尾 tailBytes 以避免整读）
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
	if _, err := f.ReadAt(buf, offset); err != nil && err != io.EOF {
		return "", err
	}
	content := string(buf)
	// 若非从文件头读起，丢弃首行（可能是半行）
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

// handleLogFileContent 尾部读取运行日志（?file=seeinpm.log&lines=500&keyword=&download=1）
func (s *Server) handleLogFileContent(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	q := r.URL.Query()
	name := q.Get("file")
	// 只允许 logs 目录下的纯 .log 文件名，防目录穿越
	if name == "" || name != filepath.Base(name) || !strings.HasSuffix(name, ".log") || strings.Contains(name, "..") {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"code":2000,"message":"invalid file name"}`)
		return
	}
	lines, _ := strconv.Atoi(q.Get("lines"))
	if lines < 1 {
		lines = 200
	}
	if lines > 2000 {
		lines = 2000
	}
	keyword := q.Get("keyword")
	download := q.Get("download") == "1"
	path := filepath.Join(logsDir, name)
	if download {
		info, err := os.Stat(path)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, `{"code":2004,"message":"file not found"}`)
			return
		}
		if info.Size() > 50*1024*1024 {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"code":2000,"message":"file too large"}`)
			return
		}
		b, err := os.ReadFile(path)
		if err != nil {
			logx.Errorf("[LOG] read log file failed file=%s err=%v", name, err)
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, `{"code":5000,"message":"read failed"}`)
			return
		}
		// 运行日志外带需留痕：此前下载日志不留任何记录，等于可无声带走全部运行信息
		s.audit(r, "log_download", name, fmt.Sprintf("size=%d", len(b)))
		logx.Infof("[LOG] downloaded runtime log file=%s size=%d by=%s", name, len(b), operatorFrom(r))
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
		w.Write(b)
		return
	}
	content, err := tailLines(path, lines, 2*1024*1024)
	if err != nil {
		logx.Warnf("[LOG] tail log failed file=%s err=%v", name, err)
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"code":2004,"message":"file not found"}`)
		return
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
	data, _ := json.Marshal(map[string]interface{}{"code": 0, "data": map[string]string{"file": name, "content": content}})
	w.Write(data)
}

// getOnlineClient 按 username 取在线 B 端连接
func (s *Server) getOnlineClient(username string) (*Client, error) {
	c, active := s.isClientActive(username)
	if !active {
		return nil, fmt.Errorf("seeinps %s is not online", username)
	}
	return c, nil
}

// handlePSLoggingGet GET /api/v1/ps-logging?username=
// 经控制通道读取在线 B 端（seeinps）当前的日志级别与轮转配置。
func (s *Server) handlePSLoggingGet(w http.ResponseWriter, r *http.Request) {
	username := r.URL.Query().Get("username")
	client, err := s.getOnlineClient(username)
	if err != nil {
		writeErr(w, http.StatusBadGateway, 5002, err.Error())
		return
	}
	msg := protocol.NewMessage(protocol.TypeLoggingGetReq, &protocol.LoggingGetReqData{})
	resp, err := s.clientRequest(client, msg, 10*time.Second)
	if err != nil {
		writeErr(w, http.StatusGatewayTimeout, 5003, err.Error())
		return
	}
	if resp.Code == nil || *resp.Code != protocol.CodeOK {
		writeErr(w, http.StatusBadGateway, 5002, "seeinps 查询日志级别失败")
		return
	}
	b, _ := json.Marshal(resp.Data)
	d := &protocol.LoggingGetRespData{}
	_ = json.Unmarshal(b, d)
	writeOK(w, map[string]interface{}{
		"level": d.Level, "path": d.Path, "max_size": d.MaxSize, "max_backups": d.MaxBackups,
	})
}

// handlePSLoggingSet PUT /api/v1/ps-logging?username=  body {"level":"debug"}
// 经控制通道把日志级别下发给在线 B 端（seeinps）。
// 此前 A 端只能读 B 端日志、级别必须登录 B 端管理台改，与"A 端集中管理"的定位不一致。
func (s *Server) handlePSLoggingSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Level string `json:"level"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, 2000, "请求体格式错误")
		return
	}
	if _, ok := logx.ParseLevel(req.Level); !ok {
		writeErr(w, http.StatusBadRequest, 2000, "invalid log level (want debug/info/warn/error)")
		return
	}
	username := r.URL.Query().Get("username")
	client, err := s.getOnlineClient(username)
	if err != nil {
		writeErr(w, http.StatusBadGateway, 5002, err.Error())
		return
	}
	msg := protocol.NewMessage(protocol.TypeLoggingSetReq, &protocol.LoggingSetReqData{Level: req.Level})
	resp, err := s.clientRequest(client, msg, 10*time.Second)
	if err != nil {
		writeErr(w, http.StatusGatewayTimeout, 5003, err.Error())
		return
	}
	if resp.Code == nil || *resp.Code != protocol.CodeOK {
		writeErr(w, http.StatusBadGateway, 5002, "seeinps 拒绝了该日志级别")
		return
	}
	b, _ := json.Marshal(resp.Data)
	d := &protocol.LoggingSetRespData{}
	_ = json.Unmarshal(b, d)
	logx.Infof("[SYS] PS log level set user=%s level=%s by=%s", username, d.Level, operatorFrom(r))
	writeOK(w, map[string]interface{}{"level": d.Level})
}

// handlePSLogList 经控制通道获取指定 B 端（seeinps）的日志文件列表（?username=）
func (s *Server) handlePSLogList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	username := r.URL.Query().Get("username")
	client, err := s.getOnlineClient(username)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, `{"code":5002,"message":%q}`, err.Error())
		return
	}
	req := protocol.NewMessage(protocol.TypeLogListReq, &protocol.LogListReqData{})
	resp, err := s.clientRequest(client, req, 10*time.Second)
	if err != nil {
		w.WriteHeader(http.StatusGatewayTimeout)
		fmt.Fprintf(w, `{"code":5003,"message":%q}`, err.Error())
		return
	}
	if resp.Code == nil || *resp.Code != protocol.CodeOK {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, `{"code":5002,"message":"seeinps returned error"}`)
		return
	}
	b, _ := json.Marshal(resp.Data)
	d := &protocol.LogListRespData{}
	json.Unmarshal(b, d)
	if d.Files == nil {
		d.Files = []protocol.LogFileInfo{}
	}
	data, _ := json.Marshal(map[string]interface{}{"code": 0, "data": d.Files})
	w.Write(data)
}

// handlePSLogContent 经控制通道拉取指定 B 端（seeinps）的运行日志内容
// （?username=&file=&lines=&keyword=&download=1）。download=1 时以附件形式整文件下载。
func (s *Server) handlePSLogContent(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	username := q.Get("username")
	download := q.Get("download") == "1"
	client, err := s.getOnlineClient(username)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, `{"code":5002,"message":%q}`, err.Error())
		return
	}
	lines, _ := strconv.Atoi(q.Get("lines"))
	if lines < 1 {
		lines = 200
	}
	if lines > 2000 {
		lines = 2000
	}
	req := protocol.NewMessage(protocol.TypeLogContentReq, &protocol.LogContentReqData{
		File: q.Get("file"), Lines: lines, Keyword: q.Get("keyword"), Download: download,
	})
	resp, err := s.clientRequest(client, req, 30*time.Second)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusGatewayTimeout)
		fmt.Fprintf(w, `{"code":5003,"message":%q}`, err.Error())
		return
	}
	if resp.Code == nil || *resp.Code != protocol.CodeOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, `{"code":5002,"message":"seeinps returned error"}`)
		return
	}
	b, _ := json.Marshal(resp.Data)
	d := &protocol.LogContentRespData{}
	json.Unmarshal(b, d)
	if download {
		// 运维侧拉取 B 端运行日志需留痕
		s.audit(r, "ps_log_download", username+"/"+d.File, fmt.Sprintf("size=%d", len(d.Content)))
		logx.Infof("[LOG] downloaded PS runtime log user=%s file=%s size=%d by=%s",
			username, d.File, len(d.Content), operatorFrom(r))
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=\""+filepath.Base(d.File)+"\"")
		_, _ = w.Write([]byte(d.Content))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	data, _ := json.Marshal(map[string]interface{}{"code": 0, "data": map[string]string{"file": d.File, "content": d.Content}})
	_, _ = w.Write(data)
}

func extractToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return ""
}

// Control channel handlers
func (s *Server) handleControlConnection(conn net.Conn) {
	defer conn.Close()

	session, err := mux.Server(conn, mux.DefaultConfig())
	if err != nil {
		logx.Warnf("[CTRL] yamux error: %v", err)
		return
	}
	defer session.Close()

	controlStream, err := session.Accept()
	if err != nil {
		logx.Warnf("[CTRL] accept stream error: %v", err)
		return
	}
	defer controlStream.Close()

	codec := protocol.NewCodec()

	helloMsg, err := codec.ReadMessage(controlStream)
	if err != nil || helloMsg.Type != protocol.TypeHello {
		logx.Warnf("[CTRL] expected HELLO")
		return
	}
	helloDataBytes, _ := json.Marshal(helloMsg.Data)
	helloData := &protocol.HelloData{}
	json.Unmarshal(helloDataBytes, helloData)
	logx.Infof("[CTRL] hello: addr=%s version=%s platform=%s/%s", conn.RemoteAddr(), helloData.Version, helloData.OS, helloData.Arch)

	resp := protocol.NewResponse(helloMsg, protocol.CodeOK, &protocol.HelloRespData{
		ServerVersion: version.Version,
		Caps:          []string{"mux", "tls"},
	})
	codec.WriteMessage(controlStream, resp)

	registerMsg, err := codec.ReadMessage(controlStream)
	if err != nil || registerMsg.Type != protocol.TypeRegister {
		logx.Warnf("[CTRL] expected REGISTER")
		return
	}

	client, err := s.handleRegister(controlStream, registerMsg, session, helloData)
	if err != nil {
		logx.Warnf("[CTRL] register error: %v", err)
		return
	}
	logx.Infof("[CTRL] client registered: user=%s session=%s", client.username, client.sessionID)
	// 版本管理：升级后节点重连上报的版本与目标一致时推进升级状态（校验回报可能在重启瞬间丢失）
	completeUpgradeIfMatches(client.username, client.version)
	s.handleHeartbeat(controlStream, client)
}

// isClientActive 判断用户是否有真正活跃的控制连接：内存中存在、心跳新鲜
// （心跳间隔 10s，90s 未上报视为死亡）、且 mux 会话未关闭。
// 数据库 online_session 在 seeinpm 重启后会残留死值，不能单独作为在线依据。
func (s *Server) isClientActive(username string) (*Client, bool) {
	s.clientsMu.RLock()
	defer s.clientsMu.RUnlock()
	c, ok := s.clients[username]
	if !ok {
		return nil, false
	}
	if time.Since(c.lastPing) > 90*time.Second || c.session == nil || c.session.IsClosed() {
		return c, false
	}
	return c, true
}

func (s *Server) handleRegister(stream net.Conn, msg *protocol.Message, session *yamux.Session, hello *protocol.HelloData) (*Client, error) {
	codec := protocol.NewCodec()
	dataBytes, _ := json.Marshal(msg.Data)
	regData := &protocol.RegisterData{}
	json.Unmarshal(dataBytes, regData)

	user, err := s.store.GetUserByUsername(regData.Username)
	if err != nil {
		resp := protocol.NewResponse(msg, protocol.CodeAuthFailed, nil)
		codec.WriteMessage(stream, resp)
		return nil, fmt.Errorf("user not found: %s", regData.Username)
	}

	expectedHash := auth.HashAuthCode(regData.AuthHash, user.AuthCodeSalt)
	if expectedHash != user.AuthCodeHash {
		resp := protocol.NewResponse(msg, protocol.CodeAuthFailed, nil)
		codec.WriteMessage(stream, resp)
		return nil, fmt.Errorf("auth code mismatch for %s", regData.Username)
	}

	// 禁用用户拒绝注册（F-U2）：授权码本身有效也拒绝，seeinps 收到 1003 后转入低速重试，管理员启用后自动恢复
	if user.Status != 1 {
		resp := protocol.NewResponse(msg, protocol.CodeSessionRevoked, map[string]string{"reason": "user_disabled"})
		codec.WriteMessage(stream, resp)
		return nil, fmt.Errorf("user disabled: %s", regData.Username)
	}

	// 有效期已过拒绝注册：seeinps 收到 1003 转入低速重试，有效期改回后自动恢复
	if userExpired(user) {
		resp := protocol.NewResponse(msg, protocol.CodeSessionRevoked, map[string]string{"reason": "user_expired"})
		codec.WriteMessage(stream, resp)
		return nil, fmt.Errorf("user expired: %s", regData.Username)
	}

	if user.OnlineSession != nil {
		old, active := s.isClientActive(regData.Username)
		if active {
			resp := protocol.NewResponse(msg, protocol.CodeAuthCodeInUse, nil)
			codec.WriteMessage(stream, resp)
			return nil, fmt.Errorf("auth code already in use by %s", regData.Username)
		}
		// online_session 为残留值（seeinpm 重启后无人清理，或旧连接已死但心跳协程尚未退出）：
		// 踢掉旧连接并清除状态后放行注册，否则 seeinps 会永远被 1002 拒绝
		logx.Infof("[CTRL] replacing stale session for %s", regData.Username)
		if old != nil && old.session != nil {
			old.session.Close()
		}
		s.store.ClearUserOnlineStatus(regData.Username)
	}

	sessionID := protocol.GenerateID()
	if err := s.store.UpdateUserOnlineStatus(regData.Username, sessionID); err != nil {
		resp := protocol.NewResponse(msg, protocol.CodeInternal, nil)
		codec.WriteMessage(stream, resp)
		return nil, fmt.Errorf("update online status: %w", err)
	}

	client := &Client{
		username:      regData.Username,
		version:       hello.Version,
		goos:          hello.OS,
		goarch:        hello.Arch,
		sessionID:     sessionID,
		connected:     time.Now(),
		lastPing:      time.Now(),
		remoteAddr:    session.RemoteAddr().String(),
		session:       session,
		controlStream: stream,
		pending:       make(map[string]chan *protocol.Message),
	}

	s.clientsMu.Lock()
	s.clients[regData.Username] = client
	s.clientsMu.Unlock()

	s.sessionsMu.Lock()
	s.sessions[sessionID] = &Session{username: regData.Username, sessionID: sessionID, created: time.Now()}
	s.sessionsMu.Unlock()

	resp := protocol.NewResponse(msg, protocol.CodeOK, &protocol.RegisterRespData{SessionID: sessionID})
	return client, codec.WriteMessage(stream, resp)
}

func (s *Server) handleHeartbeat(stream net.Conn, client *Client) {
	codec := protocol.NewCodec()
	defer func() {
		// 按会话 ID 条件清理：若该连接是被新注册顶掉的旧连接，不能误删新会话的
		// 内存 client 与 DB 在线状态（否则 cleanup 例程会误释放新会话的端口）
		s.removeClientIfCurrent(client)
		s.store.ClearUserOnlineStatusIfSession(client.username, client.sessionID)
	}()

	for {
		msg, err := codec.ReadMessage(stream)
		if err != nil {
			return
		}
		switch msg.Type {
		case protocol.TypeHeartbeat:
			s.clientsMu.Lock()
			client.lastPing = time.Now()
			s.clientsMu.Unlock()
			// 配额状态随心跳响应下发（B 端缓存后在 web 页展示进度/重置日期/超额提醒）
			var qdata interface{}
			if st, err := s.store.GetQuotaStatus(client.username, time.Now()); err == nil && st.Enabled {
				qdata = quotaDataOf(st)
			}
			client.writeControl(protocol.NewResponse(msg, protocol.CodeOK, qdata))
		case protocol.TypeAllocPort:
			s.handleAllocPort(msg, client)
		case protocol.TypeReleasePort:
			s.handleReleasePort(msg, client)
		case protocol.TypeAuditSync:
			// 混合架构：B 端上报审计记录，按 username 区分来源写入统一 audit_logs
			s.handleAuditSync(msg, client)
		case protocol.TypeUpgradeReport:
			// 版本管理：B 端上报升级包接收结果（downloaded/failed），最终成功以重连后 HELLO 版本为准
			s.handleUpgradeReport(msg, client)
		case protocol.TypeVersionListReq:
			// B 端自主升级：查询本端可用版本列表（按节点平台过滤）
			s.handleVersionListReq(msg, client)
		case protocol.TypeVersionPullReq:
			// B 端自主升级：按版本号请求拉取升级包（应答元信息后经 0x05 流直传）
			s.handleVersionPullReq(msg, client)
		case protocol.TypeLogListResp, protocol.TypeLogContentResp, protocol.TypeUpgradePushResp:
			// A 端发起的日志拉取/升级推送响应，转交等待协程
			client.pendingMu.Lock()
			ch, ok := client.pending[msg.ID]
			if ok {
				delete(client.pending, msg.ID)
			}
			client.pendingMu.Unlock()
			if ok {
				ch <- msg
			}
		default:
			client.writeControl(protocol.NewResponse(msg, protocol.CodeUnsupportedType, nil))
		}
	}
}

// handleAuditSync 处理 B 端（seeinps）上报的审计记录：写入统一 audit_logs（source='ps'）。
// it.LocalID 为 B 端本地行 id，写入 ext_id：重连补传会重复投递同一批记录，
// 靠 (source, ext_id) 唯一索引 + INSERT OR IGNORE 幂等去重，因此补传可安全重放。
func (s *Server) handleAuditSync(msg *protocol.Message, client *Client) {
	b, _ := json.Marshal(msg.Data)
	d := &protocol.AuditSyncData{}
	if err := json.Unmarshal(b, d); err != nil || len(d.Items) == 0 {
		return
	}
	inserted := 0
	for _, it := range d.Items {
		username := it.Username
		if username == "" {
			username = client.username
		}
		createdAt := it.CreatedAt
		if createdAt <= 0 {
			createdAt = time.Now().Unix()
		}
		if err := s.store.InsertAuditLogFromPS(username, it.Action, it.Target, it.Detail, createdAt, it.LocalID); err != nil {
			logx.Errorf("[AUDIT] ps-sync insert error: action=%s user=%s err=%v", it.Action, username, err)
			continue
		}
		inserted++
	}
	logx.Debugf("[AUDIT] ps-sync from %s: received=%d stored=%d (dup or failed skipped)",
		client.username, len(d.Items), inserted)
}

// clientRequest 通过控制通道向在线 B 端发送请求并等待响应（10s 超时）
func (s *Server) clientRequest(client *Client, msg *protocol.Message, timeout time.Duration) (*protocol.Message, error) {
	ch := make(chan *protocol.Message, 1)
	client.pendingMu.Lock()
	client.pending[msg.ID] = ch
	client.pendingMu.Unlock()
	defer func() {
		client.pendingMu.Lock()
		delete(client.pending, msg.ID)
		client.pendingMu.Unlock()
	}()
	if err := client.writeControl(msg); err != nil {
		return nil, err
	}
	select {
	case resp := <-ch:
		return resp, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("request %s timeout", msg.Type)
	}
}

// writeControl 带锁写控制流：心跳应答/端口响应与 SESSION_REVOKE 推送共用一把锁防帧交错。
// 节点链路可能半死（TCP 未断但读端停止），无截止时间会让写入永久阻塞并卡死
// 升级/端口分配等协程——30s 写失败快速返回，由调用方触发会话清理，节点自动重连自愈。
func (c *Client) writeControl(msg *protocol.Message) error {
	c.ctrlWriteMu.Lock()
	defer c.ctrlWriteMu.Unlock()
	c.controlStream.SetWriteDeadline(time.Now().Add(30 * time.Second))
	defer c.controlStream.SetWriteDeadline(time.Time{})
	return protocol.NewCodec().WriteMessage(c.controlStream, msg)
}

func (s *Server) handleAllocPort(msg *protocol.Message, client *Client) {
	dataBytes, _ := json.Marshal(msg.Data)
	d := &protocol.AllocPortData{}
	json.Unmarshal(dataBytes, d)

	// 代理被禁用：拒绝分配（1006），seeinps 转入低速重试，启用后自动恢复
	if p, err := s.store.GetProxy(client.username, d.ProxyID); err == nil && p.Status != 1 {
		logx.Warnf("[ALLOC] rejected, proxy disabled: proxy=%s user=%s", d.ProxyID, client.username)
		client.writeControl(protocol.NewResponse(msg, protocol.CodeProxyDisabled, nil))
		return
	}

	user, err := s.store.GetUserByUsername(client.username)
	if err != nil {
		client.writeControl(protocol.NewResponse(msg, protocol.CodeAuthFailed, nil))
		return
	}
	// 用户已过期：拒绝分配并断开（30s 过期定时器也会兜底踢线）
	if userExpired(user) {
		logx.Warnf("[ALLOC] rejected, user expired: proxy=%s user=%s", d.ProxyID, client.username)
		client.writeControl(protocol.NewResponse(msg, protocol.CodeSessionRevoked, map[string]string{"reason": "user_expired"}))
		return
	}
	// 周期流量配额：超额时除 web-ui 外一律拒绝新建（B 端转入低速重试，下一周期自动恢复）
	if d.ProxyID != webUIProxyID && s.store.QuotaExceeded(client.username, time.Now()) {
		logx.Warnf("[ALLOC] rejected, traffic quota exceeded: proxy=%s user=%s", d.ProxyID, client.username)
		client.writeControl(protocol.NewResponse(msg, protocol.CodeTrafficQuotaExceeded, map[string]string{"reason": "traffic_quota"}))
		return
	}
	// 同步用户带宽限速（对象共享，SetRate 对存量转发即时生效）
	s.syncUserLimiter(client.username, user.MaxMbps)
	// 端口数量配额：仅对"新分配"生效，代理重连的端口复用不受限
	if user.MaxPorts > 0 {
		if _, err := s.store.GetPortByUserProxy(client.username, d.ProxyID); err != nil {
			if n, err := s.store.CountActiveAllocationsByUser(client.username); err == nil && n >= user.MaxPorts {
				logx.Warnf("[ALLOC] rejected, port quota reached: proxy=%s user=%s quota=%d", d.ProxyID, client.username, user.MaxPorts)
				client.writeControl(protocol.NewResponse(msg, protocol.CodePortPoolExhausted, map[string]string{"reason": "port_quota"}))
				return
			}
		}
	}

	s.publicMu.RLock()
	if oldPL, ok := s.publicConnsByProxy[client.username+":"+d.ProxyID]; ok {
		s.publicMu.RUnlock()
		s.stopPublicListener(oldPL.Port)
	} else {
		s.publicMu.RUnlock()
	}

	// 用户端口池：非空时新端口只能落在用户池与全局池的交集中
	var userRanges []portpool.Range
	for _, rg := range user.PortRanges {
		userRanges = append(userRanges, portpool.Range{Start: rg.Start, End: rg.End})
	}
	alloc, err := s.portPool.Allocate(client.username, d.ProxyID, d.Type, d.Port, userRanges)
	if err != nil {
		logx.Errorf("[ALLOC] allocate error: proxy=%s user=%s err=%v", d.ProxyID, client.username, err)
		client.writeControl(protocol.NewResponse(msg, protocol.CodePortConflict, nil))
		return
	}

	if err := s.startPublicListener(alloc.Port, d.ProxyID, d.Type, client); err != nil {
		logx.Errorf("[ALLOC] start listener error: port=%d proxy=%s err=%v", alloc.Port, d.ProxyID, err)
		s.portPool.Release(client.username, d.ProxyID)
		client.writeControl(protocol.NewResponse(msg, protocol.CodeInternal, nil))
		return
	}

	// 代理上线登记：创建/更新代理记录并开启一条会话（重连复用时不重复开会话）
	if _, err := s.store.MarkProxyOnline(client.username, d.ProxyID, d.Type, client.remoteAddr); err != nil {
		logx.Warnf("[ALLOC] mark proxy online error: %v", err)
	}
	if err := s.store.TrimProxySessions(client.username, d.ProxyID, 50); err != nil {
		logx.Warnf("[ALLOC] trim sessions error: %v", err)
	}

	logx.Infof("[ALLOC] allocated: proxy=%s type=%s port=%d user=%s", d.ProxyID, d.Type, alloc.Port, client.username)
	client.writeControl(protocol.NewResponse(msg, protocol.CodeOK, &protocol.AllocPortRespData{ProxyID: d.ProxyID, Port: alloc.Port}))
}

func (s *Server) handleReleasePort(msg *protocol.Message, client *Client) {
	dataBytes, _ := json.Marshal(msg.Data)
	d := &protocol.ReleasePortData{}
	json.Unmarshal(dataBytes, d)

	if alloc, err := s.store.GetPortByUserProxy(client.username, d.ProxyID); err == nil {
		s.stopPublicListener(alloc.Port)
	}
	s.portPool.Release(client.username, d.ProxyID)
	s.flushTraffic(client.username, d.ProxyID)
	if err := s.store.MarkProxyOffline(client.username, d.ProxyID); err != nil {
		logx.Warnf("[RELEASE] mark proxy offline error: %v", err)
	}
	logx.Infof("[RELEASE] released: proxy=%s user=%s", d.ProxyID, client.username)
	client.writeControl(protocol.NewResponse(msg, protocol.CodeOK, nil))
}

// kickClient 断开用户的控制连接并停用其公网转发（禁用/重置授权码/过期时调用）。
// 端口在池中仍预留给该用户/代理，只有用户或代理被删除才真正放回公共池。
// 先推送 SESSION_REVOKE 通知 seeinps 停止重连（尽力而为），再关闭会话；
// 心跳协程退出时的 deferred 清理会清内存 client 与 DB 在线状态。
func (s *Server) kickClient(username, reason string) {
	s.clientsMu.RLock()
	c, ok := s.clients[username]
	s.clientsMu.RUnlock()
	if ok {
		if c.controlStream != nil {
			c.writeControl(protocol.NewMessage(protocol.TypeSessionRevoke, map[string]string{"reason": reason}))
		}
		if c.session != nil {
			c.session.Close()
		}
		logx.Infof("[USER] kicked: user=%s reason=%s", username, reason)
	} else {
		// 无活跃连接也要清 DB 残留在线状态，否则重连被 1002 语义误判
		s.store.ClearUserOnlineStatus(username)
	}
	s.stopUserForwarding(username)
}

// stopUserForwarding 停用该用户全部公网转发监听（断开/禁用/重置授权码/过期时），
// 但保留端口在公共池中的预留：端口仍归该代理所有，直到代理或用户被删除才放回公共池。
func (s *Server) stopUserForwarding(username string) {
	allocs, err := s.store.GetAllocationsByUser(username)
	if err != nil {
		logx.Warnf("[USER] stop forwarding error: user=%s err=%v", username, err)
		return
	}
	for _, a := range allocs {
		s.stopPublicListener(a.Port)
		s.flushTraffic(a.UserID, a.ProxyID)
		if err := s.store.MarkProxyOffline(a.UserID, a.ProxyID); err != nil {
			logx.Warnf("[USER] mark proxy offline error: %v", err)
		}
	}
}

// stopUserForwardingExceptWebUI 停用该用户除 web-ui 外的全部公网转发（周期流量超额时）：
// 端口预留保留，管理页 web-ui 不受影响以便用户查看用量；seeinps 对断开的代理转入低速重试。
func (s *Server) stopUserForwardingExceptWebUI(username string) {
	allocs, err := s.store.GetAllocationsByUser(username)
	if err != nil {
		logx.Warnf("[QUOTA] stop forwarding error: user=%s err=%v", username, err)
		return
	}
	for _, a := range allocs {
		if a.ProxyID == webUIProxyID {
			continue
		}
		s.stopPublicListener(a.Port)
		s.flushTraffic(a.UserID, a.ProxyID)
		if err := s.store.MarkProxyOffline(a.UserID, a.ProxyID); err != nil {
			logx.Warnf("[QUOTA] mark proxy offline error: %v", err)
		}
	}
}

// quotaDataOf 存储层快照 → 控制协议载荷
func quotaDataOf(st store.QuotaStatus) *protocol.QuotaStatusData {
	return &protocol.QuotaStatusData{
		Enabled: st.Enabled, Period: st.Period, Used: st.Used, Limit: st.Limit,
		PeriodStart: st.PeriodStart, PeriodEnd: st.PeriodEnd, Exceeded: st.Exceeded,
	}
}

// pushQuotaStatus 向在线 B 端即时推送一次配额状态（状态跃迁时用；常态由心跳响应携带）
func (s *Server) pushQuotaStatus(username string, st store.QuotaStatus) {
	s.clientsMu.RLock()
	c, ok := s.clients[username]
	s.clientsMu.RUnlock()
	if !ok || c.controlStream == nil {
		return
	}
	c.writeControl(protocol.NewMessage(protocol.TypeQuotaStatus, quotaDataOf(st)))
}

// isQuotaEnforced / markQuotaEnforced 维护“本周期已因超额断过存量”标记，防重复触发并支持恢复检测
func (s *Server) isQuotaEnforced(username string) bool {
	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	return s.quotaEnforced[username]
}

func (s *Server) markQuotaEnforced(username string, enforced bool) {
	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	if enforced {
		s.quotaEnforced[username] = true
	} else {
		delete(s.quotaEnforced, username)
	}
}

// refreshQuotaState 处理单个用户的配额状态跃迁：
// 首次超额 → 断开除 web-ui 外全部存量转发 + 审计 + 即时推送；
// 周期滚动/上调上限后恢复 → 清标记 + 推送（B 端低速重试自动重建代理）。
// 由 quotaWatcher 周期调用（在线用户），也在管理员编辑配额保存后立即调用。
func (s *Server) refreshQuotaState(username string) {
	st, err := s.store.GetQuotaStatus(username, time.Now())
	if err != nil {
		return
	}
	exceeded := st.Enabled && st.Exceeded
	enforced := s.isQuotaEnforced(username)
	if exceeded && !enforced {
		s.markQuotaEnforced(username, true)
		logx.Warnf("[QUOTA] exceeded, stopping non-web-ui forwarding: user=%s used=%d limit=%d period=%s", username, st.Used, st.Limit, st.Period)
		s.stopUserForwardingExceptWebUI(username)
		s.store.InsertAuditLog(username, "user_quota_exceeded", "", fmt.Sprintf("used=%d limit=%d period=%s", st.Used, st.Limit, st.Period))
		s.pushQuotaStatus(username, st)
	} else if !exceeded && enforced {
		s.markQuotaEnforced(username, false)
		logx.Infof("[QUOTA] recovered (period rolled or limit raised), forwarding may resume: user=%s used=%d limit=%d", username, st.Used, st.Limit)
		s.store.InsertAuditLog(username, "user_quota_recovered", "", fmt.Sprintf("used=%d limit=%d", st.Used, st.Limit))
		s.pushQuotaStatus(username, st)
	}
}

// quotaWatcher 每 30s 对在线用户做配额状态跃迁检测（在线跑超即时断存量；周期滚动自动恢复）
func (s *Server) quotaWatcher(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		s.clientsMu.RLock()
		usernames := make([]string, 0, len(s.clients))
		for u := range s.clients {
			usernames = append(usernames, u)
		}
		s.clientsMu.RUnlock()
		for _, u := range usernames {
			s.refreshQuotaState(u)
		}
	}
}

// releaseUserPorts 停止该用户全部公网监听并把端口释放回公共池（仅删除用户时调用）
func (s *Server) releaseUserPorts(username string) {
	allocs, err := s.store.GetAllocationsByUser(username)
	if err != nil {
		logx.Warnf("[USER] release ports error: user=%s err=%v", username, err)
		return
	}
	for _, a := range allocs {
		s.stopPublicListener(a.Port)
		s.portPool.Release(a.UserID, a.ProxyID)
		s.flushTraffic(a.UserID, a.ProxyID)
		if err := s.store.MarkProxyOffline(a.UserID, a.ProxyID); err != nil {
			logx.Warnf("[USER] mark proxy offline error: %v", err)
		}
	}
}

// removeClientIfCurrent 仅当 map 中仍是该 client 时才移除，
// 防止被顶掉的旧连接延迟退出时误删同用户新注册的 client
func (s *Server) removeClientIfCurrent(c *Client) {
	s.clientsMu.Lock()
	removed := false
	if cur, ok := s.clients[c.username]; ok && cur == c {
		delete(s.clients, c.username)
		s.sessionsMu.Lock()
		delete(s.sessions, c.sessionID)
		s.sessionsMu.Unlock()
		removed = true
	}
	s.clientsMu.Unlock()
	if removed {
		// seeinps 断开：其全部代理转为离线并关闭会话，流量落库
		s.flushUserTraffic(c.username)
		if err := s.store.MarkUserProxiesOffline(c.username); err != nil {
			logx.Warnf("[CTRL] mark user proxies offline error: %v", err)
		}
		logx.Infof("[CTRL] client disconnected: user=%s", c.username)
	}
}

func (s *Server) shutdown() {
	logx.Infof("[SYS] shutting down")

	s.flushAllTraffic()

	s.publicMu.Lock()
	for port, pl := range s.publicConns {
		logx.Infof("[SYS] stopping listener on :%d", port)
		if pl.Ln != nil {
			pl.Ln.Close()
		}
		delete(s.publicConns, port)
		delete(s.publicConnsByProxy, pl.mapKey())
	}
	s.publicMu.Unlock()

	s.clientsMu.RLock()
	for username, c := range s.clients {
		logx.Infof("[SYS] disconnecting client: user=%s", username)
		if c.session != nil {
			c.session.Close()
		}
	}
	s.clientsMu.RUnlock()

	s.clientsMu.Lock()
	s.clients = make(map[string]*Client)
	s.clientsMu.Unlock()

	s.sessionsMu.Lock()
	s.sessions = make(map[string]*Session)
	s.sessionsMu.Unlock()

	logx.Infof("[SYS] stopped")
}

func main() {
	confPath := flag.String("conf", "conf/seeinpm.toml", "config file")
	flag.Parse()

	cfg, err := config.LoadPMConfig(*confPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Config error: %v\n", err)
		os.Exit(1)
	}

	// 运行日志：级别过滤 + 行首时间戳 + 按天/大小轮转（conf [logging] level/max_size/max_backups 生效）。
	// restore 负责冲刷管道内残留日志到磁盘，退出前必须执行（defer 兜底 + 显式调用）
	restore := logx.Install(cfg.Logging.Path, "seeinpm", cfg.Logging.Level, cfg.Logging.MaxSize, cfg.Logging.MaxBackups)
	defer restore()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		logx.Infof("[SYS] shutdown signal received")
		cancel()
	}()

	srv, err := NewServer(cfg)
	if err != nil {
		logx.Errorf("[SYS] init error: %v", err)
		restore()
		os.Exit(1)
	}
	srv.configPath = *confPath

	if err := srv.Start(ctx); err != nil {
		logx.Errorf("[SYS] fatal: %v", err)
		restore()
		os.Exit(1)
	}
}
