package webhook

import (
	"context"
	"log/slog"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/webhookdelivery"
)

// EntRecorder 用 ent 落地出站投递记录（DeliveryRecorder 的生产实现，C24 死信）。
//
// 存储策略：成功也落一条（便于审计/统计送达率），失败落 status=failed（死信，可重放）。
// 全部 best-effort——记录失败只记日志，绝不回传/阻塞出站主流程（出站本就是 best-effort 同步语义）。
type EntRecorder struct {
	db  *ent.Client
	log *slog.Logger
}

// NewEntRecorder 构造 ent 投递记录器。db 为 nil 时 RecordDelivery 静默跳过（降级）。
func NewEntRecorder(db *ent.Client) *EntRecorder {
	return &EntRecorder{db: db, log: slog.Default()}
}

// RecordDelivery 落一条出站投递记录（实现 DeliveryRecorder）。
func (r *EntRecorder) RecordDelivery(ctx context.Context, rec DeliveryRecord) {
	if r == nil || r.db == nil {
		return
	}
	status := webhookdelivery.StatusFailed
	if rec.Success {
		status = webhookdelivery.StatusSuccess
	}
	create := r.db.WebhookDelivery.Create().
		SetURL(rec.URL).
		SetEvent(rec.Event).
		SetPayload(rec.Payload).
		SetStatus(status).
		SetAttempts(rec.Attempts).
		SetLastError(rec.LastError).
		SetLastStatusCode(rec.LastStatusCode)
	if rec.IncidentID > 0 {
		create = create.SetIncidentID(rec.IncidentID)
	}
	if _, err := create.Save(ctx); err != nil {
		// best-effort：记录失败不阻塞出站主流程（出站本身也是 best-effort）。
		r.log.Warn("record webhook delivery failed", "event", rec.Event, "url", rec.URL, "error", err)
	}
}
