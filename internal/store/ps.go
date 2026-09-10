package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PSStore 是 seeinps 本地存储（data/seeinps.db）
type PSStore struct {
	db *sql.DB
}

func NewPS(dbPath string) (*PSStore, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	s := &PSStore{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *PSStore) Close() error {
	return s.db.Close()
}

func (s *PSStore) migrate() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS local_users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			seeinpm_user TEXT NOT NULL DEFAULT '',
			auth_code TEXT NOT NULL DEFAULT '',
			registered INTEGER NOT NULL DEFAULT 0,
			must_change INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS local_proxies (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			proxy_id TEXT NOT NULL UNIQUE,
			type TEXT NOT NULL,
			local_addr TEXT NOT NULL DEFAULT '127.0.0.1',
			local_port INTEGER NOT NULL,
			public_port INTEGER NOT NULL DEFAULT 0,
			proxy_username TEXT NOT NULL DEFAULT '',
			proxy_password TEXT NOT NULL DEFAULT '',
			acl TEXT NOT NULL DEFAULT '',
			status INTEGER NOT NULL DEFAULT 1,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS local_audit_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT NOT NULL,
			action TEXT NOT NULL,
			target TEXT NOT NULL DEFAULT '',
			detail TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_ps_audit_created ON local_audit_logs(created_at DESC)`,
	}
	for _, q := range queries {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("exec migration: %w", err)
		}
	}
	// v2: 审计补传标记。synced=0 表示尚未成功同步到 seeinpm（含链路不在线时的累积），
	// 重连后由 auditSyncLoop 按 id 补传；PM 侧以 (source, ext_id) 唯一索引幂等去重。
	auditCols, err := s.tableColumns("local_audit_logs")
	if err != nil {
		return err
	}
	if !auditCols["synced"] {
		if _, err := s.db.Exec("ALTER TABLE local_audit_logs ADD COLUMN synced INTEGER NOT NULL DEFAULT 0"); err != nil {
			return fmt.Errorf("add column local_audit_logs.synced: %w", err)
		}
	}
	if _, err := s.db.Exec("CREATE INDEX IF NOT EXISTS idx_ps_audit_pending ON local_audit_logs(synced, id)"); err != nil {
		return fmt.Errorf("migrate local_audit_logs pending index: %w", err)
	}
	// 旧库补列（proxy_username/proxy_password/acl 为后期新增）
	cols, err := s.tableColumns("local_proxies")
	if err != nil {
		return err
	}
	adds := map[string]string{
		"proxy_username": "ALTER TABLE local_proxies ADD COLUMN proxy_username TEXT NOT NULL DEFAULT ''",
		"proxy_password": "ALTER TABLE local_proxies ADD COLUMN proxy_password TEXT NOT NULL DEFAULT ''",
		"acl":            "ALTER TABLE local_proxies ADD COLUMN acl TEXT NOT NULL DEFAULT ''",
	}
	for col, q := range adds {
		if _, ok := cols[col]; !ok {
			if _, err := s.db.Exec(q); err != nil {
				return fmt.Errorf("add column %s: %w", col, err)
			}
		}
	}
	return nil
}

func (s *PSStore) tableColumns(table string) (map[string]bool, error) {
	rows, err := s.db.Query("SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

// PSProxy 本地代理配置（proxy_id 即用户命名的代理名）
type PSProxy struct {
	ProxyID       string
	Type          string
	LocalAddr     string
	LocalPort     int
	PublicPort    int
	ProxyUsername string // 运维代理账号（仅 ops 类型）
	ProxyPassword string // 运维代理密码 bcrypt 哈希（仅 ops 类型）
	ACL           string // ACL JSON 数组（CIDR 列表，仅 ops 类型）
	Status        int
	CreatedAt     int64
	UpdatedAt     int64
}

func scanPSProxy(row interface{ Scan(...any) error }) (*PSProxy, error) {
	p := &PSProxy{}
	err := row.Scan(&p.ProxyID, &p.Type, &p.LocalAddr, &p.LocalPort, &p.PublicPort,
		&p.ProxyUsername, &p.ProxyPassword, &p.ACL, &p.Status, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return p, nil
}

const psProxyCols = "proxy_id, type, local_addr, local_port, public_port, proxy_username, proxy_password, acl, status, created_at, updated_at"

func (s *PSStore) ListProxies() ([]*PSProxy, error) {
	rows, err := s.db.Query("SELECT " + psProxyCols + " FROM local_proxies ORDER BY created_at ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*PSProxy
	for rows.Next() {
		p, err := scanPSProxy(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, p)
	}
	return list, rows.Err()
}

func (s *PSStore) GetProxy(proxyID string) (*PSProxy, error) {
	row := s.db.QueryRow("SELECT "+psProxyCols+" FROM local_proxies WHERE proxy_id = ?", proxyID)
	return scanPSProxy(row)
}

func (s *PSStore) CreateProxy(p *PSProxy) error {
	now := time.Now().Unix()
	_, err := s.db.Exec(
		"INSERT INTO local_proxies (proxy_id, type, local_addr, local_port, public_port, proxy_username, proxy_password, acl, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		p.ProxyID, p.Type, p.LocalAddr, p.LocalPort, p.PublicPort, p.ProxyUsername, p.ProxyPassword, p.ACL, p.Status, now, now)
	return err
}

func (s *PSStore) UpdateProxyMeta(p *PSProxy) error {
	_, err := s.db.Exec(
		"UPDATE local_proxies SET type = ?, local_addr = ?, local_port = ?, proxy_username = ?, proxy_password = ?, acl = ?, updated_at = ? WHERE proxy_id = ?",
		p.Type, p.LocalAddr, p.LocalPort, p.ProxyUsername, p.ProxyPassword, p.ACL, time.Now().Unix(), p.ProxyID)
	return err
}

func (s *PSStore) UpdateProxyPort(proxyID string, publicPort int) error {
	_, err := s.db.Exec("UPDATE local_proxies SET public_port = ?, updated_at = ? WHERE proxy_id = ?", publicPort, time.Now().Unix(), proxyID)
	return err
}

func (s *PSStore) DeleteProxy(proxyID string) error {
	_, err := s.db.Exec("DELETE FROM local_proxies WHERE proxy_id = ?", proxyID)
	return err
}

// PSLocalUser seeinps 本地管理用户（单行）
type PSLocalUser struct {
	Username     string
	PasswordHash string
	SeeinpmUser  string
	AuthCode     string
	Registered   bool
}

func (s *PSStore) GetLocalUser() (*PSLocalUser, error) {
	row := s.db.QueryRow("SELECT username, password_hash, seeinpm_user, auth_code, registered FROM local_users LIMIT 1")
	u := &PSLocalUser{}
	var registered int
	err := row.Scan(&u.Username, &u.PasswordHash, &u.SeeinpmUser, &u.AuthCode, &registered)
	if err != nil {
		return nil, err
	}
	u.Registered = registered == 1
	return u, nil
}

func (s *PSStore) UpsertLocalUser(u *PSLocalUser) error {
	now := time.Now().Unix()
	var id int64
	err := s.db.QueryRow("SELECT id FROM local_users LIMIT 1").Scan(&id)
	if err == sql.ErrNoRows {
		_, err = s.db.Exec(
			"INSERT INTO local_users (username, password_hash, seeinpm_user, auth_code, registered, must_change, created_at, updated_at) VALUES (?, ?, ?, ?, 0, 0, ?, ?)",
			u.Username, u.PasswordHash, u.SeeinpmUser, u.AuthCode, now, now)
		return err
	}
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		"UPDATE local_users SET username = ?, password_hash = ?, seeinpm_user = ?, auth_code = ?, registered = 0, updated_at = ? WHERE id = ?",
		u.Username, u.PasswordHash, u.SeeinpmUser, u.AuthCode, now, id)
	return err
}

func (s *PSStore) MarkLocalUserRegistered() error {
	_, err := s.db.Exec("UPDATE local_users SET registered = 1, updated_at = ? WHERE registered = 0", time.Now().Unix())
	return err
}

// UpdateLocalAuthCode 重新绑定 seeinpm 授权码（授权码被重置后，B端输入新码触发）
func (s *PSStore) UpdateLocalAuthCode(code string) error {
	res, err := s.db.Exec(
		"UPDATE local_users SET auth_code = ?, registered = 0, updated_at = ? WHERE id = (SELECT id FROM local_users LIMIT 1)",
		code, time.Now().Unix())
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ==================== 本地操作审计日志（local_audit_logs） ====================

// PSAuditLog seeinps B端操作审计记录；Detail 只存非敏感摘要
type PSAuditLog struct {
	ID        int64
	Username  string
	Action    string
	Target    string
	Detail    string
	CreatedAt int64
}

// InsertAuditLog 落库一条本地审计，返回自增 id（该 id 随 AUDIT_SYNC 上报，
// 供 seeinpm 端做幂等去重；补传重放同一条不会产生重复记录）。
func (s *PSStore) InsertAuditLog(username, action, target, detail string) (int64, error) {
	res, err := s.db.Exec("INSERT INTO local_audit_logs (username, action, target, detail, created_at, synced) VALUES (?, ?, ?, ?, ?, 0)",
		username, action, target, detail, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// PendingAuditLogs 取尚未同步到 seeinpm 的审计记录（按 id 升序，保证到达顺序稳定）。
func (s *PSStore) PendingAuditLogs(limit int) ([]*PSAuditLog, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.Query("SELECT id, username, action, target, COALESCE(detail,''), created_at FROM local_audit_logs WHERE synced = 0 ORDER BY id ASC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*PSAuditLog
	for rows.Next() {
		a := &PSAuditLog{}
		if err := rows.Scan(&a.ID, &a.Username, &a.Action, &a.Target, &a.Detail, &a.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, a)
	}
	return list, rows.Err()
}

// MarkAuditSynced 把指定 id 标记为已同步。
func (s *PSStore) MarkAuditSynced(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	ph := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		ph[i] = "?"
		args[i] = id
	}
	_, err := s.db.Exec("UPDATE local_audit_logs SET synced = 1 WHERE id IN ("+strings.Join(ph, ",")+")", args...)
	return err
}

// PSAuditFilter 本地审计日志查询条件
type PSAuditFilter struct {
	Username  string
	Action    string
	Keyword   string // 模糊匹配 username/action/target/detail
	StartTime int64  // Unix 秒，0=不限
	EndTime   int64  // Unix 秒，0=不限
	Page      int
	PageSize  int
}

func (s *PSStore) ListAuditLogs(f PSAuditFilter) ([]*PSAuditLog, int64, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize < 1 || f.PageSize > 200 {
		f.PageSize = 20
	}
	where, args := psAuditWhere(f)
	var total int64
	if err := s.db.QueryRow("SELECT COUNT(*) FROM local_audit_logs"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	query := "SELECT id, username, action, target, COALESCE(detail,''), created_at FROM local_audit_logs" + where + " ORDER BY id DESC LIMIT ? OFFSET ?"
	args = append(args, f.PageSize, (f.Page-1)*f.PageSize)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var list []*PSAuditLog
	for rows.Next() {
		a := &PSAuditLog{}
		if err := rows.Scan(&a.ID, &a.Username, &a.Action, &a.Target, &a.Detail, &a.CreatedAt); err != nil {
			return nil, 0, err
		}
		list = append(list, a)
	}
	return list, total, rows.Err()
}

// ExportAuditLogs 按筛选条件一次性取出最多 limit 条审计记录（时间倒序），专供 CSV 导出。
// 与 ListAuditLogs 分开：分页接口把 PageSize 上限压到 200，复用会导致导出被静默截断成 20 条。
func (s *PSStore) ExportAuditLogs(f PSAuditFilter, limit int) ([]*PSAuditLog, error) {
	if limit <= 0 || limit > 200000 {
		limit = 200000
	}
	where, args := psAuditWhere(f)
	query := "SELECT id, username, action, target, COALESCE(detail,''), created_at FROM local_audit_logs" + where + " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*PSAuditLog
	for rows.Next() {
		a := &PSAuditLog{}
		if err := rows.Scan(&a.ID, &a.Username, &a.Action, &a.Target, &a.Detail, &a.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, a)
	}
	return list, rows.Err()
}

// psAuditWhere 依据过滤条件拼装 WHERE 子句与参数（分页查询与导出共用）。
func psAuditWhere(f PSAuditFilter) (string, []interface{}) {
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
	if f.StartTime > 0 {
		where += " AND created_at >= ?"
		args = append(args, f.StartTime)
	}
	if f.EndTime > 0 {
		where += " AND created_at <= ?"
		args = append(args, f.EndTime)
	}
	return where, args
}

// CleanupAuditLogs 删除 cutoff 之前且**已成功同步**的审计记录。
//
// 只删已同步的行：未同步的行是 seeinpm 侧尚未持有的唯一副本，
// 删掉就等于永久丢失（正是此前"链路抖动 → 统一审计视图不完整"的成因之一）。
func (s *PSStore) CleanupAuditLogs(cutoff int64) (int64, error) {
	res, err := s.db.Exec("DELETE FROM local_audit_logs WHERE created_at < ? AND synced = 1", cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
