package store

import (
	"testing"
	"time"
)

func loc() *time.Location { return time.Local }

func eq(t *testing.T, name string, got, want time.Time) {
	t.Helper()
	if !got.Equal(want) {
		t.Errorf("%s: got %s want %s", name, got.Format("2006-01-02 15:04:05"), want.Format("2006-01-02 15:04:05"))
	}
}

func TestQuotaWindowMonth(t *testing.T) {
	anchor := time.Date(2026, 1, 1, 0, 0, 0, 0, loc()) // 每月 1 号
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, loc())
	w, ok := CurrentQuotaWindow(anchor.Unix(), QuotaPeriodMonth, now)
	if !ok {
		t.Fatal("not ok")
	}
	eq(t, "start", time.Unix(w.Start, 0), time.Date(2026, 9, 1, 0, 0, 0, 0, loc()))
	eq(t, "end", time.Unix(w.End, 0), time.Date(2026, 10, 1, 0, 0, 0, 0, loc()))
}

// 1/31 锚点：2 月无 31 号 → 钳到 2/28，3 月回到 31 号
func TestQuotaWindowMonthClamp(t *testing.T) {
	anchor := time.Date(2026, 1, 31, 0, 0, 0, 0, loc())
	// 落在 [2/28, 3/31) 区间：用 3/1 测试
	w, _ := CurrentQuotaWindow(anchor.Unix(), QuotaPeriodMonth, time.Date(2026, 3, 1, 0, 0, 0, 0, loc()))
	eq(t, "start(2/28)", time.Unix(w.Start, 0), time.Date(2026, 2, 28, 0, 0, 0, 0, loc()))
	eq(t, "end(3/31)", time.Unix(w.End, 0), time.Date(2026, 3, 31, 0, 0, 0, 0, loc()))
}

func TestQuotaWindowQuarter(t *testing.T) {
	anchor := time.Date(2026, 1, 1, 0, 0, 0, 0, loc())
	w, _ := CurrentQuotaWindow(anchor.Unix(), QuotaPeriodQuarter, time.Date(2026, 8, 15, 0, 0, 0, 0, loc()))
	eq(t, "start", time.Unix(w.Start, 0), time.Date(2026, 7, 1, 0, 0, 0, 0, loc()))
	eq(t, "end", time.Unix(w.End, 0), time.Date(2026, 10, 1, 0, 0, 0, 0, loc()))
}

func TestQuotaWindowYearLeapClamp(t *testing.T) {
	anchor := time.Date(2024, 2, 29, 0, 0, 0, 0, loc()) // 闰日锚点
	w, _ := CurrentQuotaWindow(anchor.Unix(), QuotaPeriodYear, time.Date(2026, 6, 1, 0, 0, 0, 0, loc()))
	eq(t, "start", time.Unix(w.Start, 0), time.Date(2026, 2, 28, 0, 0, 0, 0, loc()))
	eq(t, "end", time.Unix(w.End, 0), time.Date(2027, 2, 28, 0, 0, 0, 0, loc()))
}

// now 早于锚点：首个窗口 [anchor, anchor+step)
func TestQuotaWindowBeforeAnchor(t *testing.T) {
	anchor := time.Date(2026, 6, 1, 0, 0, 0, 0, loc())
	w, _ := CurrentQuotaWindow(anchor.Unix(), QuotaPeriodMonth, time.Date(2026, 5, 20, 0, 0, 0, 0, loc()))
	eq(t, "start", time.Unix(w.Start, 0), anchor)
	eq(t, "end", time.Unix(w.End, 0), time.Date(2026, 7, 1, 0, 0, 0, 0, loc()))
}

func TestQuotaEnabledOnly(t *testing.T) {
	if QuotaEnabled(QuotaPeriodMonth, 0) {
		t.Error("limit 0 must disable")
	}
	if QuotaEnabled("", 100) {
		t.Error("empty period must disable")
	}
	if !QuotaEnabled(QuotaPeriodQuarter, 1) {
		t.Error("quarter+limit should enable")
	}
}
