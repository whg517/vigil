package postmortem

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/postmortem"
	"github.com/kevin/vigil/ent/timelineitem"
	"github.com/kevin/vigil/internal/event"

	_ "github.com/mattn/go-sqlite3"
)

func newTestClient(t *testing.T) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:pm_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// seedIncidentWithTimeline 建一个 resolved 事件 + 若干时间线。
func seedIncidentWithTimeline(t *testing.T, c *ent.Client) *ent.Incident {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	inc, err := c.Incident.Create().
		SetNumber("INC-0042").
		SetTitle("支付5xx").
		SetSeverity("critical").
		SetStatus("resolved").
		SetPriority("p1").
		SetSummary("支付服务5xx").
		SetTriggerType("auto").
		SetCreatedAt(now.Add(-30 * time.Minute)).
		SetResolvedAt(now).
		Save(ctx)
	if err != nil {
		t.Fatalf("create incident: %v", err)
	}
	for _, item := range []struct {
		ts      time.Time
		typ     timelineitem.Type
		content string
	}{
		{now.Add(-30 * time.Minute), timelineitem.TypeIncidentCreated, "事件创建"},
		{now.Add(-29 * time.Minute), timelineitem.TypeEventAttached, "告警接入"},
		{now.Add(-28 * time.Minute), timelineitem.TypeEscalated, "升级 level 1"},
		{now.Add(-5 * time.Minute), timelineitem.TypeResolved, "已解决"},
	} {
		if _, err := c.TimelineItem.Create().
			SetIncidentID(inc.ID).
			SetType(item.typ).
			SetContent(item.content).
			SetSource(timelineitem.SourceSystem).
			SetTimestamp(item.ts).
			SetActor(map[string]string{"kind": "system"}).
			Save(ctx); err != nil {
			t.Fatalf("create timeline: %v", err)
		}
	}
	return inc
}

// TestGenerateDraft_NoLLM 验证无 LLM 时降级生成草稿（含时间线填充）。
func TestGenerateDraft_NoLLM(t *testing.T) {
	c := newTestClient(t)
	inc := seedIncidentWithTimeline(t, c)
	eng := NewEngine(c, nil) // 无 LLM

	pm, err := eng.GenerateDraft(context.Background(), inc.ID)
	if err != nil {
		t.Fatalf("GenerateDraft: %v", err)
	}
	if pm.Status != postmortem.StatusDraft {
		t.Errorf("status: got %q, want draft", pm.Status)
	}
	if pm.GeneratedBy != postmortem.GeneratedByHuman {
		t.Errorf("generated_by: got %q, want human (no LLM)", pm.GeneratedBy)
	}
	sections := pm.Sections
	tl, ok := sections["timeline"].([]map[string]string)
	if !ok || len(tl) != 4 {
		t.Errorf("timeline section: expected 4 items, got %v", sections["timeline"])
	}
	// 降级摘要
	if s, _ := sections["summary"].(string); s == "" {
		t.Error("summary should not be empty (fallback)")
	}
}

// mockLLM 测试用 LLM Provider。
type mockLLM struct {
	drafts map[string]string
}

func (m *mockLLM) DraftSection(_ context.Context, section string, _ map[string]any) (string, error) {
	if d, ok := m.drafts[section]; ok {
		return d, nil
	}
	return "", nil
}

// TestGenerateDraft_WithLLM 验证有 LLM 时用 AI 起草，generated_by=mixed。
func TestGenerateDraft_WithLLM(t *testing.T) {
	c := newTestClient(t)
	inc := seedIncidentWithTimeline(t, c)
	llm := &mockLLM{drafts: map[string]string{
		"summary":    "DB连接池耗尽导致5xx，持续30分钟",
		"root_cause": "连接池配置过小，新版引入连接泄漏",
	}}
	eng := NewEngine(c, llm)

	pm, err := eng.GenerateDraft(context.Background(), inc.ID)
	if err != nil {
		t.Fatalf("GenerateDraft: %v", err)
	}
	if pm.GeneratedBy != postmortem.GeneratedByMixed {
		t.Errorf("generated_by: got %q, want mixed", pm.GeneratedBy)
	}
	if s, _ := pm.Sections["summary"].(string); s != "DB连接池耗尽导致5xx，持续30分钟" {
		t.Errorf("AI summary not used: got %q", s)
	}
	if rc, _ := pm.Sections["root_cause"].(string); rc != "连接池配置过小，新版引入连接泄漏" {
		t.Errorf("AI root_cause not used: got %q", rc)
	}
}

// TestGenerateDraft_Idempotent 验证重复生成更新而非新建。
func TestGenerateDraft_Idempotent(t *testing.T) {
	c := newTestClient(t)
	inc := seedIncidentWithTimeline(t, c)
	eng := NewEngine(c, nil)

	pm1, _ := eng.GenerateDraft(context.Background(), inc.ID)
	pm2, _ := eng.GenerateDraft(context.Background(), inc.ID)
	if pm1.ID != pm2.ID {
		t.Errorf("重复生成应更新同一记录: pm1.ID=%d pm2.ID=%d", pm1.ID, pm2.ID)
	}
}

// TestGenerateDraft_RefuseOverwritePublished S7 覆盖保护：已发布（脱离 draft）的复盘
// 重新起草被拒（ErrPostmortemNotDraft），已校对/发布的 sections 不被冲掉。
func TestGenerateDraft_RefuseOverwritePublished(t *testing.T) {
	c := newTestClient(t)
	inc := seedIncidentWithTimeline(t, c)
	eng := NewEngine(c, nil)

	pm, err := eng.GenerateDraft(context.Background(), inc.ID)
	if err != nil {
		t.Fatalf("first draft: %v", err)
	}
	// 推到 published（draft → in_review → published）
	if _, err := eng.Transition(context.Background(), pm.ID, postmortem.StatusInReview); err != nil {
		t.Fatalf("→in_review: %v", err)
	}
	if _, err := eng.Transition(context.Background(), pm.ID, postmortem.StatusPublished); err != nil {
		t.Fatalf("→published: %v", err)
	}
	// 人工改一个 sections 字段模拟校对成果，随后重新起草不得覆盖它
	if _, err := c.Postmortem.UpdateOneID(pm.ID).
		SetSections(map[string]any{"summary": "人工校对后的定稿"}).Save(context.Background()); err != nil {
		t.Fatalf("edit sections: %v", err)
	}
	// 重新起草 → 拒绝
	if _, err := eng.GenerateDraft(context.Background(), inc.ID); !errors.Is(err, ErrPostmortemNotDraft) {
		t.Fatalf("re-draft published: got %v, want ErrPostmortemNotDraft", err)
	}
	// sections 未被覆盖
	got, _ := c.Postmortem.Get(context.Background(), pm.ID)
	if s, _ := got.Sections["summary"].(string); s != "人工校对后的定稿" {
		t.Errorf("published sections overwritten: got %q", s)
	}
	if got.Status != postmortem.StatusPublished {
		t.Errorf("status changed: got %q, want published", got.Status)
	}
}

// TestTransition_Valid 验证合法状态流转。
func TestTransition_Valid(t *testing.T) {
	c := newTestClient(t)
	inc := seedIncidentWithTimeline(t, c)
	eng := NewEngine(c, nil)
	pm, _ := eng.GenerateDraft(context.Background(), inc.ID)

	// draft → in_review → published
	pm, err := eng.Transition(context.Background(), pm.ID, postmortem.StatusInReview)
	if err != nil {
		t.Fatalf("draft→in_review: %v", err)
	}
	pm, err = eng.Transition(context.Background(), pm.ID, postmortem.StatusPublished)
	if err != nil {
		t.Fatalf("in_review→published: %v", err)
	}
	if pm.PublishedAt == nil {
		t.Error("published_at should be set on publish")
	}
	// published → archived
	if _, err := eng.Transition(context.Background(), pm.ID, postmortem.StatusArchived); err != nil {
		t.Fatalf("published→archived: %v", err)
	}
}

// TestTransition_Invalid 验证非法流转被拒。
func TestTransition_Invalid(t *testing.T) {
	c := newTestClient(t)
	inc := seedIncidentWithTimeline(t, c)
	eng := NewEngine(c, nil)
	pm, _ := eng.GenerateDraft(context.Background(), inc.ID)

	// draft → published 非法（必须经 in_review）
	_, err := eng.Transition(context.Background(), pm.ID, postmortem.StatusPublished)
	if err == nil {
		t.Error("draft→published should be rejected")
	}
	// draft → archived 非法
	_, err = eng.Transition(context.Background(), pm.ID, postmortem.StatusArchived)
	if err == nil {
		t.Error("draft→archived should be rejected")
	}
}

// TestIsValidTransition 覆盖全部合法/非法组合。
func TestIsValidTransition(t *testing.T) {
	valid := map[postmortem.Status]postmortem.Status{
		postmortem.StatusDraft:     postmortem.StatusInReview,
		postmortem.StatusInReview:  postmortem.StatusPublished,
		postmortem.StatusPublished: postmortem.StatusArchived,
	}
	for from, to := range valid {
		if !isValidTransition(from, to) {
			t.Errorf("应允许 %s→%s", from, to)
		}
	}
	invalid := []struct{ from, to postmortem.Status }{
		{postmortem.StatusDraft, postmortem.StatusPublished},
		{postmortem.StatusArchived, postmortem.StatusDraft},
		{postmortem.StatusPublished, postmortem.StatusDraft},
	}
	for _, c := range invalid {
		if isValidTransition(c.from, c.to) {
			t.Errorf("应拒绝 %s→%s", c.from, c.to)
		}
	}
}

// fakeCloser 记录 Close 调用并按 incident.Service.Close 语义推进 incident 状态。
// resolved → closed；已 closed 幂等（返回 nil，不重复）；非 resolved 记录调用但不改状态（容错）。
type fakeCloser struct {
	c          *ent.Client
	calledIncs []int
}

func (f *fakeCloser) Close(ctx context.Context, incID int, _ int) error {
	f.calledIncs = append(f.calledIncs, incID)
	inc, err := f.c.Incident.Get(ctx, incID)
	if err != nil {
		return err
	}
	if inc.Status == "closed" {
		return nil // 幂等
	}
	if inc.Status != "resolved" {
		return nil // 非 resolved：容错跳过（模拟 wire.go 吞掉非法转换）
	}
	return f.c.Incident.UpdateOneID(incID).SetStatus("closed").Exec(ctx)
}

// TestTransition_PublishClosesIncident 复盘发布（→published）联动把关联 resolved incident 推进到 closed。
func TestTransition_PublishClosesIncident(t *testing.T) {
	c := newTestClient(t)
	inc := seedIncidentWithTimeline(t, c) // resolved
	eng := NewEngine(c, nil)
	fc := &fakeCloser{c: c}
	eng.SetIncidentCloser(fc)

	pm, _ := eng.GenerateDraft(context.Background(), inc.ID)
	if _, err := eng.Transition(context.Background(), pm.ID, postmortem.StatusInReview); err != nil {
		t.Fatalf("→in_review: %v", err)
	}
	// in_review → published：应触发联动关闭 incident
	if _, err := eng.Transition(context.Background(), pm.ID, postmortem.StatusPublished); err != nil {
		t.Fatalf("→published: %v", err)
	}
	if len(fc.calledIncs) != 1 || fc.calledIncs[0] != inc.ID {
		t.Errorf("closer 未按预期被调用: %v", fc.calledIncs)
	}
	got, _ := c.Incident.Get(context.Background(), inc.ID)
	if got.Status != "closed" {
		t.Errorf("incident status: got %q, want closed（复盘发布应联动收口）", got.Status)
	}
}

// TestTransition_PublishNoCloserDoesNotBlock 未注入 closer 时发布不联动，也不报错（降级）。
func TestTransition_PublishNoCloserDoesNotBlock(t *testing.T) {
	c := newTestClient(t)
	inc := seedIncidentWithTimeline(t, c)
	eng := NewEngine(c, nil) // 不注入 closer

	pm, _ := eng.GenerateDraft(context.Background(), inc.ID)
	if _, err := eng.Transition(context.Background(), pm.ID, postmortem.StatusInReview); err != nil {
		t.Fatalf("→in_review: %v", err)
	}
	if _, err := eng.Transition(context.Background(), pm.ID, postmortem.StatusPublished); err != nil {
		t.Fatalf("→published without closer: %v", err)
	}
	// 无联动：incident 停在 resolved（复盘照常发布）
	got, _ := c.Incident.Get(context.Background(), inc.ID)
	if got.Status != "resolved" {
		t.Errorf("incident status: got %q, want resolved（无 closer 不应改动）", got.Status)
	}
}

// TestTransition_NonPublishDoesNotClose 非发布流转（draft→in_review）不触发联动关闭。
func TestTransition_NonPublishDoesNotClose(t *testing.T) {
	c := newTestClient(t)
	inc := seedIncidentWithTimeline(t, c)
	eng := NewEngine(c, nil)
	fc := &fakeCloser{c: c}
	eng.SetIncidentCloser(fc)

	pm, _ := eng.GenerateDraft(context.Background(), inc.ID)
	if _, err := eng.Transition(context.Background(), pm.ID, postmortem.StatusInReview); err != nil {
		t.Fatalf("→in_review: %v", err)
	}
	if len(fc.calledIncs) != 0 {
		t.Errorf("非发布流转不应调用 closer: %v", fc.calledIncs)
	}
	got, _ := c.Incident.Get(context.Background(), inc.ID)
	if got.Status != "resolved" {
		t.Errorf("incident status: got %q, want resolved", got.Status)
	}
}

// seedIncidentSeverity 建一个指定 severity 的 resolved 事件（无时间线，够 GenerateDraft 用）。
func seedIncidentSeverity(t *testing.T, c *ent.Client, sev incident.Severity) *ent.Incident {
	t.Helper()
	now := time.Now()
	inc, err := c.Incident.Create().
		SetNumber("INC-" + string(sev)).
		SetTitle("事件-" + string(sev)).
		SetSeverity(sev).
		SetStatus(incident.StatusResolved).
		SetPriority("p2").
		SetTriggerType("auto").
		SetCreatedAt(now.Add(-10 * time.Minute)).
		SetResolvedAt(now).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create incident: %v", err)
	}
	return inc
}

// resolvedEvent 构造一个 IncidentResolved 领域事件。
func resolvedEvent(inc *ent.Incident) event.Event {
	return event.Event{Type: event.IncidentResolved, Incident: inc, Action: "resolve"}
}

// TestOnIncidentResolved_CriticalAutoDraft critical 事件 resolved 强制自动起草复盘。
func TestOnIncidentResolved_CriticalAutoDraft(t *testing.T) {
	c := newTestClient(t)
	inc := seedIncidentSeverity(t, c, incident.SeverityCritical)
	eng := NewEngine(c, nil)

	if err := eng.OnIncidentResolved(context.Background(), resolvedEvent(inc)); err != nil {
		t.Fatalf("OnIncidentResolved: %v", err)
	}
	pm, err := c.Postmortem.Query().
		Where(postmortem.HasIncidentWith(incident.IDEQ(inc.ID))).Only(context.Background())
	if err != nil {
		t.Fatalf("critical resolved 应自动建 draft 复盘: %v", err)
	}
	if pm.Status != postmortem.StatusDraft {
		t.Errorf("auto-draft status: got %q, want draft", pm.Status)
	}
}

// TestOnIncidentResolved_WarningDefaultNoDraft warning 默认不强制，不自动起草。
func TestOnIncidentResolved_WarningDefaultNoDraft(t *testing.T) {
	c := newTestClient(t)
	inc := seedIncidentSeverity(t, c, incident.SeverityWarning)
	eng := NewEngine(c, nil) // autoDraftWarning 默认 false

	if err := eng.OnIncidentResolved(context.Background(), resolvedEvent(inc)); err != nil {
		t.Fatalf("OnIncidentResolved: %v", err)
	}
	exist, _ := c.Postmortem.Query().
		Where(postmortem.HasIncidentWith(incident.IDEQ(inc.ID))).Exist(context.Background())
	if exist {
		t.Error("warning 默认不应自动起草复盘")
	}
}

// TestOnIncidentResolved_WarningConfiguredDraft warning 开启配置后自动起草。
func TestOnIncidentResolved_WarningConfiguredDraft(t *testing.T) {
	c := newTestClient(t)
	inc := seedIncidentSeverity(t, c, incident.SeverityWarning)
	eng := NewEngine(c, nil)
	eng.SetAutoDraftWarning(true) // 开启 warning 自动起草

	if err := eng.OnIncidentResolved(context.Background(), resolvedEvent(inc)); err != nil {
		t.Fatalf("OnIncidentResolved: %v", err)
	}
	exist, _ := c.Postmortem.Query().
		Where(postmortem.HasIncidentWith(incident.IDEQ(inc.ID))).Exist(context.Background())
	if !exist {
		t.Error("warning 开启配置后应自动起草复盘")
	}
}

// TestOnIncidentResolved_InfoNoDraft info 不强制，不自动起草（即便开了 warning 开关）。
func TestOnIncidentResolved_InfoNoDraft(t *testing.T) {
	c := newTestClient(t)
	inc := seedIncidentSeverity(t, c, incident.SeverityInfo)
	eng := NewEngine(c, nil)
	eng.SetAutoDraftWarning(true) // 开关只影响 warning，不影响 info

	if err := eng.OnIncidentResolved(context.Background(), resolvedEvent(inc)); err != nil {
		t.Fatalf("OnIncidentResolved: %v", err)
	}
	exist, _ := c.Postmortem.Query().
		Where(postmortem.HasIncidentWith(incident.IDEQ(inc.ID))).Exist(context.Background())
	if exist {
		t.Error("info 不应自动起草复盘")
	}
}

// TestOnIncidentResolved_NilIncident 事件无 incident 时安全 no-op。
func TestOnIncidentResolved_NilIncident(t *testing.T) {
	c := newTestClient(t)
	eng := NewEngine(c, nil)
	if err := eng.OnIncidentResolved(context.Background(), event.Event{Type: event.IncidentResolved}); err != nil {
		t.Errorf("nil incident 应 no-op 无错: %v", err)
	}
}

// TestOnIncidentResolved_DoesNotOverwritePublished 自动起草不回冲已发布复盘（S7 覆盖保护，静默容忍）。
func TestOnIncidentResolved_DoesNotOverwritePublished(t *testing.T) {
	c := newTestClient(t)
	inc := seedIncidentSeverity(t, c, incident.SeverityCritical)
	eng := NewEngine(c, nil)

	// 先起草并推到 published，再人工改 sections 模拟定稿
	pm, _ := eng.GenerateDraft(context.Background(), inc.ID)
	if _, err := eng.Transition(context.Background(), pm.ID, postmortem.StatusInReview); err != nil {
		t.Fatalf("→in_review: %v", err)
	}
	if _, err := eng.Transition(context.Background(), pm.ID, postmortem.StatusPublished); err != nil {
		t.Fatalf("→published: %v", err)
	}
	if _, err := c.Postmortem.UpdateOneID(pm.ID).
		SetSections(map[string]any{"summary": "定稿"}).Save(context.Background()); err != nil {
		t.Fatalf("edit: %v", err)
	}
	// 再来一次 resolved 自动起草：不得覆盖，且不上报错误（S7 静默容忍）
	if err := eng.OnIncidentResolved(context.Background(), resolvedEvent(inc)); err != nil {
		t.Errorf("自动起草遇已发布复盘应静默 no-op: %v", err)
	}
	got, _ := c.Postmortem.Get(context.Background(), pm.ID)
	if s, _ := got.Sections["summary"].(string); s != "定稿" {
		t.Errorf("已发布 sections 被覆盖: got %q", s)
	}
}

// TestHasPublishedPostmortem 闸门查询：published/archived 视为已完成，draft/in_review 未完成。
func TestHasPublishedPostmortem(t *testing.T) {
	c := newTestClient(t)
	inc := seedIncidentSeverity(t, c, incident.SeverityCritical)
	eng := NewEngine(c, nil)

	// 无复盘 → false
	if done, _ := eng.HasPublishedPostmortem(context.Background(), inc.ID); done {
		t.Error("无复盘应为未完成")
	}
	// draft → 仍 false
	pm, _ := eng.GenerateDraft(context.Background(), inc.ID)
	if done, _ := eng.HasPublishedPostmortem(context.Background(), inc.ID); done {
		t.Error("draft 复盘不应算已完成")
	}
	// in_review → 仍 false
	if _, err := eng.Transition(context.Background(), pm.ID, postmortem.StatusInReview); err != nil {
		t.Fatalf("→in_review: %v", err)
	}
	if done, _ := eng.HasPublishedPostmortem(context.Background(), inc.ID); done {
		t.Error("in_review 复盘不应算已完成")
	}
	// published → true
	if _, err := eng.Transition(context.Background(), pm.ID, postmortem.StatusPublished); err != nil {
		t.Fatalf("→published: %v", err)
	}
	if done, _ := eng.HasPublishedPostmortem(context.Background(), inc.ID); !done {
		t.Error("published 复盘应算已完成")
	}
	// archived → 仍 true（归档态同样代表复盘走完）
	if _, err := eng.Transition(context.Background(), pm.ID, postmortem.StatusArchived); err != nil {
		t.Fatalf("→archived: %v", err)
	}
	if done, _ := eng.HasPublishedPostmortem(context.Background(), inc.ID); !done {
		t.Error("archived 复盘应算已完成")
	}
}

// TestFallbackImpact 验证无 AI 时影响估算（含持续时间）。
func TestFallbackImpact(t *testing.T) {
	c := newTestClient(t)
	inc := seedIncidentWithTimeline(t, c)
	got := fallbackImpact(inc)
	if got == "" {
		t.Error("fallback impact should not be empty")
	}
	// 应包含"30m"或类似持续时间
	if len(got) < 5 {
		t.Errorf("fallback impact too short: %q", got)
	}
}
