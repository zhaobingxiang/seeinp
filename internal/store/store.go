package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
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
	}
	for _, q := range queries {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("exec migration: %w", err)
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
	CreatedAt     int64
	UpdatedAt     int64
}

func (s *Store) CreateUser(username, authCodeHash, authCodeSalt string) error {
	now := time.Now().Unix()
	_, err := s.db.Exec("INSERT INTO users (username, auth_code, auth_code_salt, status, created_at, updated_at) VALUES (?, ?, ?, 1, ?, ?)", username, authCodeHash, authCodeSalt, now, now)
	return err
}

func (s *Store) GetUserByUsername(username string) (*User, error) {
	u := &User{}
	var onlineSession sql.NullString
	var onlineSince sql.NullInt64
	err := s.db.QueryRow("SELECT id, username, COALESCE(remark,''), auth_code, auth_code_salt, status, online_session, online_since, created_at, updated_at FROM users WHERE username = ?", username).Scan(&u.ID, &u.Username, &u.Remark, &u.AuthCodeHash, &u.AuthCodeSalt, &u.Status, &onlineSession, &onlineSince, &u.CreatedAt, &u.UpdatedAt)
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
	return u, nil
}

func (s *Store) ListUsers() ([]*User, error) {
	rows, err := s.db.Query("SELECT id, username, COALESCE(remark,''), auth_code, auth_code_salt, status, online_session, online_since, created_at, updated_at FROM users ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []*User
	for rows.Next() {
		u := &User{}
		var onlineSession sql.NullString
		var onlineSince sql.NullInt64
		if err := rows.Scan(&u.ID, &u.Username, &u.Remark, &u.AuthCodeHash, &u.AuthCodeSalt, &u.Status, &onlineSession, &onlineSince, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		if onlineSession.Valid {
			u.OnlineSession = &onlineSession.String
		}
		if onlineSince.Valid {
			v := onlineSince.Int64
			u.OnlineSince = &v
		}
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
