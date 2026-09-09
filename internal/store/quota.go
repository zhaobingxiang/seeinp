package store

import (
	"database/sql"
	"errors"
	"time"
)

// 用户流量配额周期：按月/季/年，锚点为 quota_start（本地 0 点，默认 1 号），
// 每个周期的重置点为“下一个同日期 0 点”（月=次月同日、季=+3月同日、年=次年同日），
// 目标月无该日时钳到当月最后一天（如 1/31 锚点 → 2/28）。
// 周期类型常量存于 users.quota_period 列。
const (
	QuotaPeriodNone    = ""
	QuotaPeriodMonth   = "month"
	QuotaPeriodQuarter = "quarter"
	QuotaPeriodYear    = "year"
)

// monthAddClamped 在本地时区上把 t 推进 n 个月，保持“日”语义并对月末溢出钳位，归零到 0 点。
func monthAddClamped(t time.Time, n int) time.Time {
	y, mo, d := t.Date()
	total := int(mo) + n
	ny := y + (total-1)/12
	nm := time.Month(((total-1)%12+12)%12 + 1)
	lastDay := time.Date(ny, nm+1, 0, 0, 0, 0, 0, t.Location()).Day()
	if d > lastDay {
		d = lastDay
	}
	return time.Date(ny, nm, d, 0, 0, 0, 0, t.Location())
}

// yearAddClamped 推进 n 年，闰日（2/29）在非闰年钳到 2/28。
func yearAddClamped(t time.Time, n int) time.Time {
	y, mo, d := t.Date()
	ny := y + n
	lastDay := time.Date(ny, mo+1, 0, 0, 0, 0, 0, t.Location()).Day()
	if d > lastDay {
		d = lastDay
	}
	return time.Date(ny, mo, d, 0, 0, 0, 0, t.Location())
}

// quotaStepMonths 返回该周期类型一次推进的月数（年用 12）。
func quotaStepMonths(period string) (int, bool) {
	switch period {
	case QuotaPeriodMonth:
		return 1, true
	case QuotaPeriodQuarter:
		return 3, true
	case QuotaPeriodYear:
		return 12, true
	}
	return 0, false
}

// QuotaWindow 当前周期区间（unix 秒，左闭右开）。
type QuotaWindow struct {
	Start int64
	End   int64
}

// QuotaEnabled 是否启用了周期流量配额（周期类型合法且上限>0）。
func QuotaEnabled(period string, quotaBytes int64) bool {
	_, ok := quotaStepMonths(period)
	return ok && quotaBytes > 0
}

// CurrentQuotaWindow 计算 now 所属周期窗口。anchorUnix 为首次周期起点（本地 0 点）；
// 若 now 早于 anchor，则首个窗口为 [anchor, anchor+step)。period 非法时返回 ok=false。
// 关键：每一起点都必须由锚点直接推算（advance(k)），不能在上一次钳位结果上迭代，
// 否则 1/31 这类锚点经 2 月钳位后会永久漂移成 28 号。
func CurrentQuotaWindow(anchorUnix int64, period string, now time.Time) (QuotaWindow, bool) {
	step, ok := quotaStepMonths(period)
	if !ok {
		return QuotaWindow{}, false
	}
	anchor := time.Unix(anchorUnix, 0).In(time.Local)
	anchor = time.Date(anchor.Year(), anchor.Month(), anchor.Day(), 0, 0, 0, 0, time.Local)
	advance := func(k int) time.Time {
		if period == QuotaPeriodYear {
			return yearAddClamped(anchor, k)
		}
		return monthAddClamped(anchor, k*step)
	}
	k := 0
	for k < 100000 && !advance(k+1).After(now) {
		k++
	}
	return QuotaWindow{Start: advance(k).Unix(), End: advance(k + 1).Unix()}, true
}

// QuotaStatus 用户周期流量配额状态快照。
type QuotaStatus struct {
	Enabled     bool
	Period      string
	Used        int64
	Limit       int64
	PeriodStart int64
	PeriodEnd   int64
	Exceeded    bool
}

// getQuotaUser 读取用户配额相关字段
func (s *Store) getQuotaUser(username string) (period string, quotaBytes, quotaStart int64, err error) {
	err = s.db.QueryRow("SELECT COALESCE(quota_period,''), COALESCE(quota_bytes,0), COALESCE(quota_start,0) FROM users WHERE username = ?", username).Scan(&period, &quotaBytes, &quotaStart)
	return
}

// GetQuotaStatus 返回用户当前周期配额状态；若周期已滚动则先清零 usage 行。
// 未启用配额时返回 Enabled=false。
func (s *Store) GetQuotaStatus(username string, now time.Time) (QuotaStatus, error) {
	period, quotaBytes, quotaStart, err := s.getQuotaUser(username)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return QuotaStatus{Enabled: false}, nil
		}
		return QuotaStatus{}, err
	}
	if !QuotaEnabled(period, quotaBytes) {
		return QuotaStatus{Enabled: false, Period: period}, nil
	}
	w, ok := CurrentQuotaWindow(quotaStart, period, now)
	if !ok {
		return QuotaStatus{Enabled: false, Period: period}, nil
	}
	// 滚动：若已存行的 period_start 与当前窗口不符，则重置为新窗口、清零
	var storedStart, used int64
	rowErr := s.db.QueryRow("SELECT period_start, bytes_used FROM user_quota_usage WHERE username = ?", username).Scan(&storedStart, &used)
	if rowErr == nil && storedStart != w.Start {
		used = 0
		s.db.Exec("UPDATE user_quota_usage SET period_start=?, period_end=?, bytes_used=0 WHERE username=?", w.Start, w.End, username)
	} else if rowErr == sql.ErrNoRows {
		s.db.Exec("INSERT INTO user_quota_usage (username, period_start, period_end, bytes_used) VALUES (?, ?, ?, 0)", username, w.Start, w.End)
	}
	return QuotaStatus{Enabled: true, Period: period, Used: used, Limit: quotaBytes, PeriodStart: w.Start, PeriodEnd: w.End, Exceeded: used >= quotaBytes}, nil
}

// AddQuotaUsage 向用户当前周期累加字节（in+out）。周期滚动后清零再累加。
func (s *Store) AddQuotaUsage(username string, delta int64, now time.Time) error {
	if delta <= 0 {
		return nil
	}
	period, quotaBytes, quotaStart, err := s.getQuotaUser(username)
	if err != nil || !QuotaEnabled(period, quotaBytes) {
		return err // 未启用配额则不记账（err 可能为 sql.ErrNoRows，视为无用户，忽略）
	}
	w, ok := CurrentQuotaWindow(quotaStart, period, now)
	if !ok {
		return nil
	}
	var storedStart int64
	err = s.db.QueryRow("SELECT period_start FROM user_quota_usage WHERE username = ?", username).Scan(&storedStart)
	if err == sql.ErrNoRows {
		_, err = s.db.Exec("INSERT INTO user_quota_usage (username, period_start, period_end, bytes_used) VALUES (?, ?, ?, ?)", username, w.Start, w.End, delta)
		return err
	}
	if err != nil {
		return err
	}
	if storedStart != w.Start {
		_, err = s.db.Exec("UPDATE user_quota_usage SET period_start=?, period_end=?, bytes_used=? WHERE username=?", w.Start, w.End, delta, username)
		return err
	}
	_, err = s.db.Exec("UPDATE user_quota_usage SET bytes_used = bytes_used + ?, period_end = ? WHERE username = ?", delta, w.End, username)
	return err
}

// ResetQuotaUsage 清空用户当前周期用量（修改配额周期类型/起始日后重新统计）
func (s *Store) ResetQuotaUsage(username string) error {
	_, err := s.db.Exec("DELETE FROM user_quota_usage WHERE username = ?", username)
	return err
}

// QuotaExceeded 判定用户当前是否已超额（无配额返回 false）。供 ALLOC 准入与转发路径快速判断。
func (s *Store) QuotaExceeded(username string, now time.Time) bool {
	st, err := s.GetQuotaStatus(username, now)
	if err != nil {
		return false
	}
	return st.Enabled && st.Exceeded
}
