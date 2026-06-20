package schedule

import (
	"context"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"

	_ "github.com/mattn/go-sqlite3"
)

func newTestClient(t *testing.T) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:sched_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// seedSchedule 建一个 Schedule + 一个 Rotation（含 3 个参与者，每日轮换）。
func seedSchedule(t *testing.T, c *ent.Client) (*ent.Schedule, *ent.Rotation) {
	t.Helper()
	ctx := context.Background()

	// 3 个值班人
	u1, _ := c.User.Create().SetUsername("u1").SetEmail("u1@x.com").Save(ctx)
	u2, _ := c.User.Create().SetUsername("u2").SetEmail("u2@x.com").Save(ctx)
	u3, _ := c.User.Create().SetUsername("u3").SetEmail("u3@x.com").Save(ctx)

	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC) // 6/1 起
	rot, err := c.Rotation.Create().
		SetName("一线").
		SetShiftLength("24h").
		SetHandoffTime("00:00").
		SetRotationType("daily").
		SetStartDate(start).
		AddParticipantIDs(u1.ID, u2.ID, u3.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create rotation: %v", err)
	}

	sched, err := c.Schedule.Create().
		SetName("支付值班").
		SetType("rotation").
		SetTimezone("UTC").
		AddRotationIDs(rot.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	return sched, rot
}

// TestOncall_BasicRotation 验证基本轮班：6/1=u1, 6/2=u2, 6/3=u3, 6/4=u1（循环）。
func TestOncall_BasicRotation(t *testing.T) {
	c := newTestClient(t)
	sched, _ := seedSchedule(t, c)
	eng := NewEngine(c, nil)
	ctx := context.Background()

	cases := []struct {
		day    int    // 6 月第几天
		wantUN string // 期望在班 username
	}{
		{1, "u1"}, {2, "u2"}, {3, "u3"}, {4, "u1"}, {5, "u2"},
	}
	for _, tc := range cases {
		at := time.Date(2026, 6, tc.day, 12, 0, 0, 0, time.UTC) // 当天中午
		res, err := eng.Oncall(ctx, sched.ID, at)
		if err != nil {
			t.Fatalf("Oncall day %d: %v", tc.day, err)
		}
		if len(res.Layers) == 0 || len(res.Layers[0].Users) == 0 {
			t.Fatalf("day %d: no oncall users", tc.day)
		}
		got := res.Layers[0].Users[0].Username
		if got != tc.wantUN {
			t.Errorf("day %d: got %q, want %q", tc.day, got, tc.wantUN)
		}
	}
}

// TestOncall_BeforeStartDate 验证 at 早于轮班开始时取首人。
func TestOncall_BeforeStartDate(t *testing.T) {
	c := newTestClient(t)
	sched, _ := seedSchedule(t, c)
	eng := NewEngine(c, nil)

	// 5/30 早于 6/1 开始
	at := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	res, err := eng.Oncall(context.Background(), sched.ID, at)
	if err != nil {
		t.Fatalf("Oncall: %v", err)
	}
	if len(res.Layers) == 0 || res.Layers[0].Users[0].Username != "u1" {
		t.Errorf("before start: expect u1, got %+v", res.Layers)
	}
}

// TestParseShiftLength 验证班次时长解析。
func TestParseShiftLength(t *testing.T) {
	cases := map[string]time.Duration{
		"24h":    24 * time.Hour,
		"168h":   7 * 24 * time.Hour,
		"1week":  7 * 24 * time.Hour,
		"":       24 * time.Hour, // 默认
		"bogus":  24 * time.Hour, // 兜底
	}
	for in, want := range cases {
		if got := parseShiftLength(in); got != want {
			t.Errorf("parseShiftLength(%q): got %v, want %v", in, got, want)
		}
	}
}

// TestParseHandoff 验证交接时刻解析。
func TestParseHandoff(t *testing.T) {
	at := time.Date(2026, 6, 20, 14, 30, 0, 0, time.UTC)
	got := parseHandoff("09:00", at)
	want := time.Date(2026, 6, 20, 9, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("parseHandoff: got %v, want %v", got, want)
	}
}

// TestPreview 验证预览生成多天数据。
func TestPreview(t *testing.T) {
	c := newTestClient(t)
	sched, _ := seedSchedule(t, c)
	eng := NewEngine(c, nil)

	days, err := eng.Preview(context.Background(), sched.ID, 7)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if len(days) != 7 {
		t.Fatalf("Preview: got %d days, want 7", len(days))
	}
	for i, d := range days {
		if len(d.Layers) == 0 {
			t.Errorf("day %d: no layers", i)
		}
	}
}
