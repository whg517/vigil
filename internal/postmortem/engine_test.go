package postmortem

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/postmortem"
	"github.com/kevin/vigil/ent/timelineitem"

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
