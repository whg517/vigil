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
func (r *Recorder) RecordRunbook(ctx context.Context, incID int, stepName, output string, success bool) error {
	content := fmt.Sprintf("执行 Runbook 步骤 %q", stepName)
	if !success {
		content += "（失败）"
	}
	return r.Record(ctx, incID, timelineitem.TypeRunbookExecuted, content,
		Actor{Kind: "system"}, timelineitem.SourceSystem,
		map[string]any{"step": stepName, "success": success, "output": output})
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
