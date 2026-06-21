package notification

import (
	"testing"
	"time"
)

// TestQuietHours_Disabled 未启用时恒不静默。
func TestQuietHours_Disabled(t *testing.T) {
	qh := &QuietHours{Enabled: false, Start: "22:00", End: "07:00", Timezone: "Asia/Shanghai"}
	now := nightTime(t, "2026-06-20T23:00:00+08:00")
	if qh.ShouldSuppress("warning", false, &now) {
		t.Error("未启用静默不应抑制")
	}
}

// TestQuietHours_CriticalBypass critical 默认穿透静默。
func TestQuietHours_CriticalBypass(t *testing.T) {
	qh := &QuietHours{Enabled: true, Start: "22:00", End: "07:00", Timezone: "Asia/Shanghai"}
	now := nightTime(t, "2026-06-20T23:00:00+08:00")
	if qh.ShouldSuppress("critical", false, &now) {
		t.Error("critical 应穿透静默")
	}
}

// TestQuietHours_OncallBypass 值班人始终通知（即使 warning 在静默窗内）。
func TestQuietHours_OncallBypass(t *testing.T) {
	qh := &QuietHours{Enabled: true, Start: "22:00", End: "07:00", Timezone: "Asia/Shanghai"}
	now := nightTime(t, "2026-06-20T23:00:00+08:00")
	if qh.ShouldSuppress("warning", true, &now) {
		t.Error("值班人（isOncall=true）应始终通知")
	}
}

// TestQuietHours_InWindowSameDay 非值班 warning 在静默窗内 → 抑制。
func TestQuietHours_InWindowSameDay(t *testing.T) {
	qh := &QuietHours{Enabled: true, Start: "22:00", End: "23:30", Timezone: "Asia/Shanghai"}
	now := nightTime(t, "2026-06-20T23:00:00+08:00")
	if !qh.ShouldSuppress("warning", false, &now) {
		t.Error("warning 在静默窗内应抑制")
	}
}

// TestQuietHours_OutsideWindow 非值班 warning 在静默窗外 → 不抑制。
func TestQuietHours_OutsideWindow(t *testing.T) {
	qh := &QuietHours{Enabled: true, Start: "22:00", End: "07:00", Timezone: "Asia/Shanghai"}
	now := nightTime(t, "2026-06-20T15:00:00+08:00") // 下午 3 点
	if qh.ShouldSuppress("warning", false, &now) {
		t.Error("静默窗外不应抑制")
	}
}

// TestQuietHours_CrossMidnight 跨午夜窗口（22:00-07:00）：凌晨 2 点命中。
func TestQuietHours_CrossMidnight(t *testing.T) {
	qh := &QuietHours{Enabled: true, Start: "22:00", End: "07:00", Timezone: "Asia/Shanghai"}
	now := nightTime(t, "2026-06-21T02:00:00+08:00")
	if !qh.ShouldSuppress("info", false, &now) {
		t.Error("跨午夜窗口凌晨 2 点应抑制")
	}
}

// TestQuietHours_InvalidTimezone 时区非法按 UTC 保守判断（不应误伤）。
func TestQuietHours_InvalidTimezone(t *testing.T) {
	qh := &QuietHours{Enabled: true, Start: "22:00", End: "07:00", Timezone: "Bogus/Zone"}
	now := time.Now()
	// 不 panic 即可（具体抑制与否取决于 UTC 是否落在窗内，这里仅验证不报错）
	_ = qh.ShouldSuppress("warning", false, &now)
}

// TestInTimeWindow_SameDay 同日窗边界。
func TestInTimeWindow_SameDay(t *testing.T) {
	at := func(s string) time.Time { return nightTime(t, s) }
	cases := []struct {
		t            time.Time
		start, end   string
		want         bool
	}{
		{at("2026-06-20T10:00:00+08:00"), "09:00", "17:00", true},  // 窗内
		{at("2026-06-20T09:00:00+08:00"), "09:00", "17:00", true},  // 起点闭区间
		{at("2026-06-20T17:00:00+08:00"), "09:00", "17:00", false}, // 终点开区间
		{at("2026-06-20T08:59:00+08:00"), "09:00", "17:00", false}, // 窗外
	}
	for _, c := range cases {
		if got := inTimeWindow(c.t, c.start, c.end); got != c.want {
			t.Errorf("inTimeWindow(%s,%s,%s): got %v, want %v", c.t.Format("15:04"), c.start, c.end, got, c.want)
		}
	}
}

// TestInTimeWindow_CrossMidnight 跨午夜窗。
func TestInTimeWindow_CrossMidnight(t *testing.T) {
	at := func(s string) time.Time { return nightTime(t, s) }
	if !inTimeWindow(at("2026-06-20T23:30:00+08:00"), "22:00", "07:00") {
		t.Error("23:30 应在 22:00-07:00 窗内")
	}
	if !inTimeWindow(at("2026-06-21T03:00:00+08:00"), "22:00", "07:00") {
		t.Error("03:00 应在 22:00-07:00 窗内")
	}
	if inTimeWindow(at("2026-06-21T08:00:00+08:00"), "22:00", "07:00") {
		t.Error("08:00 不应在 22:00-07:00 窗内")
	}
}

// TestParseClock 时钟解析。
func TestParseClock(t *testing.T) {
	cases := []struct {
		in        string
		h, m      int
		ok        bool
	}{
		{"22:00", 22, 0, true},
		{"07:30", 7, 30, true},
		{"25:00", 0, 0, false},  // 小时越界
		{"10:60", 0, 0, false},  // 分越界
		{"abc", 0, 0, false},
		{"", 0, 0, false},
	}
	for _, c := range cases {
		h, m, ok := parseClock(c.in)
		if ok != c.ok || h != c.h || m != c.m {
			t.Errorf("parseClock(%q): got (%d,%d,%v), want (%d,%d,%v)", c.in, h, m, ok, c.h, c.m, c.ok)
		}
	}
}

// nightTime 解析 RFC3339 时间为 time.Time（测试辅助）。
func nightTime(t *testing.T, s string) time.Time {
	t.Helper()
	tt, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return tt
}
