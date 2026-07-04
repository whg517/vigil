package runbook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/kevin/vigil/ent"
	entincident "github.com/kevin/vigil/ent/incident"
	entrunbook "github.com/kevin/vigil/ent/runbook"
	"github.com/kevin/vigil/ent/schema"
	domainevent "github.com/kevin/vigil/internal/event"
)

// suggestCall 捕获一次 RecordRunbookSuggested 调用（展示留痕）。
type suggestCall struct {
	incID        int
	runbookID    int
	runbookName  string
	triggerType  string
	autoRunnable bool
}

// fakeSuggestRecorder 实现 TriggerRecorder，捕获「展示」调用便于断言展示≠执行。
type fakeSuggestRecorder struct{ calls []suggestCall }

func (f *fakeSuggestRecorder) RecordRunbookSuggested(_ context.Context, incID, runbookID int, name, trigType string, autoRunnable bool) error {
	f.calls = append(f.calls, suggestCall{incID, runbookID, name, trigType, autoRunnable})
	return nil
}

// seedIncident 建一条 Incident，可选关联 Service、severity、labels（写入其聚合 Event）。
func seedIncident(t *testing.T, c *ent.Client, svc *ent.Service, severity string, labels map[string]string) *ent.Incident {
	t.Helper()
	ctx := context.Background()
	ib := c.Incident.Create().
		SetNumber("INC-" + severity + "-" + randSuffix(t)).
		SetTitle("t").
		SetSeverity(entincident.Severity(severity)).
		SetStatus(entincident.StatusTriggered)
	if svc != nil {
		ib.SetService(svc)
	}
	inc, err := ib.Save(ctx)
	if err != nil {
		t.Fatalf("create incident: %v", err)
	}
	if labels != nil {
		evt, err := c.Event.Create().
			SetSourceEventID("evt-" + inc.Number).
			SetSource("prometheus").
			SetSeverity("warning").
			SetStatus("firing").
			SetSummary("s").
			SetLabels(labels).
			SetDedupKey("dk-" + inc.Number).
			SetIncidentID(inc.ID).
			Save(ctx)
		if err != nil {
			t.Fatalf("create event: %v", err)
		}
		_ = evt
	}
	return inc
}

var suffixCounter int64

func randSuffix(t *testing.T) string {
	t.Helper()
	n := atomic.AddInt64(&suffixCounter, 1)
	return string(rune('a'+int(n%26))) + string(rune('a'+int((n/26)%26))) + string(rune('0'+int(n%10)))
}

// seedServiceWithRunbook 建 Team+Service 并关联一个 Runbook。
func seedServiceWithRunbook(t *testing.T, c *ent.Client, rb *ent.Runbook) *ent.Service {
	t.Helper()
	ctx := context.Background()
	team, err := c.Team.Create().SetName("pay").SetSlug("pay-" + randSuffix(t)).Save(ctx)
	if err != nil {
		t.Fatalf("create team: %v", err)
	}
	svc, err := c.Service.Create().
		SetName("payment-api").SetSlug("svc-" + randSuffix(t)).
		SetTeamID(team.ID).AddRunbooks(rb).Save(ctx)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	return svc
}

// mkRunbook 建一个带 trigger 的 Runbook（steps 决定是否只读，autoRun 决定是否配自动执行）。
func mkRunbook(t *testing.T, c *ent.Client, trigger map[string]any, autoRun bool, steps []schema.RunbookStep) *ent.Runbook {
	t.Helper()
	b := c.Runbook.Create().SetName("rb-" + randSuffix(t)).SetType(entrunbook.TypeExecutable).SetAutoRun(autoRun)
	if trigger != nil {
		b.SetTrigger(trigger)
	}
	if len(steps) > 0 {
		b.SetSteps(steps)
	}
	rb, err := b.Save(context.Background())
	if err != nil {
		t.Fatalf("create runbook: %v", err)
	}
	return rb
}

func readonlyStep(name, endpoint string) schema.RunbookStep {
	return schema.RunbookStep{
		ID: name, Name: name,
		Action:    schema.StepAction{Type: "diagnose", Target: schema.StepTarget{Kind: "http", Endpoint: endpoint, Readonly: true}},
		OnFailure: "continue",
	}
}

// writeStepAt 建一个指向真实 endpoint 的写步骤（用于证明写端点绝不被自动调用）。
// 与 engine_test.go 的 writeStep（固定 127.0.0.1:0 端点）区分：此处需真实可命中的地址。
func writeStepAt(name, endpoint string) schema.RunbookStep {
	return schema.RunbookStep{
		ID: name, Name: name,
		Action:          schema.StepAction{Type: "execute", Target: schema.StepTarget{Kind: "http", Endpoint: endpoint, Readonly: false}},
		OnFailure:       "continue",
		RequireApproval: true,
	}
}

// fireIncidentCreated 调用求值器，模拟 IncidentCreated 事件派发。
func fireIncidentCreated(ev *TriggerEvaluator, inc *ent.Incident) error {
	return ev.OnIncidentCreated(context.Background(), domainevent.Event{
		Type: domainevent.IncidentCreated, Incident: inc,
	})
}

// TestTrigger_OnIncident_Displays on_incident：建单即展示关联 Runbook（不执行）。
func TestTrigger_OnIncident_Displays(t *testing.T) {
	c := newTestClient(t)
	rb := mkRunbook(t, c, map[string]any{"type": "on_incident"}, false, []schema.RunbookStep{readonlyStep("s1", "http://x")})
	svc := seedServiceWithRunbook(t, c, rb)
	inc := seedIncident(t, c, svc, "warning", nil)

	rec := &fakeSuggestRecorder{}
	ev := NewTriggerEvaluator(c, NewEngine(c, newTestRegistry()), rec)
	if err := fireIncidentCreated(ev, inc); err != nil {
		t.Fatalf("OnIncidentCreated: %v", err)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("expected 1 suggested (display), got %d", len(rec.calls))
	}
	if rec.calls[0].runbookID != rb.ID || rec.calls[0].triggerType != "on_incident" {
		t.Errorf("unexpected display call: %+v", rec.calls[0])
	}
	// auto_run=false → autoRunnable=false（仅展示）
	if rec.calls[0].autoRunnable {
		t.Error("auto_run=false 应仅展示，autoRunnable 应为 false")
	}
}

// TestTrigger_OnSeverity_MatchAndMiss on_severity：达阈值才展示。
func TestTrigger_OnSeverity_MatchAndMiss(t *testing.T) {
	c := newTestClient(t)
	rb := mkRunbook(t, c, map[string]any{"type": "on_severity", "condition": "severity >= warning"}, false, nil)
	svc := seedServiceWithRunbook(t, c, rb)

	// critical ≥ warning → 展示
	rec := &fakeSuggestRecorder{}
	ev := NewTriggerEvaluator(c, nil, rec)
	if err := fireIncidentCreated(ev, seedIncident(t, c, svc, "critical", nil)); err != nil {
		t.Fatal(err)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("critical 应命中展示，got %d", len(rec.calls))
	}

	// info < warning → 不展示
	rec2 := &fakeSuggestRecorder{}
	ev2 := NewTriggerEvaluator(c, nil, rec2)
	if err := fireIncidentCreated(ev2, seedIncident(t, c, svc, "info", nil)); err != nil {
		t.Fatal(err)
	}
	if len(rec2.calls) != 0 {
		t.Fatalf("info 不应命中，got %d", len(rec2.calls))
	}
}

// TestTrigger_OnLabelMatch on_label_match：labels 子集匹配才展示。
func TestTrigger_OnLabelMatch(t *testing.T) {
	c := newTestClient(t)
	rb := mkRunbook(t, c, map[string]any{
		"type": "on_label_match", "labels": map[string]any{"service": "payment"},
	}, false, nil)
	svc := seedServiceWithRunbook(t, c, rb)

	// 匹配
	rec := &fakeSuggestRecorder{}
	ev := NewTriggerEvaluator(c, nil, rec)
	if err := fireIncidentCreated(ev, seedIncident(t, c, svc, "warning", map[string]string{"service": "payment"})); err != nil {
		t.Fatal(err)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("label 匹配应展示，got %d", len(rec.calls))
	}

	// 不匹配
	rec2 := &fakeSuggestRecorder{}
	ev2 := NewTriggerEvaluator(c, nil, rec2)
	if err := fireIncidentCreated(ev2, seedIncident(t, c, svc, "warning", map[string]string{"service": "order"})); err != nil {
		t.Fatal(err)
	}
	if len(rec2.calls) != 0 {
		t.Fatalf("label 不匹配不应展示，got %d", len(rec2.calls))
	}
}

// TestTrigger_AutoRun_ReadonlyExecutes auto_run + 全只读诊断 → 自动执行（诊断端点被调用）。
func TestTrigger_AutoRun_ReadonlyExecutes(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := newTestClient(t)
	rb := mkRunbook(t, c, map[string]any{"type": "on_incident"}, true, []schema.RunbookStep{readonlyStep("查日志", srv.URL)})
	svc := seedServiceWithRunbook(t, c, rb)
	inc := seedIncident(t, c, svc, "warning", nil)

	rec := &fakeSuggestRecorder{}
	ev := NewTriggerEvaluator(c, NewEngine(c, newTestRegistry()), rec)
	if err := fireIncidentCreated(ev, inc); err != nil {
		t.Fatal(err)
	}
	// 展示 + autoRunnable=true
	if len(rec.calls) != 1 || !rec.calls[0].autoRunnable {
		t.Fatalf("全只读 + auto_run 应展示且 autoRunnable=true，got %+v", rec.calls)
	}
	// 只读诊断端点被自动调用（自动执行生效）
	if atomic.LoadInt32(&hits) != 1 {
		t.Errorf("只读诊断步骤应被自动执行 1 次，got %d", hits)
	}
}

// TestTrigger_AutoRun_WriteStep_NeverExecutes ★★★ 安全红线锁定：
// 含写步骤的 Runbook 即使配 auto_run=true 也【绝不】自动执行，只展示。
func TestTrigger_AutoRun_WriteStep_NeverExecutes(t *testing.T) {
	var writeHits int32
	writeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&writeHits, 1) // 被调用 = 安全红线被击穿
		w.WriteHeader(http.StatusOK)
	}))
	defer writeSrv.Close()

	c := newTestClient(t)
	// 含一个只读步骤 + 一个写步骤，配 auto_run=true。
	rb := mkRunbook(t, c, map[string]any{"type": "on_incident"}, true, []schema.RunbookStep{
		writeStepAt("回滚", writeSrv.URL),
	})
	svc := seedServiceWithRunbook(t, c, rb)
	inc := seedIncident(t, c, svc, "critical", nil)

	rec := &fakeSuggestRecorder{}
	ev := NewTriggerEvaluator(c, NewEngine(c, newTestRegistry()), rec)
	if err := fireIncidentCreated(ev, inc); err != nil {
		t.Fatal(err)
	}
	// 仍展示（响应者仍需看到该 Runbook）
	if len(rec.calls) != 1 {
		t.Fatalf("含写步骤仍应展示，got %d", len(rec.calls))
	}
	// ★ 关键断言：autoRunnable 必须为 false（不满足全只读）
	if rec.calls[0].autoRunnable {
		t.Error("★ 安全红线：含写步骤即使 auto_run=true，autoRunnable 必须为 false")
	}
	// ★ 关键断言：写端点绝不被调用（自动执行绝不触发写操作）
	if atomic.LoadInt32(&writeHits) != 0 {
		t.Errorf("★ 安全红线被击穿：含写步骤的 Runbook 被自动执行，写端点被调用 %d 次", writeHits)
	}
}

// TestTrigger_AutoRun_WriteStep_EvenViaEngineGuard 双保险：即便绕过 autoRunnable 判定直接
// 让引擎跑该写步骤 Runbook（approved=false），写步骤仍被守卫跳过——展示≠执行的第二道防线。
func TestTrigger_AutoRun_WriteStep_EvenViaEngineGuard(t *testing.T) {
	var writeHits int32
	writeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&writeHits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer writeSrv.Close()

	c := newTestClient(t)
	rb := createExecRunbook(t, c, []schema.RunbookStep{writeStepAt("回滚", writeSrv.URL)})
	eng := NewEngine(c, newTestRegistry())
	// 模拟自动执行路径：approved=false, actorID=0
	res, err := eng.Execute(context.Background(), rb.ID, 0, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !res.PendingApproval || !res.Steps[0].Skipped {
		t.Errorf("approved=false 写步骤应被跳过并标记待审批，got %+v", res)
	}
	if atomic.LoadInt32(&writeHits) != 0 {
		t.Errorf("写端点绝不应被调用，got %d", writeHits)
	}
}

// TestTrigger_NoService_NoDisplay 未路由（无 Service）→ 无可展示对象，静默。
func TestTrigger_NoService_NoDisplay(t *testing.T) {
	c := newTestClient(t)
	inc := seedIncident(t, c, nil, "critical", nil) // 无 Service
	rec := &fakeSuggestRecorder{}
	ev := NewTriggerEvaluator(c, nil, rec)
	if err := fireIncidentCreated(ev, inc); err != nil {
		t.Fatal(err)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("无 Service 不应展示，got %d", len(rec.calls))
	}
}

// TestTrigger_Manual_NoDisplay manual trigger 不自动触发展示。
func TestTrigger_Manual_NoDisplay(t *testing.T) {
	c := newTestClient(t)
	rb := mkRunbook(t, c, map[string]any{"type": "manual"}, false, nil)
	svc := seedServiceWithRunbook(t, c, rb)
	inc := seedIncident(t, c, svc, "critical", nil)
	rec := &fakeSuggestRecorder{}
	ev := NewTriggerEvaluator(c, nil, rec)
	if err := fireIncidentCreated(ev, inc); err != nil {
		t.Fatal(err)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("manual trigger 不应自动展示，got %d", len(rec.calls))
	}
}
