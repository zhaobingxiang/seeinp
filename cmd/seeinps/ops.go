package main

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/seeinp/seeinp/internal/auth"
)

const opsHeadLimit = 32 * 1024

// handleOpsStream 处理运维代理数据流：Basic 认证 → ACL 校验 → CONNECT/绝对URI 转发
func (c *Client) handleOpsStream(stream net.Conn, p *Proxy) {
	defer stream.Close()
	stream.SetReadDeadline(time.Now().Add(15 * time.Second))
	br := bufio.NewReaderSize(stream, 16*1024)

	head, err := readHTTPHead(br)
	if err != nil {
		logOps(p.ID, "", "", "bad_request")
		writeOpsResponse(stream, http.StatusBadRequest, "malformed request")
		return
	}

	method, target, u, headers, err := parseProxyRequest(head)
	if err != nil {
		logOps(p.ID, "", "", "bad_request")
		writeOpsResponse(stream, http.StatusBadRequest, err.Error())
		return
	}

	user, ok := checkOpsAuth(p, headers.Get("Proxy-Authorization"))
	if !ok {
		logOps(p.ID, user, target, "auth_failed")
		writeOpsResponse(stream, http.StatusProxyAuthRequired, "proxy authentication required", "Proxy-Authenticate", `Basic realm="seeinp"`)
		return
	}

	if !aclAllowed(p.aclNets, target) {
		logOps(p.ID, user, target, "acl_denied")
		writeOpsResponse(stream, http.StatusForbidden, "target not allowed by ACL")
		return
	}

	targetAddr := normalizeTarget(target)
	tconn, err := net.DialTimeout("tcp", targetAddr, 5*time.Second)
	if err != nil {
		logOps(p.ID, user, target, "dial_failed")
		writeOpsResponse(stream, http.StatusBadGateway, "target unreachable")
		return
	}
	defer tconn.Close()
	logOps(p.ID, user, target, "ok")

	if strings.EqualFold(method, "CONNECT") {
		if _, err := stream.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
			return
		}
		relayOps(stream, tconn, br)
		return
	}

	// 绝对 URI 请求：改写为 origin-form 并强制短连接（MVP 单请求一连接）
	newHead := rewriteOriginForm(method, u, head)
	if _, err := tconn.Write([]byte(newHead)); err != nil {
		return
	}
	relayOps(stream, tconn, br)
}

func relayOps(client, target net.Conn, br *bufio.Reader) {
	client.SetReadDeadline(time.Time{})
	if n := br.Buffered(); n > 0 {
		buf := make([]byte, n)
		if _, err := io.ReadFull(br, buf); err == nil {
			target.Write(buf)
		}
	}
	done := make(chan struct{}, 2)
	go func() { io.Copy(target, client); done <- struct{}{} }()
	go func() { io.Copy(client, target); done <- struct{}{} }()
	<-done
	client.Close()
	target.Close()
	<-done
}

// readHTTPHead 读取到 \r\n\r\n 为止（不超过 opsHeadLimit）
func readHTTPHead(br *bufio.Reader) ([]byte, error) {
	var out []byte
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return nil, err
		}
		out = append(out, line...)
		if len(out) > opsHeadLimit {
			return nil, fmt.Errorf("request head too large")
		}
		if len(line) <= 2 { // 空行（\r\n 或 \n）即头部结束
			return out, nil
		}
	}
}

// parseProxyRequest 解析请求行与头部，返回方法、目标 host:port、绝对URI、头部
func parseProxyRequest(head []byte) (method, target string, u *url.URL, headers http.Header, err error) {
	lines := strings.Split(string(head), "\n")
	if len(lines) == 0 {
		return "", "", nil, nil, fmt.Errorf("empty request")
	}
	parts := strings.Fields(strings.TrimSpace(lines[0]))
	if len(parts) != 3 {
		return "", "", nil, nil, fmt.Errorf("malformed request line")
	}
	method, uri := parts[0], parts[1]

	headers = http.Header{}
	for _, ln := range lines[1:] {
		ln = strings.TrimRight(ln, "\r")
		if ln == "" {
			continue
		}
		if i := strings.IndexByte(ln, ':'); i > 0 {
			headers.Add(strings.TrimSpace(ln[:i]), strings.TrimSpace(ln[i+1:]))
		}
	}

	if strings.EqualFold(method, "CONNECT") {
		if _, _, err := net.SplitHostPort(uri); err != nil {
			return "", "", nil, nil, fmt.Errorf("invalid CONNECT target")
		}
		return method, uri, nil, headers, nil
	}

	u, err = url.ParseRequestURI(uri)
	if err != nil || u.Host == "" {
		return "", "", nil, nil, fmt.Errorf("expected absolute-form URI")
	}
	return method, u.Host, u, headers, nil
}

// checkOpsAuth 校验 Proxy-Authorization: Basic，返回用户名与是否通过
func checkOpsAuth(p *Proxy, authHeader string) (string, bool) {
	if p.ProxyPasswordHash == "" {
		return "", false
	}
	scheme, cred, _ := strings.Cut(authHeader, " ")
	if !strings.EqualFold(strings.TrimSpace(scheme), "Basic") {
		return "", false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(cred))
	if err != nil {
		return "", false
	}
	user, pass, _ := strings.Cut(string(raw), ":")
	if user != p.ProxyUsername || !auth.CheckPassword(pass, p.ProxyPasswordHash) {
		return user, false
	}
	return user, true
}

// aclAllowed 目标 host（IP 或域名解析后任一 IP）命中任一网段即放行
func aclAllowed(nets []*net.IPNet, target string) bool {
	if len(nets) == 0 {
		return false
	}
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		host = target
	}
	if ip := net.ParseIP(host); ip != nil {
		return ipInNets(ip, nets)
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return false
	}
	for _, ip := range ips {
		if ipInNets(ip, nets) {
			return true
		}
	}
	return false
}

func ipInNets(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func normalizeTarget(target string) string {
	if _, _, err := net.SplitHostPort(target); err == nil {
		return target
	}
	return target + ":80"
}

// rewriteOriginForm 绝对 URI 改写为 origin-form，并强制 Connection: close
func rewriteOriginForm(method string, u *url.URL, head []byte) string {
	lines := strings.Split(string(head), "\n")
	var b strings.Builder
	path := u.RequestURI()
	b.WriteString(method + " " + path + " HTTP/1.1\r\n")
	for _, ln := range lines[1:] {
		ln = strings.TrimRight(ln, "\r")
		if ln == "" {
			continue
		}
		lower := strings.ToLower(ln)
		if strings.HasPrefix(lower, "proxy-authorization:") ||
			strings.HasPrefix(lower, "proxy-connection:") ||
			strings.HasPrefix(lower, "connection:") {
			continue
		}
		b.WriteString(ln + "\r\n")
	}
	b.WriteString("Connection: close\r\n\r\n")
	return b.String()
}

func writeOpsResponse(w io.Writer, code int, msg string, extraHeaders ...string) {
	status := http.StatusText(code)
	if status == "" {
		status = fmt.Sprintf("status %d", code)
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("HTTP/1.1 %d %s\r\n", code, status))
	for i := 0; i+1 < len(extraHeaders); i += 2 {
		b.WriteString(extraHeaders[i] + ": " + extraHeaders[i+1] + "\r\n")
	}
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString(fmt.Sprintf("Content-Length: %d\r\n", len(msg)))
	b.WriteString("Connection: close\r\n\r\n")
	b.WriteString(msg)
	w.Write([]byte(b.String()))
}

// logOps 访问日志（F-P8）：不含密码/凭据
func logOps(proxyID, user, target, result string) {
	fmt.Printf("[OPS] proxy=%s user=%q target=%s result=%s\n", proxyID, user, target, result)
}
