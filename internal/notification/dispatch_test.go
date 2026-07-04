// dispatch_test.go T2.2 通知分发补全的核心行为测试：
//   - NotificationRule.condition/channels 参与评估分发（B7/C12）
//   - 逐通道兜底降级链：主通道失败降级到下一通道（C12）
//   - 电话/短信可触发（B8）
//   - 送达三态落库 + 查询（B22/M13）
//   - quiet_hours 命中记 suppressed（B22）
//   - 整条链全失败触发兜底 hook（B22）
package notification

import (
	"context"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	entincident "github.com/kevin/vigil/ent/incident"
	entnotification "github.com/kevin/vigil/ent/notification"
	"github.com/kevin/vigil/internal/escalation"

	_ "github.com/mattn/go-sqlite3"
)

// stubChannel 可控成功/失败的测试通道，记录被调用次数。
type stubChannel struct {
	name  string
	fail  bool // true=每次 Send 返回失败结果
	empty bool // true=返回空结果（模拟未配置降级，链应跳过）
	calls int
}

func (s *stubChannel) Name() string { return s.name }

func (s *stubChannel) Send(_ context.Context, _ *Message) ([]SendResult, error) {
	s.calls++
	if s.empty {
		return nil, nil
	}
	if s.fail {
		return []SendResult{{Channel: s.name, Target: "t", Success: false, Error: "boom"}}, nil
	}
	return []SendResult{{Channel: s.name, Target: "t", Success: true}}, nil
}

func newDispatchClient(t *testing.T) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:dispatch_"+t.Name()+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// mkIncident 建一个带 team/service 的 incident（用于 condition 匹配）。
func mkIncident(t *testing.T, c *ent.Client, severity string) (*ent.Incident, int, int) {
	t.Helper()
	ctx := context.Background()
	tm, err := c.Team.Create().SetName("payments").SetSlug("pay-" + t.Name()).Save(ctx)
	if err != nil {
		t.Fatalf("team: %v", err)
	}
	svc, err := c.Service.Create().SetName("checkout").SetSlug("chk-" + t.Name()).SetTeamID(tm.ID).Save(ctx)
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	inc, err := c.Incident.Create().
		SetNumber("INC-9001").
		SetTitle("5xx spike").
		SetSeverity(entincident.Severity(severity)).
		SetStatus("triggered").
		SetPriority("p1").
		SetSummary("checkout 5xx").
		SetTriggerType("auto").
		SetTeamID(tm.ID).
		SetService(svc).
		Save(ctx)
	if err != nil {
		t.Fatalf("incident: %v", err)
	}
	return inc, tm.ID, svc.ID
}

// TestRuleResolver_ConditionMatch B7/C12：condition 按 severity/team/service 匹配，取更具体的规则。
func TestRuleResolver_ConditionMatch(t *testing.T) {
	c := newDispatchClient(t)
	ctx := context.Background()
	inc, teamID, _ := mkIncident(t, c, "critical")

	// 兜底规则（无条件，channels=[email]）
	_, _ = c.NotificationRule.Create().
		SetName("catch-all").SetCondition(map[string]any{}).SetChannels([]string{"email"}).Save(ctx)
	// 具体规则（severity+team，channels=[im,phone]）—— 更具体应优先
	_, _ = c.NotificationRule.Create().
		SetName("crit-team").
		SetCondition(map[string]any{"severity": "critical", "team_id": teamID}).
		SetChannels([]string{"im", "phone"}).
		SetTemplateID("my_tmpl").
		Save(ctx)

	rr := NewRuleResolver(c)
	m := rr.Resolve(ctx, inc)
	if m == nil {
		t.Fatal("expected a matched rule, got nil")
	}
	if m.RuleName != "crit-team" {
		t.Errorf("expected more-specific rule crit-team, got %q", m.RuleName)
	}
	if len(m.Channels) != 2 || m.Channels[0] != "im" || m.Channels[1] != "phone" {
		t.Errorf("expected channels [im phone], got %v", m.Channels)
	}
	if m.TemplateName != "my_tmpl" {
		t.Errorf("expected template my_tmpl, got %q", m.TemplateName)
	}
}

// TestRuleResolver_NoMatch 无规则命中返回 nil（notifier 退回默认，向后兼容）。
func TestRuleResolver_NoMatch(t *testing.T) {
	c := newDispatchClient(t)
	ctx := context.Background()
	inc, _, _ := mkIncident(t, c, "warning")
	// 只有一条 severity=critical 的规则，warning 事件不命中
	_, _ = c.NotificationRule.Create().
		SetName("only-crit").
		SetCondition(map[string]any{"severity": "critical"}).
		SetChannels([]string{"im"}).Save(ctx)
	if m := NewRuleResolver(c).Resolve(ctx, inc); m != nil {
		t.Errorf("expected no match for warning incident, got %+v", m)
	}
}

// TestNotify_RuleChannelsParticipate B7/C12：规则 channels 参与分发（不再取默认）。
func TestNotify_RuleChannelsParticipate(t *testing.T) {
	c := newDispatchClient(t)
	ctx := context.Background()
	inc, teamID, _ := mkIncident(t, c, "critical")
	_, _ = c.NotificationRule.Create().
		SetName("r").
		SetCondition(map[string]any{"team_id": teamID}).
		SetChannels([]string{"sms"}). // 规则只启用 sms
		Save(ctx)

	reg := NewRegistry()
	im := &stubChannel{name: "im"}
	sms := &stubChannel{name: "sms"}
	reg.Register(im)
	reg.Register(sms)
	n := NewNotifier(reg, []string{"im"}) // 默认链是 im，但规则应覆盖为 sms
	n.SetRuleResolver(NewRuleResolver(c))

	// channels=nil：让规则的 channels 决定
	err := n.NotifyEscalation(ctx, inc, 0, []escalation.NotifyTarget{{UserID: 1, Name: "u", Source: "user"}}, nil)
	if err != nil {
		t.Fatalf("NotifyEscalation: %v", err)
	}
	if sms.calls == 0 {
		t.Error("rule channel sms was not used")
	}
	if im.calls != 0 {
		t.Error("default channel im should not be used when rule specifies sms")
	}
}

// TestNotify_LevelChannelsBeatRule T2.1 层级 notify_channels 优先于规则 channels。
func TestNotify_LevelChannelsBeatRule(t *testing.T) {
	c := newDispatchClient(t)
	ctx := context.Background()
	inc, teamID, _ := mkIncident(t, c, "critical")
	_, _ = c.NotificationRule.Create().
		SetName("r").SetCondition(map[string]any{"team_id": teamID}).SetChannels([]string{"sms"}).Save(ctx)

	reg := NewRegistry()
	email := &stubChannel{name: "email"}
	sms := &stubChannel{name: "sms"}
	reg.Register(email)
	reg.Register(sms)
	n := NewNotifier(reg, []string{"im"})
	n.SetRuleResolver(NewRuleResolver(c))

	// 层级显式 channels=[email]：应压过规则的 sms
	err := n.NotifyEscalation(ctx, inc, 1, []escalation.NotifyTarget{{UserID: 1, Name: "u", Source: "user"}}, []string{"email"})
	if err != nil {
		t.Fatalf("NotifyEscalation: %v", err)
	}
	if email.calls == 0 {
		t.Error("level channel email was not used")
	}
	if sms.calls != 0 {
		t.Error("rule channel sms should be overridden by level channels")
	}
}

// TestNotify_FallbackChain C12：主通道失败降级到下一通道，首个成功即停止。
func TestNotify_FallbackChain(t *testing.T) {
	c := newDispatchClient(t)
	ctx := context.Background()
	inc, _, _ := mkIncident(t, c, "critical")

	reg := NewRegistry()
	im := &stubChannel{name: "im", fail: true} // 主通道失败
	email := &stubChannel{name: "email"}       // 兜底成功
	phone := &stubChannel{name: "phone"}       // 不应被触达（email 已成功）
	reg.Register(im)
	reg.Register(email)
	reg.Register(phone)
	n := NewNotifier(reg, []string{"im", "email", "phone"})
	n.SetDeliveryRecorder(NewDeliveryRecorder(c))

	err := n.NotifyEscalation(ctx, inc, 0, []escalation.NotifyTarget{{UserID: 1, Name: "u", Source: "user"}}, nil)
	if err != nil {
		t.Fatalf("NotifyEscalation: %v", err)
	}
	if im.calls == 0 {
		t.Error("primary channel im should be attempted")
	}
	if email.calls == 0 {
		t.Error("fallback channel email should be attempted after im fails")
	}
	if phone.calls != 0 {
		t.Error("phone should NOT be attempted once email succeeds (chain stops at first success)")
	}
	// 送达记录：im failed + email sent
	failed, _ := c.Notification.Query().Where(entnotification.StatusEQ(entnotification.StatusFailed)).Count(ctx)
	sent, _ := c.Notification.Query().Where(entnotification.StatusEQ(entnotification.StatusSent)).Count(ctx)
	if failed == 0 {
		t.Error("expected a failed delivery record for im")
	}
	if sent == 0 {
		t.Error("expected a sent delivery record for email")
	}
}

// TestNotify_PhoneSMSTriggerable B8：电话/短信在降级链中可触发。
func TestNotify_PhoneSMSTriggerable(t *testing.T) {
	c := newDispatchClient(t)
	ctx := context.Background()
	inc, _, _ := mkIncident(t, c, "critical")

	reg := NewRegistry()
	phone := &stubChannel{name: "phone"}
	reg.Register(phone)
	n := NewNotifier(reg, []string{"phone"})

	err := n.NotifyEscalation(ctx, inc, 0, []escalation.NotifyTarget{{UserID: 1, Name: "u", Source: "user"}}, []string{"phone"})
	if err != nil {
		t.Fatalf("NotifyEscalation: %v", err)
	}
	if phone.calls == 0 {
		t.Error("phone channel was not triggered")
	}
}

// TestNotify_SkipEmptyChannelInChain 通道返回空结果（未配置降级）时链跳过继续下一通道。
func TestNotify_SkipEmptyChannelInChain(t *testing.T) {
	c := newDispatchClient(t)
	ctx := context.Background()
	inc, _, _ := mkIncident(t, c, "critical")

	reg := NewRegistry()
	phone := &stubChannel{name: "phone", empty: true} // 未配置：返回空
	sms := &stubChannel{name: "sms"}                  // 兜底成功
	reg.Register(phone)
	reg.Register(sms)
	n := NewNotifier(reg, []string{"phone", "sms"})

	_ = n.NotifyEscalation(ctx, inc, 0, []escalation.NotifyTarget{{UserID: 1, Name: "u", Source: "user"}}, nil)
	if phone.calls == 0 || sms.calls == 0 {
		t.Errorf("expected both attempted (phone empty→skip, sms success): phone=%d sms=%d", phone.calls, sms.calls)
	}
}

// TestNotify_QuietHoursSuppressed B22：非 critical 命中静默记 suppressed，不发也不丢。
func TestNotify_QuietHoursSuppressed(t *testing.T) {
	c := newDispatchClient(t)
	ctx := context.Background()
	inc, _, _ := mkIncident(t, c, "warning") // 非 critical 才会被静默

	reg := NewRegistry()
	im := &stubChannel{name: "im"}
	reg.Register(im)
	n := NewNotifier(reg, []string{"im"})
	n.SetDeliveryRecorder(NewDeliveryRecorder(c))
	// 全天静默（00:00-23:59），非值班人应被静默
	n.SetQuietHoursResolver(func(*ent.Incident) *QuietHours {
		return &QuietHours{Enabled: true, Start: "00:00", End: "23:59", Timezone: "UTC"}
	})

	// source=user（非 schedule）→ 非值班人 → 应被静默
	err := n.NotifyEscalation(ctx, inc, 0, []escalation.NotifyTarget{{UserID: 1, Name: "u", Source: "user"}}, nil)
	if err != nil {
		t.Fatalf("NotifyEscalation: %v", err)
	}
	if im.calls != 0 {
		t.Error("im should not be called when suppressed by quiet_hours")
	}
	sup, _ := c.Notification.Query().Where(entnotification.StatusEQ(entnotification.StatusSuppressed)).Count(ctx)
	if sup == 0 {
		t.Error("expected a suppressed delivery record")
	}
}

// TestNotify_OncallBypassesQuietHours 值班人（source=schedule）穿透静默照常发送。
func TestNotify_OncallBypassesQuietHours(t *testing.T) {
	c := newDispatchClient(t)
	ctx := context.Background()
	inc, _, _ := mkIncident(t, c, "warning")

	reg := NewRegistry()
	im := &stubChannel{name: "im"}
	reg.Register(im)
	n := NewNotifier(reg, []string{"im"})
	n.SetQuietHoursResolver(func(*ent.Incident) *QuietHours {
		return &QuietHours{Enabled: true, Start: "00:00", End: "23:59", Timezone: "UTC"}
	})

	_ = n.NotifyEscalation(ctx, inc, 0, []escalation.NotifyTarget{{UserID: 1, Name: "oncall", Source: "schedule"}}, nil)
	if im.calls == 0 {
		t.Error("oncall (source=schedule) must bypass quiet_hours and be notified")
	}
}

// TestNotify_AllFailedHook B22：整条链全失败触发兜底 hook + 记 failed。
func TestNotify_AllFailedHook(t *testing.T) {
	c := newDispatchClient(t)
	ctx := context.Background()
	inc, _, _ := mkIncident(t, c, "critical")

	reg := NewRegistry()
	im := &stubChannel{name: "im", fail: true}
	email := &stubChannel{name: "email", fail: true}
	reg.Register(im)
	reg.Register(email)
	n := NewNotifier(reg, []string{"im", "email"})
	n.SetDeliveryRecorder(NewDeliveryRecorder(c))

	hookFired := false
	var hookInc *ent.Incident
	n.SetAllFailedHook(func(_ context.Context, i *ent.Incident, _ Target, _, _ string) {
		hookFired = true
		hookInc = i
	})

	_ = n.NotifyEscalation(ctx, inc, 0, []escalation.NotifyTarget{{UserID: 1, Name: "u", Source: "user"}}, nil)
	if !hookFired {
		t.Error("all-failed hook should fire when entire chain fails")
	}
	if hookInc == nil || hookInc.ID != inc.ID {
		t.Error("hook should receive the failed incident")
	}
	failed, _ := c.Notification.Query().Where(entnotification.StatusEQ(entnotification.StatusFailed)).Count(ctx)
	if failed == 0 {
		t.Error("expected failed delivery records")
	}
}

// TestQueryByIncident B22/M13：送达记录可按 incident 查询、分页。
func TestQueryByIncident(t *testing.T) {
	c := newDispatchClient(t)
	ctx := context.Background()
	inc, _, _ := mkIncident(t, c, "critical")
	rec := NewDeliveryRecorder(c)
	for i := 0; i < 3; i++ {
		if err := rec.Record(ctx, DeliveryRecord{
			IncidentID: inc.ID, UserID: 1, Channel: "im", Target: "u", Status: StatusSent, Level: i,
		}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	items, total, err := QueryByIncident(ctx, c, inc.ID, 100, 0)
	if err != nil {
		t.Fatalf("QueryByIncident: %v", err)
	}
	if total != 3 || len(items) != 3 {
		t.Errorf("expected 3 records, got total=%d len=%d", total, len(items))
	}
}

// TestNotify_NoRuleUsesDefault 无规则/无 resolver 时用默认链（向后兼容）。
func TestNotify_NoRuleUsesDefault(t *testing.T) {
	c := newDispatchClient(t)
	ctx := context.Background()
	inc, _, _ := mkIncident(t, c, "critical")

	reg := NewRegistry()
	im := &stubChannel{name: "im"}
	reg.Register(im)
	n := NewNotifier(reg, []string{"im"})
	// 不注入 RuleResolver：应回落默认链
	_ = n.NotifyEscalation(ctx, inc, 0, []escalation.NotifyTarget{{UserID: 1, Name: "u", Source: "user"}}, nil)
	if im.calls == 0 {
		t.Error("default channel should be used when no rule resolver")
	}
}
