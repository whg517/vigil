// noise_learning_test.go N1.4 AI 噪声学习闭环测试：
//   - suggestNoise 产出 noise_suggestion（带 match_labels + evidence，critical 不产出）。
//   - accept noise_suggestion → 沉淀 SuppressionRule（source=ai），且该规则后续命中同类 Event 抑制。
//   - reject 不生成规则；重复 accept 幂等（不重复建规则）。
package ai

import (
	"context"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/aiinsight"
	"github.com/kevin/vigil/ent/enttest"
	entincidentn "github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/suppressionrule"
	"github.com/kevin/vigil/internal/timeline"
	"github.com/kevin/vigil/internal/triage"

	_ "github.com/mattn/go-sqlite3"
)

// newNoiseTestClient 独立命名的内存库（避免与共享 triageai_test 库交叉污染抑制规则）。
func newNoiseTestClient(t *testing.T) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:noise_"+t.Name()+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// seedIncidentWithLabeledEvents 造 team + incident + N 条带 labels 的关联 Event。
func seedIncidentWithLabeledEvents(t *testing.T, c *ent.Client, sev string, labels map[string]string) (*ent.Incident, int) {
	t.Helper()
	ctx := context.Background()
	tm := c.Team.Create().SetName("ops").SetSlug("ops-" + t.Name()).SaveX(ctx)
	inc, err := c.Incident.Create().
		SetNumber("INC-N1").SetTitle("演练告警").
		SetSeverity(entincidentn.Severity(sev)).SetStatus("triggered").
		SetSummary("周期演练，无需响应").SetTriggerType("auto").SetTeamID(tm.ID).Save(ctx)
	if err != nil {
		t.Fatalf("create incident: %v", err)
	}
	for i := 0; i < 2; i++ {
		_, err := c.Event.Create().
			SetSourceEventID("ev" + string(rune('a'+i))).SetSource("prometheus").
			SetSeverity("warning").SetStatus("firing").
			SetSummary("演练告警 drill").SetDedupKey("dk-noise").
			SetLabels(labels).
			SetIncidentID(inc.ID).Save(ctx)
		if err != nil {
			t.Fatalf("create event: %v", err)
		}
	}
	return inc, tm.ID
}

// seedNoiseInsight 造 team + incident + 一条 noise_suggestion（suggested）AIInsight（含 match_labels）。
func seedNoiseInsight(t *testing.T, c *ent.Client, matchLabels map[string]string) (*ent.Incident, *ent.AIInsight) {
	t.Helper()
	ctx := context.Background()
	tm := c.Team.Create().SetName("ops").SetSlug("ops-" + t.Name()).SaveX(ctx)
	inc, err := c.Incident.Create().SetNumber("INC-N2").SetTitle("噪声单").
		SetSeverity("warning").SetStatus("triggered").SetSummary("s").
		SetTriggerType("auto").SetTeamID(tm.ID).Save(ctx)
	if err != nil {
		t.Fatalf("create incident: %v", err)
	}
	// content.match_labels 用 map[string]any（模拟 JSON round-trip 后形态）。
	ml := map[string]any{}
	for k, v := range matchLabels {
		ml[k] = v
	}
	ins, err := c.AIInsight.Create().
		SetIncidentID(inc.ID).
		SetStage(aiinsight.StageTriage).
		SetType(aiinsight.TypeNoiseSuggestion).
		SetContent(map[string]any{"match_labels": ml, "reason": "周期演练"}).
		SetConfidence(0.9).
		SetEvidence([]map[string]any{{"kind": "event", "event_id": 1}}).
		SetStatus(aiinsight.StatusSuggested).
		Save(ctx)
	if err != nil {
		t.Fatalf("create noise insight: %v", err)
	}
	return inc, ins
}

// TestSuggestNoise_Produces 验证 suggestNoise 产出 noise_suggestion：带 match_labels（只保留候选内标签）+ evidence。
func TestSuggestNoise_Produces(t *testing.T) {
	c := newNoiseTestClient(t)
	ctx := context.Background()
	inc, _ := seedIncidentWithLabeledEvents(t, c, "warning", map[string]string{"alertname": "Drill", "env": "staging"})
	// LLM 判为噪声，挑出 alertname=Drill 作复用标签；env 也在候选内。
	mp := &mockProvider{avail: true,
		resp: `{"is_noise":true,"match_labels":{"alertname":"Drill"},"confidence":0.88,"reason":"周期演练"}`}
	e := NewTriageAIEngine(c, mp)
	e.SetRecorder(timeline.NewRecorder(c))

	ins, err := e.suggestNoise(ctx, inc)
	if err != nil {
		t.Fatalf("suggestNoise: %v", err)
	}
	if ins == nil {
		t.Fatal("应产出 noise_suggestion 建议")
	}
	if string(ins.Type) != "noise_suggestion" {
		t.Errorf("type: got %q, want noise_suggestion", ins.Type)
	}
	if string(ins.Status) != "suggested" {
		t.Errorf("status: got %q, want suggested", ins.Status)
	}
	if len(ins.Evidence) == 0 {
		t.Error("noise 建议必须带 evidence")
	}
	ml, _ := ins.Content["match_labels"].(map[string]string)
	if ml["alertname"] != "Drill" || len(ml) != 1 {
		t.Errorf("content.match_labels: got %v, want {alertname:Drill}", ins.Content["match_labels"])
	}
}

// TestSuggestNoise_Critical_NotProduced 安全守卫：critical 级 Incident 不产出降噪建议（不误抑真故障）。
func TestSuggestNoise_Critical_NotProduced(t *testing.T) {
	c := newNoiseTestClient(t)
	ctx := context.Background()
	inc, _ := seedIncidentWithLabeledEvents(t, c, "critical", map[string]string{"alertname": "Drill"})
	mp := &mockProvider{avail: true,
		resp: `{"is_noise":true,"match_labels":{"alertname":"Drill"},"confidence":0.99,"reason":"x"}`}
	e := NewTriageAIEngine(c, mp)

	ins, err := e.suggestNoise(ctx, inc)
	if err != nil {
		t.Fatalf("suggestNoise: %v", err)
	}
	if ins != nil {
		t.Error("critical 级不应产出降噪建议（安全守卫）")
	}
}

// TestSuggestNoise_HallucinatedLabelFiltered 防幻觉：LLM 编造的候选外标签被过滤，无有效标签则不产出。
func TestSuggestNoise_HallucinatedLabelFiltered(t *testing.T) {
	c := newNoiseTestClient(t)
	ctx := context.Background()
	inc, _ := seedIncidentWithLabeledEvents(t, c, "warning", map[string]string{"alertname": "Drill"})
	// LLM 编造候选里没有的标签 team=payments（值也不匹配）。
	mp := &mockProvider{avail: true,
		resp: `{"is_noise":true,"match_labels":{"team":"payments"},"confidence":0.9,"reason":"x"}`}
	e := NewTriageAIEngine(c, mp)

	ins, err := e.suggestNoise(ctx, inc)
	if err != nil {
		t.Fatalf("suggestNoise: %v", err)
	}
	if ins != nil {
		t.Error("挑出的标签全在候选外（幻觉），过滤后无有效标签，不应产出")
	}
}

// TestResolveNoise_Accept_CreatesSuppressionRule accept noise_suggestion → 沉淀 SuppressionRule（source=ai）→ applied。
func TestResolveNoise_Accept_CreatesSuppressionRule(t *testing.T) {
	c := newNoiseTestClient(t)
	ctx := context.Background()
	inc, ins := seedNoiseInsight(t, c, map[string]string{"alertname": "Drill"})

	diag := NewDiagnoseEngine(c, nil)
	got, err := diag.ResolveInsight(ctx, ins.ID, 7, true)
	if err != nil {
		t.Fatalf("ResolveInsight: %v", err)
	}
	if string(got.Status) != "applied" {
		t.Errorf("accept noise 沉淀规则成功应为 applied: got %q", got.Status)
	}
	// 应生成一条 source=ai 的抑制规则，归属 incident 所属 team，且带 source_insight_id 幂等键。
	rules := c.SuppressionRule.Query().Where(suppressionrule.SourceEQ(suppressionrule.SourceAi)).AllX(ctx)
	if len(rules) != 1 {
		t.Fatalf("应生成 1 条 AI 抑制规则, got %d", len(rules))
	}
	r := rules[0]
	if r.SourceInsightID != ins.ID {
		t.Errorf("source_insight_id: got %d, want %d", r.SourceInsightID, ins.ID)
	}
	if r.MatchLabels["alertname"] != "Drill" {
		t.Errorf("规则 match_labels: got %v, want {alertname:Drill}", r.MatchLabels)
	}
	if string(r.Action) != "suppress" {
		t.Errorf("规则 action: got %q, want suppress", r.Action)
	}
	if !r.PreserveCritical {
		t.Error("AI 沉淀规则应 preserve_critical=true（critical 不误抑）")
	}
	// 归属 team：经 insight→incident→team 反查。
	tm, terr := r.QueryTeam().Only(ctx)
	if terr != nil {
		t.Fatalf("规则应归属 incident 的 team: %v", terr)
	}
	incTeam, _ := inc.QueryTeam().Only(ctx)
	if tm.ID != incTeam.ID {
		t.Errorf("规则 team: got %d, want %d", tm.ID, incTeam.ID)
	}
}

// TestResolveNoise_RuleSuppressesMatchingEvent 沉淀的规则后续命中同类 Event → 抑制（is_noise）。
// 这是 N1.4 的闭环终点：AI 学到的噪声真正被下一条 Event 自动抑制。
func TestResolveNoise_RuleSuppressesMatchingEvent(t *testing.T) {
	c := newNoiseTestClient(t)
	ctx := context.Background()
	_, ins := seedNoiseInsight(t, c, map[string]string{"alertname": "Drill"})

	// accept → 生成规则。
	diag := NewDiagnoseEngine(c, nil)
	if _, err := diag.ResolveInsight(ctx, ins.ID, 7, true); err != nil {
		t.Fatalf("ResolveInsight: %v", err)
	}

	// 造一条命中同类 label 的新 Event（warning 级），跑抑制引擎。
	evt := c.Event.Create().SetSourceEventID("new-drill").SetSource("prometheus").
		SetSeverity("warning").SetStatus("firing").SetSummary("又一次演练").
		SetDedupKey("dk2").SetLabels(map[string]string{"alertname": "Drill", "extra": "x"}).SaveX(ctx)

	supEngine := triage.NewSuppressionEngine(c)
	out, err := supEngine.Evaluate(ctx, evt)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !out.Matched {
		t.Fatal("沉淀的 AI 规则应命中同类 Event")
	}
	if out.Action != triage.SuppressActionSuppress {
		t.Errorf("命中动作应为 suppress: got %q", out.Action)
	}
	// Apply 后 Event 标记为噪声。
	got, err := supEngine.Apply(ctx, evt, out)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !got.IsNoise {
		t.Error("命中 AI 沉淀规则的 Event 应被标记 is_noise")
	}
}

// TestResolveNoise_Reject_NoRule reject noise_suggestion 不生成规则，终态 rejected（只记录供分析）。
func TestResolveNoise_Reject_NoRule(t *testing.T) {
	c := newNoiseTestClient(t)
	ctx := context.Background()
	_, ins := seedNoiseInsight(t, c, map[string]string{"alertname": "Drill"})

	diag := NewDiagnoseEngine(c, nil)
	got, err := diag.ResolveInsight(ctx, ins.ID, 7, false)
	if err != nil {
		t.Fatalf("ResolveInsight: %v", err)
	}
	if string(got.Status) != "rejected" {
		t.Errorf("reject 终态应为 rejected: got %q", got.Status)
	}
	cnt := c.SuppressionRule.Query().Count
	if n, _ := cnt(ctx); n != 0 {
		t.Errorf("reject 不应生成任何抑制规则, got %d", n)
	}
}

// TestResolveNoise_Idempotent 幂等：即便绕过 status 前置校验，applyNoiseSuggestion 不为同一 insight 重复建规则。
func TestResolveNoise_Idempotent(t *testing.T) {
	c := newNoiseTestClient(t)
	ctx := context.Background()
	_, ins := seedNoiseInsight(t, c, map[string]string{"alertname": "Drill"})

	diag := NewDiagnoseEngine(c, nil)
	// 第一次 accept：生成规则。
	if _, err := diag.ResolveInsight(ctx, ins.ID, 7, true); err != nil {
		t.Fatalf("first accept: %v", err)
	}
	// 直接再调 applyNoiseSuggestion（模拟并发/重放绕过 status 校验）——幂等命中，不重复建。
	freshIns := c.AIInsight.GetX(ctx, ins.ID)
	if diag.applyNoiseSuggestion(ctx, freshIns) {
		t.Error("重复 apply 同一 noise insight 应幂等返回 false（不重复建规则）")
	}
	n := c.SuppressionRule.Query().CountX(ctx)
	if n != 1 {
		t.Errorf("幂等：应仍只有 1 条规则, got %d", n)
	}
}

// TestApplyNoiseSuggestion_NoLabels_KeepsAccepted content.match_labels 为空时不建规则（保持 accepted）。
func TestApplyNoiseSuggestion_NoLabels_KeepsAccepted(t *testing.T) {
	c := newNoiseTestClient(t)
	ctx := context.Background()
	_, ins := seedNoiseInsight(t, c, map[string]string{}) // 空 match_labels

	diag := NewDiagnoseEngine(c, nil)
	got, err := diag.ResolveInsight(ctx, ins.ID, 7, true)
	if err != nil {
		t.Fatalf("ResolveInsight: %v", err)
	}
	if string(got.Status) != "accepted" {
		t.Errorf("无 match_labels 应保持 accepted（不谎报 applied）: got %q", got.Status)
	}
	if n := c.SuppressionRule.Query().CountX(ctx); n != 0 {
		t.Errorf("无 match_labels 不应建规则, got %d", n)
	}
}
