package postmortem

import (
	"context"
	"testing"

	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/postmortem"

	_ "github.com/mattn/go-sqlite3"
)

// stubEmbedder 测试用 Embedder，返回固定向量。
type stubEmbedder struct{}

func (stubEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	return []float32{0.1, 0.2, 0.3}, nil
}

// TestEnsurePublishedEmbedding_ComputesAndStores published 时计算 embedding 入库。
func TestEnsurePublishedEmbedding_ComputesAndStores(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:pm_embed_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()

	// 建一个 published 复盘（含 sections）
	pm, _ := c.Postmortem.Create().
		SetStatus(postmortem.StatusPublished).
		SetGeneratedBy("human").
		SetSections(map[string]any{"summary": "DB 连接满", "root_cause": "连接池未配置上限"}).
		Save(ctx)

	e := &Engine{db: c, embedder: stubEmbedder{}}
	err := e.ensurePublishedEmbedding(ctx, pm)
	if err != nil {
		t.Fatalf("ensurePublishedEmbedding: %v", err)
	}
	// 确认 embedding 已写入（sqlite 存 blob，非 nil 即可）
	updated, _ := c.Postmortem.Get(ctx, pm.ID)
	if updated.Embedding == nil {
		t.Error("embedding not stored after ensurePublishedEmbedding")
	}
}

// TestExtractPostmortemText 提取 summary + root_cause。
func TestExtractPostmortemText(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:pm_text_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	pm, _ := c.Postmortem.Create().
		SetStatus(postmortem.StatusDraft).SetGeneratedBy("human").
		SetSections(map[string]any{"summary": "标题A", "root_cause": "根因B", "impact": "影响C"}).
		Save(ctx)

	text := extractPostmortemText(pm)
	// 应含 summary 和 root_cause，不含 impact
	if !contains(text, "标题A") || !contains(text, "根因B") {
		t.Errorf("text missing summary/root_cause: %q", text)
	}
	if contains(text, "影响C") {
		t.Errorf("text should not contain impact: %q", text)
	}
}

// TestTransition_PublishTriggersEmbedding publish 时自动触发 embedding（embedder 配置时）。
func TestTransition_PublishTriggersEmbedding(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:pm_transition_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()

	// 建 incident + 复盘（in_review 态）
	team, _ := c.Team.Create().SetName("t").SetSlug("t1").Save(ctx)
	svc, _ := c.Service.Create().SetName("s").SetSlug("s1").SetTeamID(team.ID).Save(ctx)
	inc, _ := c.Incident.Create().
		SetNumber("INC-001").SetTitle("test").SetSeverity("critical").
		SetStatus("triggered").SetPriority("p1").SetSummary("test").
		SetTriggerType("auto").SetTriggerSourceEventID("e1").SetServiceID(svc.ID).SetTeamID(team.ID).
		Save(ctx)
	pm, _ := c.Postmortem.Create().
		SetStatus(postmortem.StatusInReview).SetGeneratedBy("human").
		SetSections(map[string]any{"summary": "故障A"}).SetIncidentID(inc.ID).Save(ctx)

	e := NewEngine(c, nil)
	e.SetEmbedder(stubEmbedder{})
	// in_review → published，应触发 embedding
	_, err := e.Transition(ctx, pm.ID, postmortem.StatusPublished)
	if err != nil {
		t.Fatalf("Transition to published: %v", err)
	}
	updated, _ := c.Postmortem.Get(ctx, pm.ID)
	if updated.Embedding == nil {
		t.Error("embedding not computed on publish")
	}
	if updated.Status != postmortem.StatusPublished {
		t.Errorf("status=%q, want published", updated.Status)
	}
}

// TestTransition_PublishWithoutEmbedder publish 时无 embedder 不报错（降级）。
// 重点验证：无 embedder 时 publish 仍成功（不因缺 embedder 阻塞）。
func TestTransition_PublishWithoutEmbedder(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:pm_noembed_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()

	team, _ := c.Team.Create().SetName("t").SetSlug("t2").Save(ctx)
	svc, _ := c.Service.Create().SetName("s").SetSlug("s2").SetTeamID(team.ID).Save(ctx)
	inc, _ := c.Incident.Create().
		SetNumber("INC-002").SetTitle("test2").SetSeverity("warning").
		SetStatus("triggered").SetPriority("p2").SetSummary("test2").
		SetTriggerType("auto").SetTriggerSourceEventID("e2").SetServiceID(svc.ID).SetTeamID(team.ID).
		Save(ctx)
	pm, _ := c.Postmortem.Create().
		SetStatus(postmortem.StatusInReview).SetGeneratedBy("human").
		SetSections(map[string]any{}).SetIncidentID(inc.ID).Save(ctx)

	e := NewEngine(c, nil) // 无 embedder
	updated, err := e.Transition(ctx, pm.ID, postmortem.StatusPublished)
	if err != nil {
		t.Fatalf("Transition without embedder should not fail: %v", err)
	}
	if updated.Status != postmortem.StatusPublished {
		t.Errorf("status=%q, want published", updated.Status)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
