// engine.go Runbook 执行引擎。
//
// 核心安全控制（对应 capabilities §5 + 设计基线第 8 条）：
// · readonly=true 的诊断动作直接执行（内置安全）
// · readonly=false 的处置动作必须 RequireApproval=true 且调用方提供 approved=true 才执行；
//   否则跳过（Skipped=true），不执行写操作
// · 失败按 OnFailure 处理：continue(继续下步) | abort(中止) | escalate(中止并升级)
package runbook

import (
	"context"
	"fmt"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/schema"
)

// Engine Runbook 执行引擎。
type Engine struct {
	db        *ent.Client
	registry  *Registry
	timeline  TimelineRecorder // 时间线记录接口，由 main 注入；nil 则不记录
}

// TimelineRecorder 时间线记录接口（解耦 runbook 与 incident 包）。
type TimelineRecorder interface {
	RecordRunbook(ctx context.Context, incID int, stepName, output string, success bool) error
}

// NewEngine 创建执行引擎。
func NewEngine(db *ent.Client, reg *Registry) *Engine {
	return &Engine{db: db, registry: reg}
}

// SetTimelineRecorder 注入时间线记录器。
func (e *Engine) SetTimelineRecorder(r TimelineRecorder) {
	e.timeline = r
}

// ExecuteResult 整个 Runbook 的执行结果。
type ExecuteResult struct {
	RunbookID int
	IncidentID int
	Steps     []StepResult
	Aborted   bool   // 是否中止（on_failure=abort/escalate）
	Reason    string // 中止原因
}

// Execute 执行一个 Runbook 的全部步骤。
// approved 指示调用方是否已确认（用于 require_approval 的写动作）。
// runbookID 对应 ent.Runbook.ID；incID 为关联事件（0 则不记时间线）。
func (e *Engine) Execute(ctx context.Context, runbookID, incID int, approved bool) (*ExecuteResult, error) {
	rb, err := e.db.Runbook.Get(ctx, runbookID)
	if err != nil {
		return nil, fmt.Errorf("get runbook %d: %w", runbookID, err)
	}
	// 文档式 runbook 无可执行步骤
	if rb.Type != "executable" || len(rb.Steps) == 0 {
		return &ExecuteResult{RunbookID: runbookID, IncidentID: incID}, nil
	}

	res := &ExecuteResult{RunbookID: runbookID, IncidentID: incID}
	for _, step := range rb.Steps {
		sr := e.executeStep(ctx, step, approved)
		res.Steps = append(res.Steps, sr)

		// 记时间线
		if e.timeline != nil && incID > 0 && !sr.Skipped {
			_ = e.timeline.RecordRunbook(ctx, incID, step.Name, sr.Output, sr.Success)
		}

		// 跳过的步骤（未确认的写动作）不算失败，继续
		if sr.Skipped {
			continue
		}
		// 失败处理
		if !sr.Success {
			switch step.OnFailure {
			case "abort":
				res.Aborted = true
				res.Reason = fmt.Sprintf("step %q failed: %s", step.Name, sr.Error)
				return res, nil
			case "escalate":
				res.Aborted = true
				res.Reason = fmt.Sprintf("step %q failed, escalate: %s", step.Name, sr.Error)
				return res, nil // TODO: 触发升级（交 escalation 包）
			default: // continue
				continue
			}
		}
	}
	return res, nil
}

// executeStep 执行单步。
func (e *Engine) executeStep(ctx context.Context, step schema.RunbookStep, approved bool) StepResult {
	start := time.Now()
	sr := StepResult{StepID: step.ID, Name: step.Name, Action: step.Action.Type}

	// ★ 核心安全控制：写动作必须确认
	if !step.Action.Target.Readonly && step.RequireApproval && !approved {
		sr.Skipped = true
		sr.Error = "require_approval not confirmed, skipped"
		return sr
	}

	// wait/notify/approve 类型当前简化处理
	switch step.Action.Type {
	case "wait":
		sr.Success = true
		sr.Output = "waited"
		return sr
	case "notify", "approve":
		sr.Success = true
		sr.Output = step.Action.Type + " (no-op)"
		return sr
	}

	// diagnose/execute：用对应执行器
	executor, ok := e.registry.Get(step.Action.Target.Kind)
	if !ok {
		sr.Error = fmt.Sprintf("no executor for kind %q", step.Action.Target.Kind)
		return sr
	}
	output, err := executor.Execute(ctx, step.Action.Target, step.Action.Params)
	sr.Duration = time.Since(start)
	if err != nil {
		sr.Error = err.Error()
		return sr
	}
	sr.Success = true
	sr.Output = output
	return sr
}

// IsReadOnly 判断 Runbook 是否全部为只读诊断（用于判断是否可自动执行）。
func IsReadOnly(rb *ent.Runbook) bool {
	if rb.Type != "executable" {
		return true // 文档式视为只读
	}
	for _, s := range rb.Steps {
		if !s.Action.Target.Readonly {
			return false
		}
	}
	return true
}
