// Package timeline 实现能力域 10：事件时间线。
//
// 对应 docs/capabilities/07-timeline-ai.md §A：
// · 自动捕获事件全程（系统/人工/AI 动作）
// · 统一 Recorder 供各域写时间线，消除重复
// · 查询 API（按 incident + 筛选 type/source + 分页）
// · 手动追加（响应者备注）
//
// 时间线是协同与复盘的事实基础——"全程留痕"。
package timeline

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/timelineitem"
)

// Actor 动作执行者。
type Actor struct {
	Kind string // system | user | integration | ai
	ID   string // 执行者标识（user id 等）
}

// TimelineBroadcaster 时间线新增广播器（B11）。
// 由 ws.Hub 实现（BroadcastTimelineAdded），装配时经 SetBroadcaster 注入。
// 抽象成接口而非直接依赖 ws 包，避免 timeline→ws 的包依赖（ws 已依赖 event，
// 若再让 timeline 依赖 ws 会增加耦合）。item 为刚写入的时间线条目（*ent.TimelineItem，
// 用 any 以免 ws 侧被迫依赖 ent 具体类型——ws 只作为 WS Data 透传下发）。
type TimelineBroadcaster interface {
	BroadcastTimelineAdded(incidentID int, item any)
}

// Recorder 统一时间线记录器。各域（escalation/runbook/...）通过它写时间线。
// 实现了 runbook.TimelineRecorder 接口（RecordRunbook）。
type Recorder struct {
	db *ent.Client
	// broadcaster B11：写入时间线后经此广播 timeline_added WS 消息，使 Web 详情页时间线实时刷新。
	// 为 nil 时跳过广播（降级/测试）。
	broadcaster TimelineBroadcaster
}

// NewRecorder 创建记录器。
func NewRecorder(db *ent.Client) *Recorder {
	return &Recorder{db: db}
}

// SetBroadcaster 注入时间线广播器（B11）。装配时传入 ws.Hub，使新增条目实时推 WS。
func (r *Recorder) SetBroadcaster(b TimelineBroadcaster) {
	r.broadcaster = b
}

// Record 写一条时间线。
// 这是核心写入 API，各域统一调用。
func (r *Recorder) Record(ctx context.Context, incID int, typ timelineitem.Type, content string, actor Actor, source timelineitem.Source, detail map[string]any) error {
	if content == "" {
		return fmt.Errorf("content required")
	}
	actorMap := map[string]string{"kind": actor.Kind}
	if actor.ID != "" {
		actorMap["id"] = actor.ID
	}
	create := r.db.TimelineItem.Create().
		SetIncidentID(incID).
		SetType(typ).
		SetContent(content).
		SetActor(actorMap).
		SetSource(source).
		SetTimestamp(time.Now())
	if detail != nil {
		create = create.SetDetail(detail)
	}
	item, err := create.Save(ctx)
	if err != nil {
		return err
	}
	// B11：写入成功后广播 timeline_added WS 消息，使 Web 详情页时间线实时刷新。
	// 广播为 best-effort（hub 内部对无订阅者静默跳过），不影响写入结果。
	if r.broadcaster != nil {
		r.broadcaster.BroadcastTimelineAdded(incID, item)
	}
	return nil
}

// RecordRunbook 实现 runbook.TimelineRecorder 接口。
// 让 runbook 引擎无需感知 ent 类型细节，统一通过 Recorder 记录。
// actorID 为执行发起人（0 视为系统），据此在时间线留痕"谁执行了该步"（C.5.3）。
// approved 记录本次执行是否经审批，让时间线可区分"已审批处置"与"只读干跑"（S10/C14）。
func (r *Recorder) RecordRunbook(ctx context.Context, incID int, stepName, output string, success, approved bool, actorID int) error {
	content := fmt.Sprintf("执行 Runbook 步骤 %q", stepName)
	if !success {
		content += "（失败）"
	}
	actor, source := runbookActor(actorID)
	return r.Record(ctx, incID, timelineitem.TypeRunbookExecuted, content,
		actor, source,
		map[string]any{"step": stepName, "success": success, "approved": approved, "output": output})
}

// RecordRunbookBlocked 实现 runbook.TimelineRecorder 接口：记录写步骤未获审批被阻断。
// human-in-the-loop 闸门生效时留痕"谁在何时尝试执行未获批的写操作"（安全审计，C.5.3）。
func (r *Recorder) RecordRunbookBlocked(ctx context.Context, incID int, stepName string, actorID int) error {
	content := fmt.Sprintf("Runbook 步骤 %q 为写操作，未获审批，已阻断", stepName)
	actor, source := runbookActor(actorID)
	return r.Record(ctx, incID, timelineitem.TypeRunbookExecuted, content,
		actor, source,
		map[string]any{"step": stepName, "blocked": true, "reason": "require_approval"})
}

// RecordRunbookSuggested 实现 runbook.TriggerRecorder 接口：记录「trigger 命中，自动展示关联 Runbook」。
//
// 语义区分（B13 安全红线）：这是「展示」而非「执行」——trigger（on_incident/on_severity/on_label_match）
// 命中后把关联 Runbook 呈现给响应者（写 runbook_suggested 时间线，Web/IM 可见），响应者一眼看到该用
// 哪个 Runbook，但绝不代表已执行任何步骤。actor.kind=system、source=system（引擎自动触发，非人工）。
// autoRunnable 标记该 Runbook 是否满足「全只读诊断 + 显式 auto_run」将被自动执行（供前端区分展示态）。
func (r *Recorder) RecordRunbookSuggested(ctx context.Context, incID, runbookID int, runbookName, triggerType string, autoRunnable bool) error {
	content := fmt.Sprintf("触发命中（%s），自动展示关联 Runbook「%s」", triggerType, runbookName)
	return r.Record(ctx, incID, timelineitem.TypeRunbookSuggested, content,
		Actor{Kind: "system"}, timelineitem.SourceSystem,
		map[string]any{
			"runbook_id":    runbookID,
			"runbook_name":  runbookName,
			"trigger_type":  triggerType,
			"auto_runnable": autoRunnable, // true=将自动执行（全只读诊断）；false=仅展示，须人工执行
		})
}

// runbookActor 把执行发起人 ID 映射为时间线 Actor + Source。
// actorID>0 → 人工发起（source=web，Runbook 执行入口目前仅 Web/API）；否则系统。
func runbookActor(actorID int) (Actor, timelineitem.Source) {
	if actorID > 0 {
		return Actor{Kind: "user", ID: strconv.Itoa(actorID)}, timelineitem.SourceWeb
	}
	return Actor{Kind: "system"}, timelineitem.SourceSystem
}

// Query 查询某事件的时间线。
// typeFilter/sourceFilter 为空时不限；limit 默认 100，上限 500。
func (r *Recorder) Query(ctx context.Context, incID int, typeFilter timelineitem.Type, sourceFilter timelineitem.Source, limit, offset int) ([]*ent.TimelineItem, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := r.db.TimelineItem.Query().
		Where(timelineitem.HasIncidentWith(incident.IDEQ(incID))).
		Order(ent.Asc(timelineitem.FieldTimestamp)).
		Limit(limit)
	if offset > 0 {
		q = q.Offset(offset)
	}
	if typeFilter != "" {
		q = q.Where(timelineitem.TypeEQ(typeFilter))
	}
	if sourceFilter != "" {
		q = q.Where(timelineitem.SourceEQ(sourceFilter))
	}
	return q.All(ctx)
}

// Count 统计某事件的时间线条目数（分页用）。
func (r *Recorder) Count(ctx context.Context, incID int) (int, error) {
	return r.db.TimelineItem.Query().
		Where(timelineitem.HasIncidentWith(incident.IDEQ(incID))).
		Count(ctx)
}
