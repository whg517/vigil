package runbook

import (
	"context"
	"net/http"
	"net/http/httptest"
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
	eng := NewEngine(c, NewRegistry())

	// approved=false，但只读步骤应执行
	res, err := eng.Execute(context.Background(), rb.ID, 0, false)
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
	eng := NewEngine(c, NewRegistry())

	// approved=false —— 写动作应被跳过，writeCalled 必须为 false
	res, err := eng.Execute(context.Background(), rb.ID, 0, false)
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
	res2, _ := eng.Execute(context.Background(), rb.ID, 0, true)
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
	eng := NewEngine(c, NewRegistry())

	// approved=false：写操作必须 skip（C4 修复后与 RequireApproval 标志解耦）
	res, err := eng.Execute(context.Background(), rb.ID, 0, false)
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
	eng := NewEngine(c, NewRegistry())

	res, _ := eng.Execute(context.Background(), rb.ID, 0, true)
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
	eng := NewEngine(c, NewRegistry())

	res, _ := eng.Execute(context.Background(), rb.ID, 0, true)
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
	eng := NewEngine(c, NewRegistry())

	res, err := eng.Execute(context.Background(), rb.ID, 0, true)
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
