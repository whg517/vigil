package runbook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/runbook"
	"github.com/kevin/vigil/ent/schema"

	_ "github.com/mattn/go-sqlite3"
)

func newTestClient(t *testing.T) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:rb_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// createExecRunbook 建一个可执行 Runbook，含指定步骤。
func createExecRunbook(t *testing.T, c *ent.Client, steps []schema.RunbookStep) *ent.Runbook {
	t.Helper()
	rb, err := c.Runbook.Create().
		SetName("test-rb").
		SetType(runbook.TypeExecutable).
		SetSteps(steps).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create runbook: %v", err)
	}
	return rb
}

// TestExecute_DiagnoseReadOnly 验证只读诊断步骤无确认即可执行。
func TestExecute_DiagnoseReadOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"logs":"ok"}`))
	}))
	defer srv.Close()

	c := newTestClient(t)
	rb := createExecRunbook(t, c, []schema.RunbookStep{
		{
			ID: "s1", Name: "查日志",
			Action: schema.StepAction{
				Type:   "diagnose",
				Target: schema.StepTarget{Kind: "http", Endpoint: srv.URL, Readonly: true},
			},
			OnFailure: "continue",
		},
	})
	eng := NewEngine(c, newTestRegistry())

	// approved=false，但只读步骤应执行
	res, err := eng.Execute(context.Background(), rb.ID, 0, false, 0)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Steps) != 1 || !res.Steps[0].Success {
		t.Fatalf("expected 1 success step, got %+v", res.Steps)
	}
	if res.Steps[0].Output == "" {
		t.Error("output should not be empty")
	}
}

// TestExecute_WriteRequiresApproval 验证写动作未确认时被跳过（核心安全控制）。
func TestExecute_WriteRequiresApproval(t *testing.T) {
	writeCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeCalled = true // 写端点被调用 = 安全控制失败
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(t)
	rb := createExecRunbook(t, c, []schema.RunbookStep{
		{
			ID: "s1", Name: "回滚",
			Action: schema.StepAction{
				Type:   "execute", // 写操作
				Target: schema.StepTarget{Kind: "http", Endpoint: srv.URL, Readonly: false},
			},
			RequireApproval: true, // 必须人确认
			OnFailure:       "continue",
		},
	})
	eng := NewEngine(c, newTestRegistry())

	// approved=false —— 写动作应被跳过，writeCalled 必须为 false
	res, err := eng.Execute(context.Background(), rb.ID, 0, false, 0)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if writeCalled {
		t.Fatal("写端点不应被调用：require_approval 未确认却执行了写操作（安全漏洞）")
	}
	if len(res.Steps) != 1 || !res.Steps[0].Skipped {
		t.Fatalf("expected step skipped, got %+v", res.Steps)
	}

	// approved=true —— 写动作应执行
	writeCalled = false
	res2, _ := eng.Execute(context.Background(), rb.ID, 0, true, 0)
	if !writeCalled {
		t.Error("approved=true 时写端点应被调用")
	}
	if !res2.Steps[0].Success {
		t.Error("approved=true 时写步骤应成功")
	}
}

// TestExecute_WriteBypass_ConfigNoApproval QA 审计 C4：写步骤即便配置成
// RequireApproval=false（用户误配/恶意配置），approved=false 时也必须 skip。
// 旧实现用 `!Readonly && RequireApproval && !approved`（AND）→ 此场景会绕过审批直接执行。
func TestExecute_WriteBypass_ConfigNoApproval(t *testing.T) {
	writeCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(t)
	rb := createExecRunbook(t, c, []schema.RunbookStep{
		{
			ID: "s1", Name: "扩容",
			Action: schema.StepAction{
				Type:   "execute",
				Target: schema.StepTarget{Kind: "http", Endpoint: srv.URL, Readonly: false},
			},
			RequireApproval: false, // ★ 故意配置成"不需确认"——旧逻辑会被绕过
			OnFailure:       "continue",
		},
	})
	eng := NewEngine(c, newTestRegistry())

	// approved=false：写操作必须 skip（C4 修复后与 RequireApproval 标志解耦）
	res, err := eng.Execute(context.Background(), rb.ID, 0, false, 0)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if writeCalled {
		t.Fatal("写端点不应被调用：写操作必须 approved（即使 RequireApproval=false，C4 安全红线）")
	}
	if len(res.Steps) != 1 || !res.Steps[0].Skipped {
		t.Fatalf("expected step skipped (write without approval), got %+v", res.Steps)
	}
}

// TestExecute_FailedStepKeepsStructuredOutput 强化 FIX-E：某步执行失败（HTTP≥400）时，
// StepResult 除 Error 外还应保留执行器返回的结构化 Output（含 status_code/body），
// 否则前端只看到一句 "http 500" 无从定位。
func TestExecute_FailedStepKeepsStructuredOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"msg":"boom"}`))
	}))
	defer srv.Close()

	c := newTestClient(t)
	rb := createExecRunbook(t, c, []schema.RunbookStep{
		{
			ID: "s1", Name: "查依赖服务",
			Action: schema.StepAction{
				Type:   "diagnose",
				Target: schema.StepTarget{Kind: "http", Endpoint: srv.URL, Readonly: true},
			},
			OnFailure: "continue",
		},
	})
	eng := NewEngine(c, newTestRegistry())

	res, err := eng.Execute(context.Background(), rb.ID, 0, true, 0)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(res.Steps))
	}
	sr := res.Steps[0]
	if sr.Success {
		t.Fatal("HTTP 500 步骤不应标记为 Success")
	}
	if sr.Error == "" {
		t.Error("失败步骤应有 Error")
	}
	// ★ 核心断言：失败分支必须保留结构化 Output，且含 status_code
	if sr.Output == "" {
		t.Fatal("FIX-E: 失败步骤 Output 不应为空（丢了状态码/响应体诊断信息）")
	}
	if !strings.Contains(sr.Output, `"status_code":500`) {
		t.Errorf("FIX-E: Output 应含 status_code:500，got %q", sr.Output)
	}
}

// TestExecute_OnFailureAbort 验证 abort 中止后续步骤。
func TestExecute_OnFailureAbort(t *testing.T) {
	c := newTestClient(t)
	rb := createExecRunbook(t, c, []schema.RunbookStep{
		{
			ID: "s1", Name: "失败步骤",
			Action: schema.StepAction{
				Type:   "diagnose",
				Target: schema.StepTarget{Kind: "http", Endpoint: "http://127.0.0.1:0/unreachable", Readonly: true},
			},
			OnFailure: "abort",
		},
		{
			ID: "s2", Name: "不应执行",
			Action: schema.StepAction{
				Type:   "diagnose",
				Target: schema.StepTarget{Kind: "internal", Endpoint: "ok", Readonly: true},
			},
			OnFailure: "continue",
		},
	})
	eng := NewEngine(c, newTestRegistry())

	res, _ := eng.Execute(context.Background(), rb.ID, 0, true, 0)
	if !res.Aborted {
		t.Error("应因 abort 中止")
	}
	if len(res.Steps) != 1 {
		t.Errorf("abort 后应只执行了 1 步，got %d", len(res.Steps))
	}
}

// TestExecute_OnFailureContinue 验证 continue 继续后续步骤。
func TestExecute_OnFailureContinue(t *testing.T) {
	c := newTestClient(t)
	rb := createExecRunbook(t, c, []schema.RunbookStep{
		{
			ID: "s1", Name: "失败步骤",
			Action: schema.StepAction{
				Type:   "diagnose",
				Target: schema.StepTarget{Kind: "http", Endpoint: "http://127.0.0.1:0/x", Readonly: true},
			},
			OnFailure: "continue",
		},
		{
			ID: "s2", Name: "后续步骤",
			Action: schema.StepAction{
				Type:   "diagnose",
				Target: schema.StepTarget{Kind: "internal", Endpoint: "ok", Readonly: true},
			},
			OnFailure: "continue",
		},
	})
	eng := NewEngine(c, newTestRegistry())

	res, _ := eng.Execute(context.Background(), rb.ID, 0, true, 0)
	if res.Aborted {
		t.Error("continue 不应中止")
	}
	if len(res.Steps) != 2 {
		t.Errorf("应执行了 2 步，got %d", len(res.Steps))
	}
	if !res.Steps[1].Success {
		t.Error("第二步应成功")
	}
}

// TestExecute_DocumentRunbook 验证文档式 runbook 无步骤直接返回。
func TestExecute_DocumentRunbook(t *testing.T) {
	c := newTestClient(t)
	rb, _ := c.Runbook.Create().
		SetName("doc").
		SetType(runbook.TypeDocument).
		SetContentMarkdown("## 处置步骤\n1. ...").
		Save(context.Background())
	eng := NewEngine(c, newTestRegistry())

	res, err := eng.Execute(context.Background(), rb.ID, 0, true, 0)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Steps) != 0 {
		t.Errorf("文档式应无步骤执行，got %d", len(res.Steps))
	}
}

// TestIsReadOnly 判断 runbook 是否全只读。
func TestIsReadOnly(t *testing.T) {
	c := newTestClient(t)

	// 全只读
	rbRO, _ := c.Runbook.Create().SetName("ro").SetType(runbook.TypeExecutable).
		SetSteps([]schema.RunbookStep{
			{ID: "s1", Action: schema.StepAction{Target: schema.StepTarget{Kind: "http", Readonly: true}}},
		}).Save(context.Background())
	if !IsReadOnly(rbRO) {
		t.Error("全只读 runbook 应判定为 readonly")
	}

	// 含写操作
	rbRW, _ := c.Runbook.Create().SetName("rw").SetType(runbook.TypeExecutable).
		SetSteps([]schema.RunbookStep{
			{ID: "s1", Action: schema.StepAction{Target: schema.StepTarget{Kind: "http", Readonly: false}}},
		}).Save(context.Background())
	if IsReadOnly(rbRW) {
		t.Error("含写操作的 runbook 不应判定为 readonly")
	}
}

// TestRegistry 验证执行器注册表。
func TestRegistry(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Get("http"); !ok {
		t.Error("http executor not registered")
	}
	if _, ok := r.Get("internal"); !ok {
		t.Error("internal executor not registered")
	}
}

// —— 写审批闸门：阻断 + on_failure + actor 留痕（安全红线，QA 审计 S10/C14）——

// recCall 记录一次时间线调用（供 fakeTimeline 断言）。
type recCall struct {
	kind     string // "exec" | "blocked"
	step     string
	success  bool
	approved bool
	actorID  int
}

// fakeTimeline 实现 TimelineRecorder，捕获调用便于断言 actor 透传。
type fakeTimeline struct{ calls []recCall }

func (f *fakeTimeline) RecordRunbook(_ context.Context, _ int, stepName, _ string, success, approved bool, actorID int) error {
	f.calls = append(f.calls, recCall{kind: "exec", step: stepName, success: success, approved: approved, actorID: actorID})
	return nil
}
func (f *fakeTimeline) RecordRunbookBlocked(_ context.Context, _ int, stepName string, actorID int) error {
	f.calls = append(f.calls, recCall{kind: "blocked", step: stepName, actorID: actorID})
	return nil
}

// fakeEscalator 实现 EscalationTrigger，捕获 escalate 是否触发及 actor 透传。
type fakeEscalator struct {
	called  bool
	actorID int
}

func (f *fakeEscalator) Trigger(_ context.Context, _ int, _ string, actorID int) error {
	f.called = true
	f.actorID = actorID
	return nil
}

// writeStep 建一个写步骤（Readonly=false + require_approval），指定 on_failure。
func writeStep(id, name, onFailure string) schema.RunbookStep {
	return schema.RunbookStep{
		ID: id, Name: name,
		Action: schema.StepAction{
			Type:   "execute",
			Target: schema.StepTarget{Kind: "http", Endpoint: "http://127.0.0.1:0/x", Readonly: false},
		},
		RequireApproval: true,
		OnFailure:       onFailure,
	}
}

// TestExecute_WriteBlocked_OnFailureAbort 未获审批的写步骤 on_failure=abort → 整体中止，后续步骤不执行。
func TestExecute_WriteBlocked_OnFailureAbort(t *testing.T) {
	c := newTestClient(t)
	rb := createExecRunbook(t, c, []schema.RunbookStep{
		writeStep("s1", "回滚", "abort"),
		{ID: "s2", Name: "不应执行", Action: schema.StepAction{
			Type: "diagnose", Target: schema.StepTarget{Kind: "internal", Endpoint: "ok", Readonly: true},
		}, OnFailure: "continue"},
	})
	eng := NewEngine(c, newTestRegistry())

	res, _ := eng.Execute(context.Background(), rb.ID, 0, false, 0)
	if !res.PendingApproval {
		t.Error("应标记 PendingApproval（存在被阻断的写步骤）")
	}
	if !res.Aborted {
		t.Error("on_failure=abort 的写步骤未获审批应中止")
	}
	if len(res.Steps) != 1 || !res.Steps[0].Skipped {
		t.Fatalf("应仅执行 1 步且被 skip，got %+v", res.Steps)
	}
}

// TestExecute_WriteBlocked_OnFailureEscalate 未获审批 + on_failure=escalate → 中止并触发升级（actor 透传）。
func TestExecute_WriteBlocked_OnFailureEscalate(t *testing.T) {
	c := newTestClient(t)
	rb := createExecRunbook(t, c, []schema.RunbookStep{writeStep("s1", "回滚", "escalate")})
	eng := NewEngine(c, newTestRegistry())
	esc := &fakeEscalator{}
	eng.SetEscalationTrigger(esc)

	res, _ := eng.Execute(context.Background(), rb.ID, 99, false, 42)
	if !res.Aborted || !res.PendingApproval {
		t.Fatalf("应中止且待审批，got %+v", res)
	}
	if !esc.called {
		t.Error("on_failure=escalate 未获审批应触发升级")
	}
	if esc.actorID != 42 {
		t.Errorf("升级 actorID = %d, want 42（发起人透传）", esc.actorID)
	}
}

// TestExecute_WriteBlocked_OnFailureContinue 未获审批 + on_failure=continue → 跳过继续（合法干跑），并留痕 actor。
func TestExecute_WriteBlocked_OnFailureContinue(t *testing.T) {
	c := newTestClient(t)
	rb := createExecRunbook(t, c, []schema.RunbookStep{
		writeStep("s1", "回滚", "continue"),
		{ID: "s2", Name: "只读诊断", Action: schema.StepAction{
			Type: "diagnose", Target: schema.StepTarget{Kind: "internal", Endpoint: "ok", Readonly: true},
		}, OnFailure: "continue"},
	})
	eng := NewEngine(c, newTestRegistry())
	tl := &fakeTimeline{}
	eng.SetTimelineRecorder(tl)

	res, _ := eng.Execute(context.Background(), rb.ID, 77, false, 42)
	if res.Aborted {
		t.Error("on_failure=continue 不应中止（干跑）")
	}
	if !res.PendingApproval {
		t.Error("应标记 PendingApproval")
	}
	if len(res.Steps) != 2 || !res.Steps[0].Skipped || !res.Steps[1].Success {
		t.Fatalf("干跑应 skip 写步、执行只读步，got %+v", res.Steps)
	}
	// 时间线：写步骤记 blocked（含 actor）、只读步骤记 exec（含 actor）
	if len(tl.calls) != 2 {
		t.Fatalf("应记 2 条时间线，got %+v", tl.calls)
	}
	if tl.calls[0].kind != "blocked" || tl.calls[0].actorID != 42 {
		t.Errorf("首条应为 blocked 且 actorID=42，got %+v", tl.calls[0])
	}
	if tl.calls[1].kind != "exec" || tl.calls[1].actorID != 42 {
		t.Errorf("次条应为 exec 且 actorID=42，got %+v", tl.calls[1])
	}
}

// newTestRegistry 创建允许私网地址的测试用 registry（httptest 绑定 127.0.0.1）。
// 生产用 NewRegistry（AllowPrivate=false，SSRF 防护生效）。
func newTestRegistry() *Registry {
	r := &Registry{executors: make(map[string]Executor)}
	// 复用生产构造函数拿默认 client，再开 AllowPrivate（测试需要打 httptest 的 127.0.0.1）
	hc := NewHTTPExecutor()
	hc.SetAllowPrivate(true)
	r.Register(hc)
	ic := NewInternalExecutor()
	ic.SetAllowPrivate(true)
	r.Register(ic)
	return r
}
