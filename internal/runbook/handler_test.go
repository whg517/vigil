// handler_test.go Runbook handler 数据层兜底校验（QA 审计 C4）+ 执行审计（S10/C14）。
package runbook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/kevin/vigil/ent/auditlog"
	"github.com/kevin/vigil/ent/runbook"
	"github.com/kevin/vigil/ent/schema"
	"github.com/kevin/vigil/internal/auth"

	"github.com/labstack/echo/v5"
	_ "github.com/mattn/go-sqlite3"
)

// TestValidateSteps_ReadOnlyOk 只读步骤不要求 require_approval。
func TestValidateSteps_ReadOnlyOk(t *testing.T) {
	steps := []schema.RunbookStep{
		{ID: "s1", Name: "诊断", Action: schema.StepAction{
			Type:   "diagnose",
			Target: schema.StepTarget{Kind: "http", Readonly: true},
		}, RequireApproval: false},
	}
	if err := validateSteps(steps); err != nil {
		t.Errorf("readonly step should pass without require_approval, got: %v", err)
	}
}

// TestValidateSteps_WriteRequiresApproval 写步骤（Readonly=false）必须 RequireApproval=true。
// 这是 C4 数据层兜底：防通过 API 配置成"写操作不需确认"绕过 engine 的强制 approved。
func TestValidateSteps_WriteRequiresApproval(t *testing.T) {
	steps := []schema.RunbookStep{
		{ID: "s1", Name: "回滚", Action: schema.StepAction{
			Type:   "execute",
			Target: schema.StepTarget{Kind: "http", Readonly: false}, // 写操作
		}, RequireApproval: false}, // ★ 不要求确认 → 必须拒绝
	}
	err := validateSteps(steps)
	if err == nil {
		t.Fatal("write step with require_approval=false should be rejected")
	}
}

// TestValidateSteps_WriteWithApprovalOk 写步骤 + RequireApproval=true → 通过。
func TestValidateSteps_WriteWithApprovalOk(t *testing.T) {
	steps := []schema.RunbookStep{
		{ID: "s1", Name: "扩容", Action: schema.StepAction{
			Type:   "execute",
			Target: schema.StepTarget{Kind: "http", Readonly: false},
		}, RequireApproval: true},
	}
	if err := validateSteps(steps); err != nil {
		t.Errorf("write step with require_approval=true should pass, got: %v", err)
	}
}

// TestExecute_Audited 触发执行 → 落一条 runbook.execute 审计（S10/C14）。
// 用只读诊断步骤（无需真实外接），走完整 HTTP execute 链路，断言审计含 who/runbook/approved。
func TestExecute_Audited(t *testing.T) {
	c := newTestClient(t)
	rb, err := c.Runbook.Create().
		SetName("诊断簿").
		SetType(runbook.TypeExecutable).
		SetSteps([]schema.RunbookStep{
			{ID: "s1", Name: "查日志", Action: schema.StepAction{
				// notify 类型为 no-op，无需外接执行器，专测审计落库。
				Type:   "notify",
				Target: schema.StepTarget{Kind: "http", Readonly: true},
			}, OnFailure: "continue"},
		}).
		Save(context.Background())
	if err != nil {
		t.Fatalf("seed runbook: %v", err)
	}

	h := NewHandler(c, NewEngine(c, NewRegistry()))
	h.SetAuditRecorder(auth.NewAuditRecorder(c)) // 不注入 authz/scope → checkAccess 放行，专测审计

	e := echo.New()
	e.POST("/api/v1/runbooks/:id/execute", h.execute, auth.RequireUser(true, nil))
	body := strings.NewReader(`{"incident_id":0,"approved":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runbooks/"+strconv.Itoa(rb.ID)+"/execute", body)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set("X-Vigil-User-ID", "77")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("execute status = %d, body=%s", rec.Code, rec.Body.String())
	}
	logs, err := c.AuditLog.Query().Where(auditlog.ActionEQ(auth.ActionRunbookExecute)).All(context.Background())
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 runbook.execute audit, got %d", len(logs))
	}
	lg := logs[0]
	if lg.ActorUserID != 77 {
		t.Errorf("actor = %d, want 77", lg.ActorUserID)
	}
	if lg.ResourceType != "runbook" || lg.ResourceID != rb.ID {
		t.Errorf("resource = %s/%d, want runbook/%d", lg.ResourceType, lg.ResourceID, rb.ID)
	}
	if lg.Result != auditlog.ResultSuccess {
		t.Errorf("result = %q, want success", lg.Result)
	}
	if approved, _ := lg.Detail["approved"].(bool); !approved {
		t.Errorf("detail.approved = %v, want true", lg.Detail["approved"])
	}
}
