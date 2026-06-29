// handler_test.go Runbook handler 数据层兜底校验（QA 审计 C4）。
package runbook

import (
	"testing"

	"github.com/kevin/vigil/ent/schema"
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
