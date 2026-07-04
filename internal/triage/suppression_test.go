package triage

import (
	"context"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/event"
	"github.com/kevin/vigil/ent/suppressionrule"
)

// createSuppressEvent 建一个 Event（firing/指定 severity，labels 可定制）。
func createSuppressEvent(t *testing.T, c *ent.Client, severity event.Severity, dedupKey string, labels map[string]string) *ent.Event {
	t.Helper()
	if labels == nil {
		labels = map[string]string{"service": "payment"}
	}
	evt, err := c.Event.Create().
		SetSourceEventID(dedupKey).
		SetSource("prometheus").
		SetSeverity(severity).
		SetStatus(event.StatusFiring).
		SetSummary("告警 " + dedupKey).
		SetLabels(labels).
		SetDedupKey(dedupKey).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	return evt
}

// makeRule 建一条抑制规则（启用），可选 time_window。
func makeRule(t *testing.T, c *ent.Client, name string, labels map[string]string, action suppressionrule.Action, opts ...func(*ent.SuppressionRuleCreate)) *ent.SuppressionRule {
	t.Helper()
	b := c.SuppressionRule.Create().
		SetName(name).
		SetMatchLabels(labels).
		SetAction(action).
		SetEnabled(true)
	for _, opt := range opts {
		opt(b)
	}
	r, err := b.Save(context.Background())
	if err != nil {
		t.Fatalf("create rule: %v", err)
	}
	return r
}

// TestEvaluate_NoMatchEnabledRules 规则的 label 不全等匹配时不命中。
func TestEvaluate_NoMatchEnabledRules(t *testing.T) {
	c := newTestClient(t)
	makeRule(t, c, "r1", map[string]string{"env": "maintenance"}, suppressionrule.ActionSuppress)
	se := NewSuppressionEngine(c)
	evt := createSuppressEvent(t, c, event.SeverityWarning, "d1", map[string]string{"service": "payment"})
	out, err := se.Evaluate(context.Background(), evt)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if out.Matched {
		t.Error("label 不匹配不应命中")
	}
}

// TestEvaluate_SuppressMatch label 全等匹配且 preserve_critical=false（warning）→ 命中 suppress。
func TestEvaluate_SuppressMatch(t *testing.T) {
	c := newTestClient(t)
	makeRule(t, c, "维护窗口", map[string]string{"env": "maintenance"}, suppressionrule.ActionSuppress)
	se := NewSuppressionEngine(c)
	evt := createSuppressEvent(t, c, event.SeverityWarning, "d1", map[string]string{"env": "maintenance"})
	out, err := se.Evaluate(context.Background(), evt)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !out.Matched || out.Action != SuppressActionSuppress {
		t.Errorf("应命中 suppress，got %+v", out)
	}
	if out.RuleName != "维护窗口" {
		t.Errorf("rule name: got %q", out.RuleName)
	}
}

// TestEvaluate_PreserveCritical critical + preserve_critical → suppress 规则被跳过。
func TestEvaluate_PreserveCritical(t *testing.T) {
	c := newTestClient(t)
	makeRule(t, c, "r", map[string]string{"env": "m"}, suppressionrule.ActionSuppress) // preserve_critical 默认 true
	se := NewSuppressionEngine(c)
	evt := createSuppressEvent(t, c, event.SeverityCritical, "d1", map[string]string{"env": "m"})
	out, err := se.Evaluate(context.Background(), evt)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	// critical 被保护，不抑制
	if out.Matched {
		t.Errorf("critical 应被 preserve_critical 保护，不抑制；got %+v", out)
	}
}

// TestEvaluate_PreserveCriticalDisabled preserve_critical=false 时 critical 也被抑制。
func TestEvaluate_PreserveCriticalDisabled(t *testing.T) {
	c := newTestClient(t)
	makeRule(t, c, "r", map[string]string{"env": "m"}, suppressionrule.ActionSuppress,
		func(b *ent.SuppressionRuleCreate) { b.SetPreserveCritical(false) })
	se := NewSuppressionEngine(c)
	evt := createSuppressEvent(t, c, event.SeverityCritical, "d1", map[string]string{"env": "m"})
	out, err := se.Evaluate(context.Background(), evt)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !out.Matched || out.Action != SuppressActionSuppress {
		t.Errorf("preserve_critical=false 时 critical 应可抑制，got %+v", out)
	}
}

// TestEvaluate_ReduceSeverity critical + reduce_severity + preserve_critical → 跳过；
// warning → 命中降级。
func TestEvaluate_ReduceSeverity(t *testing.T) {
	c := newTestClient(t)
	makeRule(t, c, "r", map[string]string{"env": "m"}, suppressionrule.ActionReduceSeverity,
		func(b *ent.SuppressionRuleCreate) { b.SetReduceTo("info") })
	se := NewSuppressionEngine(c)

	// critical 被 preserve_critical 保护，跳过
	evtC := createSuppressEvent(t, c, event.SeverityCritical, "dc", map[string]string{"env": "m"})
	outC, err := se.Evaluate(context.Background(), evtC)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if outC.Matched {
		t.Errorf("critical 不应被 reduce（preserve_critical），got %+v", outC)
	}

	// warning 命中降级
	evtW := createSuppressEvent(t, c, event.SeverityWarning, "dw", map[string]string{"env": "m"})
	outW, err := se.Evaluate(context.Background(), evtW)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !outW.Matched || outW.Action != SuppressActionReduceSeverity || outW.ReduceTo != "info" {
		t.Errorf("warning 应命中降级，got %+v", outW)
	}
}

// TestApply_SuppressMarksNoise Apply suppress 后 Event.is_noise=true。
func TestApply_SuppressMarksNoise(t *testing.T) {
	c := newTestClient(t)
	r := makeRule(t, c, "r", map[string]string{"env": "m"}, suppressionrule.ActionSuppress)
	se := NewSuppressionEngine(c)
	evt := createSuppressEvent(t, c, event.SeverityInfo, "d1", map[string]string{"env": "m"})
	out := &SuppressionOutcome{Matched: true, RuleID: r.ID, Action: SuppressActionSuppress}
	updated, err := se.Apply(context.Background(), evt, out)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !updated.IsNoise {
		t.Error("suppress 后 is_noise 应为 true")
	}
	// DB 持久化
	reloaded, _ := c.Event.Get(context.Background(), evt.ID)
	if !reloaded.IsNoise {
		t.Error("DB 中 is_noise 应为 true")
	}
}

// TestApply_ReduceSeverityUpdatesDB Apply reduce_severity 后 DB severity 更新。
func TestApply_ReduceSeverityUpdatesDB(t *testing.T) {
	c := newTestClient(t)
	se := NewSuppressionEngine(c)
	evt := createSuppressEvent(t, c, event.SeverityWarning, "d1", map[string]string{"env": "m"})
	out := &SuppressionOutcome{Matched: true, Action: SuppressActionReduceSeverity, ReduceTo: "info"}
	if _, err := se.Apply(context.Background(), evt, out); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	reloaded, _ := c.Event.Get(context.Background(), evt.ID)
	if reloaded.Severity != event.SeverityInfo {
		t.Errorf("DB severity: got %s, want info", reloaded.Severity)
	}
}

// TestMatchRule_TimeWindow 时间窗外不命中。
func TestMatchRule_TimeWindow(t *testing.T) {
	c := newTestClient(t)
	pastStart := time.Now().Add(-2 * time.Hour).Format(time.RFC3339)
	pastEnd := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
	makeRule(t, c, "过期窗口", map[string]string{"env": "m"}, suppressionrule.ActionSuppress,
		func(b *ent.SuppressionRuleCreate) {
			b.SetTimeWindow(map[string]any{"start": pastStart, "end": pastEnd})
		})
	se := NewSuppressionEngine(c)
	evt := createSuppressEvent(t, c, event.SeverityWarning, "d1", map[string]string{"env": "m"})
	out, err := se.Evaluate(context.Background(), evt)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if out.Matched {
		t.Error("时间窗外不应命中")
	}
}

// TestMatchRule_TimeWindowInside 时间窗内命中。
func TestMatchRule_TimeWindowInside(t *testing.T) {
	c := newTestClient(t)
	futureStart := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
	futureEnd := time.Now().Add(1 * time.Hour).Format(time.RFC3339)
	makeRule(t, c, "活跃窗口", map[string]string{"env": "m"}, suppressionrule.ActionSuppress,
		func(b *ent.SuppressionRuleCreate) {
			b.SetTimeWindow(map[string]any{"start": futureStart, "end": futureEnd})
		})
	se := NewSuppressionEngine(c)
	evt := createSuppressEvent(t, c, event.SeverityWarning, "d1", map[string]string{"env": "m"})
	out, err := se.Evaluate(context.Background(), evt)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !out.Matched {
		t.Error("时间窗内应命中")
	}
}

// TestMatchRule_SeverityFilter severity_filter 不含当前严重度时不命中。
func TestMatchRule_SeverityFilter(t *testing.T) {
	c := newTestClient(t)
	makeRule(t, c, "r", map[string]string{"env": "m"}, suppressionrule.ActionSuppress,
		func(b *ent.SuppressionRuleCreate) { b.SetSeverityFilter([]string{"info"}) })
	se := NewSuppressionEngine(c)
	// warning 不在 filter 内
	evt := createSuppressEvent(t, c, event.SeverityWarning, "d1", map[string]string{"env": "m"})
	out, _ := se.Evaluate(context.Background(), evt)
	if out.Matched {
		t.Error("severity_filter=[info] 不应命中 warning")
	}
	// info 命中
	evt2 := createSuppressEvent(t, c, event.SeverityInfo, "d2", map[string]string{"env": "m"})
	out2, _ := se.Evaluate(context.Background(), evt2)
	if !out2.Matched {
		t.Error("severity_filter=[info] 应命中 info")
	}
}

// TestNormalizeSeverity 降级归一化：不升 severity，无目标按级降。
func TestNormalizeSeverity(t *testing.T) {
	cases := []struct {
		reduceTo string
		current  event.Severity
		want     string
	}{
		{"info", event.SeverityWarning, "info"},        // 指定 info → info
		{"warning", event.SeverityCritical, "warning"}, // critical→warning
		{"critical", event.SeverityWarning, "warning"}, // 指定 critical 但当前 warning → 不升，保持
		{"", event.SeverityCritical, "warning"},        // 无目标 → critical→warning
		{"", event.SeverityWarning, "info"},            // 无目标 → warning→info
		{"", event.SeverityInfo, "info"},               // info 已最低
	}
	for _, tc := range cases {
		got := normalizeSeverity(tc.reduceTo, tc.current)
		if got != tc.want {
			t.Errorf("normalizeSeverity(%q,%s): got %q, want %q", tc.reduceTo, tc.current, got, tc.want)
		}
	}
}

// TestEvaluate_ExpiredRuleSkipped B15：过期规则（expires_at < now）不命中，即使 enabled。
func TestEvaluate_ExpiredRuleSkipped(t *testing.T) {
	c := newTestClient(t)
	past := time.Now().Add(-time.Hour)
	makeRule(t, c, "expired", map[string]string{"service": "payment"}, suppressionrule.ActionSuppress,
		func(b *ent.SuppressionRuleCreate) { b.SetExpiresAt(past) })
	se := NewSuppressionEngine(c)
	evt := createSuppressEvent(t, c, event.SeverityWarning, "d1", map[string]string{"service": "payment"})
	out, err := se.Evaluate(context.Background(), evt)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if out.Matched {
		t.Errorf("过期规则不应命中，got %+v", out)
	}
}

// TestEvaluate_FutureExpiryStillMatches B15：未到期规则（expires_at > now）正常命中。
func TestEvaluate_FutureExpiryStillMatches(t *testing.T) {
	c := newTestClient(t)
	future := time.Now().Add(time.Hour)
	makeRule(t, c, "future", map[string]string{"service": "payment"}, suppressionrule.ActionSuppress,
		func(b *ent.SuppressionRuleCreate) { b.SetExpiresAt(future) })
	se := NewSuppressionEngine(c)
	evt := createSuppressEvent(t, c, event.SeverityWarning, "d1", map[string]string{"service": "payment"})
	out, err := se.Evaluate(context.Background(), evt)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !out.Matched {
		t.Errorf("未到期规则应命中，got %+v", out)
	}
}

// TestEvaluate_MostSpecificRuleWins B15：多规则命中时 match_labels 多的（更具体）优先。
func TestEvaluate_MostSpecificRuleWins(t *testing.T) {
	c := newTestClient(t)
	// 宽松规则：只匹配 service=payment（1 标签），reduce_severity。
	makeRule(t, c, "broad", map[string]string{"service": "payment"}, suppressionrule.ActionReduceSeverity)
	// 具体规则：service=payment + env=prod（2 标签），suppress。
	makeRule(t, c, "specific", map[string]string{"service": "payment", "env": "prod"}, suppressionrule.ActionSuppress)
	se := NewSuppressionEngine(c)
	// Event 两条都满足 → 命中更具体的 specific（suppress）。
	evt := createSuppressEvent(t, c, event.SeverityWarning, "d1", map[string]string{"service": "payment", "env": "prod"})
	out, err := se.Evaluate(context.Background(), evt)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !out.Matched || out.RuleName != "specific" {
		t.Errorf("应命中更具体的 specific 规则，got %+v", out)
	}
}

// TestEngine_ProcessSuppressed 集成：Process 命中 suppress 返回 ActionSuppressed、不入 Incident。
func TestEngine_ProcessSuppressed(t *testing.T) {
	c := newTestClient(t)
	seedServiceAndTeam(t, c) // payment service 存在
	makeRule(t, c, "r", map[string]string{"service": "payment"}, suppressionrule.ActionSuppress,
		func(b *ent.SuppressionRuleCreate) { b.SetSeverityFilter([]string{"info"}) })

	eng := NewEngine(c, nil)
	evt := createSuppressEvent(t, c, event.SeverityInfo, "d_suppress", nil)
	res, err := eng.Process(context.Background(), evt.ID)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.Action != ActionSuppressed || !res.Suppressed {
		t.Errorf("应 ActionSuppressed，got %+v", res)
	}
	// 不应创建 Incident
	count, _ := c.Incident.Query().Count(context.Background())
	if count != 0 {
		t.Errorf("suppress 不应创建 Incident，got %d", count)
	}
}
