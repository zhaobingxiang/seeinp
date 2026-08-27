package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/seeinp/seeinp/internal/auth"
	"github.com/seeinp/seeinp/internal/config"
	"github.com/seeinp/seeinp/internal/mux"
	"github.com/seeinp/seeinp/internal/portpool"
	"github.com/seeinp/seeinp/internal/protocol"
	"github.com/seeinp/seeinp/internal/store"
	"github.com/seeinp/seeinp/internal/tlsutil"
	"github.com/seeinp/seeinp/internal/version"
)

const (
	connReadTimeout  = 60 * time.Second
	connIdleTimeout  = 300 * time.Second
)

type Server struct {
	config           *config.PMConfig
	tlsConfig        *tls.Config
	store            *store.Store
	jwt              *auth.JWTManager
	portPool         *portpool.Pool
	clients          map[string]*Client
	clientsMu        sync.RWMutex
	sessions         map[string]*Session
	sessionsMu       sync.RWMutex
	publicConns      map[int]*PublicListener
	publicConnsByProxy map[string]*PublicListener
	publicMu         sync.RWMutex
}

type Client struct {
	username      string
	sessionID     string
	connected     time.Time
	lastPing      time.Time
	session       *yamux.Session
	controlStream net.Conn
	// ctrlWriteMu 保护控制流写入（心跳应答与 SESSION_REVOKE 推送可能并发）
	ctrlWriteMu sync.Mutex
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
	Client  *Client
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

	ranges := make([]portpool.Range, len(config.PortPool.Ranges))
	for i, r := range config.PortPool.Ranges {
		ranges[i] = portpool.Range{Start: r.Start, End: r.End}
	}

	return &Server{
		config:           config,
		store:            s,
		jwt:              jwtMgr,
		portPool:         portpool.New(ranges, s),
		clients:          make(map[string]*Client),
		sessions:         make(map[string]*Session),
		publicConns:      make(map[int]*PublicListener),
		publicConnsByProxy: make(map[string]*PublicListener),
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
		fmt.Printf("[TLS] Fingerprint: %s\n", fp)
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

	fmt.Println("[seeinpm] Started - control :999, api :9998")

	<-ctx.Done()
	s.shutdown()
	return nil
}

func (s *Server) startControlListener(ctx context.Context) {
	ln, err := tls.Listen("tcp", s.config.Server.Addr, s.tlsConfig)
	if err != nil {
		log.Fatalf("[FATAL] Control listener: %v", err)
	}
	defer ln.Close()
	fmt.Printf("[CTRL] Listening on %s\n", s.config.Server.Addr)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("[CTRL] Accept error: %v", err)
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
	mux.HandleFunc("/api/v1/auth/refresh", s.handleAuthRefresh)
	mux.HandleFunc("/api/v1/users", s.authMiddleware(s.handleUsers))
	mux.HandleFunc("POST /api/v1/users/{username}/disable", s.authMiddleware(s.handleUserDisable))
	mux.HandleFunc("POST /api/v1/users/{username}/enable", s.authMiddleware(s.handleUserEnable))
	mux.HandleFunc("POST /api/v1/users/{username}/reset-code", s.authMiddleware(s.handleUserResetCode))
	mux.HandleFunc("/api/v1/clients", s.authMiddleware(s.handleClientsAPI))
	mux.HandleFunc("/api/v1/auth/verify-code", s.handleVerifyCode)
	mux.HandleFunc("/api/v1/port-pool", s.authMiddleware(s.handlePortPool))

	staticDir := "./web-pm/dist"
	if _, err := os.Stat(staticDir); err == nil {
		fs := http.FileServer(http.Dir(staticDir))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			path := filepath.Join(staticDir, filepath.Clean(r.URL.Path))
			info, err := os.Stat(path)
			if err != nil || info.IsDir() {
				http.ServeFile(w, r, filepath.Join(staticDir, "index.html"))
				return
			}
			fs.ServeHTTP(w, r)
		})
	}

	addr := ":9998"
	fmt.Printf("[HTTP] Listening on %s\n", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Printf("[HTTP] Error: %v", err)
	}
}

func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" ||
			r.URL.Path == "/api/v1/auth/login" ||
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

		if _, err := s.jwt.ValidateToken(token); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintf(w, `{"code":1003,"message":"invalid token"}`)
			return
		}

		next(w, r)
	}
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

func (s *Server) cleanupStaleAllocations() {
	stale, err := s.store.GetStaleAllocations()
	if err != nil {
		log.Printf("[CLEANUP] Error: %v", err)
		return
	}

	for _, alloc := range stale {
		s.stopPublicListener(alloc.Port)
		s.portPool.Release(alloc.ProxyID)
		fmt.Printf("[CLEANUP] Released port %d for offline user %s\n", alloc.Port, alloc.UserID)
	}

	if len(stale) > 0 {
		fmt.Printf("[CLEANUP] Cleaned up %d stale allocations\n", len(stale))
	}
}

// Public listener functions
func (s *Server) startPublicListener(port int, proxyID, proxyType string, client *Client) error {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("listen on port %d: %w", port, err)
	}

	pl := &PublicListener{
		Port:    port,
		ProxyID: proxyID,
		Type:    proxyType,
		Ln:      ln,
		Client:  client,
	}

	s.publicMu.Lock()
	s.publicConns[port] = pl
	s.publicConnsByProxy[proxyID] = pl
	s.publicMu.Unlock()

	fmt.Printf("[PUBLIC] Listening on :%d for proxy %s\n", port, proxyID)
	go s.acceptPublicConnections(pl)
	return nil
}

func (s *Server) stopPublicListener(port int) {
	s.publicMu.Lock()
	pl, ok := s.publicConns[port]
	if ok {
		delete(s.publicConns, port)
		delete(s.publicConnsByProxy, pl.ProxyID)
	}
	s.publicMu.Unlock()

	if ok && pl.Ln != nil {
		pl.Ln.Close()
		fmt.Printf("[PUBLIC] Stopped listening on :%d\n", port)
	}
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

func (s *Server) handlePublicConnection(pl *PublicListener, conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(connIdleTimeout))

	fmt.Printf("[PUBLIC] New connection on :%d from %s\n", pl.Port, conn.RemoteAddr())

	stream, err := pl.Client.session.Open()
	if err != nil {
		log.Printf("[PUBLIC] Open stream error: %v", err)
		return
	}
	defer stream.Close()

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
		log.Printf("[PUBLIC] Write header error: %v", err)
		return
	}

	done := make(chan struct{}, 2)
	go func() {
		stream.SetReadDeadline(time.Now().Add(connReadTimeout))
		io.Copy(stream, conn)
		done <- struct{}{}
	}()
	go func() {
		conn.SetReadDeadline(time.Now().Add(connReadTimeout))
		io.Copy(conn, stream)
		done <- struct{}{}
	}()
	<-done
	<-done
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
	fmt.Fprintf(w, `{"code":0,"message":"ok"}`)
}

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "POST" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		fmt.Fprintf(w, `{"code":405,"message":"method not allowed"}`)
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
	user, err := s.store.GetAdminUser(req.Username)
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"code":1001,"message":"invalid credentials"}`)
		return
	}
	if !auth.CheckPassword(req.Password, user.PasswordHash) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"code":1001,"message":"invalid credentials"}`)
		return
	}
	accessToken, _ := s.jwt.GenerateAccessToken(req.Username, "admin")
	refreshToken, _ := s.jwt.GenerateRefreshToken(req.Username, "admin")
	fmt.Fprintf(w, `{"code":0,"message":"ok","data":{"access_token":"%s","refresh_token":"%s"}}`, accessToken, refreshToken)
}

func (s *Server) handleAuthRefresh(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
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
	type userResp struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
		Remark   string `json:"remark,omitempty"`
		Status   int    `json:"status"`
		Online   bool   `json:"online"`
	}
	list := make([]userResp, 0, len(users))
	for _, u := range users {
		_, active := s.isClientActive(u.Username)
		list = append(list, userResp{ID: u.ID, Username: u.Username, Remark: u.Remark, Status: u.Status, Online: active})
	}
	data, _ := json.Marshal(map[string]interface{}{"code": 0, "data": list})
	w.Write(data)
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Remark   string `json:"remark"`
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
	code, salt, _ := auth.GenerateAuthCode()
	authCodeHash := auth.HashAuthCode(code, salt)
	if err := s.store.CreateUser(req.Username, authCodeHash, salt); err != nil {
		w.WriteHeader(http.StatusConflict)
		fmt.Fprintf(w, `{"code":3001,"message":"username already exists"}`)
		return
	}
	fmt.Fprintf(w, `{"code":0,"message":"ok","data":{"username":"%s","authCode":"%s","remark":"%s"}}`, req.Username, code, req.Remark)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	username := r.URL.Query().Get("username")
	if username == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if err := s.store.DeleteUser(username); err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
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
	fmt.Printf("[USER] %s disabled (disconnectNow=%v)\n", username, req.DisconnectNow)
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
	fmt.Printf("[USER] %s enabled\n", username)
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
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"code":5000,"message":"generate auth code failed"}`)
		return
	}
	if err := s.store.ResetUserAuthCode(username, auth.HashAuthCode(code, salt), salt); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"code":5000,"message":"update failed"}`)
		return
	}
	s.kickClient(username, "auth_code_reset")
	fmt.Printf("[USER] %s auth code reset\n", username)
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
	user, err := s.store.GetUserByUsername(req.Username)
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"code":1001,"message":"user not found"}`)
		return
	}
	if user.Status != 1 {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintf(w, `{"code":1003,"message":"user disabled"}`)
		return
	}
	expectedHash := auth.HashAuthCode(req.AuthCode, user.AuthCodeSalt)
	if expectedHash != user.AuthCodeHash {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"code":1001,"message":"invalid auth code"}`)
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
		Username  string `json:"username"`
		SessionID string `json:"sessionId"`
		Connected int64  `json:"connected"`
		LastPing  int64  `json:"lastPing"`
	}
	list := make([]clientInfo, 0, len(s.clients))
	for _, c := range s.clients {
		list = append(list, clientInfo{Username: c.username, SessionID: c.sessionID, Connected: c.connected.Unix(), LastPing: c.lastPing.Unix()})
	}
	s.clientsMu.RUnlock()
	data, _ := json.Marshal(map[string]interface{}{"code": 0, "data": list})
	w.Write(data)
}

func (s *Server) handlePortPool(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	total, used := s.portPool.GetStats()
	fmt.Fprintf(w, `{"code":0,"data":{"total":%d,"used":%d}}`, total, used)
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
		log.Printf("[CTRL] yamux error: %v", err)
		return
	}
	defer session.Close()

	controlStream, err := session.Accept()
	if err != nil {
		log.Printf("[CTRL] Accept stream error: %v", err)
		return
	}
	defer controlStream.Close()

	codec := protocol.NewCodec()

	helloMsg, err := codec.ReadMessage(controlStream)
	if err != nil || helloMsg.Type != protocol.TypeHello {
		log.Printf("[CTRL] Expected HELLO")
		return
	}
	helloDataBytes, _ := json.Marshal(helloMsg.Data)
	helloData := &protocol.HelloData{}
	json.Unmarshal(helloDataBytes, helloData)
	fmt.Printf("[HELLO] %s v%s (%s/%s)\n", conn.RemoteAddr(), helloData.Version, helloData.OS, helloData.Arch)

	resp := protocol.NewResponse(helloMsg, protocol.CodeOK, &protocol.HelloRespData{
		ServerVersion: version.Version,
		Caps:          []string{"mux", "tls"},
	})
	codec.WriteMessage(controlStream, resp)

	registerMsg, err := codec.ReadMessage(controlStream)
	if err != nil || registerMsg.Type != protocol.TypeRegister {
		log.Printf("[CTRL] Expected REGISTER")
		return
	}

	client, err := s.handleRegister(controlStream, registerMsg, session)
	if err != nil {
		log.Printf("[CTRL] Register error: %v", err)
		return
	}
	fmt.Printf("[+] Client registered: %s (session: %s)\n", client.username, client.sessionID)
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

func (s *Server) handleRegister(stream net.Conn, msg *protocol.Message, session *yamux.Session) (*Client, error) {
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

	if user.OnlineSession != nil {
		old, active := s.isClientActive(regData.Username)
		if active {
			resp := protocol.NewResponse(msg, protocol.CodeAuthCodeInUse, nil)
			codec.WriteMessage(stream, resp)
			return nil, fmt.Errorf("auth code already in use by %s", regData.Username)
		}
		// online_session 为残留值（seeinpm 重启后无人清理，或旧连接已死但心跳协程尚未退出）：
		// 踢掉旧连接并清除状态后放行注册，否则 seeinps 会永远被 1002 拒绝
		fmt.Printf("[CTRL] Replacing stale session for %s\n", regData.Username)
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
		sessionID:     sessionID,
		connected:     time.Now(),
		lastPing:      time.Now(),
		session:       session,
		controlStream: stream,
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
			client.writeControl(protocol.NewResponse(msg, protocol.CodeOK, nil))
		case protocol.TypeAllocPort:
			s.handleAllocPort(msg, client)
		case protocol.TypeReleasePort:
			s.handleReleasePort(msg, client)
		default:
			client.writeControl(protocol.NewResponse(msg, protocol.CodeUnsupportedType, nil))
		}
	}
}

// writeControl 带锁写控制流：心跳应答/端口响应与 SESSION_REVOKE 推送共用一把锁防帧交错
func (c *Client) writeControl(msg *protocol.Message) {
	c.ctrlWriteMu.Lock()
	defer c.ctrlWriteMu.Unlock()
	protocol.NewCodec().WriteMessage(c.controlStream, msg)
}

func (s *Server) handleAllocPort(msg *protocol.Message, client *Client) {
	dataBytes, _ := json.Marshal(msg.Data)
	d := &protocol.AllocPortData{}
	json.Unmarshal(dataBytes, d)

	s.publicMu.RLock()
	if oldPL, ok := s.publicConnsByProxy[d.ProxyID]; ok {
		s.publicMu.RUnlock()
		s.stopPublicListener(oldPL.Port)
	} else {
		s.publicMu.RUnlock()
	}

	alloc, err := s.portPool.Allocate(client.username, d.ProxyID, d.Type, d.Port)
	if err != nil {
		log.Printf("[ALLOC] Error: %v", err)
		client.writeControl(protocol.NewResponse(msg, protocol.CodePortConflict, nil))
		return
	}

	if err := s.startPublicListener(alloc.Port, d.ProxyID, d.Type, client); err != nil {
		log.Printf("[ALLOC] Start listener error: %v", err)
		s.portPool.Release(d.ProxyID)
		client.writeControl(protocol.NewResponse(msg, protocol.CodeInternal, nil))
		return
	}

	fmt.Printf("[ALLOC] proxy=%s type=%s -> port=%d (user=%s)\n", d.ProxyID, d.Type, alloc.Port, client.username)
	client.writeControl(protocol.NewResponse(msg, protocol.CodeOK, &protocol.AllocPortRespData{ProxyID: d.ProxyID, Port: alloc.Port}))
}

func (s *Server) handleReleasePort(msg *protocol.Message, client *Client) {
	dataBytes, _ := json.Marshal(msg.Data)
	d := &protocol.ReleasePortData{}
	json.Unmarshal(dataBytes, d)

	if alloc, err := s.store.GetPortByProxyID(d.ProxyID); err == nil {
		s.stopPublicListener(alloc.Port)
	}
	s.portPool.Release(d.ProxyID)
	fmt.Printf("[RELEASE] proxy=%s\n", d.ProxyID)
	client.writeControl(protocol.NewResponse(msg, protocol.CodeOK, nil))
}

// kickClient 断开用户的控制连接并释放其全部端口（禁用/重置授权码时调用）。
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
		fmt.Printf("[USER] Kicked %s (reason=%s)\n", username, reason)
	} else {
		// 无活跃连接也要清 DB 残留在线状态，否则重连被 1002 语义误判
		s.store.ClearUserOnlineStatus(username)
	}
	s.releaseUserPorts(username)
}

// releaseUserPorts 停止该用户全部公网监听并释放端口
func (s *Server) releaseUserPorts(username string) {
	allocs, err := s.store.GetAllocationsByUser(username)
	if err != nil {
		log.Printf("[USER] Release ports for %s error: %v", username, err)
		return
	}
	for _, a := range allocs {
		s.stopPublicListener(a.Port)
		s.portPool.Release(a.ProxyID)
	}
}

// removeClientIfCurrent 仅当 map 中仍是该 client 时才移除，
// 防止被顶掉的旧连接延迟退出时误删同用户新注册的 client
func (s *Server) removeClientIfCurrent(c *Client) {
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()
	if cur, ok := s.clients[c.username]; ok && cur == c {
		delete(s.clients, c.username)
		s.sessionsMu.Lock()
		delete(s.sessions, c.sessionID)
		s.sessionsMu.Unlock()
		fmt.Printf("[-] Client disconnected: %s\n", c.username)
	}
}

func (s *Server) shutdown() {
	fmt.Println("[seeinpm] Shutting down...")

	s.publicMu.Lock()
	for port, pl := range s.publicConns {
		fmt.Printf("[seeinpm] Stopping listener on :%d\n", port)
		if pl.Ln != nil {
			pl.Ln.Close()
		}
		delete(s.publicConns, port)
		delete(s.publicConnsByProxy, pl.ProxyID)
	}
	s.publicMu.Unlock()

	s.clientsMu.RLock()
	for username, c := range s.clients {
		fmt.Printf("[seeinpm] Disconnecting client: %s\n", username)
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

	fmt.Println("[seeinpm] Stopped")
}

func main() {
	confPath := flag.String("conf", "conf/seeinpm.toml", "config file")
	flag.Parse()

	cfg, err := config.LoadPMConfig(*confPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Config error: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\nShutting down...")
		cancel()
	}()

	srv, err := NewServer(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Init error: %v\n", err)
		os.Exit(1)
	}

	if err := srv.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
