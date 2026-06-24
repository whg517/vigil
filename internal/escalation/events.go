// events.go 升级引擎的领域事件订阅入口。
//
// 设计：incident.Service 不再直接持有 *Engine 指针（消除构建期依赖环），
// 改为发布领域事件（event.Bus）。本文件提供适配 event.Handler 签名的方法，
// 供装配时 bus.Subscribe(event.IncidentAcked, engine.OnAcked) 等挂载。
//
// 三个订阅点对应原先 incident.Service 里的三处直接调用：
//   - OnAcked          ← IncidentAcked   （原 Ack → CancelOnAck）
//   - OnCreated        ← IncidentCreated （原 triage.OnIncidentCreated → StartEscalation）
//   - OnManualEscalate ← IncidentEscalated（原 Escalate → TriggerLevelNow）
package escalation

import (
	"context"

	"github.com/kevin/vigil/internal/event"
)

// OnAcked 处理 IncidentAcked 事件：取消该 Incident 所有待触发升级任务。
//
// 事件不携带 policy levels，这里从 DB 重新查询（与原 incident.Service 内联逻辑一致）。
// 无策略 / 查询失败时跳过（状态守卫兜底：HandleTask 对已 ack 的 incident 不动作）。
// 实现 event.Handler，签名 func(ctx, event.Event) error。
func (e *Engine) OnAcked(ctx context.Context, ev event.Event) error {
	if ev.Incident == nil {
		return nil
	}
	policy, err := ev.Incident.QueryEscalationPolicy().Only(ctx)
	if err != nil || len(policy.Levels) == 0 {
		return nil // 无升级策略，无需取消
	}
	return e.CancelOnAck(ctx, ev.Incident.ID, policy.Levels, policy.RepeatTimes)
}

// OnCreated 处理 IncidentCreated 事件：启动升级链（入队 level[0] 延迟任务）。
//
// 注意：triage 在创建 Incident 后会先绑定 EscalationPolicy（SetEscalationPolicyID）
// 再发布本事件，但事件载荷里的 *ent.Incident 是绑定前抓取的快照，policy edge 未加载。
// 因此这里按 ID 从 DB 重新取最新 incident 再查 policy，避免读到空策略而误跳过。
// 无策略时跳过（该 incident 无需升级）。
func (e *Engine) OnCreated(ctx context.Context, ev event.Event) error {
	if ev.Incident == nil {
		return nil
	}
	inc, err := e.db.Incident.Get(ctx, ev.Incident.ID)
	if err != nil {
		return err
	}
	policy, err := inc.QueryEscalationPolicy().Only(ctx)
	if err != nil || len(policy.Levels) == 0 {
		return nil
	}
	return e.StartEscalation(ctx, inc.ID, policy.Levels)
}

// OnManualEscalate 处理 IncidentEscalated 事件：立即触发目标 level 的升级任务。
//
// ev.Level 为目标 level 索引（由 incident.Service.Escalate 计算并携带）。
// 无策略时不应收到此事件（incident 侧 policyLevels==0 时不发布），但这里仍防御性检查。
func (e *Engine) OnManualEscalate(ctx context.Context, ev event.Event) error {
	if ev.Incident == nil {
		return nil
	}
	return e.TriggerLevelNow(ctx, ev.Incident.ID, ev.Level)
}
