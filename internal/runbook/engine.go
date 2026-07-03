// engine.go Runbook 执行引擎。
//
// 核心安全控制（对应 capabilities §5 + 设计基线第 8 条）：
//   - readonly=true 的诊断动作直接执行（内置安全）。
//   - readonly=false 的处置动作必须调用方提供 approved=true 才执行（human-in-the-loop
//     闸门）；未获批时拒绝执行该步（Skipped=true），绝不触碰写操作。
//   - 被阻断的写步骤按 OnFailure 语义处理（与执行失败同一分支）：continue 跳过继续（合法
//     "干跑"/预览）；abort 中止；escalate 中止并升级（关键处置未获批=未完成，需要人介入）。
//   - 执行失败同样按 OnFailure 处理：continue（继续下步）| abort（中止）| escalate（中止并升级）。
//   - 执行/阻断/升级动作在时间线记录 actor（谁在生产上跑了/阻断了写操作，可追溯）。
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
	timeline  TimelineRecorder  // 时间线记录接口，由 main 注入；nil 则不记录
	escalator EscalationTrigger // 升级触发器，on_failure=escalate 时调用；nil 则仅中止不升级
}

// TimelineRecorder 时间线记录接口（解耦 runbook 与 incident 包）。
// actorID 为执行发起人（0 视为系统/匿名），用于在时间线留痕"谁执行/阻断了动作"。
type TimelineRecorder interface {
	RecordRunbook(ctx context.Context, incID int, stepName, output string, success bool, actorID int) error
	// RecordRunbookBlocked 记录一条"写步骤未获审批被阻断"的时间线（含 actor），
	// 让"谁在何时尝试执行未获批的写操作"可审计。
	RecordRunbookBlocked(ctx context.Context, incID int, stepName string, actorID int) error
}

// EscalationTrigger 升级触发接口（解耦 runbook 与 escalation/incident 包）。
// on_failure=escalate 时调用，触发该 incident 的立即升级。
// actorID 为触发升级的发起人（0 视为系统），透传给底层 Escalate 以留痕。
// 由 main 注入（实际实现调 incident.Service.Escalate 或 escalation.Engine）。
type EscalationTrigger interface {
	Trigger(ctx context.Context, incID int, reason string, actorID int) error
}

// NewEngine 创建执行引擎。
func NewEngine(db *ent.Client, reg *Registry) *Engine {
	return &Engine{db: db, registry: reg}
}

// SetTimelineRecorder 注入时间线记录器。
func (e *Engine) SetTimelineRecorder(r TimelineRecorder) {
	e.timeline = r
}

// SetEscalationTrigger 注入升级触发器（on_failure=escalate 用）。
func (e *Engine) SetEscalationTrigger(t EscalationTrigger) {
	e.escalator = t
}

// ExecuteResult 整个 Runbook 的执行结果。
type ExecuteResult struct {
	RunbookID       int
	IncidentID      int
	Steps           []StepResult
	Aborted         bool   // 是否中止（on_failure=abort/escalate）
	Reason          string // 中止原因
	PendingApproval bool   // 是否存在因未获审批被阻断的写步骤（human-in-the-loop 闸门生效）
}

// Execute 执行一个 Runbook 的全部步骤。
// approved 指示调用方是否已确认（用于写动作的 human-in-the-loop 闸门）。
// actorID 为执行发起人（来自鉴权中间件；0 视为系统/匿名），用于时间线留痕。
// runbookID 对应 ent.Runbook.ID；incID 为关联事件（0 则不记时间线）。
func (e *Engine) Execute(ctx context.Context, runbookID, incID int, approved bool, actorID int) (*ExecuteResult, error) {
	rb, err := e.db.Runbook.Get(ctx, runbookID)
	if err != nil {
		return nil, fmt.Errorf("get runbook %d: %w", runbookID, err)
	}
	// 文档式 runbook 无可执行步骤
	if rb.Type != "executable" || len(rb.Steps) == 0 {
		return &ExecuteResult{RunbookID: runbookID, IncidentID: incID}, nil
	}

	res := &ExecuteResult{RunbookID: runbookID, IncidentID: incID}
	return e.executeSteps(ctx, incID, rb.Steps, approved, actorID, res), nil
}

// executeSteps 执行步骤列表，处理 on_failure（continue/abort/escalate）+ 时间线记录。
// 抽成独立方法便于测试 on_failure 逻辑（无需构造完整 runbook 实体）。
func (e *Engine) executeSteps(ctx context.Context, incID int, steps []schema.RunbookStep, approved bool, actorID int, res *ExecuteResult) *ExecuteResult {
	for _, step := range steps {
		sr := e.executeStep(ctx, step, approved)
		res.Steps = append(res.Steps, sr)

		// 写步骤未获审批被阻断：留痕（含 actor）+ 标记待审批 + 按 on_failure 处理。
		// 阻断不是"执行失败"而是治理闸门，但对 on_failure=abort/escalate 的步骤，
		// "关键处置未完成"同样需要中止/升级（拉人介入）；on_failure=continue 则跳过继续（干跑）。
		if sr.Skipped {
			res.PendingApproval = true
			if e.timeline != nil && incID > 0 {
				_ = e.timeline.RecordRunbookBlocked(ctx, incID, step.Name, actorID)
			}
			if e.applyOnFailure(ctx, step, incID, actorID, res, "write action requires approval") {
				return res
			}
			continue
		}

		// 记时间线（执行结果，含 actor）
		if e.timeline != nil && incID > 0 {
			_ = e.timeline.RecordRunbook(ctx, incID, step.Name, sr.Output, sr.Success, actorID)
		}

		// 失败处理
		if !sr.Success {
			if e.applyOnFailure(ctx, step, incID, actorID, res, sr.Error) {
				return res
			}
		}
	}
	return res
}

// applyOnFailure 按 step.OnFailure 处理"失败/阻断"，返回 true 表示应中止整个 Runbook。
// reason 描述原因（执行错误信息，或"未获审批"）。
func (e *Engine) applyOnFailure(ctx context.Context, step schema.RunbookStep, incID, actorID int, res *ExecuteResult, reason string) bool {
	switch step.OnFailure {
	case "abort":
		res.Aborted = true
		res.Reason = fmt.Sprintf("step %q aborted: %s", step.Name, reason)
		return true
	case "escalate":
		res.Aborted = true
		res.Reason = fmt.Sprintf("step %q escalate: %s", step.Name, reason)
		// 触发立即升级（escalator 未配置时仅中止，不升级——降级）
		if e.escalator != nil && incID > 0 {
			_ = e.escalator.Trigger(ctx, incID, res.Reason, actorID)
		}
		return true
	default: // continue
		return false
	}
}

// executeStep 执行单步。
func (e *Engine) executeStep(ctx context.Context, step schema.RunbookStep, approved bool) StepResult {
	start := time.Now()
	sr := StepResult{StepID: step.ID, Name: step.Name, Action: step.Action.Type}

	// ★ 核心安全控制（QA 审计 C4）：写操作一律需 confirmed，与 RequireApproval 标志解耦。
	// 旧逻辑 `!Readonly && RequireApproval && !approved` 用 AND——若配置成
	// RequireApproval=false 且 Readonly=false，写操作会无确认直接执行，绕过安全红线
	// （可触发回滚/扩容等危险动作）。改为：凡非只读步骤，approved=false 一律 skip。
	if !step.Action.Target.Readonly && !approved {
		sr.Skipped = true
		sr.Error = "write action requires approval, skipped"
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
