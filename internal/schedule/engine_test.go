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
		"24h":   24 * time.Hour,
		"168h":  7 * 24 * time.Hour,
		"1week": 7 * 24 * time.Hour,
		"":      24 * time.Hour, // 默认
		"bogus": 24 * time.Hour, // 兜底
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

// TestOncall_ResponseStructure 锁定 C7：oncall 响应结构为 {schedule_id, schedule_name, layers[]}，
// 每层含 name/priority/users[]，user 含 override 标志。
func TestOncall_ResponseStructure(t *testing.T) {
	c := newTestClient(t)
	sched, _ := seedSchedule(t, c)
	eng := NewEngine(c, nil)

	at := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	res, err := eng.Oncall(context.Background(), sched.ID, at)
	if err != nil {
		t.Fatalf("Oncall: %v", err)
	}
	if res.ScheduleID != sched.ID {
		t.Errorf("ScheduleID: got %d, want %d", res.ScheduleID, sched.ID)
	}
	if res.ScheduleName != "支付值班" {
		t.Errorf("ScheduleName: got %q, want 支付值班", res.ScheduleName)
	}
	if len(res.Layers) == 0 {
		t.Fatal("expected at least one layer")
	}
	l := res.Layers[0]
	if len(l.Users) == 0 {
		t.Fatal("expected users in layer")
	}
	if l.Users[0].Override {
		t.Error("rotation user should have override=false")
	}
}

// TestOncall_Override 验证 C5/M8：换班时段内顶替人覆盖 Rotation 结果，override=true 且最高优先级。
func TestOncall_Override(t *testing.T) {
	c := newTestClient(t)
	sched, _ := seedSchedule(t, c)
	eng := NewEngine(c, nil)
	ctx := context.Background()

	// 顶替人 u4（非轮换参与者）。
	u4, _ := c.User.Create().SetUsername("u4").SetEmail("u4@x.com").SetName("替班王").Save(ctx)

	// 6/1 全天换班给 u4（原本 6/1 应是 u1）。
	winStart := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	winEnd := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)
	_, err := c.Override.Create().
		SetScheduleID(sched.ID).
		SetUserID(u4.ID).
		SetStartTime(winStart).
		SetEndTime(winEnd).
		SetReason("u1 请假").
		Save(ctx)
	if err != nil {
		t.Fatalf("create override: %v", err)
	}

	// 时段内（6/1 12:00）：应是 u4 顶替，override=true，且为最高优先级层。
	at := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	res, err := eng.Oncall(ctx, sched.ID, at)
	if err != nil {
		t.Fatalf("Oncall: %v", err)
	}
	if len(res.Layers) == 0 || len(res.Layers[0].Users) == 0 {
		t.Fatal("expected override layer")
	}
	top := res.Layers[0].Users[0]
	if top.Username != "u4" || !top.Override {
		t.Errorf("override: got user=%q override=%v, want u4 override=true", top.Username, top.Override)
	}

	// 时段外（6/2 12:00）：override 不再命中，回到 Rotation（u2）。
	after := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	res2, err := eng.Oncall(ctx, sched.ID, after)
	if err != nil {
		t.Fatalf("Oncall after: %v", err)
	}
	if res2.Layers[0].Users[0].Username != "u2" || res2.Layers[0].Users[0].Override {
		t.Errorf("after override window: got %q override=%v, want u2 override=false",
			res2.Layers[0].Users[0].Username, res2.Layers[0].Users[0].Override)
	}
}

// TestOncall_DisabledUserNotResolved 验证 B21：禁用参与者不进在班计算。
func TestOncall_DisabledUserNotResolved(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	// 单人轮换，该人禁用 → 空班。
	u, _ := c.User.Create().SetUsername("lone").SetEmail("lone@x.com").
		SetStatus("disabled").Save(ctx)
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	rot, _ := c.Rotation.Create().SetName("一线").SetShiftLength("24h").
		SetHandoffTime("00:00").SetRotationType("daily").SetStartDate(start).
		AddParticipantIDs(u.ID).Save(ctx)
	sched, _ := c.Schedule.Create().SetName("单人值班").SetType("rotation").
		SetTimezone("UTC").AddRotationIDs(rot.ID).Save(ctx)

	eng := NewEngine(c, nil)
	at := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	res, err := eng.Oncall(ctx, sched.ID, at)
	if err != nil {
		t.Fatalf("Oncall: %v", err)
	}
	// 禁用者被过滤 → 无在班人（空班）。
	for _, l := range res.Layers {
		if len(l.Users) > 0 {
			t.Errorf("disabled user should not be resolved, got %+v", l.Users)
		}
	}
}

// TestOncall_EmptyShiftAlerts 验证 C4：空班触发 EmptyShiftAlerter。
func TestOncall_EmptyShiftAlerts(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	// 无参与者的 Rotation → 空班。
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	rot, _ := c.Rotation.Create().SetName("空层").SetShiftLength("24h").
		SetHandoffTime("00:00").SetRotationType("daily").SetStartDate(start).Save(ctx)
	sched, _ := c.Schedule.Create().SetName("空排班").SetType("rotation").
		SetTimezone("UTC").AddRotationIDs(rot.ID).Save(ctx)

	eng := NewEngine(c, nil)
	spy := &spyAlerter{}
	eng.SetEmptyShiftAlerter(spy)

	at := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	if _, err := eng.Oncall(ctx, sched.ID, at); err != nil {
		t.Fatalf("Oncall: %v", err)
	}
	if spy.calls != 1 {
		t.Errorf("expected empty-shift alerter called once, got %d", spy.calls)
	}
	if spy.lastSchedID != sched.ID {
		t.Errorf("alerter schedule id: got %d, want %d", spy.lastSchedID, sched.ID)
	}
}

// TestOncall_NoEmptyShiftAlertWhenStaffed 验证有在班人时不触发空班告警。
func TestOncall_NoEmptyShiftAlertWhenStaffed(t *testing.T) {
	c := newTestClient(t)
	sched, _ := seedSchedule(t, c)
	eng := NewEngine(c, nil)
	spy := &spyAlerter{}
	eng.SetEmptyShiftAlerter(spy)

	at := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	if _, err := eng.Oncall(context.Background(), sched.ID, at); err != nil {
		t.Fatalf("Oncall: %v", err)
	}
	if spy.calls != 0 {
		t.Errorf("staffed schedule should not trigger empty-shift alert, got %d calls", spy.calls)
	}
}

// TestOncall_CalendarTypeAllStaff 验证 B21：calendar 型取全体在职参与者（无轮换）。
func TestOncall_CalendarTypeAllStaff(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	u1, _ := c.User.Create().SetUsername("c1").SetEmail("c1@x.com").Save(ctx)
	u2, _ := c.User.Create().SetUsername("c2").SetEmail("c2@x.com").Save(ctx)
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	rot, _ := c.Rotation.Create().SetName("日历层").SetShiftLength("24h").
		SetHandoffTime("00:00").SetRotationType("daily").SetStartDate(start).
		AddParticipantIDs(u1.ID, u2.ID).Save(ctx)
	sched, _ := c.Schedule.Create().SetName("日历值班").SetType("calendar").
		SetTimezone("UTC").AddRotationIDs(rot.ID).Save(ctx)

	eng := NewEngine(c, nil)
	at := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	res, err := eng.Oncall(ctx, sched.ID, at)
	if err != nil {
		t.Fatalf("Oncall: %v", err)
	}
	if len(res.Layers) == 0 || len(res.Layers[0].Users) != 2 {
		t.Fatalf("calendar type should return all %d participants, got %+v", 2, res.Layers)
	}
}

// spyAlerter 记录 EmptyShiftAlerter 调用（测试桩）。
type spyAlerter struct {
	calls       int
	lastSchedID int
}

func (s *spyAlerter) AlertEmptyShift(_ context.Context, sched *ent.Schedule, _ time.Time) {
	s.calls++
	s.lastSchedID = sched.ID
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
