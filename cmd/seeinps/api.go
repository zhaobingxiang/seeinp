package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/seeinp/seeinp/internal/auth"
	"github.com/seeinp/seeinp/internal/store"
	"github.com/seeinp/seeinp/internal/version"
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
		fmt.Printf("[INIT] migrate legacy user: %v\n", err)
		return
	}
	os.Rename(legacyLocalUserPath, legacyLocalUserPath+".bak")
	fmt.Printf("[INIT] Migrated legacy local user: %s\n", legacy.Username)
}

func (c *Client) startLocalServer() error {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/v1/auth/status", c.handleAuthStatus)
	mux.HandleFunc("POST /api/v1/auth/init", c.handleAuthInit)
	mux.HandleFunc("POST /api/v1/auth/login", c.handleAuthLogin)
	mux.HandleFunc("POST /api/v1/auth/rebind", c.requireAuth(c.handleAuthRebind))
	mux.HandleFunc("GET /health", c.handleHealth)

	mux.HandleFunc("GET /api/v1/proxies", c.requireAuth(c.handleProxyList))
	mux.HandleFunc("POST /api/v1/proxies", c.requireAuth(c.handleProxyCreate))
	mux.HandleFunc("PATCH /api/v1/proxies/{id}", c.requireAuth(c.handleProxyUpdate))
	mux.HandleFunc("DELETE /api/v1/proxies/{id}", c.requireAuth(c.handleProxyDelete))
	mux.HandleFunc("GET /api/v1/status", c.requireAuth(c.handleStatus))
	mux.HandleFunc("GET /api/v1/stats", c.requireAuth(c.handleStats))

	for _, dir := range []string{"web", "web-ps/dist", "/root/seeinp/web"} {
		if _, err := os.Stat(dir); err == nil {
			c.serveWeb(mux, dir)
			break
		}
	}

	addr := c.config.Local.BendAddr
	fmt.Printf("[B端] Listening on %s\n", addr)

	server := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	return server.ListenAndServe()
}

func (c *Client) serveWeb(mux *http.ServeMux, webDir string) {
	fs := http.FileServer(http.Dir(webDir))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(webDir, filepath.Clean(r.URL.Path))
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			// SPA 入口：禁止缓存，保证前端升级后浏览器立即拉到新版（assets 文件名带 hash 可长缓存）
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFile(w, r, filepath.Join(webDir, "index.html"))
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		fs.ServeHTTP(w, r)
	})
	fmt.Printf("[B端] Serving web from %s\n", webDir)
}

func (c *Client) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" {
			writeErr(w, http.StatusUnauthorized, 1003, "未登录")
			return
		}
		if _, err := c.jwt.ValidateToken(token); err != nil {
			writeErr(w, http.StatusUnauthorized, 1003, "登录已过期，请重新登录")
			return
		}
		next(w, r)
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
	fmt.Printf("[INIT] Local user initialized: %s\n", req.Username)
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
	fmt.Println("[REBIND] Auth code updated via web console; restarting control loop")
	c.restartControl()
	writeOK(w, map[string]interface{}{"restarted": true})
}

func (c *Client) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, 2001, "请求格式错误")
		return
	}
	u, err := c.store.GetLocalUser()
	if err != nil {
		writeErr(w, http.StatusBadRequest, 3002, "尚未初始化")
		return
	}
	if req.Username != u.Username || !auth.CheckPassword(req.Password, u.PasswordHash) {
		writeErr(w, http.StatusUnauthorized, 1001, "用户名或密码错误")
		return
	}
	token, err := c.jwt.GenerateAccessToken(u.Username, "ps")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, 5000, "生成令牌失败")
		return
	}
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
	case "tcp":
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
	case "udp":
		return nil, fmt.Errorf("UDP 映射暂未实现")
	case "ops_socks":
		return nil, fmt.Errorf("SOCKS5 运维代理为二期功能")
	default:
		return nil, fmt.Errorf("代理类型仅支持 tcp / ops_http")
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
			writeErr(w, http.StatusBadGateway, 5002, "端口分配失败，代理未保存: "+err.Error())
			return
		}
	}
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
	delete(c.proxies, id)
	c.proxiesMu.Unlock()

	if link := c.getLink(); link != nil {
		c.releaseProxy(link, p)
	}
	if err := c.store.DeleteProxy(id); err != nil {
		writeErr(w, http.StatusInternalServerError, 5000, "删除代理失败")
		return
	}
	fmt.Printf("[PROXY] Deleted: %s\n", id)
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
		"authCodeReset": c.revoked.Load(),
	})
}

func (c *Client) handleStats(w http.ResponseWriter, r *http.Request) {
	c.proxiesMu.RLock()
	n := len(c.proxies)
	c.proxiesMu.RUnlock()
	writeOK(w, map[string]interface{}{"connections": 0, "traffic": 0, "proxies": n})
}
