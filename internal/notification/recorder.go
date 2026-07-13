// recorder.go 通知送达记录（M13 / B22）。
//
// 每次向某人某通道发送/静默/失败，落一条 Notification 记录，使：
//   - 送达三态（sent/failed/suppressed）+ pending 可查、可补发、有 metrics 数据源；
//   - 被静默时段拦截的通知不再直接丢弃无痕（B22）；
//   - 全通道失败时可查出 failed 记录并触发兜底告警。
//
// 与 escalation 的送达回调解耦：notifier 只调 DeliveryRecorder.Record，
// 装配方（wire.go）注入本实现（有 db）或 nil（单测降级为不落库）。
package notification

import (
	"context"
	"fmt"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/incident"
	entnotification "github.com/kevin/vigil/ent/notification"
)

// DeliveryStatus 送达状态（与 ent Notification.status 枚举对齐）。
type DeliveryStatus string

const (
	// StatusPending 已入队/在途（重试中）。
	StatusPending DeliveryStatus = "pending"
	// StatusSent 已送达（通道返回成功）。
	StatusSent DeliveryStatus = "sent"
	// StatusFailed 发送失败（含全通道失败兜底后仍失败）。
	StatusFailed DeliveryStatus = "failed"
	// StatusSuppressed 被静默时段拦截（未发，可补发）—— B22 不再直接丢弃。
	StatusSuppressed DeliveryStatus = "suppressed"
)

// DeliveryRecord 一次送达尝试的记录（落 Notification 表）。
type DeliveryRecord struct {
	IncidentID int            // 关联事件 ID，0=未路由兜底（无单）
	UserID     int            // 关联用户 ID，0=无（群/webhook）
	Channel    string         // im|phone|sms|email|webhook
	Target     string         // 送达目标标识：user id/email/phone/url
	Status     DeliveryStatus // sent|failed|suppressed|pending
	Reason     string         // 状态原因：失败错误/静默原因/兜底说明
	Level      int            // 升级层级
	Severity   string         // 严重度快照
}

// DeliveryRecorder 送达记录落库接口。db 为 nil 时装配方传 nil，notifier 降级不落库。
type DeliveryRecorder interface {
	Record(ctx context.Context, rec DeliveryRecord) error
}

// DeliveryStore 送达记录存取扩展（投递 Asynq 化，ADR-0017 修订）。
//
// 异步投递以 Notification 行为锚点：入队前先落一条 pending 行，行 ID 即任务幂等键
// （TaskID=notif:{id}）；任务每次尝试后回写状态——成功置 sent、重试中更新 reason、
// 重试耗尽置 failed。与 DeliveryRecorder 的"只追加"不同，pending 行是唯一会被更新的行
// （终态一旦落定不再变，worker 以此做幂等守卫）。
type DeliveryStore interface {
	DeliveryRecorder
	// CreatePending 先落一条 pending 行，返回行 ID（作 Asynq TaskID 幂等键）。
	CreatePending(ctx context.Context, rec DeliveryRecord) (int, error)
	// UpdateStatus 回写行状态/通道/目标/原因。channel/target 为空时保留原值。
	UpdateStatus(ctx context.Context, id int, status DeliveryStatus, channel, target, reason string) error
	// Status 读行当前状态（worker 幂等守卫：非 pending 即已处理过，跳过）。
	Status(ctx context.Context, id int) (DeliveryStatus, error)
}

// entDeliveryRecorder 用 ent 落库的送达记录器。
type entDeliveryRecorder struct {
	db *ent.Client
}

// NewDeliveryRecorder 创建基于 ent 的送达记录器。
func NewDeliveryRecorder(db *ent.Client) DeliveryRecorder {
	return &entDeliveryRecorder{db: db}
}

// NewDeliveryStore 创建基于 ent 的送达记录存取器（异步投递用，含 pending 行更新能力）。
func NewDeliveryStore(db *ent.Client) DeliveryStore {
	return &entDeliveryRecorder{db: db}
}

// Record 落一条 Notification 记录（只追加，不修改）。
func (r *entDeliveryRecorder) Record(ctx context.Context, rec DeliveryRecord) error {
	if r.db == nil {
		return nil
	}
	b := r.db.Notification.Create().
		SetChannel(rec.Channel).
		SetTarget(rec.Target).
		SetUserID(rec.UserID).
		SetStatus(entnotification.Status(rec.Status)).
		SetReason(rec.Reason).
		SetLevel(rec.Level).
		SetSeverity(rec.Severity)
	if rec.IncidentID > 0 {
		b = b.SetIncidentID(rec.IncidentID)
	}
	return b.Exec(ctx)
}

// CreatePending 先落一条 pending 行，返回行 ID（异步投递的任务幂等键锚点）。
func (r *entDeliveryRecorder) CreatePending(ctx context.Context, rec DeliveryRecord) (int, error) {
	if r.db == nil {
		return 0, fmt.Errorf("delivery store: no db")
	}
	b := r.db.Notification.Create().
		SetChannel(rec.Channel).
		SetTarget(rec.Target).
		SetUserID(rec.UserID).
		SetStatus(entnotification.StatusPending).
		SetReason(rec.Reason).
		SetLevel(rec.Level).
		SetSeverity(rec.Severity)
	if rec.IncidentID > 0 {
		b = b.SetIncidentID(rec.IncidentID)
	}
	row, err := b.Save(ctx)
	if err != nil {
		return 0, err
	}
	return row.ID, nil
}

// UpdateStatus 回写行状态（任务每次尝试后调用）。channel/target 为空时保留原值，
// 避免降级链走到兜底通道时把「实际送达通道」误清空。
func (r *entDeliveryRecorder) UpdateStatus(ctx context.Context, id int, status DeliveryStatus, channel, target, reason string) error {
	if r.db == nil {
		return nil
	}
	u := r.db.Notification.UpdateOneID(id).
		SetStatus(entnotification.Status(status)).
		SetReason(reason)
	if channel != "" {
		u = u.SetChannel(channel)
	}
	if target != "" {
		u = u.SetTarget(target)
	}
	return u.Exec(ctx)
}

// Status 读行当前状态（worker 幂等守卫用）。
func (r *entDeliveryRecorder) Status(ctx context.Context, id int) (DeliveryStatus, error) {
	if r.db == nil {
		return "", fmt.Errorf("delivery store: no db")
	}
	row, err := r.db.Notification.Get(ctx, id)
	if err != nil {
		return "", err
	}
	return DeliveryStatus(row.Status), nil
}

// QueryByIncident 查询某事件的送达记录（按时间升序，分页）。
// 供 GET /incidents/:id/notifications 端点用。
func QueryByIncident(ctx context.Context, db *ent.Client, incID, limit, offset int) ([]*ent.Notification, int, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	base := db.Notification.Query().
		Where(entnotification.HasIncidentWith(incident.IDEQ(incID)))
	total, err := base.Clone().Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	q := base.
		Order(ent.Asc(entnotification.FieldCreatedAt)).
		Limit(limit)
	if offset > 0 {
		q = q.Offset(offset)
	}
	items, err := q.All(ctx)
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}
