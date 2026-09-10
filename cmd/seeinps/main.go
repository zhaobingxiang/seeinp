package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/seeinp/seeinp/internal/auth"
	"github.com/seeinp/seeinp/internal/config"
	"github.com/seeinp/seeinp/internal/guard"
	"github.com/seeinp/seeinp/internal/logx"
	"github.com/seeinp/seeinp/internal/mux"
	"github.com/seeinp/seeinp/internal/protocol"
	"github.com/seeinp/seeinp/internal/store"
	"github.com/seeinp/seeinp/internal/udpframing"
	"github.com/seeinp/seeinp/internal/version"
)

type Client struct {
	config     *config.PSConfig
	configPath string // 配置文件路径，用于日志级别等设置持久化写回
	store      *store.PSStore
	jwt        *auth.JWTManager
	guard      *guard.LoginGuard

	linkMu    sync.Mutex
	link      *controlLink
	sessionID string
	// revoked: 授权码被重置（停止重连，等待 B端重新绑定）；用户禁用走低速重试等管理员启用
	revoked atomic.Bool
	// lastPing: 最近一次心跳成功时间（unix 秒），供 B端仪表盘展示
	lastPing atomic.Int64
	// quotaCache: A 端同步的周期流量配额状态（*protocol.QuotaStatusData），供 web-ui 展示用量/重置日期/超额提醒
	quotaCache    atomic.Value
	quotaSyncedAt atomic.Int64

	// rootCtx 为 main 的生命周期 ctx；controlEpoch 用于重绑后重启控制循环并让旧循环退出
	rootCtx      context.Context
	controlEpoch atomic.Int64

	// 审计补传：链路不在线时记录留在本地（synced=0），连接恢复后由 auditSyncLoop 补推；
	// auditPoke 用于新记录落库后立即触发补传，避免等下一个周期。
	auditPoke     chan struct{}
	auditLoopOnce sync.Once

	// loginFailAt 登录失败审计的按 IP 节流（防 spraying 刷爆审计表）
	loginFailMu sync.Mutex
	loginFailAt map[string]time.Time

	proxiesMu sync.RWMutex
	proxies   map[string]*Proxy
}

type Proxy struct {
	ID          string
	Type        string
	LocalAddr   string
	LocalPort   int
	ForwardPort int

	// 运维代理（ops_http）专属
	ProxyUsername     string
	ProxyPasswordHash string
	ACLRaw            string
	aclNets           []*net.IPNet
}

// DefaultOpsACL 运维代理默认 ACL：仅内网网段（F-P5）
var DefaultOpsACL = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}

// errPortPoolExhausted seeinpm 对 ALLOC 失败统一回 3001；seeinps 只发随机端口申请，
// 被拒即端口池无可用端口，向前端透出友好提示
var errPortPoolExhausted = errors.New("端口池获取失败，请联系管理员")

// errPortQuotaExhausted seeinpm 回 3002：用户端口数量配额已用满，新代理无法再分配端口
var errPortQuotaExhausted = errors.New("端口数量已达上限，请联系管理员")

// errTrafficQuotaExceeded seeinpm 回 3006：本周期总流量已达上限，除 web-ui 外代理停止转发，周期重置后自动恢复
var errTrafficQuotaExceeded = errors.New("本周期流量已达上限，代理暂时不可用，周期重置后将自动恢复")

type proxySnapshot struct {
	LocalAddr, ProxyUsername, ProxyPasswordHash, ACLRaw string
	LocalPort                                           int
}

func (p *Proxy) snapshot() proxySnapshot {
	return proxySnapshot{
		LocalAddr: p.LocalAddr, LocalPort: p.LocalPort,
		ProxyUsername: p.ProxyUsername, ProxyPasswordHash: p.ProxyPasswordHash, ACLRaw: p.ACLRaw,
	}
}

func (p *Proxy) restore(s proxySnapshot) {
	p.LocalAddr, p.LocalPort = s.LocalAddr, s.LocalPort
	p.ProxyUsername, p.ProxyPasswordHash, p.ACLRaw = s.ProxyUsername, s.ProxyPasswordHash, s.ACLRaw
	if nets, err := parseOpsACL(p.ACLRaw); err == nil {
		p.aclNets = nets
	}
}

// parseOpsACL 解析 ACL JSON；无配置时用默认内网段；非法条目忽略
func parseOpsACL(raw string) ([]*net.IPNet, error) {
	list := DefaultOpsACL
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &list); err != nil {
			return nil, fmt.Errorf("invalid acl json: %w", err)
		}
	}
	var nets []*net.IPNet
	for _, cidr := range list {
		if _, n, err := net.ParseCIDR(strings.TrimSpace(cidr)); err == nil {
			nets = append(nets, n)
		}
	}
	if len(nets) == 0 {
		return nil, fmt.Errorf("acl contains no valid cidr")
	}
	return nets, nil
}

// controlLink 封装注册成功后的控制流：单读协程分发，请求/响应按消息 ID 关联
type controlLink struct {
	stream    net.Conn
	codec     *protocol.Codec
	writeMu   sync.Mutex
	pendingMu sync.Mutex
	pending   map[string]chan *protocol.Message
	done      chan struct{}
	closeOnce sync.Once
	// client 指向所属 Client（日志拉取等回调用），由 connectOnce 赋值
	client *Client
	// onRevoke 收到 SESSION_REVOKE 时回调（参数为 reason）；
	// auth_code_reset 停止重连等 B端重绑，user_disabled 断开后低速重试
	onRevoke func(reason string)
	// onProxyRevoke 收到 PROXY_REVOKE 时回调（代理被 seeinpm 禁用）：清除转发端口转入重试
	onProxyRevoke func(proxyID, reason string)
}

func newControlLink(stream net.Conn) *controlLink {
	return &controlLink{
		stream:  stream,
		codec:   protocol.NewCodec(),
		pending: make(map[string]chan *protocol.Message),
		done:    make(chan struct{}),
	}
}

func (l *controlLink) send(msg *protocol.Message) error {
	l.writeMu.Lock()
	defer l.writeMu.Unlock()
	return l.codec.WriteMessage(l.stream, msg)
}

func (l *controlLink) request(msg *protocol.Message, timeout time.Duration) (*protocol.Message, error) {
	ch := make(chan *protocol.Message, 1)
	if l.isClosed() {
		return nil, fmt.Errorf("control link closed")
	}
	l.pendingMu.Lock()
	l.pending[msg.ID] = ch
	l.pendingMu.Unlock()
	defer func() {
		l.pendingMu.Lock()
		delete(l.pending, msg.ID)
		l.pendingMu.Unlock()
	}()
	if err := l.send(msg); err != nil {
		return nil, err
	}
	select {
	case resp := <-ch:
		return resp, nil
	case <-l.done:
		return nil, fmt.Errorf("control link closed")
	case <-time.After(timeout):
		return nil, fmt.Errorf("request %s timeout", msg.Type)
	}
}

func (l *controlLink) readLoop() {
	for {
		l.stream.SetReadDeadline(time.Now().Add(35 * time.Second))
		msg, err := l.codec.ReadMessage(l.stream)
		if err != nil {
			l.close()
			return
		}
		// 会话吊销：授权码重置则停止重连等 B端重绑；用户禁用断开后靠 1003 转入低速重试
		if msg.Type == protocol.TypeSessionRevoke {
			reason := ""
			if msg.Data != nil {
				if m, ok := msg.Data.(map[string]interface{}); ok {
					if v, ok := m["reason"].(string); ok {
						reason = v
					}
				}
			}
			logx.Infof("[CTRL] session revoke received, disconnecting: reason=%s", reason)
			if l.onRevoke != nil {
				l.onRevoke(reason)
			}
			l.close()
			return
		}
		// 代理被 seeinpm 禁用：清除该代理的转发端口，由 resyncLoop 低速重试
		if msg.Type == protocol.TypeProxyRevoke {
			proxyID, reason := "", ""
			if m, ok := msg.Data.(map[string]interface{}); ok {
				if v, ok := m["proxyId"].(string); ok {
					proxyID = v
				}
				if v, ok := m["reason"].(string); ok {
					reason = v
				}
			}
			logx.Infof("[CTRL] proxy revoke received: proxy=%s reason=%s", proxyID, reason)
			if l.onProxyRevoke != nil {
				l.onProxyRevoke(proxyID, reason)
			}
			continue
		}
		// A 端按需拉取 B 端运行日志（混合架构：运行日志留本地，PM 侧经控制通道读取）
		if msg.Type == protocol.TypeLogListReq {
			if c := l.client; c != nil {
				l.send(c.handleLogListReq(msg))
			}
			continue
		}
		if msg.Type == protocol.TypeLogContentReq {
			if c := l.client; c != nil {
				l.send(c.handleLogContentReq(msg))
			}
			continue
		}
		// A 端在线查询/修改 B 端日志级别（LOGGING_GET_REQ / LOGGING_SET_REQ）
		if msg.Type == protocol.TypeLoggingGetReq {
			if c := l.client; c != nil {
				l.send(c.handleLoggingGetReq(msg))
			}
			continue
		}
		if msg.Type == protocol.TypeLoggingSetReq {
			if c := l.client; c != nil {
				l.send(c.handleLoggingSetReq(msg))
			}
			continue
		}
		// A 端推送升级包（随后经 0x05 数据流传输二进制）
		if msg.Type == protocol.TypeUpgradePush {
			if c := l.client; c != nil {
				l.send(c.handleUpgradePush(msg))
			}
			continue
		}
		// 周期流量配额状态同步：心跳响应携带 / 状态跃迁时 QUOTA_STATUS 主动推送，缓存供 web-ui 展示
		if msg.Type == protocol.TypeHeartbeatResp || msg.Type == protocol.TypeQuotaStatus {
			if c := l.client; c != nil {
				c.updateQuotaCache(msg)
			}
			continue
		}
		l.pendingMu.Lock()
		ch, ok := l.pending[msg.ID]
		l.pendingMu.Unlock()
		if ok {
			ch <- msg
		}
		// 无 pending 关联的消息（如心跳响应）直接忽略
	}
}

func (l *controlLink) close() {
	l.closeOnce.Do(func() {
		close(l.done)
		l.stream.Close()
	})
}

func (l *controlLink) isClosed() bool {
	select {
	case <-l.done:
		return true
	default:
		return false
	}
}

func NewClient(cfg *config.PSConfig, psStore *store.PSStore, jwt *auth.JWTManager) *Client {
	return &Client{
		config:     cfg,
		store:      psStore,
		jwt:        jwt,
		guard:      guard.New(),
		proxies:    make(map[string]*Proxy),
		auditPoke:  make(chan struct{}, 1),
		loginFailAt: make(map[string]time.Time),
	}
}

func (c *Client) setLink(l *controlLink, sessionID string) {
	c.linkMu.Lock()
	c.link = l
	c.sessionID = sessionID
	c.linkMu.Unlock()
	// 连接建立即视为一次成功心跳，后续由 heartbeatLoop 每 10s 刷新
	c.lastPing.Store(time.Now().Unix())
	// 链路恢复：把断线期间累积的未同步审计补传给 seeinpm（按 id 幂等，可安全重放）
	c.startAuditSyncLoop()
	c.pokeAuditSync()
}

func (c *Client) clearLink(l *controlLink) {
	c.linkMu.Lock()
	if c.link == l {
		c.link = nil
	}
	c.linkMu.Unlock()
}

func (c *Client) getLink() *controlLink {
	c.linkMu.Lock()
	defer c.linkMu.Unlock()
	if c.link == nil || c.link.isClosed() {
		return nil
	}
	return c.link
}

func (c *Client) loadProxies() error {
	list, err := c.store.ListProxies()
	if err != nil {
		return err
	}
	for _, p := range list {
		proxy := &Proxy{
			ID:                p.ProxyID,
			Type:              p.Type,
			LocalAddr:         p.LocalAddr,
			LocalPort:         p.LocalPort,
			ForwardPort:       p.PublicPort,
			ProxyUsername:     p.ProxyUsername,
			ProxyPasswordHash: p.ProxyPassword,
			ACLRaw:            p.ACL,
		}
		if p.Type == "ops_http" {
			nets, err := parseOpsACL(p.ACL)
			if err != nil {
				logx.Warnf("[PROXY] acl invalid, fallback to default: proxy=%s err=%v", p.ProxyID, err)
				nets, _ = parseOpsACL("")
			}
			proxy.aclNets = nets
		}
		c.proxies[p.ProxyID] = proxy
	}
	return nil
}

func (c *Client) registerCredentials() (string, string) {
	if u, err := c.store.GetLocalUser(); err == nil && u.SeeinpmUser != "" {
		code := u.AuthCode
		if code == "" {
			code = c.config.Auth.AuthCode
		}
		return u.SeeinpmUser, code
	}
	return c.config.Auth.Username, c.config.Auth.AuthCode
}

func (c *Client) connectOnce(ctx context.Context) error {
	tlsConfig := &tls.Config{InsecureSkipVerify: c.config.TLS.SkipVerify}
	addr := c.config.Server.ServerAddr
	logx.Infof("[CTRL] connecting to %s", addr)
	conn, err := tls.Dial("tcp", addr, tlsConfig)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	session, err := mux.Client(conn, mux.DefaultConfig())
	if err != nil {
		return fmt.Errorf("yamux: %w", err)
	}
	defer session.Close()
	controlStream, err := session.Open()
	if err != nil {
		return fmt.Errorf("open stream: %w", err)
	}
	defer controlStream.Close()
	codec := protocol.NewCodec()

	helloMsg := protocol.NewMessage(protocol.TypeHello, &protocol.HelloData{Version: version.Version, Arch: runtime.GOARCH, OS: runtime.GOOS})
	if err := codec.WriteMessage(controlStream, helloMsg); err != nil {
		return fmt.Errorf("hello write: %w", err)
	}
	helloResp, err := codec.ReadMessage(controlStream)
	if err != nil {
		return fmt.Errorf("hello read: %w", err)
	}
	if helloResp.Code == nil || *helloResp.Code != protocol.CodeOK {
		return fmt.Errorf("HELLO failed")
	}
	logx.Infof("[CTRL] handshake ok")

	regUser, regCode := c.registerCredentials()
	registerMsg := protocol.NewMessage(protocol.TypeRegister, &protocol.RegisterData{Username: regUser, AuthHash: regCode})
	if err := codec.WriteMessage(controlStream, registerMsg); err != nil {
		return fmt.Errorf("register write: %w", err)
	}
	registerResp, err := codec.ReadMessage(controlStream)
	if err != nil {
		return fmt.Errorf("register read: %w", err)
	}
	if registerResp.Code == nil || *registerResp.Code != protocol.CodeOK {
		code := 0
		if registerResp.Code != nil {
			code = int(*registerResp.Code)
		}
		// 会话被吊销：码正确但用户被禁用或已过期 → 低速重试，管理员处理（启用/改有效期）后自动恢复
		if code == int(protocol.CodeSessionRevoked) {
			b, _ := json.Marshal(registerResp.Data)
			reason := &struct {
				Reason string `json:"reason"`
			}{}
			json.Unmarshal(b, reason)
			if reason.Reason == "user_expired" {
				return fmt.Errorf("register rejected: user expired (code=1003)")
			}
			return errUserDisabled
		}
		// 授权码错误（1001）：码已被重置或从未生效 → 置重绑提示，继续退避重试
		if code == int(protocol.CodeAuthFailed) {
			return errAuthCodeRejected
		}
		return fmt.Errorf("register deferred (code=%d)", code)
	}
	logx.Infof("[CTRL] registered ok")
	if err := c.store.MarkLocalUserRegistered(); err != nil {
		logx.Warnf("[INIT] mark registered error: %v", err)
	}
	sessionID := ""
	if registerResp.Data != nil {
		b, _ := json.Marshal(registerResp.Data)
		rd := &protocol.RegisterRespData{}
		if json.Unmarshal(b, rd) == nil {
			sessionID = rd.SessionID
		}
	}

	link := newControlLink(controlStream)
	link.client = c
	// 授权码重置为终态；用户禁用断开后下一轮注册收 1003 转入低速重试
	link.onRevoke = func(reason string) {
		if reason == "auth_code_reset" {
			c.revoked.Store(true)
		}
	}
	// 代理被 seeinpm 禁用：清除内存中的转发端口，resyncLoop 会周期重试（启用后自动恢复）
	link.onProxyRevoke = func(proxyID, reason string) {
		c.proxiesMu.Lock()
		if p, ok := c.proxies[proxyID]; ok {
			p.ForwardPort = 0
		}
		c.proxiesMu.Unlock()
	}
	go link.readLoop()
	go c.acceptDataStreams(session)
	c.setLink(link, sessionID)
	defer func() {
		link.close()
		c.clearLink(link)
	}()

	c.syncProxies(link)
	go c.resyncLoop(ctx, link)
	return c.heartbeatLoop(ctx, link)
}

// errUserDisabled 注册被拒（1003）：授权码正确但用户被禁用，低速重试等启用
var errUserDisabled = errors.New("register rejected: user disabled (code=1003)")

// errAuthCodeRejected 注册被拒（1001）：授权码错误（已被重置或从未生效），提示重绑
var errAuthCodeRejected = errors.New("register rejected: auth code invalid (code=1001)")

// slowRetryBackoff 用户禁用期间的固定重试间隔：管理员启用后最多 10s 内自动恢复
const slowRetryBackoff = 10 * time.Second

func (c *Client) runControlLoop(ctx context.Context, epoch int64) {
	backoff := time.Second
	const maxBackoff = 60 * time.Second
	for {
		if ctx.Err() != nil || c.controlEpoch.Load() != epoch {
			return
		}
		err := c.connectOnce(ctx)
		if ctx.Err() != nil || c.controlEpoch.Load() != epoch {
			return
		}
		// 授权码被重置：停止重连，进程保持运行，等待 B端 rebind 新授权码
		if c.revoked.Load() {
			logx.Infof("[CTRL] auth code reset by seeinpm; stop reconnecting, re-bind the new auth code on the web console")
			return
		}
		if err != nil {
			if errors.Is(err, errUserDisabled) {
				logx.Warnf("[CTRL] %v; user disabled, slow retry every %s (re-enables automatically once admin enables the user)", err, slowRetryBackoff)
				backoff = slowRetryBackoff
			} else {
				// 授权码被拒：置重绑提示（B端横幅），继续退避重试；重绑后自动恢复
				if errors.Is(err, errAuthCodeRejected) {
					if !c.revoked.Load() {
						logx.Warnf("[CTRL] auth code rejected by seeinpm; it may have been reset, re-bind the new auth code on the web console")
					}
					c.revoked.Store(true)
				}
				logx.Warnf("[CTRL] %v; retry in %s", err, backoff)
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
		} else {
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

// StartControlLoop 启动控制循环；epoch 自增使旧循环（若存在）在下一个检查点退出，
// 避免重绑授权码后出现新旧两个循环并发注册竞争
func (c *Client) StartControlLoop() {
	epoch := c.controlEpoch.Add(1)
	go c.runControlLoop(c.rootCtx, epoch)
}

// restartControl 重新绑定授权码后恢复连接：清除终态标记、踢掉旧会话、重启控制循环
func (c *Client) restartControl() {
	c.revoked.Store(false)
	if link := c.getLink(); link != nil {
		link.close()
	}
	c.StartControlLoop()
}

func (c *Client) syncProxies(link *controlLink) {
	c.proxiesMu.RLock()
	list := make([]*Proxy, 0, len(c.proxies))
	for _, p := range c.proxies {
		list = append(list, p)
	}
	c.proxiesMu.RUnlock()
	for _, p := range list {
		if _, err := c.allocProxy(link, p); err != nil {
			logx.Warnf("[ALLOC] alloc failed: proxy=%s err=%v", p.ID, err)
		}
	}
}

// resyncLoop 周期重试未分配到端口的代理（初次失败/被禁用后），连接断开即退出
func (c *Client) resyncLoop(ctx context.Context, link *controlLink) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-link.done:
			return
		case <-ticker.C:
			c.proxiesMu.RLock()
			var pending []*Proxy
			for _, p := range c.proxies {
				if p.ForwardPort <= 0 {
					pending = append(pending, p)
				}
			}
			c.proxiesMu.RUnlock()
			for _, p := range pending {
				if _, err := c.allocProxy(link, p); err != nil {
					logx.Warnf("[ALLOC] resync alloc failed: proxy=%s err=%v", p.ID, err)
				}
			}
		}
	}
}

func (c *Client) allocProxy(link *controlLink, p *Proxy) (int, error) {
	msg := protocol.NewMessage(protocol.TypeAllocPort, &protocol.AllocPortData{ProxyID: p.ID, Type: p.Type})
	resp, err := link.request(msg, 10*time.Second)
	if err != nil {
		return 0, err
	}
	if resp.Code == nil || *resp.Code != protocol.CodeOK {
		code := 0
		if resp.Code != nil {
			code = int(*resp.Code)
		}
		// 分配失败：清除转发端口标记，交给 resyncLoop 周期重试
		c.proxiesMu.Lock()
		p.ForwardPort = 0
		c.proxiesMu.Unlock()
		// 1006：代理被 seeinpm 禁用，低速重试，管理员启用后自动恢复
		if code == int(protocol.CodeProxyDisabled) {
			return 0, fmt.Errorf("ALLOC_PORT rejected: proxy disabled on seeinpm (code=1006)")
		}
		if code == int(protocol.CodePortConflict) {
			return 0, errPortPoolExhausted
		}
		if code == int(protocol.CodePortPoolExhausted) {
			return 0, errPortQuotaExhausted
		}
		if code == int(protocol.CodeTrafficQuotaExceeded) {
			return 0, errTrafficQuotaExceeded
		}
		return 0, fmt.Errorf("ALLOC_PORT code=%d", code)
	}
	d := &protocol.AllocPortRespData{}
	b, _ := json.Marshal(resp.Data)
	json.Unmarshal(b, d)
	if d.Port <= 0 {
		return 0, fmt.Errorf("ALLOC_PORT invalid port")
	}
	c.proxiesMu.Lock()
	p.ForwardPort = d.Port
	c.proxiesMu.Unlock()
	if err := c.store.UpdateProxyPort(p.ID, d.Port); err != nil {
		logx.Warnf("[ALLOC] save port error: proxy=%s err=%v", p.ID, err)
	}
	logx.Infof("[ALLOC] allocated: proxy=%s local=%s:%d forward=%d", p.ID, p.LocalAddr, p.LocalPort, d.Port)
	return d.Port, nil
}

func (c *Client) releaseProxy(link *controlLink, p *Proxy) {
	if p.ForwardPort <= 0 {
		return
	}
	msg := protocol.NewMessage(protocol.TypeReleasePort, &protocol.ReleasePortData{ProxyID: p.ID, Port: p.ForwardPort})
	if _, err := link.request(msg, 5*time.Second); err != nil {
		logx.Warnf("[RELEASE] notify error: proxy=%s err=%v", p.ID, err)
	}
	logx.Infof("[RELEASE] released: proxy=%s port=%d", p.ID, p.ForwardPort)
}

func (c *Client) heartbeatLoop(ctx context.Context, link *controlLink) error {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			link.close()
			return nil
		case <-ticker.C:
			msg := protocol.NewMessage(protocol.TypeHeartbeat, nil)
			if err := link.send(msg); err != nil {
				link.close()
				return fmt.Errorf("heartbeat: %w", err)
			}
			c.lastPing.Store(time.Now().Unix())
		case <-link.done:
			return fmt.Errorf("control link closed")
		}
	}
}

// updateQuotaCache 从控制消息解析 A 端下发的周期流量配额状态并缓存；
// 心跳响应无 data 视为“未启用配额”，缓存清零（web 端据此隐藏配额卡片）。
func (c *Client) updateQuotaCache(msg *protocol.Message) {
	q := &protocol.QuotaStatusData{}
	if msg.Data != nil {
		b, _ := json.Marshal(msg.Data)
		if err := json.Unmarshal(b, q); err != nil {
			return
		}
	}
	c.quotaCache.Store(q)
	c.quotaSyncedAt.Store(time.Now().Unix())
}

// quotaSnapshot 返回最近一次 A 端同步的配额状态与同步时间（未同步过时返回未启用空态）
func (c *Client) quotaSnapshot() (*protocol.QuotaStatusData, int64) {
	q, _ := c.quotaCache.Load().(*protocol.QuotaStatusData)
	if q == nil {
		q = &protocol.QuotaStatusData{}
	}
	return q, c.quotaSyncedAt.Load()
}

func (c *Client) acceptDataStreams(session *yamux.Session) {
	for {
		stream, err := session.Accept()
		if err != nil {
			return
		}
		go c.handleDataStream(stream)
	}
}

func (c *Client) handleDataStream(stream net.Conn) {
	defer stream.Close()
	// 流头: stype(1B)，代理流随后为 proxyIdLen(1B) + proxyId，精确按长度读取，剩余字节留给载荷
	var stype [1]byte
	if _, err := io.ReadFull(stream, stype[:]); err != nil {
		return
	}
	if stype[0] == protocol.StreamTypeUpgrade {
		// 版本管理升级流：无 proxyID，头部为 4B 长度 + JSON 元信息 + 二进制内容
		c.handleUpgradeStream(stream)
		return
	}
	var idLenByte [1]byte
	if _, err := io.ReadFull(stream, idLenByte[:]); err != nil {
		return
	}
	idLen := int(idLenByte[0])
	if idLen < 1 || idLen > 128 {
		logx.Warnf("[DATA] invalid proxyIdLen: %d", idLen)
		return
	}
	idBuf := make([]byte, idLen)
	if _, err := io.ReadFull(stream, idBuf); err != nil {
		return
	}
	proxyID := string(idBuf)
	c.proxiesMu.RLock()
	proxy, ok := c.proxies[proxyID]
	c.proxiesMu.RUnlock()
	if !ok {
		logx.Warnf("[DATA] unknown proxyID: %q", proxyID)
		return
	}
	switch stype[0] {
	case protocol.StreamTypeOps:
		c.handleOpsStream(stream, proxy)
	case protocol.StreamTypeTCP:
		c.handleTCPStream(stream, proxy)
	case protocol.StreamTypeUDP:
		c.handleUDPStream(stream, proxy)
	default:
		logx.Warnf("[DATA] unsupported stype %d for proxy %s", stype[0], proxyID)
	}
}

func (c *Client) handleTCPStream(stream net.Conn, proxy *Proxy) {
	targetConn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", proxy.LocalAddr, proxy.LocalPort), 5*time.Second)
	if err != nil {
		logx.Errorf("[DATA] dial local target failed: proxy=%s err=%v", proxy.ID, err)
		return
	}
	defer targetConn.Close()
	done := make(chan struct{}, 2)
	go func() { io.Copy(targetConn, stream); done <- struct{}{} }()
	go func() { io.Copy(stream, targetConn); done <- struct{}{} }()
	<-done
	<-done
}

// handleUDPStream 处理一条 UDP 会话流。会话由 seeinpm 首次收包时建立，
// 载荷格式见协议规范 §3/§8.3：先 clientAddr，随后双向分帧数据报。
// 会话无需跨流状态，本流自持内网 UDP 套接字自给自足。
func (c *Client) handleUDPStream(stream net.Conn, proxy *Proxy) {
	var clLen [1]byte
	if _, err := io.ReadFull(stream, clLen[:]); err != nil {
		return
	}
	if clLen[0] < 1 || clLen[0] > 128 {
		stream.Close()
		return
	}
	clBuf := make([]byte, clLen[0])
	if _, err := io.ReadFull(stream, clBuf); err != nil {
		return
	}

	// 拨内网 UDP 目标
	dst, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", proxy.LocalAddr, proxy.LocalPort))
	if err != nil {
		stream.Close()
		return
	}
	uconn, err := net.DialUDP("udp", nil, dst)
	if err != nil {
		logx.Errorf("[UDP] dial local target failed: proxy=%s err=%v", proxy.ID, err)
		stream.Close()
		return
	}

	// 泵B：内网目标回包 → 会话流
	go func() {
		defer uconn.Close()
		buf := make([]byte, udpframing.MaxDatagram)
		for {
			n, rerr := uconn.Read(buf)
			if rerr != nil {
				return
			}
			if err := udpframing.WriteFrame(stream, buf[:n]); err != nil {
				return
			}
		}
	}()

	// 泵A：会话流帧 → 内网目标
	var rbuf []byte
	for {
		payload, err := udpframing.ReadFrame(stream, rbuf)
		if err != nil {
			break
		}
		rbuf = payload
		if _, err := uconn.Write(payload); err != nil {
			break
		}
	}
	uconn.Close()
	stream.Close()
}

func main() {
	confPath := flag.String("conf", "conf/seeinps.toml", "config")
	runService := flag.Bool("service", false, "run as a Windows service (windows only)")
	flag.Parse()

	// Windows 服务模式：svc.Run 常驻，由 SCM 控制生命周期（部署工具注册服务时携带 --service）
	if *runService {
		if runAsService(*confPath) {
			return
		}
	}

	runForeground(*confPath)
}

// runForeground 前台模式：信号驱动，保持进程运行直到收到中断/终止。
func runForeground(confPath string) {
	ctx, cancel, restore, err := startWorker(confPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	// LIFO：先 cancel 触发关停，后 restore 冲刷日志落盘
	defer restore()
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-sigCh; cancel() }()
	<-ctx.Done()
}

// startWorker 加载配置并启动 seeinps 后台工作协程（配置、锁、日志、存储、控制循环、B端服务）。
// 返回生命周期上下文、取消函数与日志冲刷函数；平台模式（前台/服务）各自负责按序调用。
func startWorker(confPath string) (context.Context, context.CancelFunc, func(), error) {
	cfg, err := config.LoadPSConfig(confPath)
	if err != nil {
		return nil, nil, nil, err
	}
	// 单实例锁：重复启动会互踢（1002）并抢占 B 端端口，直接拒绝第二个实例
	if err := os.MkdirAll("data", 0755); err != nil {
		return nil, nil, nil, fmt.Errorf("create data dir: %v", err)
	}
	instanceLock, err := acquireInstanceLock("data/seeinps.lock")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("another seeinps instance is running (%v), exiting", err)
	}
	restore := logx.Install(cfg.Logging.Path, "seeinps", cfg.Logging.Level, cfg.Logging.MaxSize, cfg.Logging.MaxBackups)
	psStore, err := store.NewPS("data/seeinps.db")
	if err != nil {
		instanceLock.Close()
		restore()
		return nil, nil, nil, err
	}
	jwtSecret, err := auth.GenerateJWTSecret()
	if err != nil {
		instanceLock.Close()
		restore()
		return nil, nil, nil, err
	}
	jwt := auth.NewJWTManager(jwtSecret, 24*time.Hour, 48*time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	// 生命周期结束时释放单实例锁与本地存储句柄
	context.AfterFunc(ctx, func() {
		instanceLock.Close()
		psStore.Close()
	})

	client := NewClient(cfg, psStore, jwt)
	client.configPath = confPath
	client.rootCtx = ctx
	if err := client.loadProxies(); err != nil {
		cancel() // 触发 AfterFunc 释放单实例锁并关闭存储
		restore()
		return nil, nil, nil, fmt.Errorf("load proxies: %w", err)
	}
	client.migrateLegacyLocalUser()

	// B 端绑定失败（如端口被占）不退出进程：代理与控制通道不受影响，
	// startLocalServer 内部每 5s 重试绑定，端口释放后管理页自动恢复
	go func() {
		if err := client.startLocalServer(); err != nil {
			logx.Infof("[WEB] local server exited: %v", err)
		}
	}()

	// 控制循环终止（授权码被重置等）不退出进程：B端 web 保持可用，等待重绑授权码
	client.StartControlLoop()

	// 每小时清理 90 天前的本地审计日志
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// 仅清理已成功同步到 seeinpm 的记录；未同步的是 PM 侧尚未持有的唯一副本
				if n, err := psStore.CleanupAuditLogs(time.Now().Add(-90 * 24 * time.Hour).Unix()); err != nil {
					logx.Warnf("[CLEANUP] audit logs cleanup error: %v", err)
				} else if n > 0 {
					logx.Infof("[CLEANUP] removed %d synced audit logs older than 90 days", n)
				}
				if pending, perr := psStore.PendingAuditLogs(1); perr == nil && len(pending) > 0 {
					logx.Warnf("[AUDIT] there are unsynced audit records awaiting seeinpm; they are kept until delivered")
				}
			}
		}
	}()

	return ctx, cancel, restore, nil
}
