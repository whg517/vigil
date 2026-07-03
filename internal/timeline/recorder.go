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

// Recorder 统一时间线记录器。各域（escalation/runbook/...）通过它写时间线。
// 实现了 runbook.TimelineRecorder 接口（RecordRunbook）。
type Recorder struct {
	db *ent.Client
}

// NewRecorder 创建记录器。
func NewRecorder(db *ent.Client) *Recorder {
	return &Recorder{db: db}
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
	return create.Exec(ctx)
}

// RecordRunbook 实现 runbook.TimelineRecorder 接口。
// 让 runbook 引擎无需感知 ent 类型细节，统一通过 Recorder 记录。
// actorID 为执行发起人（0 视为系统），据此在时间线留痕"谁执行了该步"（C.5.3）。
func (r *Recorder) RecordRunbook(ctx context.Context, incID int, stepName, output string, success bool, actorID int) error {
	content := fmt.Sprintf("执行 Runbook 步骤 %q", stepName)
	if !success {
		content += "（失败）"
	}
	actor, source := runbookActor(actorID)
	return r.Record(ctx, incID, timelineitem.TypeRunbookExecuted, content,
		actor, source,
		map[string]any{"step": stepName, "success": success, "output": output})
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
