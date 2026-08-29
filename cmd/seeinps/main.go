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
	"github.com/seeinp/seeinp/internal/logx"
	"github.com/seeinp/seeinp/internal/mux"
	"github.com/seeinp/seeinp/internal/protocol"
	"github.com/seeinp/seeinp/internal/store"
	"github.com/seeinp/seeinp/internal/version"
)

type Client struct {
	config *config.PSConfig
	store  *store.PSStore
	jwt    *auth.JWTManager

	linkMu    sync.Mutex
	link      *controlLink
	sessionID string
	// revoked: 授权码被重置（停止重连，等待 B端重新绑定）；用户禁用走低速重试等管理员启用
	revoked atomic.Bool
	// lastPing: 最近一次心跳成功时间（unix 秒），供 B端仪表盘展示
	lastPing atomic.Int64

	// rootCtx 为 main 的生命周期 ctx；controlEpoch 用于重绑后重启控制循环并让旧循环退出
	rootCtx       context.Context
	controlEpoch  atomic.Int64

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
			fmt.Printf("[CTRL] SESSION_REVOKE received (reason=%s), disconnecting\n", reason)
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
			fmt.Printf("[CTRL] PROXY_REVOKE received (proxy=%s, reason=%s)\n", proxyID, reason)
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
		// A 端推送升级包（随后经 0x05 数据流传输二进制）
		if msg.Type == protocol.TypeUpgradePush {
			if c := l.client; c != nil {
				l.send(c.handleUpgradePush(msg))
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
		config:  cfg,
		store:   psStore,
		jwt:     jwt,
		proxies: make(map[string]*Proxy),
	}
}

func (c *Client) setLink(l *controlLink, sessionID string) {
	c.linkMu.Lock()
	c.link = l
	c.sessionID = sessionID
	c.linkMu.Unlock()
	// 连接建立即视为一次成功心跳，后续由 heartbeatLoop 每 10s 刷新
	c.lastPing.Store(time.Now().Unix())
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
				fmt.Printf("[PROXY] %s acl invalid (%v), fallback to default\n", p.ProxyID, err)
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
	fmt.Printf("[seeinps] Connecting to %s...\n", addr)
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
	fmt.Println("[seeinps] Handshake OK")

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
	fmt.Println("[seeinps] Registered OK")
	if err := c.store.MarkLocalUserRegistered(); err != nil {
		fmt.Printf("[INIT] mark registered: %v\n", err)
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
			fmt.Println("[CTRL] Auth code reset by seeinpm; stop reconnecting. Re-bind the new auth code on the web console.")
			return
		}
		if err != nil {
			if errors.Is(err, errUserDisabled) {
				fmt.Printf("[CTRL] %v; user disabled, slow retry every %s (re-enables automatically once admin enables the user)\n", err, slowRetryBackoff)
				backoff = slowRetryBackoff
			} else {
				// 授权码被拒：置重绑提示（B端横幅），继续退避重试；重绑后自动恢复
				if errors.Is(err, errAuthCodeRejected) {
					if !c.revoked.Load() {
						fmt.Println("[CTRL] Auth code rejected by seeinpm; it may have been reset. Re-bind the new auth code on the web console.")
					}
					c.revoked.Store(true)
				}
				fmt.Printf("[CTRL] %v; retry in %s\n", err, backoff)
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
			fmt.Printf("[ALLOC] %s failed: %v\n", p.ID, err)
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
					fmt.Printf("[RESYNC] %s alloc failed: %v\n", p.ID, err)
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
		fmt.Printf("[ALLOC] save port: %v\n", err)
	}
	fmt.Printf("[ALLOC] %s: local %s:%d -> forward %d\n", p.ID, p.LocalAddr, p.LocalPort, d.Port)
	return d.Port, nil
}

func (c *Client) releaseProxy(link *controlLink, p *Proxy) {
	if p.ForwardPort <= 0 {
		return
	}
	msg := protocol.NewMessage(protocol.TypeReleasePort, &protocol.ReleasePortData{ProxyID: p.ID, Port: p.ForwardPort})
	if _, err := link.request(msg, 5*time.Second); err != nil {
		fmt.Printf("[RELEASE] %s notify error: %v\n", p.ID, err)
	}
	fmt.Printf("[RELEASE] %s port %d\n", p.ID, p.ForwardPort)
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
		fmt.Printf("[DATA] invalid proxyIdLen %d\n", idLen)
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
		fmt.Printf("[DATA] unknown proxyID %q\n", proxyID)
		return
	}
	switch stype[0] {
	case protocol.StreamTypeOps:
		c.handleOpsStream(stream, proxy)
	case protocol.StreamTypeTCP:
		c.handleTCPStream(stream, proxy)
	default:
		fmt.Printf("[DATA] unsupported stype %d for %s\n", stype[0], proxyID)
	}
}

func (c *Client) handleTCPStream(stream net.Conn, proxy *Proxy) {
	targetConn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", proxy.LocalAddr, proxy.LocalPort), 5*time.Second)
	if err != nil {
		fmt.Printf("[DATA] dial %s fail: %v\n", proxy.ID, err)
		return
	}
	defer targetConn.Close()
	done := make(chan struct{}, 2)
	go func() { io.Copy(targetConn, stream); done <- struct{}{} }()
	go func() { io.Copy(stream, targetConn); done <- struct{}{} }()
	<-done
	<-done
}

func main() {
	confPath := flag.String("conf", "conf/seeinps.toml", "config")
	flag.Parse()
	cfg, err := config.LoadPSConfig(*confPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	// 运行日志：级别过滤 + 行首时间戳 + 按天/大小轮转（conf [logging] level/max_size/max_backups 生效）
	// 单实例锁：重复启动会互踢（1002）并抢占 B 端端口，直接拒绝第二个实例
	instanceLock, err := acquireInstanceLock("data/seeinps.lock")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: another seeinps instance is running (%v), exiting\n", err)
		os.Exit(1)
	}
	defer instanceLock.Close()

	logx.Install(cfg.Logging.Path, "seeinps", cfg.Logging.Level, cfg.Logging.MaxSize, cfg.Logging.MaxBackups)
	psStore, err := store.NewPS("data/seeinps.db")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer psStore.Close()
	jwtSecret, err := auth.GenerateJWTSecret()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	jwt := auth.NewJWTManager(jwtSecret, 24*time.Hour, 48*time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-sigCh; cancel() }()

	client := NewClient(cfg, psStore, jwt)
	client.rootCtx = ctx
	if err := client.loadProxies(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: load proxies: %v\n", err)
		os.Exit(1)
	}
	client.migrateLegacyLocalUser()

	// B 端绑定失败（如端口被占）不退出进程：代理与控制通道不受影响，
	// startLocalServer 内部每 5s 重试绑定，端口释放后管理页自动恢复
	go func() {
		if err := client.startLocalServer(); err != nil {
			fmt.Printf("[B端] local server error: %v\n", err)
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
				if n, err := psStore.CleanupAuditLogs(time.Now().Add(-90 * 24 * time.Hour).Unix()); err != nil {
					fmt.Printf("[CLEANUP] audit logs: %v\n", err)
				} else if n > 0 {
					fmt.Printf("[CLEANUP] Removed %d audit logs over 90 days\n", n)
				}
			}
		}
	}()

	<-ctx.Done()
}
