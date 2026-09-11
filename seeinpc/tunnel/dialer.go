package tunnel

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"
)

// 探测/拨号结果的语义化错误，供上层区分提示文案
var (
	ErrProxyAuthFailed   = errors.New("代理认证失败(407)")
	ErrACLDenied         = errors.New("目标被 ACL 拒绝(403)")
	ErrProxyUnreachable  = errors.New("无法连接代理服务器")
	ErrTargetUnreachable = errors.New("认证通过但目标不可达(5xx)")
)

// Dialer 通过 seeinpm 运维端口（HTTP 代理，Basic 认证）向上游发起 CONNECT 隧道。
// 等价于原 proxytovpn 的 ChainedProxy+tun2socks 组合，但以纯 Go 实现。
type Dialer struct {
	// Servers 代理服务器地址列表（seeinpm A 端），按序回退；
	// 仅 TCP 连接失败才尝试下一台，握手/认证结果以首台可达者为准。
	Servers []string
	// OpsID 运维ID（seeinpm 对外分配的端口）
	OpsID int
	// Username/Password 运维代理账号密码（认证发生在 seeinps 侧）
	Username string
	Password string
	// Timeout 建连+握手超时
	Timeout time.Duration
}

// Addr 返回首地址（host:port），用于日志与提示展示
func (d *Dialer) Addr() string {
	if len(d.Servers) == 0 {
		return ""
	}
	return net.JoinHostPort(d.Servers[0], strconv.Itoa(d.OpsID))
}

// DialContext 建立到 target（"ip:port"）的 CONNECT 隧道，返回可读写的数据连接。
// 日志约定：逐台尝试与握手状态码记 DEBUG（per-connection 路径，高频）；
// 服务器回退记限频 WARN（"主服务器不通自动切备用"是可观测的重要事件）。
// 注意：Basic 认证串绝不落日志。
func (d *Dialer) DialContext(ctx context.Context, target string) (net.Conn, error) {
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	nd := net.Dialer{Timeout: timeout}
	var lastErr error
	for i, srv := range d.Servers {
		if ctx.Err() != nil {
			break
		}
		addr := net.JoinHostPort(srv, strconv.Itoa(d.OpsID))
		lg.Debugf("dial: try %s target=%s attempt=%d/%d", addr, target, i+1, len(d.Servers))
		conn, err := nd.DialContext(ctx, "tcp", addr)
		if err != nil {
			lastErr = err
			if i+1 < len(d.Servers) {
				warnThrottled("fallback:"+addr, 60*time.Second,
					"dial: server %s unreachable, falling back to next server: %v", addr, err)
			}
			continue // TCP 不通才回退下一台
		}
		return d.handshake(conn, target, addr, timeout, ctx)
	}
	lg.Debugf("dial: all servers exhausted target=%s servers=%v err=%v", target, d.Servers, lastErr)
	return nil, fmt.Errorf("%w: %v", ErrProxyUnreachable, lastErr)
}

// Probe 探测代理可用性并返回"到代理的 TCP 握手耗时"。
// rtt 仅计量 TCP 三次握手（首台可达服务器），不含 seeinps 拨上游目标的时间，
// 因此能真实反映客户端到代理的链路延迟；err 为 CONNECT 认证结果（nil/407/403/不可达等）。
// 结果由调用方（探测循环）按级别记录，此处只记 DEBUG，避免重复刷屏。
func (d *Dialer) Probe(ctx context.Context, target string) (time.Duration, error) {
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	nd := net.Dialer{Timeout: timeout}
	var lastErr error
	for i, srv := range d.Servers {
		if ctx.Err() != nil {
			break
		}
		addr := net.JoinHostPort(srv, strconv.Itoa(d.OpsID))
		start := time.Now()
		conn, err := nd.DialContext(ctx, "tcp", addr)
		rtt := time.Since(start)
		if err != nil {
			lastErr = err
			if i+1 < len(d.Servers) {
				warnThrottled("fallback:"+addr, 60*time.Second,
					"probe: server %s unreachable, falling back to next server: %v", addr, err)
			}
			continue
		}
		// TCP 已连通，rtt 即握手耗时；继续做 CONNECT 认证得到状态
		if _, herr := d.handshake(conn, target, addr, timeout, ctx); herr != nil {
			lg.Debugf("probe: %s target=%s rtt=%s result=%v", addr, target, rtt, herr)
			return rtt, herr
		}
		lg.Debugf("probe: %s target=%s rtt=%s ok", addr, target, rtt)
		return rtt, nil
	}
	lg.Debugf("probe: all servers exhausted target=%s servers=%v err=%v", target, d.Servers, lastErr)
	return 0, fmt.Errorf("%w: %v", ErrProxyUnreachable, lastErr)
}

// handshake 在已连通的 TCP 连接上完成 CONNECT + Basic 认证握手。
// addr 仅用于日志。
func (d *Dialer) handshake(conn net.Conn, target, addr string, timeout time.Duration, ctx context.Context) (net.Conn, error) {
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}

	req := "CONNECT " + target + " HTTP/1.1\r\n" +
		"Host: " + target + "\r\n"
	if d.Username != "" || d.Password != "" {
		auth := base64.StdEncoding.EncodeToString([]byte(d.Username + ":" + d.Password))
		req += "Proxy-Authorization: Basic " + auth + "\r\n"
	}
	req += "Proxy-Connection: keep-alive\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		conn.Close()
		lg.Debugf("handshake: write CONNECT fail addr=%s target=%s err=%v", addr, target, err)
		return nil, fmt.Errorf("发送 CONNECT 请求失败: %w", err)
	}

	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		conn.Close()
		lg.Debugf("handshake: read proxy response fail addr=%s target=%s err=%v", addr, target, err)
		return nil, fmt.Errorf("读取代理响应失败: %w", err)
	}
	code := parseStatusCode(line)
	// 读完剩余响应头
	for {
		h, herr := br.ReadString('\n')
		if herr != nil || h == "\r\n" || h == "\n" {
			break
		}
	}
	_ = conn.SetDeadline(time.Time{})
	switch code {
	case 200:
		return conn, nil
	case 407:
		conn.Close()
		lg.Debugf("handshake: auth failed (407) addr=%s user=%s", addr, d.Username)
		return nil, ErrProxyAuthFailed
	case 403:
		conn.Close()
		lg.Debugf("handshake: target denied (403) addr=%s target=%s", addr, target)
		return nil, ErrACLDenied
	default:
		conn.Close()
		if code >= 500 {
			lg.Debugf("handshake: target unreachable (%d) addr=%s target=%s", code, addr, target)
			return nil, fmt.Errorf("%w: %s", ErrTargetUnreachable, firstLine(line))
		}
		lg.Warnf("handshake: unexpected proxy status addr=%s code=%d line=%q", addr, code, firstLine(line))
		return nil, fmt.Errorf("代理返回异常状态: %s", firstLine(line))
	}
}

// ProbeConnect 对代理做带认证的连通性预检：向 target 发起 CONNECT。
// 返回值语义：nil=代理可用（且目标可达）；错误可匹配上面三个语义化错误。
func (d *Dialer) ProbeConnect(ctx context.Context, target string) error {
	conn, err := d.DialContext(ctx, target)
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}

func parseStatusCode(statusLine string) int {
	// HTTP/1.1 200 Connection established
	var sp1, sp2 int
	for i := 0; i < len(statusLine); i++ {
		if statusLine[i] == ' ' {
			if sp1 == 0 {
				sp1 = i
			} else {
				sp2 = i
				break
			}
		}
	}
	if sp1 == 0 || sp2 == 0 {
		return -1
	}
	code, err := strconv.Atoi(statusLine[sp1+1 : sp2])
	if err != nil {
		return -1
	}
	return code
}

func firstLine(s string) string {
	for i, c := range s {
		if c == '\r' || c == '\n' {
			return s[:i]
		}
	}
	return s
}
