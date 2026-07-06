package schedule

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/schema"

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

// ===== follow_the_sun 跨时区接力（P3.2）=====

// seedFollowTheSun 建一个 follow_the_sun Schedule：3 个时区区域 layer（亚太/欧洲/美洲），
// 各配本地时区 + 09:00~17:00 工作时段，每层单人（便于断言接力）。
// 返回 schedule 及三层的值班人（apac/emea/amer）。
func seedFollowTheSun(t *testing.T, c *ent.Client) (*ent.Schedule, *ent.User, *ent.User, *ent.User) {
	t.Helper()
	ctx := context.Background()

	apac, _ := c.User.Create().SetUsername("apac").SetEmail("apac@x.com").SetName("亚太值班").Save(ctx)
	emea, _ := c.User.Create().SetUsername("emea").SetEmail("emea@x.com").SetName("欧洲值班").Save(ctx)
	amer, _ := c.User.Create().SetUsername("amer").SetEmail("amer@x.com").SetName("美洲值班").Save(ctx)

	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	mkRot := func(name string, uid int) *ent.Rotation {
		r, err := c.Rotation.Create().SetName(name).SetShiftLength("24h").
			SetHandoffTime("00:00").SetRotationType("daily").SetStartDate(start).
			AddParticipantIDs(uid).Save(ctx)
		if err != nil {
			t.Fatalf("create rotation %s: %v", name, err)
		}
		return r
	}
	rApac := mkRot("亚太", apac.ID)
	rEmea := mkRot("欧洲", emea.ID)
	rAmer := mkRot("美洲", amer.ID)

	layers := []schema.ScheduleLayer{
		{ID: itoa(rApac.ID), Name: "亚太", Priority: 1, RotationID: itoa(rApac.ID),
			Timezone: "Asia/Shanghai", WorkStart: "09:00", WorkEnd: "17:00"},
		{ID: itoa(rEmea.ID), Name: "欧洲", Priority: 2, RotationID: itoa(rEmea.ID),
			Timezone: "Europe/London", WorkStart: "09:00", WorkEnd: "17:00"},
		{ID: itoa(rAmer.ID), Name: "美洲", Priority: 3, RotationID: itoa(rAmer.ID),
			Timezone: "America/New_York", WorkStart: "09:00", WorkEnd: "17:00"},
	}
	sched, err := c.Schedule.Create().SetName("日不落值班").SetType("follow_the_sun").
		SetTimezone("UTC").
		AddRotationIDs(rApac.ID, rEmea.ID, rAmer.ID).
		SetLayers(layers).Save(ctx)
	if err != nil {
		t.Fatalf("create fts schedule: %v", err)
	}
	return sched, apac, emea, amer
}

func itoa(i int) string { return fmt.Sprintf("%d", i) }

// TestFollowTheSun_Relay 验证跨时区接力：不同 UTC 时刻解出不同时区区域值班人。
// Asia/Shanghai=UTC+8，Europe/London=UTC+1(夏令时)，America/New_York=UTC-4(夏令时)。
// 6 月为北半球夏令时。各区 09:00~17:00 本地工作时段。
func TestFollowTheSun_Relay(t *testing.T) {
	c := newTestClient(t)
	sched, _, _, _ := seedFollowTheSun(t, c)
	eng := NewEngine(c, nil)
	ctx := context.Background()

	cases := []struct {
		name   string
		utcH   int    // UTC 小时
		wantUN string // 期望值班 username
	}{
		// 亚太 09:00~17:00 本地 = UTC 01:00~09:00。UTC 03:00 → 亚太上午。
		{"apac morning", 3, "apac"},
		// 欧洲 09:00~17:00 本地(夏令时 UTC+1) = UTC 08:00~16:00。UTC 12:00 → 欧洲下午。
		{"emea afternoon", 12, "emea"},
		// 美洲 09:00~17:00 本地(夏令时 UTC-4) = UTC 13:00~21:00。UTC 19:00 → 美洲下午。
		{"amer afternoon", 19, "amer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			at := time.Date(2026, 6, 15, tc.utcH, 0, 0, 0, time.UTC)
			res, err := eng.Oncall(ctx, sched.ID, at)
			if err != nil {
				t.Fatalf("Oncall: %v", err)
			}
			if len(res.Layers) == 0 || len(res.Layers[0].Users) == 0 {
				t.Fatalf("utc %dh: no oncall, got %+v", tc.utcH, res.Layers)
			}
			// 命中层可能不止一个（接力重叠），但期望的人须在结果中且为工作时段命中。
			found := false
			for _, l := range res.Layers {
				for _, u := range l.Users {
					if u.Username == tc.wantUN {
						found = true
					}
				}
			}
			if !found {
				t.Errorf("utc %dh: want %q oncall, got layers %+v", tc.utcH, tc.wantUN, res.Layers)
			}
		})
	}
}

// TestFollowTheSun_RelayOverlap 验证接力交接重叠段：两区工作时段重叠时两层同时在班。
// 欧洲本地工作止 = UTC 16:00；美洲本地工作起 = UTC 13:00。UTC 13:00~16:00 两区重叠。
func TestFollowTheSun_RelayOverlap(t *testing.T) {
	c := newTestClient(t)
	sched, _, _, _ := seedFollowTheSun(t, c)
	eng := NewEngine(c, nil)

	at := time.Date(2026, 6, 15, 14, 0, 0, 0, time.UTC) // UTC 14:00 落在欧洲(08-16)且美洲(13-21)
	res, err := eng.Oncall(context.Background(), sched.ID, at)
	if err != nil {
		t.Fatalf("Oncall: %v", err)
	}
	names := map[string]bool{}
	for _, l := range res.Layers {
		for _, u := range l.Users {
			names[u.Username] = true
		}
	}
	if !names["emea"] || !names["amer"] {
		t.Errorf("overlap window should have both emea & amer, got %+v", names)
	}
	if names["apac"] {
		t.Errorf("apac should be off-shift at UTC 14:00, got %+v", names)
	}
}

// TestFollowTheSun_Fallback 验证接力空档兜底：无区在工作时段时取"最快上班"的层。
// 各区仅 09:00~17:00 工作，UTC 各时刻会否全空档取决于时区。构造全空档时刻：
// 令三区工作时段合并后仍留空档。用窄工作时段（仅 1h）制造空档。
func TestFollowTheSun_Fallback(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	// 两区，各仅 1h 工作时段，制造大片空档。
	u1, _ := c.User.Create().SetUsername("z1").SetEmail("z1@x.com").Save(ctx)
	u2, _ := c.User.Create().SetUsername("z2").SetEmail("z2@x.com").Save(ctx)
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	r1, _ := c.Rotation.Create().SetName("z1").SetShiftLength("24h").SetHandoffTime("00:00").
		SetRotationType("daily").SetStartDate(start).AddParticipantIDs(u1.ID).Save(ctx)
	r2, _ := c.Rotation.Create().SetName("z2").SetShiftLength("24h").SetHandoffTime("00:00").
		SetRotationType("daily").SetStartDate(start).AddParticipantIDs(u2.ID).Save(ctx)
	layers := []schema.ScheduleLayer{
		// UTC 时区，z1 工作 10:00~11:00，z2 工作 20:00~21:00。UTC 15:00 全空档。
		{ID: itoa(r1.ID), Name: "z1", Priority: 1, RotationID: itoa(r1.ID),
			Timezone: "UTC", WorkStart: "10:00", WorkEnd: "11:00"},
		{ID: itoa(r2.ID), Name: "z2", Priority: 2, RotationID: itoa(r2.ID),
			Timezone: "UTC", WorkStart: "20:00", WorkEnd: "21:00"},
	}
	sched, _ := c.Schedule.Create().SetName("空档值班").SetType("follow_the_sun").
		SetTimezone("UTC").AddRotationIDs(r1.ID, r2.ID).SetLayers(layers).Save(ctx)

	eng := NewEngine(c, nil)
	// UTC 15:00：z1 已下班(11:00)，z2 未上班(20:00)。距 z2 上班 5h，距 z1 明天上班 19h。
	// 兜底取最快上班者 = z2。
	at := time.Date(2026, 6, 15, 15, 0, 0, 0, time.UTC)
	res, err := eng.Oncall(ctx, sched.ID, at)
	if err != nil {
		t.Fatalf("Oncall: %v", err)
	}
	if len(res.Layers) != 1 || len(res.Layers[0].Users) == 0 {
		t.Fatalf("fallback: expected single fallback layer, got %+v", res.Layers)
	}
	if res.Layers[0].Users[0].Username != "z2" {
		t.Errorf("fallback should pick soonest-to-start z2, got %q", res.Layers[0].Users[0].Username)
	}
}

// TestFollowTheSun_CrossMidnight 验证跨午夜工作时段（WorkStart>WorkEnd，如夜班 22:00~06:00）。
func TestFollowTheSun_CrossMidnight(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	night, _ := c.User.Create().SetUsername("night").SetEmail("night@x.com").Save(ctx)
	day, _ := c.User.Create().SetUsername("day").SetEmail("day@x.com").Save(ctx)
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	rN, _ := c.Rotation.Create().SetName("夜").SetShiftLength("24h").SetHandoffTime("00:00").
		SetRotationType("daily").SetStartDate(start).AddParticipantIDs(night.ID).Save(ctx)
	rD, _ := c.Rotation.Create().SetName("日").SetShiftLength("24h").SetHandoffTime("00:00").
		SetRotationType("daily").SetStartDate(start).AddParticipantIDs(day.ID).Save(ctx)
	layers := []schema.ScheduleLayer{
		// UTC 时区。夜班 22:00~06:00（跨午夜），日班 06:00~22:00。全天无缝覆盖。
		{ID: itoa(rN.ID), Name: "夜班", Priority: 1, RotationID: itoa(rN.ID),
			Timezone: "UTC", WorkStart: "22:00", WorkEnd: "06:00"},
		{ID: itoa(rD.ID), Name: "日班", Priority: 2, RotationID: itoa(rD.ID),
			Timezone: "UTC", WorkStart: "06:00", WorkEnd: "22:00"},
	}
	sched, _ := c.Schedule.Create().SetName("昼夜值班").SetType("follow_the_sun").
		SetTimezone("UTC").AddRotationIDs(rN.ID, rD.ID).SetLayers(layers).Save(ctx)

	eng := NewEngine(c, nil)
	cases := []struct {
		utcH   int
		wantUN string
	}{
		{2, "night"},  // 02:00 在夜班跨午夜段（22-06）
		{23, "night"}, // 23:00 在夜班段
		{10, "day"},   // 10:00 在日班段
		{5, "night"},  // 05:00 仍夜班（<06:00）
	}
	for _, tc := range cases {
		at := time.Date(2026, 6, 15, tc.utcH, 0, 0, 0, time.UTC)
		res, err := eng.Oncall(ctx, sched.ID, at)
		if err != nil {
			t.Fatalf("Oncall utc %dh: %v", tc.utcH, err)
		}
		if len(res.Layers) == 0 || res.Layers[0].Users[0].Username != tc.wantUN {
			t.Errorf("utc %dh: want %q, got %+v", tc.utcH, tc.wantUN, res.Layers)
		}
	}
}

// TestFollowTheSun_Override 验证 follow_the_sun 与 Override 叠加：换班时段内顶替人最高优先级。
func TestFollowTheSun_Override(t *testing.T) {
	c := newTestClient(t)
	sched, _, _, _ := seedFollowTheSun(t, c)
	eng := NewEngine(c, nil)
	ctx := context.Background()

	sub, _ := c.User.Create().SetUsername("sub").SetEmail("sub@x.com").SetName("替班").Save(ctx)
	// UTC 12:00（欧洲在班）时段换给 sub。
	winStart := time.Date(2026, 6, 15, 11, 0, 0, 0, time.UTC)
	winEnd := time.Date(2026, 6, 15, 13, 0, 0, 0, time.UTC)
	if _, err := c.Override.Create().SetScheduleID(sched.ID).SetUserID(sub.ID).
		SetStartTime(winStart).SetEndTime(winEnd).SetReason("emea 请假").Save(ctx); err != nil {
		t.Fatalf("create override: %v", err)
	}

	at := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	res, err := eng.Oncall(ctx, sched.ID, at)
	if err != nil {
		t.Fatalf("Oncall: %v", err)
	}
	top := res.Layers[0].Users[0]
	if top.Username != "sub" || !top.Override {
		t.Errorf("override in fts: want sub override=true at top, got %q override=%v", top.Username, top.Override)
	}
}

// TestFollowTheSun_DisabledNotResolved 验证 follow_the_sun 跳过禁用值班人。
func TestFollowTheSun_DisabledNotResolved(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	// 单区单人，该人禁用 + 在工作时段 → 无在班（禁用被过滤，且无兜底人）。
	u, _ := c.User.Create().SetUsername("off").SetEmail("off@x.com").SetStatus("disabled").Save(ctx)
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	rot, _ := c.Rotation.Create().SetName("单区").SetShiftLength("24h").SetHandoffTime("00:00").
		SetRotationType("daily").SetStartDate(start).AddParticipantIDs(u.ID).Save(ctx)
	layers := []schema.ScheduleLayer{
		{ID: itoa(rot.ID), Name: "单区", Priority: 1, RotationID: itoa(rot.ID),
			Timezone: "UTC", WorkStart: "00:00", WorkEnd: "23:59"},
	}
	sched, _ := c.Schedule.Create().SetName("单区值班").SetType("follow_the_sun").
		SetTimezone("UTC").AddRotationIDs(rot.ID).SetLayers(layers).Save(ctx)

	eng := NewEngine(c, nil)
	at := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	res, err := eng.Oncall(ctx, sched.ID, at)
	if err != nil {
		t.Fatalf("Oncall: %v", err)
	}
	for _, l := range res.Layers {
		if len(l.Users) > 0 {
			t.Errorf("disabled user should not be resolved in fts, got %+v", l.Users)
		}
	}
}

// TestFollowTheSun_Preview 验证预览反映一天内多时区接力（并集含各区）。
func TestFollowTheSun_Preview(t *testing.T) {
	c := newTestClient(t)
	sched, _, _, _ := seedFollowTheSun(t, c)
	eng := NewEngine(c, nil)

	days, err := eng.Preview(context.Background(), sched.ID, 3)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if len(days) != 3 {
		t.Fatalf("expected 3 days, got %d", len(days))
	}
	// 每天并集应覆盖亚太/欧洲/美洲三区（一天内接力全部出现）。
	for i, d := range days {
		got := map[string]bool{}
		for _, l := range d.Layers {
			for _, u := range l.Users {
				got[u.Username] = true
			}
		}
		for _, want := range []string{"apac", "emea", "amer"} {
			if !got[want] {
				t.Errorf("day %d preview missing %q, got %+v", i, want, got)
			}
		}
	}
}

// TestInWorkWindow 单元测试工作时段命中判定（含普通/跨午夜/全天/边界）。
func TestInWorkWindow(t *testing.T) {
	mk := func(h, m int) time.Time { return time.Date(2026, 6, 15, h, m, 0, 0, time.UTC) }
	cases := []struct {
		name       string
		at         time.Time
		start, end string
		want       bool
	}{
		{"normal in", mk(10, 0), "09:00", "17:00", true},
		{"normal start boundary (inclusive)", mk(9, 0), "09:00", "17:00", true},
		{"normal end boundary (exclusive)", mk(17, 0), "09:00", "17:00", false},
		{"normal before", mk(8, 59), "09:00", "17:00", false},
		{"normal after", mk(17, 1), "09:00", "17:00", false},
		{"cross-midnight late night in", mk(23, 0), "22:00", "06:00", true},
		{"cross-midnight early morning in", mk(5, 0), "22:00", "06:00", true},
		{"cross-midnight end boundary exclusive", mk(6, 0), "22:00", "06:00", false},
		{"cross-midnight midday out", mk(12, 0), "22:00", "06:00", false},
		{"empty = all day", mk(3, 0), "", "", true},
		{"unparseable = all day", mk(3, 0), "9am", "5pm", true},
		{"equal start end = all day", mk(3, 0), "00:00", "00:00", true},
	}
	for _, tc := range cases {
		if got := inWorkWindow(tc.at, tc.start, tc.end); got != tc.want {
			t.Errorf("%s: inWorkWindow(%v,%q,%q)=%v, want %v", tc.name, tc.at, tc.start, tc.end, got, tc.want)
		}
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
