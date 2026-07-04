// trigger.go Runbook 触发求值与自动展示（B13 / roadmap T5.3）。
//
// 背景：Runbook.trigger 字段（manual/on_incident/on_severity/on_label_match）此前可存但不求值，
// 只能手动 execute；IsReadOnly 亦无调用方（auto-run 未实现）。本文件补齐求值路径：
//
//	订阅 IncidentCreated → 求值该 Incident 所属 Service 关联的每个 Runbook 的 trigger →
//	命中则「自动展示」该 Runbook（关联到 Incident 时间线 runbook_suggested，Web/IM 可见）。
//
// ★★★ 安全红线（B13）：触发命中的默认结果是「展示」而非「执行」。
//   - 展示（默认）：写一条 runbook_suggested 时间线，让响应者一眼看到该用哪个 Runbook。
//   - 自动执行（例外）：仅当 Runbook 同时满足 ① 显式配置 auto_run=true ② 全部步骤只读诊断
//     （IsReadOnly==true）两个条件时才复用执行引擎自动跑（approved=false，写步骤守卫仍在）。
//   - 含任一写步骤的 Runbook 即使配 auto_run=true 也【绝不】自动执行——只展示。此守卫在
//     evaluateOne 里硬编码（IsReadOnly 判定），并有测试锁定（trigger_test.go）。
//
// 触发类型语义：
//   - manual：不自动触发（响应者手动 execute），求值直接跳过。
//   - on_incident：Incident 创建即命中（该 Service 关联的 Runbook 无条件展示）。
//   - on_severity：Incident severity 满足条件（≥ 阈值，severity 有序：critical>warning>info）。
//   - on_label_match：Incident 关联 Event 的 labels 匹配 trigger.labels（全部键值命中）。
package runbook

import (
	"context"
	"log/slog"

	"github.com/kevin/vigil/ent"
	domainevent "github.com/kevin/vigil/internal/event"
)

// TriggerRecorder 触发展示的时间线记录接口（解耦 runbook 与 timeline/incident 包）。
// 由 timeline.Recorder 实现（RecordRunbookSuggested）。nil 时不记录（降级/单测）。
type TriggerRecorder interface {
	// RecordRunbookSuggested 记录「trigger 命中，自动展示关联 Runbook」（非执行）。
	// autoRunnable 标记该 Runbook 是否将被自动执行（全只读诊断 + auto_run），供前端区分展示态。
	RecordRunbookSuggested(ctx context.Context, incID, runbookID int, runbookName, triggerType string, autoRunnable bool) error
}

// TriggerEvaluator 订阅 IncidentCreated，求值关联 Runbook 的 trigger，命中则自动展示（可选自动执行）。
//
// 装配：bus.Subscribe(event.IncidentCreated, evaluator.OnIncidentCreated)。
// 复用同一 Engine 执行引擎（自动执行走 Engine.Execute，approved=false 使写步骤被守卫跳过）。
type TriggerEvaluator struct {
	db       *ent.Client
	engine   *Engine         // 复用执行引擎；仅在全只读诊断 + auto_run 时自动执行
	recorder TriggerRecorder // 展示留痕（写 runbook_suggested 时间线）；nil 时跳过
}

// NewTriggerEvaluator 创建触发求值器。engine 用于自动执行（全只读诊断 Runbook），recorder 用于展示留痕。
func NewTriggerEvaluator(db *ent.Client, engine *Engine, recorder TriggerRecorder) *TriggerEvaluator {
	return &TriggerEvaluator{db: db, engine: engine, recorder: recorder}
}

// OnIncidentCreated 实现 event.Handler：Incident 创建时求值关联 Runbook 的 trigger。
//
// best-effort：任一 Runbook 求值失败仅记日志，不影响其它 Runbook、不阻断事件派发。
// 无 Service（未路由）/ Service 无关联 Runbook → 无可展示对象，静默返回。
func (t *TriggerEvaluator) OnIncidentCreated(ctx context.Context, e domainevent.Event) error {
	if e.Incident == nil {
		return nil
	}
	inc := e.Incident

	// 取 Incident 所属 Service 关联的候选 Runbook（触发只在这些里求值）。
	svc, err := inc.QueryService().Only(ctx)
	if err != nil {
		// 无 Service（未路由）→ 无候选 Runbook → 不展示（非错误，降级）。
		return nil //nolint:nilerr // 未路由是正常降级路径
	}
	candidates, err := svc.QueryRunbooks().All(ctx)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		return nil
	}

	// on_label_match 需要 Incident 的 labels——Incident 自身无 labels 字段，
	// labels 存在其聚合的 Event 上。合并所有关联 Event 的 labels（后者覆盖前者，取并集）。
	labels := t.incidentLabels(ctx, inc)

	for _, rb := range candidates {
		t.evaluateOne(ctx, inc, rb, labels)
	}
	return nil
}

// incidentLabels 收集 Incident 关联 Event 的 labels 并集（on_label_match 求值用）。
// best-effort：查询失败返回空 map（视为无 label，on_label_match 不命中）。
func (t *TriggerEvaluator) incidentLabels(ctx context.Context, inc *ent.Incident) map[string]string {
	evts, err := inc.QueryEvents().All(ctx)
	if err != nil {
		return map[string]string{}
	}
	merged := map[string]string{}
	for _, ev := range evts {
		for k, v := range ev.Labels {
			merged[k] = v
		}
	}
	return merged
}

// evaluateOne 求值单个 Runbook 的 trigger，命中则展示（并在满足条件时自动执行）。
//
// ★ 安全红线在此实现：
//   - 命中 → 先「展示」（写 runbook_suggested 时间线）。
//   - 自动执行仅当 auto_run==true 且 IsReadOnly(rb)==true（全只读诊断）。
//     含写步骤的 Runbook（IsReadOnly==false）即使 auto_run=true 也绝不自动执行——只展示。
func (t *TriggerEvaluator) evaluateOne(ctx context.Context, inc *ent.Incident, rb *ent.Runbook, labels map[string]string) {
	if !matchTrigger(rb.Trigger, string(inc.Severity), labels) {
		return
	}

	// ★ auto-run 硬守卫：只有「显式 auto_run + 全只读诊断」才自动执行；含写步骤一律只展示。
	autoRunnable := rb.AutoRun && IsReadOnly(rb)

	// 1) 展示：写 runbook_suggested 时间线（默认行为，触发的核心产出）。
	if t.recorder != nil {
		if err := t.recorder.RecordRunbookSuggested(ctx, inc.ID, rb.ID, rb.Name, triggerType(rb.Trigger), autoRunnable); err != nil {
			slog.Warn("runbook trigger: record suggested failed",
				"incident_id", inc.ID, "runbook_id", rb.ID, "error", err)
		}
	}

	// 2) 自动执行（例外）：仅全只读诊断 + auto_run。actorID=0（系统触发），approved=false
	//    （执行引擎的写步骤守卫仍在——即便这里已保证全只读，approved=false 是第二道防线：
	//     任何非只读步骤都会被 executeStep 跳过，绝不触发写操作）。
	if autoRunnable && t.engine != nil {
		if _, err := t.engine.Execute(ctx, rb.ID, inc.ID, false, 0); err != nil {
			slog.Warn("runbook trigger: auto-run failed",
				"incident_id", inc.ID, "runbook_id", rb.ID, "error", err)
		}
	}
}
