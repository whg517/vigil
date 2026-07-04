// Package incident 实现事件动作领域服务。
//
// 它是 IM 操作（能力域 8）与 Web 操作共用的「事件动作入口」，
// 把原先散落在 triage 引擎里内联的 SetStatus 抽象成带状态机守卫的统一动作：
//
//	Ack / Resolve / Escalate / AddResponder
//
// 每个动作统一做四件事：
//  1. 状态机守卫（非法状态转移直接报错，不写库）
//  2. 推进 Incident 状态 + 更新字段（assignee / resolved_at 等）
//  3. 经 timeline.Recorder 记时间线（source 区分 web / im）
//  4. 发布领域事件（event.Bus），由 escalation/ws/webhook/im 各自订阅
//
// 设计动机（对应 capabilities/05-im-chatops.md §6 鉴权铁律 + §8 状态同步）：
// IM 操作走与 Web 完全相同的链路——同一入口、同一状态机、同一时间线。
//
// 事件解耦（架构基线）：本服务不再持有 escalation.Engine 指针，改为发布事件：
//   - Ack → IncidentAcked（escalation 订阅后取消后续升级）
//   - Escalate → IncidentEscalated（escalation 订阅后触发目标 level 通知）
//
// 这样 incident 包不反向依赖 escalation，消除构建期依赖环。
package incident

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/timelineitem"
	"github.com/kevin/vigil/internal/event"
	"github.com/kevin/vigil/internal/timeline"
)

// ErrInvalidTransition 状态机守卫：非法状态转移。
// 例如对已 resolved 的 Incident 再次 ack。
var ErrInvalidTransition = errors.New("invalid incident status transition")

// ErrNotFound Incident 不存在。
var ErrNotFound = errors.New("incident not found")

// ErrAlreadyClosed Incident 已是 closed 终态。
// 与 ErrInvalidTransition 区分：closed 是幂等友好的终态——重复 close 不该被当成「非法转换」报错，
// 而应让调用方（如复盘发布联动）识别为「已收口」无操作跳过。故单列哨兵供 errors.Is 判定。
var ErrAlreadyClosed = errors.New("incident already closed")

// ErrPostmortemRequired 复盘闸门（T4.1）：critical 事件 resolved 后须先完成复盘
// （发布或显式跳过）才能 close。未满足时人工 close 被拒，让单据停在「待复盘」。
// 与 ErrInvalidTransition 区分：状态转换本身合法（resolved→closed），只是治理前置未满足，
// 故单列哨兵，供 handler 返回专门的可读提示（提示先完成/跳过复盘）。
var ErrPostmortemRequired = errors.New("critical incident requires postmortem before close")

// Source 动作来源，决定时间线 source 字段与回调语义。
type Source string

const (
	SourceWeb     Source = "web"
	SourceIM      Source = "im"
	SourceAPI     Source = "api"
	SourceRunbook Source = "runbook" // Runbook on_failure=escalate 自动触发
	SourceSystem  Source = "system"  // 系统联动触发（如复盘发布 → close），时间线 source 记 system
)

// PostmortemGate 复盘闸门查询接口（T4.1）。
//
// 由 postmortem 引擎实现并经 SetPostmortemGate 注入，回答「该 incident 是否已完成复盘」
// （存在 published/archived 复盘）。用接口而非直接依赖 postmortem 包，避免构建期依赖环
// （与 IncidentCloser、runbookEscalator 同款解耦）。
//
// 未注入（nil）时闸门降级为放行——单测/无复盘引擎场景不阻断 close。
type PostmortemGate interface {
	// HasPublishedPostmortem 该 incident 是否已有已完成（published/archived）复盘。
	// true 表示复盘闸门已满足，可放行 close。
	HasPublishedPostmortem(ctx context.Context, incID int) (bool, error)
}

// Service 事件动作领域服务。
//
// 依赖：
//   - db        ent client
//   - recorder  时间线记录器（统一写入，nil 时跳过记录）
//   - bus       领域事件总线（nil 时跳过事件发布——降级/测试用）
//   - pmGate    复盘闸门（nil 时闸门放行——降级/测试用）
type Service struct {
	db       *ent.Client
	recorder *timeline.Recorder
	bus      *event.Bus
	pmGate   PostmortemGate
}

// SetPostmortemGate 注入复盘闸门（main 装配时调用，T4.1）。
// 配置后 critical 事件 resolved→closed 前校验复盘已完成（published/archived）或已显式跳过。
// 未注入时降级为无闸门（任何 resolved 单可直接 close）。
func (s *Service) SetPostmortemGate(g PostmortemGate) { s.pmGate = g }

// Action 动作类型，随事件发布供订阅方区分语义。
type Action string

const (
	ActionAck          Action = "ack"
	ActionResolve      Action = "resolve"
	ActionClose        Action = "close"
	ActionReopen       Action = "reopen"
	ActionEscalate     Action = "escalate"
	ActionAddResponder Action = "add_responder"
)

// NewService 创建事件动作服务。
//
// bus 为 nil 时跳过事件发布（降级：escalation 取消/通知、ws/webhook/im 推送都不会发生）。
// 生产装配时必须注入非 nil bus；测试可传 nil 仅验证状态机/时间线。
func NewService(db *ent.Client, recorder *timeline.Recorder, bus *event.Bus) *Service {
	return &Service{db: db, recorder: recorder, bus: bus}
}

// Ack 确认事件：触发态/升级态 → acked，设当前责任人，取消后续升级。
// actorID 为执行者 User ID（0 表示系统动作）。
//
// 「取消后续升级」通过发布 IncidentAcked 事件实现：escalation 订阅后调用 CancelOnAck。
// 本服务不直接依赖 escalation，消除构建期依赖环。
func (s *Service) Ack(ctx context.Context, incID int, actorID int, src Source) (*ent.Incident, error) {
	inc, err := s.db.Incident.Get(ctx, incID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get incident: %w", err)
	}
	st := incident.Status(inc.Status)
	if st != incident.StatusTriggered && st != incident.StatusEscalated {
		return nil, fmt.Errorf("%w: ack from %s", ErrInvalidTransition, st)
	}

	upd := s.db.Incident.UpdateOneID(inc.ID).
		SetStatus(incident.StatusAcked).
		SetAckedAt(time.Now())
	if actorID > 0 {
		upd.SetAssigneeID(actorID)
	}
	inc, err = upd.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update incident: %w", err)
	}

	s.record(ctx, inc, timelineitem.TypeAck, actorID, src,
		fmt.Sprintf("%s 确认了事件", actorLabel(actorID)), map[string]any{"status": "acked"})
	s.publish(ctx, event.IncidentAcked, inc, ActionAck, actorID, src)
	return inc, nil
}

// Resolve 解决事件：任意活跃态 → resolved，记 resolved_at。
func (s *Service) Resolve(ctx context.Context, incID int, actorID int, src Source) (*ent.Incident, error) {
	inc, err := s.db.Incident.Get(ctx, incID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get incident: %w", err)
	}
	st := incident.Status(inc.Status)
	if st == incident.StatusResolved || st == incident.StatusClosed {
		return nil, fmt.Errorf("%w: resolve from %s", ErrInvalidTransition, st)
	}

	inc, err = s.db.Incident.UpdateOneID(inc.ID).
		SetStatus(incident.StatusResolved).
		SetResolvedAt(time.Now()).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update incident: %w", err)
	}

	s.record(ctx, inc, timelineitem.TypeResolved, actorID, src,
		fmt.Sprintf("%s 解决了事件", actorLabel(actorID)), map[string]any{"status": "resolved"})
	s.publish(ctx, event.IncidentResolved, inc, ActionResolve, actorID, src)
	return inc, nil
}

// Close 关闭事件：resolved → closed（进入终态）。
//
// 状态机约束：closed 是终态，唯一入边是 resolved → closed（见 data-model.md 状态机）。
// 非 resolved 状态直接 close 属非法转换（如 triggered 未处置就归档），返回 ErrInvalidTransition。
// 已 closed 幂等返回 ErrAlreadyClosed（供复盘发布联动等调用方识别「已收口」跳过，不当失败处理）。
//
// 复盘闸门（T4.1）：critical 事件须先完成复盘（published/archived）或显式跳过（postmortem_skipped）
// 才能 close，否则返回 ErrPostmortemRequired，让单据停在「待复盘」。非 critical 不受约束。
// 本方法用于人工/API 主动 close（走闸门）；复盘发布联动收口走 CloseAfterPostmortem（绕过闸门，
// 因发布本身即已满足复盘）。
//
// 触发路径：① Web/IM 人工点「关闭」；② 复盘发布联动（复盘 published → 关联 incident 收口）。
// 与 resolve 分离的意义：resolved 表示「问题已处理」，closed 表示「复盘/归档完成，不再变更」——
// 补 closed 终态可达，才让单据生命周期真正闭合，而非永远停在 resolved。
func (s *Service) Close(ctx context.Context, incID int, actorID int, src Source) (*ent.Incident, error) {
	return s.closeInternal(ctx, incID, actorID, src, false)
}

// CloseAfterPostmortem 复盘发布联动收口：resolved → closed，绕过复盘闸门。
//
// 为什么绕过闸门：调用方是复盘发布链路（postmortem.Engine.Transition → IncidentCloser.Close），
// 此刻复盘刚 published，闸门本会放行；但为避免读取时序竞态（发布事务与闸门查询交错），
// 显式绕过更稳。人工/API close 仍走 Close（受闸门约束）。
func (s *Service) CloseAfterPostmortem(ctx context.Context, incID int, actorID int, src Source) (*ent.Incident, error) {
	return s.closeInternal(ctx, incID, actorID, src, true)
}

// closeInternal close 的内部实现，bypassGate 决定是否跳过复盘闸门。
func (s *Service) closeInternal(ctx context.Context, incID int, actorID int, src Source, bypassGate bool) (*ent.Incident, error) {
	inc, err := s.db.Incident.Get(ctx, incID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get incident: %w", err)
	}
	st := incident.Status(inc.Status)
	if st == incident.StatusClosed {
		return nil, ErrAlreadyClosed
	}
	if st != incident.StatusResolved {
		return nil, fmt.Errorf("%w: close from %s", ErrInvalidTransition, st)
	}

	// 复盘闸门（T4.1）：critical 事件须先完成复盘或显式跳过。
	// bypassGate=true（复盘发布联动）跳过；非 critical 或已跳过或已有复盘则放行。
	if !bypassGate {
		if err := s.checkPostmortemGate(ctx, inc); err != nil {
			return nil, err
		}
	}

	inc, err = s.db.Incident.UpdateOneID(inc.ID).
		SetStatus(incident.StatusClosed).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update incident: %w", err)
	}

	// 记 status_changed 时间线（closed 无独立 timeline type，复用状态变更条目，detail.status 标注终态）。
	s.record(ctx, inc, timelineitem.TypeStatusChanged, actorID, src,
		fmt.Sprintf("%s 关闭了事件", actorLabel(actorID)), map[string]any{"status": "closed"})
	s.publish(ctx, event.IncidentClosed, inc, ActionClose, actorID, src)
	return inc, nil
}

// checkPostmortemGate 复盘闸门判定（T4.1）：
//   - 非 critical：不受约束，放行。
//   - critical + postmortem_skipped=true：已显式跳过，放行。
//   - critical + 已有 published/archived 复盘：闸门满足，放行。
//   - critical + 无复盘 + 未跳过：返回 ErrPostmortemRequired（停「待复盘」）。
//
// pmGate 未注入时降级放行（单测/无复盘引擎场景）。
func (s *Service) checkPostmortemGate(ctx context.Context, inc *ent.Incident) error {
	if incident.Severity(inc.Severity) != incident.SeverityCritical {
		return nil // 非 critical 不受闸门约束
	}
	if inc.PostmortemSkipped {
		return nil // 已显式跳过复盘
	}
	if s.pmGate == nil {
		return nil // 未注入闸门：降级放行
	}
	done, err := s.pmGate.HasPublishedPostmortem(ctx, inc.ID)
	if err != nil {
		return fmt.Errorf("check postmortem gate: %w", err)
	}
	if !done {
		return ErrPostmortemRequired
	}
	return nil
}

// SkipPostmortem 显式跳过复盘闸门（T4.1）：置 postmortem_skipped=true 并记时间线。
// 用于 critical 事件确无复盘必要（如误报、演练）时放行 close，而非强制走复盘流程。
// 幂等：重复跳过无副作用。仅对 resolved（尚未 close）的单有意义，但不硬性校验状态
// （closed 单跳过无害，triggered 单跳过为后续 resolve→close 预留）。
func (s *Service) SkipPostmortem(ctx context.Context, incID int, actorID int, src Source) (*ent.Incident, error) {
	inc, err := s.db.Incident.Get(ctx, incID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get incident: %w", err)
	}
	if inc.PostmortemSkipped {
		return inc, nil // 幂等：已跳过
	}
	inc, err = s.db.Incident.UpdateOneID(inc.ID).
		SetPostmortemSkipped(true).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update incident: %w", err)
	}
	s.record(ctx, inc, timelineitem.TypeStatusChanged, actorID, src,
		fmt.Sprintf("%s 跳过了复盘", actorLabel(actorID)), map[string]any{"postmortem_skipped": true})
	return inc, nil
}

// Reopen 重新打开已解决/已关闭的事件。
// 状态回退为 triggered（待响应），清空 resolved_at；记 reopened 时间线并触发事件。
// 设计：误解决或问题复现时使用，与 resolve 对称。权限点 incident.reopen。
func (s *Service) Reopen(ctx context.Context, incID int, actorID int, src Source) (*ent.Incident, error) {
	inc, err := s.db.Incident.Get(ctx, incID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get incident: %w", err)
	}
	st := incident.Status(inc.Status)
	// 仅 resolved/closed 可重新打开；其它状态已是活跃态，重开无意义。
	if st != incident.StatusResolved && st != incident.StatusClosed {
		return nil, fmt.Errorf("%w: reopen from %s", ErrInvalidTransition, st)
	}

	inc, err = s.db.Incident.UpdateOneID(inc.ID).
		SetStatus(incident.StatusTriggered).
		ClearResolvedAt().
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update incident: %w", err)
	}

	s.record(ctx, inc, timelineitem.TypeReopened, actorID, src,
		fmt.Sprintf("%s 重新打开了事件", actorLabel(actorID)), map[string]any{"status": "triggered"})
	s.publish(ctx, event.IncidentReopened, inc, ActionReopen, actorID, src)
	return inc, nil
}

// Escalate 人工触发跳级：推进到下一升级 level。
// 与升级引擎自动升级不同，这是人主动「我现在就需要更高层级介入」。
// 若无升级策略或已在最高 level，则仅记时间线不报错（幂等友好）。
func (s *Service) Escalate(ctx context.Context, incID int, actorID int, src Source) (*ent.Incident, error) {
	inc, err := s.db.Incident.Get(ctx, incID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get incident: %w", err)
	}
	st := incident.Status(inc.Status)
	if st == incident.StatusResolved || st == incident.StatusClosed {
		return nil, fmt.Errorf("%w: escalate from %s", ErrInvalidTransition, st)
	}

	// 目标 level = 当前 level + 1（跳到下一升级层级）。
	// 取策略 levels 数判断是否越界；无策略时 nextLevel 仅作展示用。
	nextLevel := inc.CurrentLevel + 1
	policyLevels := 0
	if policy, perr := inc.QueryEscalationPolicy().Only(ctx); perr == nil {
		policyLevels = len(policy.Levels)
	}
	// targetLevel 是要触发升级任务的 level 索引（0-based）。
	// 当前 current_level 表示「已执行到的层级」，所以下一级索引 = current_level。
	// 例：current_level=0 表示 level[0] 已执行/在执行，手动升级应触发 level[1]。
	targetLevelIdx := inc.CurrentLevel

	canEscalate := policyLevels == 0 || targetLevelIdx < policyLevels
	if canEscalate {
		// 更新状态 + current_level；inc 重新赋值确保后续时间线用最新值（修原作用域 bug）。
		inc, err = s.db.Incident.UpdateOneID(inc.ID).
			SetStatus(incident.StatusEscalated).
			SetCurrentLevel(nextLevel).
			SetEscalatedCount(inc.EscalatedCount + 1).
			Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("update incident: %w", err)
		}
	}

	s.record(ctx, inc, timelineitem.TypeEscalated, actorID, src,
		fmt.Sprintf("%s 手动升级到 level %d", actorLabel(actorID), nextLevel),
		map[string]any{"level": nextLevel, "manual": true})

	// 有升级策略时发布 IncidentEscalated 事件，escalation 订阅后触发目标 level 通知。
	// 无策略（policyLevels==0）时不发布——没有更高层级可通知，避免 escalation 做无谓触发。
	// targetLevelIdx 通过事件载荷传递，escalation 订阅方据此调用 TriggerLevelNow。
	// 通知触发是否失败由 escalation 订阅方自行处理（best-effort，不回传本服务）。
	if policyLevels > 0 {
		s.publishWithLevel(ctx, event.IncidentEscalated, inc, ActionEscalate, actorID, src, targetLevelIdx)
	}
	return inc, nil
}

// AddResponder 拉人协同：把 targetUserID 加入 responders（去重）。
// 对应 capabilities §5 拉人即授权——临时权限授予由调用方（IM 层）处理，
// 本服务只负责加入 responders + 时间线。
func (s *Service) AddResponder(ctx context.Context, incID, opUserID, targetUserID int, src Source) (*ent.Incident, error) {
	inc, err := s.db.Incident.Get(ctx, incID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get incident: %w", err)
	}
	target, err := s.db.User.Get(ctx, targetUserID)
	if err != nil {
		return nil, fmt.Errorf("get target user: %w", err)
	}

	inc, err = s.db.Incident.UpdateOneID(inc.ID).
		AddResponderIDs(targetUserID).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("add responder: %w", err)
	}

	s.record(ctx, inc, timelineitem.TypeResponderAdded, opUserID, src,
		fmt.Sprintf("拉入响应者 %s", target.Name),
		map[string]any{"responder_id": targetUserID, "responder_name": target.Name})
	s.publish(ctx, event.IncidentResponderAdded, inc, ActionAddResponder, opUserID, src)
	return inc, nil
}

// record 统一写时间线，recorder 为 nil 或写失败不阻塞主流程（记日志）。
func (s *Service) record(ctx context.Context, inc *ent.Incident, typ timelineitem.Type, actorID int, src Source, content string, detail map[string]any) {
	if s.recorder == nil {
		return
	}
	actor := timeline.Actor{Kind: "system"}
	if actorID > 0 {
		actor = timeline.Actor{Kind: "user", ID: fmt.Sprintf("%d", actorID)}
	}
	source := timelineitem.SourceSystem
	switch src {
	case SourceWeb:
		source = timelineitem.SourceWeb
	case SourceIM:
		source = timelineitem.SourceIm
	case SourceAPI:
		source = timelineitem.SourceAPI
	}
	_ = s.recorder.Record(ctx, inc.ID, typ, content, actor, source, detail)
}

// publish 发布领域事件（无 level）。
// bus 为 nil 时跳过（降级/测试）。ctx 直接透传发布方 ctx（同步派发，在调用栈内完成）。
func (s *Service) publish(ctx context.Context, typ event.Type, inc *ent.Incident, action Action, actorID int, src Source) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(ctx, event.Event{
		Type: typ, Incident: inc, Action: event.Action(action), ActorID: actorID, Via: string(src),
	})
}

// publishWithLevel 发布携带升级目标 level 的事件（仅 IncidentEscalated 用）。
func (s *Service) publishWithLevel(ctx context.Context, typ event.Type, inc *ent.Incident, action Action, actorID int, src Source, level int) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(ctx, event.Event{
		Type: typ, Incident: inc, Action: event.Action(action), ActorID: actorID, Level: level, Via: string(src),
	})
}

// actorLabel 把 actorID 转成时间线可读文案。
func actorLabel(actorID int) string {
	if actorID <= 0 {
		return "系统"
	}
	return fmt.Sprintf("用户 %d", actorID)
}
