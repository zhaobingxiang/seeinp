package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func New(dbPath string) (*Store, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	queries := []string{
		"CREATE TABLE IF NOT EXISTS admin_users (id INTEGER PRIMARY KEY AUTOINCREMENT, username TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)",
		"CREATE TABLE IF NOT EXISTS users (id INTEGER PRIMARY KEY AUTOINCREMENT, username TEXT NOT NULL UNIQUE, remark TEXT DEFAULT '', auth_code TEXT NOT NULL, auth_code_salt TEXT NOT NULL, status INTEGER NOT NULL DEFAULT 1, online_session TEXT, online_since INTEGER, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)",
		"CREATE UNIQUE INDEX IF NOT EXISTS ux_users_auth_code ON users(auth_code)",
		"CREATE TABLE IF NOT EXISTS port_allocations (id INTEGER PRIMARY KEY AUTOINCREMENT, port INTEGER NOT NULL UNIQUE, user_id TEXT NOT NULL, proxy_id TEXT NOT NULL, proxy_type TEXT NOT NULL, status INTEGER NOT NULL DEFAULT 1, allocated_at INTEGER NOT NULL, released_at INTEGER)",
		"CREATE INDEX IF NOT EXISTS idx_port_alloc_proxy ON port_allocations(proxy_id)",
		"CREATE TABLE IF NOT EXISTS proxies (id INTEGER PRIMARY KEY AUTOINCREMENT, username TEXT NOT NULL, proxy_id TEXT NOT NULL, proxy_type TEXT NOT NULL DEFAULT '', status INTEGER NOT NULL DEFAULT 1, online INTEGER NOT NULL DEFAULT 0, last_online_at INTEGER, last_offline_at INTEGER, bytes_in INTEGER NOT NULL DEFAULT 0, bytes_out INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, UNIQUE(username, proxy_id))",
		"CREATE TABLE IF NOT EXISTS proxy_sessions (id INTEGER PRIMARY KEY AUTOINCREMENT, username TEXT NOT NULL, proxy_id TEXT NOT NULL, online_at INTEGER NOT NULL, offline_at INTEGER, remote_addr TEXT NOT NULL DEFAULT '')",
		"CREATE INDEX IF NOT EXISTS idx_proxy_sessions_proxy ON proxy_sessions(username, proxy_id, id DESC)",
		"CREATE TABLE IF NOT EXISTS audit_logs (id INTEGER PRIMARY KEY AUTOINCREMENT, username TEXT NOT NULL, action TEXT NOT NULL, target TEXT NOT NULL DEFAULT '', detail TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL)",
		"CREATE INDEX IF NOT EXISTS idx_audit_logs_created ON audit_logs(created_at DESC)",
		"CREATE TABLE IF NOT EXISTS port_pool_config (id INTEGER PRIMARY KEY CHECK(id=1), ranges TEXT NOT NULL, updated_at INTEGER NOT NULL)",
		"CREATE TABLE IF NOT EXISTS versions (id INTEGER PRIMARY KEY AUTOINCREMENT, endpoint TEXT NOT NULL, version TEXT NOT NULL, goos TEXT NOT NULL DEFAULT 'linux', goarch TEXT NOT NULL DEFAULT 'amd64', file_name TEXT NOT NULL, file_size INTEGER NOT NULL, sha256 TEXT NOT NULL, note TEXT NOT NULL DEFAULT '', released_by TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, UNIQUE(endpoint, version, goos, goarch))",
	}
	for _, q := range queries {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("exec migration: %w", err)
		}
	}
	// v2: audit_logs 增加 source 列（'pm'=管理端本地操作，'ps'=B 端上报），老库 ALTER 补齐
	if _, err := s.db.Exec("ALTER TABLE audit_logs ADD COLUMN source TEXT NOT NULL DEFAULT 'pm'"); err != nil {
		// 列已存在时 SQLite 报 duplicate column，忽略即可
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migrate audit_logs.source: %w", err)
		}
	}
	if _, err := s.db.Exec("CREATE INDEX IF NOT EXISTS idx_audit_logs_source_created ON audit_logs(source, created_at DESC)"); err != nil {
		return fmt.Errorf("migrate audit_logs index: %w", err)
	}
	// v4: versions 表增加平台维度（goos/goarch），唯一约束扩为 (endpoint, version, goos, goarch)。
	// SQLite 无法修改表内 UNIQUE，老库检测到旧 schema（无 goos 列）时整表重建，历史行回填 linux/amd64。
	var hasVersions int
	s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='versions'").Scan(&hasVersions)
	if hasVersions > 0 {
		var hasGoos int
		s.db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('versions') WHERE name='goos'").Scan(&hasGoos)
		if hasGoos == 0 {
			steps := []string{
				"CREATE TABLE versions_v4 (id INTEGER PRIMARY KEY AUTOINCREMENT, endpoint TEXT NOT NULL, version TEXT NOT NULL, goos TEXT NOT NULL DEFAULT 'linux', goarch TEXT NOT NULL DEFAULT 'amd64', file_name TEXT NOT NULL, file_size INTEGER NOT NULL, sha256 TEXT NOT NULL, note TEXT NOT NULL DEFAULT '', released_by TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, UNIQUE(endpoint, version, goos, goarch))",
				"INSERT INTO versions_v4 (id, endpoint, version, goos, goarch, file_name, file_size, sha256, note, released_by, created_at) SELECT id, endpoint, version, 'linux', 'amd64', file_name, file_size, sha256, COALESCE(note,''), COALESCE(released_by,''), created_at FROM versions",
				"DROP TABLE versions",
				"ALTER TABLE versions_v4 RENAME TO versions",
			}
			for _, q := range steps {
				if _, err := s.db.Exec(q); err != nil {
					return fmt.Errorf("migrate versions v4: %w", err)
				}
			}
		}
	}

	// v3: 用户有效期/端口数量配额/用户端口池（老库 ALTER 补齐）
	for _, col := range []string{
		"ALTER TABLE users ADD COLUMN expires_at INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE users ADD COLUMN max_ports INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE users ADD COLUMN port_ranges TEXT NOT NULL DEFAULT '[]'",
	} {
		if _, err := s.db.Exec(col); err != nil {
			if !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("migrate users limits: %w", err)
			}
		}
	}
	return nil
}

type AdminUser struct {
	ID           int64
	Username     string
	PasswordHash string
	CreatedAt    int64
	UpdatedAt    int64
}

func (s *Store) GetAdminUser(username string) (*AdminUser, error) {
	u := &AdminUser{}
	err := s.db.QueryRow("SELECT id, username, password_hash, created_at, updated_at FROM admin_users WHERE username = ?", username).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Store) CreateAdminUser(username, passwordHash string) error {
	now := time.Now().Unix()
	_, err := s.db.Exec("INSERT INTO admin_users (username, password_hash, created_at, updated_at) VALUES (?, ?, ?, ?)", username, passwordHash, now, now)
	return err
}

func (s *Store) HasAdminUser() bool {
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM admin_users").Scan(&count)
	return count > 0
}

type User struct {
	ID            int64
	Username      string
	Remark        string
	AuthCodeHash  string
	AuthCodeSalt  string
	Status        int
	OnlineSession *string
	OnlineSince   *int64
	ExpiresAt     int64 // 有效期截止时间（unix 秒；0=不过期），到期断开并拒绝注册
	MaxPorts      int   // 端口数量配额（0=不限）
	PortRanges    []PortRange
	CreatedAt     int64
	UpdatedAt     int64
}

func (s *Store) CreateUser(username, authCodeHash, authCodeSalt string, expiresAt int64, maxPorts int, portRanges []PortRange) error {
	now := time.Now().Unix()
	rj := "[]"
	if len(portRanges) > 0 {
		b, _ := json.Marshal(portRanges)
		rj = string(b)
	}
	_, err := s.db.Exec("INSERT INTO users (username, auth_code, auth_code_salt, status, expires_at, max_ports, port_ranges, created_at, updated_at) VALUES (?, ?, ?, 1, ?, ?, ?, ?, ?)",
		username, authCodeHash, authCodeSalt, expiresAt, maxPorts, rj, now, now)
	return err
}

// UpdateUserLimits 更新用户有效期/端口数量配额/用户端口池
func (s *Store) UpdateUserLimits(username string, expiresAt int64, maxPorts int, portRanges []PortRange) error {
	now := time.Now().Unix()
	rj := "[]"
	if len(portRanges) > 0 {
		b, _ := json.Marshal(portRanges)
		rj = string(b)
	}
	res, err := s.db.Exec("UPDATE users SET expires_at = ?, max_ports = ?, port_ranges = ?, updated_at = ? WHERE username = ?",
		expiresAt, maxPorts, rj, now, username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("user not found")
	}
	return nil
}

// CountActiveAllocationsByUser 统计用户当前活跃（status=1）的端口分配数
func (s *Store) CountActiveAllocationsByUser(userID string) (int, error) {
	var n int
	err := s.db.QueryRow("SELECT COUNT(*) FROM port_allocations WHERE status = 1 AND user_id = ?", userID).Scan(&n)
	return n, err
}

func (s *Store) GetUserByUsername(username string) (*User, error) {
	u := &User{}
	var onlineSession sql.NullString
	var onlineSince sql.NullInt64
	var portRanges string
	err := s.db.QueryRow("SELECT id, username, COALESCE(remark,''), auth_code, auth_code_salt, status, online_session, online_since, COALESCE(expires_at,0), COALESCE(max_ports,0), COALESCE(port_ranges,'[]'), created_at, updated_at FROM users WHERE username = ?", username).
		Scan(&u.ID, &u.Username, &u.Remark, &u.AuthCodeHash, &u.AuthCodeSalt, &u.Status, &onlineSession, &onlineSince, &u.ExpiresAt, &u.MaxPorts, &portRanges, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if onlineSession.Valid {
		u.OnlineSession = &onlineSession.String
	}
	if onlineSince.Valid {
		v := onlineSince.Int64
		u.OnlineSince = &v
	}
	json.Unmarshal([]byte(portRanges), &u.PortRanges)
	return u, nil
}

func (s *Store) ListUsers() ([]*User, error) {
	rows, err := s.db.Query("SELECT id, username, COALESCE(remark,''), auth_code, auth_code_salt, status, online_session, online_since, COALESCE(expires_at,0), COALESCE(max_ports,0), COALESCE(port_ranges,'[]'), created_at, updated_at FROM users ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []*User
	for rows.Next() {
		u := &User{}
		var onlineSession sql.NullString
		var onlineSince sql.NullInt64
		var portRanges string
		if err := rows.Scan(&u.ID, &u.Username, &u.Remark, &u.AuthCodeHash, &u.AuthCodeSalt, &u.Status, &onlineSession, &onlineSince, &u.ExpiresAt, &u.MaxPorts, &portRanges, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		if onlineSession.Valid {
			u.OnlineSession = &onlineSession.String
		}
		if onlineSince.Valid {
			v := onlineSince.Int64
			u.OnlineSince = &v
		}
		json.Unmarshal([]byte(portRanges), &u.PortRanges)
		users = append(users, u)
	}
	return users, nil
}

func (s *Store) UpdateUserOnlineStatus(username, sessionID string) error {
	now := time.Now().Unix()
	_, err := s.db.Exec("UPDATE users SET online_session = ?, online_since = ?, updated_at = ? WHERE username = ?", sessionID, now, now, username)
	return err
}

func (s *Store) ClearUserOnlineStatus(username string) error {
	now := time.Now().Unix()
	_, err := s.db.Exec("UPDATE users SET online_session = NULL, online_since = NULL, updated_at = ? WHERE username = ?", now, username)
	return err
}

// ClearUserOnlineStatusIfSession 仅当当前 online_session 与给定 sessionID 一致时才清除，
// 防止被替换的旧连接延迟退出时误删新会话的在线状态（否则 cleanup 会误释放新会话的端口）
func (s *Store) ClearUserOnlineStatusIfSession(username, sessionID string) error {
	now := time.Now().Unix()
	_, err := s.db.Exec("UPDATE users SET online_session = NULL, online_since = NULL, updated_at = ? WHERE username = ? AND online_session = ?", now, username, sessionID)
	return err
}

func (s *Store) DeleteUser(username string) error {
	_, err := s.db.Exec("DELETE FROM users WHERE username = ?", username)
	return err
}

// SetUserStatus 设置用户状态（1 启用 / 0 禁用）；返回用户是否存在
func (s *Store) SetUserStatus(username string, status int) (bool, error) {
	now := time.Now().Unix()
	res, err := s.db.Exec("UPDATE users SET status = ?, updated_at = ? WHERE username = ?", status, now, username)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// GetAllocationsByUser 返回该用户当前活跃（status=1）的端口分配
func (s *Store) GetAllocationsByUser(userID string) ([]*PortAllocation, error) {
	rows, err := s.db.Query("SELECT id, port, user_id, proxy_id, proxy_type, status, allocated_at, released_at FROM port_allocations WHERE status = 1 AND user_id = ?", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var allocs []*PortAllocation
	for rows.Next() {
		pa := &PortAllocation{}
		var releasedAt sql.NullInt64
		if err := rows.Scan(&pa.ID, &pa.Port, &pa.UserID, &pa.ProxyID, &pa.ProxyType, &pa.Status, &pa.AllocatedAt, &releasedAt); err != nil {
			return nil, err
		}
		if releasedAt.Valid {
			pa.ReleasedAt = &releasedAt.Int64
		}
		allocs = append(allocs, pa)
	}
	return allocs, nil
}

func (s *Store) ResetUserAuthCode(username, newAuthCodeHash, newSalt string) error {
	now := time.Now().Unix()
	_, err := s.db.Exec("UPDATE users SET auth_code = ?, auth_code_salt = ?, online_session = NULL, online_since = NULL, updated_at = ? WHERE username = ?", newAuthCodeHash, newSalt, now, username)
	return err
}

type PortAllocation struct {
	ID          int64
	Port        int
	UserID      string
	ProxyID     string
	ProxyType   string
	Status      int
	AllocatedAt int64
	ReleasedAt  *int64
}

// GetPortByProxyID 返回该代理当前活跃（status=1）的端口分配
func (s *Store) GetPortByProxyID(proxyID string) (*PortAllocation, error) {
	pa := &PortAllocation{}
	var releasedAt sql.NullInt64
	err := s.db.QueryRow("SELECT id, port, user_id, proxy_id, proxy_type, status, allocated_at, released_at FROM port_allocations WHERE proxy_id = ? AND status = 1", proxyID).Scan(&pa.ID, &pa.Port, &pa.UserID, &pa.ProxyID, &pa.ProxyType, &pa.Status, &pa.AllocatedAt, &releasedAt)
	if err != nil {
		return nil, err
	}
	if releasedAt.Valid {
		pa.ReleasedAt = &releasedAt.Int64
	}
	return pa, nil
}

// GetLastPortByProxyID 返回该代理最近一次使用的端口（无论当前状态），用于断线重连后端口稳定
func (s *Store) GetLastPortByProxyID(proxyID string) (*PortAllocation, error) {
	pa := &PortAllocation{}
	var releasedAt sql.NullInt64
	err := s.db.QueryRow("SELECT id, port, user_id, proxy_id, proxy_type, status, allocated_at, released_at FROM port_allocations WHERE proxy_id = ? ORDER BY allocated_at DESC, id DESC LIMIT 1", proxyID).Scan(&pa.ID, &pa.Port, &pa.UserID, &pa.ProxyID, &pa.ProxyType, &pa.Status, &pa.AllocatedAt, &releasedAt)
	if err != nil {
		return nil, err
	}
	if releasedAt.Valid {
		pa.ReleasedAt = &releasedAt.Int64
	}
	return pa, nil
}

// DeleteReleasedByProxyID 清理该代理的历史释放记录，避免残留行占用 UNIQUE(port)
func (s *Store) DeleteReleasedByProxyID(proxyID string) error {
	_, err := s.db.Exec("DELETE FROM port_allocations WHERE proxy_id = ? AND status = 0", proxyID)
	return err
}

// UpsertPortAllocation 按端口 upsert：端口上若有已释放行则直接复活，规避唯一约束冲突
func (s *Store) UpsertPortAllocation(port int, userID, proxyID, proxyType string) error {
	now := time.Now().Unix()
	_, err := s.db.Exec(`INSERT INTO port_allocations (port, user_id, proxy_id, proxy_type, status, allocated_at)
VALUES (?, ?, ?, ?, 1, ?)
ON CONFLICT(port) DO UPDATE SET user_id = excluded.user_id, proxy_id = excluded.proxy_id, proxy_type = excluded.proxy_type, status = 1, allocated_at = excluded.allocated_at, released_at = NULL`,
		port, userID, proxyID, proxyType, now)
	return err
}

func (s *Store) ReleasePort(proxyID string) error {
	now := time.Now().Unix()
	_, err := s.db.Exec("UPDATE port_allocations SET status = 0, released_at = ? WHERE proxy_id = ? AND status = 1", now, proxyID)
	return err
}

func (s *Store) IsPortAllocated(port int) bool {
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM port_allocations WHERE port = ? AND status = 1", port).Scan(&count)
	return count > 0
}

func (s *Store) GetAllocatedPorts() ([]*PortAllocation, error) {
	rows, err := s.db.Query("SELECT id, port, user_id, proxy_id, proxy_type, status, allocated_at, released_at FROM port_allocations WHERE status = 1")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var allocs []*PortAllocation
	for rows.Next() {
		pa := &PortAllocation{}
		var releasedAt sql.NullInt64
		if err := rows.Scan(&pa.ID, &pa.Port, &pa.UserID, &pa.ProxyID, &pa.ProxyType, &pa.Status, &pa.AllocatedAt, &releasedAt); err != nil {
			return nil, err
		}
		if releasedAt.Valid {
			pa.ReleasedAt = &releasedAt.Int64
		}
		allocs = append(allocs, pa)
	}
	return allocs, nil
}

func (s *Store) CleanupStaleAllocations() error {
	_, err := s.db.Exec("UPDATE port_allocations SET status = 0, released_at = ? WHERE status = 1 AND user_id IN (SELECT username FROM users WHERE online_session IS NULL)", time.Now().Unix())
	return err
}

func (s *Store) GetStaleAllocations() ([]*PortAllocation, error) {
	rows, err := s.db.Query("SELECT pa.id, pa.port, pa.user_id, pa.proxy_id, pa.proxy_type, pa.status, pa.allocated_at, pa.released_at FROM port_allocations pa JOIN users u ON pa.user_id = u.username WHERE pa.status = 1 AND u.online_session IS NULL")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var allocs []*PortAllocation
	for rows.Next() {
		pa := &PortAllocation{}
		var releasedAt sql.NullInt64
		if err := rows.Scan(&pa.ID, &pa.Port, &pa.UserID, &pa.ProxyID, &pa.ProxyType, &pa.Status, &pa.AllocatedAt, &releasedAt); err != nil {
			return nil, err
		}
		if releasedAt.Valid {
			pa.ReleasedAt = &releasedAt.Int64
		}
		allocs = append(allocs, pa)
	}
	return allocs, nil
}

// ==================== 代理管理（proxies / proxy_sessions） ====================

// PMProxy 一条代理记录：标识为 (username, proxy_id)，对应 seeinps 上配置的一个端口映射。
// status 为手动开关（1 启用 / 0 禁用），online 为当前在线标记；流量为累计字节数。
type PMProxy struct {
	ID            int64
	Username      string
	ProxyID       string
	ProxyType     string
	Status        int
	Online        bool
	LastOnlineAt  *int64
	LastOfflineAt *int64
	BytesIn       int64
	BytesOut      int64
	CreatedAt     int64
	UpdatedAt     int64
}

// ProxySession 代理的一次在线会话（上线 -> 离线）
type ProxySession struct {
	ID         int64
	Username   string
	ProxyID    string
	OnlineAt   int64
	OfflineAt  *int64
	RemoteAddr string
}

const pmProxyCols = "id, username, proxy_id, proxy_type, status, online, last_online_at, last_offline_at, bytes_in, bytes_out, created_at, updated_at"

func scanPMProxy(row interface{ Scan(...interface{}) error }) (*PMProxy, error) {
	p := &PMProxy{}
	var online int
	var lastOnline, lastOffline sql.NullInt64
	if err := row.Scan(&p.ID, &p.Username, &p.ProxyID, &p.ProxyType, &p.Status, &online, &lastOnline, &lastOffline, &p.BytesIn, &p.BytesOut, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.Online = online == 1
	if lastOnline.Valid {
		p.LastOnlineAt = &lastOnline.Int64
	}
	if lastOffline.Valid {
		p.LastOfflineAt = &lastOffline.Int64
	}
	return p, nil
}

// MarkProxyOnline 代理上线：不存在则创建（默认启用），存在则置在线并刷新最近连接时间；
// 同时开启一条会话记录（若已有未关闭会话则不重复开）。返回代理当前 status（供禁用校验）。
func (s *Store) MarkProxyOnline(username, proxyID, proxyType, remoteAddr string) (int, error) {
	now := time.Now().Unix()
	if _, err := s.db.Exec(`INSERT INTO proxies (username, proxy_id, proxy_type, status, online, last_online_at, created_at, updated_at)
VALUES (?, ?, ?, 1, 1, ?, ?, ?)
ON CONFLICT(username, proxy_id) DO UPDATE SET proxy_type = excluded.proxy_type, online = 1, last_online_at = excluded.last_online_at, updated_at = excluded.updated_at`,
		username, proxyID, proxyType, now, now, now); err != nil {
		return 0, err
	}
	var status int
	if err := s.db.QueryRow("SELECT status FROM proxies WHERE username = ? AND proxy_id = ?", username, proxyID).Scan(&status); err != nil {
		return 0, err
	}
	var openSessions int
	s.db.QueryRow("SELECT COUNT(*) FROM proxy_sessions WHERE username = ? AND proxy_id = ? AND offline_at IS NULL", username, proxyID).Scan(&openSessions)
	if openSessions == 0 {
		if _, err := s.db.Exec("INSERT INTO proxy_sessions (username, proxy_id, online_at, remote_addr) VALUES (?, ?, ?, ?)", username, proxyID, now, remoteAddr); err != nil {
			return 0, err
		}
	}
	return status, nil
}

// MarkProxyOffline 代理离线：置离线标记、刷新最近离线时间并关闭未关闭的会话
func (s *Store) MarkProxyOffline(username, proxyID string) error {
	now := time.Now().Unix()
	if _, err := s.db.Exec("UPDATE proxies SET online = 0, last_offline_at = ?, updated_at = ? WHERE username = ? AND proxy_id = ?", now, now, username, proxyID); err != nil {
		return err
	}
	_, err := s.db.Exec("UPDATE proxy_sessions SET offline_at = ? WHERE username = ? AND proxy_id = ? AND offline_at IS NULL", now, username, proxyID)
	return err
}

// MarkUserProxiesOffline 用户全部在线代理离线（seeinps 断开时调用）
func (s *Store) MarkUserProxiesOffline(username string) error {
	now := time.Now().Unix()
	if _, err := s.db.Exec("UPDATE proxies SET online = 0, last_offline_at = ?, updated_at = ? WHERE username = ? AND online = 1", now, now, username); err != nil {
		return err
	}
	_, err := s.db.Exec("UPDATE proxy_sessions SET offline_at = ? WHERE username = ? AND offline_at IS NULL", now, username)
	return err
}

// ListProxies 返回全部代理记录（含离线与禁用），按用户名、代理ID排序
func (s *Store) ListProxies() ([]*PMProxy, error) {
	rows, err := s.db.Query("SELECT " + pmProxyCols + " FROM proxies ORDER BY username, proxy_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*PMProxy
	for rows.Next() {
		p, err := scanPMProxy(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, p)
	}
	return list, nil
}

// GetProxy 返回单个代理记录
func (s *Store) GetProxy(username, proxyID string) (*PMProxy, error) {
	return scanPMProxy(s.db.QueryRow("SELECT "+pmProxyCols+" FROM proxies WHERE username = ? AND proxy_id = ?", username, proxyID))
}

// SetProxyStatus 设置代理启用/禁用状态；返回记录是否存在
func (s *Store) SetProxyStatus(username, proxyID string, status int) (bool, error) {
	res, err := s.db.Exec("UPDATE proxies SET status = ?, updated_at = ? WHERE username = ? AND proxy_id = ?", status, time.Now().Unix(), username, proxyID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// GetProxySessions 返回代理最近的会话记录（按时间倒序，limit 条）
func (s *Store) GetProxySessions(username, proxyID string, limit int) ([]*ProxySession, error) {
	rows, err := s.db.Query("SELECT id, username, proxy_id, online_at, offline_at, remote_addr FROM proxy_sessions WHERE username = ? AND proxy_id = ? ORDER BY id DESC LIMIT ?", username, proxyID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*ProxySession
	for rows.Next() {
		ps := &ProxySession{}
		var offlineAt sql.NullInt64
		if err := rows.Scan(&ps.ID, &ps.Username, &ps.ProxyID, &ps.OnlineAt, &offlineAt, &ps.RemoteAddr); err != nil {
			return nil, err
		}
		if offlineAt.Valid {
			ps.OfflineAt = &offlineAt.Int64
		}
		list = append(list, ps)
	}
	return list, nil
}

// TrimProxySessions 每个代理只保留最近 keep 条会话记录
func (s *Store) TrimProxySessions(username, proxyID string, keep int) error {
	_, err := s.db.Exec(`DELETE FROM proxy_sessions WHERE username = ? AND proxy_id = ? AND id NOT IN
(SELECT id FROM proxy_sessions WHERE username = ? AND proxy_id = ? ORDER BY id DESC LIMIT ?)`,
		username, proxyID, username, proxyID, keep)
	return err
}

// AddProxyTraffic 累加代理流量计数（in: 外部->内网方向字节, out: 内网->外部方向字节）
func (s *Store) AddProxyTraffic(username, proxyID string, in, out int64) error {
	_, err := s.db.Exec("UPDATE proxies SET bytes_in = bytes_in + ?, bytes_out = bytes_out + ? WHERE username = ? AND proxy_id = ?", in, out, username, proxyID)
	return err
}

// CleanupOfflineProxies 删除离线超过 cutoff 时间戳且未禁用的代理及其会话记录。
// 手动禁用（status=0）的代理永不自动清理；被清理的代理重新连接时会自动重建记录。
func (s *Store) CleanupOfflineProxies(cutoff int64) (int64, error) {
	rows, err := s.db.Query("SELECT username, proxy_id FROM proxies WHERE status = 1 AND online = 0 AND COALESCE(last_offline_at, created_at) < ?", cutoff)
	if err != nil {
		return 0, err
	}
	type key struct{ u, p string }
	var doomed []key
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.u, &k.p); err != nil {
			rows.Close()
			return 0, err
		}
		doomed = append(doomed, k)
	}
	rows.Close()
	for _, k := range doomed {
		if _, err := s.db.Exec("DELETE FROM proxy_sessions WHERE username = ? AND proxy_id = ?", k.u, k.p); err != nil {
			return 0, err
		}
		if _, err := s.db.Exec("DELETE FROM proxies WHERE username = ? AND proxy_id = ?", k.u, k.p); err != nil {
			return 0, err
		}
	}
	return int64(len(doomed)), nil
}

// ==================== 操作审计日志（audit_logs） ====================

// AuditLog 管理员操作审计记录。Detail 只存非敏感摘要（禁止记录密码/授权码/Token）。
// Source 标记来源：'pm'=管理端本地操作，'ps'=B 端（seeinps）上报。
type AuditLog struct {
	ID        int64
	Username  string
	Action    string
	Target    string
	Detail    string
	CreatedAt int64
	Source    string
}

// InsertAuditLog 追加一条管理端（PM）审计记录；失败只返回错误由调用方决定是否吞掉（不影响主流程）
func (s *Store) InsertAuditLog(username, action, target, detail string) error {
	_, err := s.db.Exec("INSERT INTO audit_logs (username, action, target, detail, created_at, source) VALUES (?, ?, ?, ?, ?, 'pm')",
		username, action, target, detail, time.Now().Unix())
	return err
}

// InsertAuditLogFromPS 写入一条由 seeinps 上报的审计记录（source='ps'，保留原始时间）
func (s *Store) InsertAuditLogFromPS(username, action, target, detail string, createdAt int64) error {
	_, err := s.db.Exec("INSERT INTO audit_logs (username, action, target, detail, created_at, source) VALUES (?, ?, ?, ?, ?, 'ps')",
		username, action, target, detail, createdAt)
	return err
}

// AuditFilter 审计日志查询条件
type AuditFilter struct {
	Username  string
	Action    string
	Keyword   string // 模糊匹配 username/action/target/detail
	Source    string // pm / ps / 空=全部
	StartTime int64  // Unix 秒，0=不限
	EndTime   int64  // Unix 秒，0=不限
	Page      int
	PageSize  int
}

// ListAuditLogs 分页查询审计日志（按时间倒序），条件为空则不过滤
func (s *Store) ListAuditLogs(f AuditFilter) ([]*AuditLog, int64, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize < 1 || f.PageSize > 200 {
		f.PageSize = 20
	}
	where := " WHERE 1=1"
	args := []interface{}{}
	if f.Username != "" {
		where += " AND username = ?"
		args = append(args, f.Username)
	}
	if f.Action != "" {
		where += " AND action = ?"
		args = append(args, f.Action)
	}
	if f.Keyword != "" {
		where += " AND (username LIKE ? OR action LIKE ? OR target LIKE ? OR detail LIKE ?)"
		kw := "%" + f.Keyword + "%"
		args = append(args, kw, kw, kw, kw)
	}
	if f.Source != "" {
		where += " AND source = ?"
		args = append(args, f.Source)
	}
	if f.StartTime > 0 {
		where += " AND created_at >= ?"
		args = append(args, f.StartTime)
	}
	if f.EndTime > 0 {
		where += " AND created_at <= ?"
		args = append(args, f.EndTime)
	}
	var total int64
	if err := s.db.QueryRow("SELECT COUNT(*) FROM audit_logs"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	query := "SELECT id, username, action, target, COALESCE(detail,''), created_at, COALESCE(source,'pm') FROM audit_logs" + where + " ORDER BY id DESC LIMIT ? OFFSET ?"
	args = append(args, f.PageSize, (f.Page-1)*f.PageSize)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var list []*AuditLog
	for rows.Next() {
		a := &AuditLog{}
		if err := rows.Scan(&a.ID, &a.Username, &a.Action, &a.Target, &a.Detail, &a.CreatedAt, &a.Source); err != nil {
			return nil, 0, err
		}
		list = append(list, a)
	}
	return list, total, rows.Err()
}

// CleanupAuditLogs 删除 cutoff 之前的审计记录
func (s *Store) CleanupAuditLogs(cutoff int64) (int64, error) {
	res, err := s.db.Exec("DELETE FROM audit_logs WHERE created_at < ?", cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ==================== 端口池配置（port_pool_config） ====================

// PortRange 端口池一段范围
type PortRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// GetPortPoolConfig 读取端口池配置（JSON 范围数组）；无配置返回 nil
func (s *Store) GetPortPoolConfig() ([]PortRange, error) {
	var raw string
	err := s.db.QueryRow("SELECT ranges FROM port_pool_config WHERE id=1").Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ranges []PortRange
	if err := json.Unmarshal([]byte(raw), &ranges); err != nil {
		return nil, err
	}
	return ranges, nil
}

// SavePortPoolConfig 保存端口池配置（单行 upsert）
func (s *Store) SavePortPoolConfig(ranges []PortRange) error {
	b, err := json.Marshal(ranges)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("INSERT INTO port_pool_config (id, ranges, updated_at) VALUES (1, ?, ?) "+
		"ON CONFLICT(id) DO UPDATE SET ranges=excluded.ranges, updated_at=excluded.updated_at",
		string(b), time.Now().Unix())
	return err
}

// ==================== 版本管理（versions） ====================

// Version 一条版本发布记录：包文件落盘于 data/releases/{endpoint}/，表只存元数据
type Version struct {
	ID         int64
	Endpoint   string // seeinps（v1 仅此端）
	Version    string // 五段数字版本号，如 1.0.26.0829.01
	GoOS       string // linux / windows / darwin
	GoArch     string // amd64 / arm64 / ...
	FileName   string
	FileSize   int64
	Sha256     string
	Note       string
	ReleasedBy string
	CreatedAt  int64
}

const versionCols = "id, endpoint, version, goos, goarch, file_name, file_size, sha256, COALESCE(note,''), COALESCE(released_by,''), created_at"

func scanVersion(row interface{ Scan(...interface{}) error }) (*Version, error) {
	v := &Version{}
	if err := row.Scan(&v.ID, &v.Endpoint, &v.Version, &v.GoOS, &v.GoArch, &v.FileName, &v.FileSize, &v.Sha256, &v.Note, &v.ReleasedBy, &v.CreatedAt); err != nil {
		return nil, err
	}
	return v, nil
}

// CreateVersion 新增版本发布记录
func (s *Store) CreateVersion(v *Version) error {
	res, err := s.db.Exec("INSERT INTO versions (endpoint, version, goos, goarch, file_name, file_size, sha256, note, released_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		v.Endpoint, v.Version, v.GoOS, v.GoArch, v.FileName, v.FileSize, v.Sha256, v.Note, v.ReleasedBy, time.Now().Unix())
	if err != nil {
		return err
	}
	v.ID, _ = res.LastInsertId()
	return nil
}

// ListVersions 按端返回版本列表（新版本在前）
func (s *Store) ListVersions(endpoint string) ([]*Version, error) {
	rows, err := s.db.Query("SELECT "+versionCols+" FROM versions WHERE endpoint = ? ORDER BY created_at DESC, id DESC", endpoint)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*Version
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	return list, rows.Err()
}

// GetVersion 按 ID 取版本记录
func (s *Store) GetVersion(id int64) (*Version, error) {
	return scanVersion(s.db.QueryRow("SELECT "+versionCols+" FROM versions WHERE id = ?", id))
}

// GetVersionByName 按端/版本号/平台取记录（上传查重用）
func (s *Store) GetVersionByName(endpoint, version, goos, goarch string) (*Version, error) {
	return scanVersion(s.db.QueryRow("SELECT "+versionCols+" FROM versions WHERE endpoint = ? AND version = ? AND goos = ? AND goarch = ?", endpoint, version, goos, goarch))
}

// GetLatestVersion 返回该端指定平台最新发布的版本；无记录返回 sql.ErrNoRows
func (s *Store) GetLatestVersion(endpoint, goos, goarch string) (*Version, error) {
	return scanVersion(s.db.QueryRow("SELECT "+versionCols+" FROM versions WHERE endpoint = ? AND goos = ? AND goarch = ? ORDER BY created_at DESC, id DESC LIMIT 1", endpoint, goos, goarch))
}

// DeleteVersion 删除版本记录；返回是否存在
func (s *Store) DeleteVersion(id int64) (bool, error) {
	res, err := s.db.Exec("DELETE FROM versions WHERE id = ?", id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
