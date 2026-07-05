// merge.go Incident 人工合并（M3.5/M3.6，审计 M7）。
//
// 把一个或多个「源单」合并进一个「目标主单」，落实分诊降噪的核心动作之一：
// 同一故障触发的多张单，人工确认后收敛成一张，避免响应者在多张单之间来回切换。
//
// 合并语义（capabilities/02-triage-routing.md §2.6）：
//   - 源单 merged_into 指向主单、状态置终态 closed（合并即收口，不再单独处置）。
//   - 源单的 related_events（Event.incident 边）转移到主单——事件是原始信号，
//     并单后应挂到统一处理单元下，主单时间线/诊断才看得到全量信号。
//   - 源单的 responders 并入主单（去重）——参与过源单的人自然也是主单的干系人。
//   - 取消源单 pending 升级计时器（发布 IncidentMerged，escalation 订阅后 CancelOnAck；
//     状态守卫兜底：源单已 closed，HandleTask 对非活跃单本就不动作）。
//   - 时间线双记：主单记 incident_merged（合入了哪些单），每张源单记 merged（并入了哪个主单）。
//   - IncidentAction 审计：主单落一条 merge 动作（payload 记源单 id 列表）。
//
// 边界（不可逆）：合并一般不可逆——源单已 closed 且 events 已转移，无「拆单」逆操作。
// 如误合并，走 reopen 主单 + 人工重建源单，而非自动回滚。故校验从严：
//   - 不能把单合并进自己（源 == 目标）。
//   - 已 merged（merged_into 非空）/ closed 的单不能作为源或目标（避免链式/重复合并的歧义）。
package incident

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/incidentaction"
	"github.com/kevin/vigil/ent/timelineitem"
	"github.com/kevin/vigil/internal/event"
)

// ErrMergeIntoSelf 不能把单合并进自己（源 id 命中目标 id）。
var ErrMergeIntoSelf = errors.New("cannot merge incident into itself")

// ErrMergeNoSources 合并请求未指定任何源单（去重/去自身后为空）。
var ErrMergeNoSources = errors.New("no source incidents to merge")

// ErrMergeTargetTerminal 目标主单已处于终态/已被合并，不能作为合并目标。
// 语义：合并是把源单吸收进「仍在处置」的主单；已 closed 或已 merged 的单再吸收源单会造成
// 「已收口的单又活起来」的歧义，故拒绝。调用方应选一张活跃单作主单。
var ErrMergeTargetTerminal = errors.New("target incident is closed or already merged")

// ErrMergeSourceTerminal 源单已处于终态/已被合并，不能再次作为源单。
// 语义：已 closed/merged 的单要么已收口、要么已并入别处，重复合并无意义且会打乱既有归属。
var ErrMergeSourceTerminal = errors.New("source incident is closed or already merged")

// Merge 把 sourceIDs 指向的一个或多个 incident 合并进 targetID 主单（M3.5/M3.6）。
//
// 返回合并后的主单快照（含最新 responders）。任一校验失败整体不写（先校验后写）。
//
// 校验（先做完，避免部分写库）：
//   - targetID 存在且非终态/未被合并（否则 ErrMergeTargetTerminal / ErrNotFound）。
//   - sourceIDs 去重、去掉命中 target 的项；为空 → ErrMergeNoSources；含 target → ErrMergeIntoSelf。
//   - 每个源单存在且非终态/未被合并（否则 ErrMergeSourceTerminal / ErrNotFound）。
//
// 写入（逐源单）：
//  1. Event.incident 边整体改指到主单（related_events 转移）。
//  2. 源单 responders 并入主单（去重由 ent AddResponderIDs 语义 + 主单已有集合保证）。
//  3. 源单置 merged_into=target、status=closed、trigger 保持不变（不改 trigger_type，避免掩盖来源）。
//  4. 源单记 merged 时间线 + 发布 IncidentMerged（escalation 取消 pending / 多端同步）。
//
// 最后主单记 incident_merged 时间线 + IncidentAction merge 审计（payload 记 source_ids）。
func (s *Service) Merge(ctx context.Context, targetID int, sourceIDs []int, actorID int, src Source) (*ent.Incident, error) {
	target, err := s.db.Incident.Get(ctx, targetID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get target incident: %w", err)
	}
	if isTerminalOrMerged(target) {
		return nil, ErrMergeTargetTerminal
	}

	// 去重 + 去自身：同一源 id 传多次只合一次；命中目标 id 视为「合并进自己」直接拒绝。
	seen := make(map[int]bool, len(sourceIDs))
	uniqueSources := make([]int, 0, len(sourceIDs))
	for _, sid := range sourceIDs {
		if sid == targetID {
			return nil, ErrMergeIntoSelf
		}
		if seen[sid] {
			continue
		}
		seen[sid] = true
		uniqueSources = append(uniqueSources, sid)
	}
	if len(uniqueSources) == 0 {
		return nil, ErrMergeNoSources
	}

	// 先取全部源单并校验（先校验后写：任一非法则整体不写，避免部分合并的脏状态）。
	sources := make([]*ent.Incident, 0, len(uniqueSources))
	for _, sid := range uniqueSources {
		srcInc, err := s.db.Incident.Get(ctx, sid)
		if err != nil {
			if ent.IsNotFound(err) {
				return nil, fmt.Errorf("%w: source %d", ErrNotFound, sid)
			}
			return nil, fmt.Errorf("get source incident %d: %w", sid, err)
		}
		if isTerminalOrMerged(srcInc) {
			return nil, fmt.Errorf("%w: source %d (status=%s)", ErrMergeSourceTerminal, sid, srcInc.Status)
		}
		sources = append(sources, srcInc)
	}

	// 逐源单执行合并写入。
	mergedIDs := make([]int, 0, len(sources))
	for _, srcInc := range sources {
		if err := s.mergeOne(ctx, target, srcInc, actorID, src); err != nil {
			return nil, err
		}
		mergedIDs = append(mergedIDs, srcInc.ID)
	}

	// 主单记 incident_merged 时间线（合入了哪些单），并落 IncidentAction merge 审计。
	s.record(ctx, target, timelineitem.TypeMerged, actorID, src,
		fmt.Sprintf("%s 合并了 %d 个事件到本单", actorLabel(actorID), len(mergedIDs)),
		map[string]any{"merged_source_ids": mergedIDs, "role": "target"})
	s.recordMergeAction(ctx, target.ID, actorID, src, mergedIDs)

	// 回读主单最新快照（responders 已并入）。
	fresh, err := s.db.Incident.Query().
		Where(incident.IDEQ(target.ID)).
		WithResponders().
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("reload merged target: %w", err)
	}
	return fresh, nil
}

// mergeOne 把单个源单合并进主单：转移 events/responders、置终态、记时间线、发事件。
func (s *Service) mergeOne(ctx context.Context, target, srcInc *ent.Incident, actorID int, src Source) error {
	// 1. related_events 转移：源单关联的 Event 改挂主单（Event.incident 边为 Unique，改指即转移）。
	//    用 UpdateOneID 逐条改指；数量可控（同故障聚合的 event 有限）。
	eventIDs, err := srcInc.QueryEvents().IDs(ctx)
	if err != nil {
		return fmt.Errorf("query source %d events: %w", srcInc.ID, err)
	}
	for _, eid := range eventIDs {
		if err := s.db.Event.UpdateOneID(eid).SetIncidentID(target.ID).Exec(ctx); err != nil {
			return fmt.Errorf("transfer event %d to target: %w", eid, err)
		}
	}

	// 2. responders 转移：源单响应者并入主单（AddResponderIDs 幂等去重——已在主单集合的不重复加）。
	responderIDs, err := srcInc.QueryResponders().IDs(ctx)
	if err != nil {
		return fmt.Errorf("query source %d responders: %w", srcInc.ID, err)
	}
	if len(responderIDs) > 0 {
		if err := s.db.Incident.UpdateOneID(target.ID).
			AddResponderIDs(responderIDs...).Exec(ctx); err != nil {
			return fmt.Errorf("merge responders into target: %w", err)
		}
	}

	// 3. 源单置终态：merged_into 指向主单 + status=closed（合并即收口）。
	//    closed_at 一并落，与人工 close 语义一致（单据生命周期收口时间可查）。
	updatedSrc, err := s.db.Incident.UpdateOneID(srcInc.ID).
		SetMergedInto(fmt.Sprintf("%d", target.ID)).
		SetStatus(incident.StatusClosed).
		SetClosedAt(time.Now()).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("close source incident %d: %w", srcInc.ID, err)
	}

	// 4. 源单记 merged 时间线（并入了哪个主单）+ 发布 IncidentMerged（取消 pending 升级 / 多端同步）。
	s.record(ctx, updatedSrc, timelineitem.TypeMerged, actorID, src,
		fmt.Sprintf("%s 将本单并入主单 #%d", actorLabel(actorID), target.ID),
		map[string]any{"merged_into": target.ID, "role": "source"})
	s.publish(ctx, event.IncidentMerged, updatedSrc, ActionMerge, actorID, src)
	return nil
}

// recordMergeAction 主单落一条 IncidentAction merge 审计（who/via + 源单 id 列表）。
//
// 为什么不走事件总线（ActionRecorder）：ActionRecorder 订阅的是「每张单自身状态变更」类事件，
// 而 merge 是主单吸收多张源单的聚合动作——主单本身状态不变，故不发主单事件，直接在此落审计。
// 源单侧的收口另由 IncidentMerged 事件驱动多端同步，但源单是被动收口，审计以主单 merge 一条为准
// （payload.source_ids 已记全部源单，可追溯合并了哪些）。best-effort：写失败仅记日志不阻断合并。
func (s *Service) recordMergeAction(ctx context.Context, targetID, actorID int, src Source, sourceIDs []int) {
	create := s.db.IncidentAction.Create().
		SetIncidentID(targetID).
		SetType(incidentaction.TypeMerge).
		SetActor(actorMap(actorID)).
		SetVia(viaFromEvent(string(src))). // Source 底层为 string，复用 event via 映射（web/im/api/automation）
		SetResult(incidentaction.ResultSuccess).
		SetPayload(map[string]any{"action": string(ActionMerge), "source_ids": sourceIDs})
	if _, err := create.Save(ctx); err != nil {
		// 审计写失败不回滚合并（合并已成功落库）；记录供排查。
		slog.Warn("merge: record incident action failed",
			"target_id", targetID, "source_ids", sourceIDs, "error", err)
	}
}

// isTerminalOrMerged 源/目标是否已 closed 或已被合并（merged_into 非空）——两者都不可再参与合并。
func isTerminalOrMerged(inc *ent.Incident) bool {
	return incident.Status(inc.Status) == incident.StatusClosed || inc.MergedInto != ""
}
