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
//  4. 触发 OnIncidentChanged 回调（供 IM 层刷新卡片、Web 层 WebSocket 推送等）
//
// 设计动机（对应 capabilities/05-im-chatops.md §6 鉴权铁律 + §8 状态同步）：
// IM 操作走与 Web 完全相同的链路——同一入口、同一状态机、同一时间线。
// 升级取消（escalation.Engine.CancelOnAck）也收敛到这里，
// 避免 IM / triage 各自重复实现 ack 副作用。
package incident

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/timelineitem"
	"github.com/kevin/vigil/internal/escalation"
	"github.com/kevin/vigil/internal/timeline"
)

// ErrInvalidTransition 状态机守卫：非法状态转移。
// 例如对已 resolved 的 Incident 再次 ack。
var ErrInvalidTransition = errors.New("invalid incident status transition")

// ErrNotFound Incident 不存在。
var ErrNotFound = errors.New("incident not found")

// Source 动作来源，决定时间线 source 字段与回调语义。
type Source string

const (
	SourceWeb Source = "web"
	SourceIM  Source = "im"
	SourceAPI Source = "api"
)

// Service 事件动作领域服务。
//
// 依赖：
//   - db         ent client
//   - recorder   时间线记录器（统一写入，nil 时跳过记录）
//   - escEngine  升级引擎（ack 时取消后续升级，nil 时跳过取消——状态守卫兜底）
//   - onIncidentChanged  Incident 状态变更后的回调（nil 时跳过）
//     典型用途：IM 层在回调里刷新已发卡片；Web 层 WebSocket 推送。
type Service struct {
	db                *ent.Client
	recorder          *timeline.Recorder
	escEngine         *escalation.Engine
	onIncidentChanged func(ctx context.Context, inc *ent.Incident, action Action)
}

// Action 动作类型，传给 OnIncidentChanged 回调供订阅方区分语义。
type Action string

const (
	ActionAck          Action = "ack"
	ActionResolve      Action = "resolve"
	ActionEscalate     Action = "escalate"
	ActionAddResponder Action = "add_responder"
)

// NewService 创建事件动作服务。
func NewService(db *ent.Client, recorder *timeline.Recorder, esc *escalation.Engine) *Service {
	return &Service{db: db, recorder: recorder, escEngine: esc}
}

// SetOnIncidentChanged 注入 Incident 变更回调（由 main 装配时注入 IM/Web 订阅方）。
func (s *Service) SetOnIncidentChanged(fn func(ctx context.Context, inc *ent.Incident, action Action)) {
	s.onIncidentChanged = fn
}

// Ack 确认事件：触发态/升级态 → acked，设当前责任人，取消后续升级。
// actorID 为执行者 User ID（0 表示系统动作）。
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
		SetStatus(incident.StatusAcked)
	if actorID > 0 {
		upd.SetAssigneeID(actorID)
	}
	inc, err = upd.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update incident: %w", err)
	}

	// 取消后续升级任务（无 escEngine / 无 Redis 时状态守卫兜底，安全跳过）
	if s.escEngine != nil {
		if policy, perr := inc.QueryEscalationPolicy().Only(ctx); perr == nil && len(policy.Levels) > 0 {
			_ = s.escEngine.CancelOnAck(ctx, inc.ID, policy.Levels, policy.RepeatTimes)
		}
	}

	s.record(ctx, inc, timelineitem.TypeAck, actorID, src,
		fmt.Sprintf("%s 确认了事件", actorLabel(actorID)), map[string]any{"status": "acked"})
	s.fire(ctx, inc, ActionAck)
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
	s.fire(ctx, inc, ActionResolve)
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

	nextLevel := inc.CurrentLevel + 1
	policyLevels := 0
	if policy, perr := inc.QueryEscalationPolicy().Only(ctx); perr == nil {
		policyLevels = len(policy.Levels)
	}
	// 不超过策略最大 level（无策略则只记时间线，状态转 escalated）
	if policyLevels == 0 || nextLevel <= policyLevels {
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
	s.fire(ctx, inc, ActionEscalate)
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
	s.fire(ctx, inc, ActionAddResponder)
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

// fire 触发 Incident 变更回调（订阅方刷新卡片 / WebSocket 推送）。
func (s *Service) fire(ctx context.Context, inc *ent.Incident, action Action) {
	if s.onIncidentChanged != nil {
		s.onIncidentChanged(ctx, inc, action)
	}
}

// actorLabel 把 actorID 转成时间线可读文案。
func actorLabel(actorID int) string {
	if actorID <= 0 {
		return "系统"
	}
	return fmt.Sprintf("用户 %d", actorID)
}
