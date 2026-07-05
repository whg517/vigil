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
	"fmt"

	"github.com/kevin/vigil/internal/event"

	"go.uber.org/zap"
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

// OnReopened 处理 IncidentReopened 事件：从升级策略首层重启升级链。
//
// 缺陷背景：incident reopen（resolved/closed → triggered）后若不重启升级链，
// 会静默停在 triggered——不重发通知、不再升级，等于「重开了但没人管」。
// escalation 原先只订阅 Created/Acked/Escalated，reopen 事件无人消费，故补此订阅。
//
// 为什么从首层重启（而非续接旧 level）：reopen 是「问题复现/误解决后重新响应」，
// 语义上是一次全新的响应周期，应从 level 0 重新走完整升级路径，而不是接着上次
// 停下的层级。故先把 current_level/escalated_count 归零，再入队 level[0]。
//
// 幂等/去重：reopen 前该 incident 通常已 resolved/closed，残留 pending 升级任务
// 应已被 ack/resolve 时的 CancelOnAck 清掉；但为防御「残留任务撞 TaskID」
// （StartEscalation 入队 esc:{id}:0:0 时若旧任务同 key 未清会 ErrTaskIDConflict），
// 这里先 CancelOnAck 清一遍所有 level×repeat 组合，再排新的首层任务，确保不重复升级。
//
// 无升级策略 / 查询失败时跳过（该 incident 无需升级，与 OnCreated 一致）。
func (e *Engine) OnReopened(ctx context.Context, ev event.Event) error {
	if ev.Incident == nil {
		return nil
	}
	// 按 ID 重取最新 incident（事件载荷快照可能未加载 policy edge，与 OnCreated 同源）。
	inc, err := e.db.Incident.Get(ctx, ev.Incident.ID)
	if err != nil {
		return err
	}
	policy, err := inc.QueryEscalationPolicy().Only(ctx)
	if err != nil || len(policy.Levels) == 0 {
		return nil // 无升级策略，无需重启升级链
	}

	// 1. 清残留：删掉所有可能仍待触发的旧升级任务（防撞 TaskID / 防重复升级）。
	//    失败不阻塞——HandleTask 的状态守卫兜底，且新首层任务用同 TaskID 会覆盖同一 key。
	if cErr := e.CancelOnAck(ctx, inc.ID, policy.Levels, policy.RepeatTimes); cErr != nil {
		e.log().Warn("reopen: cancel residual escalation tasks failed",
			zap.Int("incident_id", inc.ID), zap.Error(cErr))
	}

	// 2. 升级层级归零：从首层重启，回到「待响应」计数起点。
	//    reopen 已把 status 置回 triggered，这里补齐 current_level/escalated_count，
	//    使后续手动升级的 targetLevelIdx 计算（=current_level）也从 0 起。
	if _, uErr := e.db.Incident.UpdateOneID(inc.ID).
		SetCurrentLevel(0).
		SetEscalatedCount(0).
		Save(ctx); uErr != nil {
		return fmt.Errorf("reset incident level on reopen: %w", uErr)
	}

	// 3. 从首层重新启动升级链（复用 StartEscalation，与 OnCreated 同款调度）。
	return e.StartEscalation(ctx, inc.ID, policy.Levels)
}

// OnMerged 处理 IncidentMerged 事件：取消被合并源单的所有待触发升级任务。
//
// 合并把源单置 closed 终态（merged_into 指向主单）。若不取消其 pending 升级计时器，
// 到点后 HandleTask 的状态守卫虽会因源单非活跃而跳过（不误升级），但残留的延迟任务
// 仍占用队列且可能干扰后续同 id 复用——与 ack 收口同理，主动清一遍更干净。
// 与 OnAcked 同款：从 DB 查 policy levels，无策略/查询失败则跳过（状态守卫兜底）。
func (e *Engine) OnMerged(ctx context.Context, ev event.Event) error {
	if ev.Incident == nil {
		return nil
	}
	policy, err := ev.Incident.QueryEscalationPolicy().Only(ctx)
	if err != nil || len(policy.Levels) == 0 {
		return nil // 无升级策略，无需取消
	}
	return e.CancelOnAck(ctx, ev.Incident.ID, policy.Levels, policy.RepeatTimes)
}

// OnManualEscalate 处理 IncidentEscalated 事件：立即触发目标 level 的升级任务。
//
// ev.Level 为目标 level 索引（由 incident.Service.Escalate 计算并携带）。
// 无策略时不应收到此事件（incident 侧 policyLevels==0 时不发布），但这里仍防御性检查。
//
// B10：自动升级（计时器到点）处理完 level 后也发布 IncidentEscalated 供多端同步，
// 该事件 SystemTriggered=true——若在此再触发同 level 会死循环，故直接跳过（本 level 已处理完）。
func (e *Engine) OnManualEscalate(ctx context.Context, ev event.Event) error {
	if ev.Incident == nil || ev.SystemTriggered {
		return nil
	}
	// B6b：手动跳级前，取消目标 level 已排的自动延迟任务，避免"手动立即触发 + 延迟任务到点"
	// 对同一 level 重复通知。repeatTimes 从策略查（决定要清几个 repeat 序号）。
	// 查失败仅影响清理精度（可能残留一条延迟任务），不阻塞立即升级——故只记 warn。
	if policy, perr := ev.Incident.QueryEscalationPolicy().Only(ctx); perr == nil && policy != nil {
		if cErr := e.CancelLevelPending(ctx, ev.Incident.ID, ev.Level, policy.RepeatTimes); cErr != nil {
			e.log().Warn("manual escalate: cancel pending level tasks failed",
				zap.Int("incident_id", ev.Incident.ID), zap.Int("level", ev.Level), zap.Error(cErr))
		}
	}
	return e.TriggerLevelNow(ctx, ev.Incident.ID, ev.Level)
}
