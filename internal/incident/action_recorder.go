// action_recorder.go IncidentAction 操作审计写入器（审计 B4/B5/C30）。
//
// 背景：ent/schema 有 IncidentAction 实体（操作审计：谁、何时、经哪个渠道、做了什么），
// 但全仓零写入——所有处置动作只落时间线（TimelineItem），审计维度（via 渠道统计、
// 撤销/重放基础）缺失。IncidentAction 与 TimelineItem 的分工（ADR-0029 双轨审计）：
//   - TimelineItem：人类可读的「全程留痕」，供协同与复盘阅读（含系统/AI/IM 消息等）。
//   - IncidentAction：结构化的「操作审计」，只记对 Incident 的显式处置动作，
//     强调 via（IM-first 关键指标：多少动作在 IM 完成）+ 可撤销/重放。
//
// 实现方式：事件驱动，与 timeline.Recorder 同款——订阅领域事件（event.Bus），
// 在每个 Incident 处置动作（ack/resolve/escalate/reopen/close/add_responder）落一条
// IncidentAction。via 从事件的 Via 字段（由 incident.Service 从 Source 派生）映射。
//
// 为什么订阅事件而非在 Service 内联写：与既有解耦一致——Service 只发事件，
// 审计/通知/升级各订阅方自取所需，互不侵入。系统自动触发（triage 自动恢复、
// escalation 自动升级）也发同类事件，故这些系统动作同样被审计（via=automation）。
package incident

import (
	"context"
	"fmt"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/incidentaction"
	"github.com/kevin/vigil/internal/event"
)

// ActionRecorder 把领域事件落成 IncidentAction 审计记录。
// db 为 nil 不可用（必须经 NewActionRecorder 构造）。
type ActionRecorder struct {
	db *ent.Client
}

// NewActionRecorder 创建操作审计写入器。
func NewActionRecorder(db *ent.Client) *ActionRecorder {
	return &ActionRecorder{db: db}
}

// Subscribe 把本记录器挂到事件总线：订阅全部 Incident 处置事件。
// 与 wire.go 装配一致，集中在此声明订阅集，避免装配处散落遗漏某类动作。
func (r *ActionRecorder) Subscribe(bus *event.Bus) {
	bus.Subscribe(event.IncidentAcked, r.OnIncidentEvent)
	bus.Subscribe(event.IncidentResolved, r.OnIncidentEvent)
	bus.Subscribe(event.IncidentEscalated, r.OnIncidentEvent)
	bus.Subscribe(event.IncidentReopened, r.OnIncidentEvent)
	bus.Subscribe(event.IncidentClosed, r.OnIncidentEvent)
	bus.Subscribe(event.IncidentResponderAdded, r.OnIncidentEvent)
}

// OnIncidentEvent 事件处理：把一条领域事件落成 IncidentAction。
// 无 incident / 无法映射 action type 的事件静默跳过（best-effort，不回传发布方）。
func (r *ActionRecorder) OnIncidentEvent(ctx context.Context, e event.Event) error {
	if e.Incident == nil {
		return nil
	}
	typ, ok := actionType(e.Action)
	if !ok {
		// 未知/无审计意义的 action（如 created 由 triage 发的建单事件）不落 IncidentAction。
		return nil
	}
	create := r.db.IncidentAction.Create().
		SetIncidentID(e.Incident.ID).
		SetType(typ).
		SetActor(actorMap(e.ActorID)).
		SetVia(viaFromEvent(e.Via)).
		SetResult(incidentaction.ResultSuccess)
	// payload 记 action 语义标签 + 升级目标 level（如有），供审计/重放读取。
	payload := map[string]any{"action": string(e.Action)}
	if e.Level > 0 {
		payload["level"] = e.Level
	}
	create = create.SetPayload(payload)
	if _, err := create.Save(ctx); err != nil {
		return fmt.Errorf("save incident action: %w", err)
	}
	return nil
}

// actionType 把领域事件的 Action 映射为 IncidentAction 的 type 枚举。
// 返回 ok=false 表示该 action 无对应审计动作类型（跳过写入）。
func actionType(a event.Action) (incidentaction.Type, bool) {
	switch Action(a) {
	case ActionAck:
		return incidentaction.TypeAck, true
	case ActionResolve:
		return incidentaction.TypeResolve, true
	case ActionEscalate:
		return incidentaction.TypeEscalate, true
	case ActionReopen:
		return incidentaction.TypeReopen, true
	case ActionClose:
		return incidentaction.TypeClose, true
	case ActionAddResponder:
		return incidentaction.TypeAddResponder, true
	default:
		return "", false
	}
}

// viaFromEvent 把事件 Via 字符串（源自 incident.Source）映射为 IncidentAction.via 枚举。
// 空串或系统联动（system）/未识别值统一归入 automation——
// IncidentAction.via 枚举无 system 值（web/im/api/automation），系统触发即视为自动化渠道。
func viaFromEvent(via string) incidentaction.Via {
	switch Source(via) {
	case SourceWeb:
		return incidentaction.ViaWeb
	case SourceIM:
		return incidentaction.ViaIm
	case SourceAPI:
		return incidentaction.ViaAPI
	default:
		// SourceRunbook / SourceSystem / 空串：系统/自动化触发。
		return incidentaction.ViaAutomation
	}
}

// actorMap 把 actorID 转成 IncidentAction.actor（kind + id）。
// actorID<=0 视为系统动作（kind=system，无 id）。
func actorMap(actorID int) map[string]string {
	if actorID <= 0 {
		return map[string]string{"kind": "system"}
	}
	return map[string]string{"kind": "user", "id": fmt.Sprintf("%d", actorID)}
}

// QueryActions 查询某 Incident 的操作审计（按时间升序）。
// limit<=0 或 >500 归一为 100；offset<=0 不偏移。供 handler 的 GET /incidents/:id/actions 用。
func (r *ActionRecorder) QueryActions(ctx context.Context, incID, limit, offset int) ([]*ent.IncidentAction, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := r.db.IncidentAction.Query().
		Where(incidentaction.HasIncidentWith(incident.IDEQ(incID))).
		Order(ent.Asc(incidentaction.FieldTimestamp)).
		Limit(limit)
	if offset > 0 {
		q = q.Offset(offset)
	}
	return q.All(ctx)
}

// CountActions 统计某 Incident 的操作审计条目数（分页用）。
func (r *ActionRecorder) CountActions(ctx context.Context, incID int) (int, error) {
	return r.db.IncidentAction.Query().
		Where(incidentaction.HasIncidentWith(incident.IDEQ(incID))).
		Count(ctx)
}
