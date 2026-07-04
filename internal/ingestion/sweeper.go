// sweeper.go 原始告警自动回灌巡检（T5.5，能力域 02 §B.15/B.10）。
//
// 场景：限流/背压/入队失败时，RawEvent 落库标记 requeued（payload 已保存，不丢告警），
// 但若无人回灌就永滞库里无人消费。本巡检周期性把 requeued 的 RawEvent 重新投入归一化队列，
// 使系统从过载/故障恢复后自动补投——无需人工逐条重放。
//
// 设计：goroutine ticker（与 server.runAggregationFlusher 同款，纳入优雅关闭），
// 每轮取一批 requeued（限量，避免一次回灌打爆队列），逐条重置回 received 并入队。
// 幂等：归一化落 Event 的唯一索引兜底，重复回灌不产重复 Event。
package ingestion

import (
	"context"
	"log/slog"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/rawevent"
)

// RequeueSweeper 周期性回灌 requeued 的 RawEvent。
type RequeueSweeper struct {
	db       *ent.Client
	ingest   *Handler // 复用 enqueueNormalize
	batch    int      // 单轮回灌上限（防打爆队列）
	interval time.Duration
}

// NewRequeueSweeper 构造巡检器。batch<=0 用默认 50，interval<=0 用默认 30s。
func NewRequeueSweeper(db *ent.Client, ingest *Handler, batch int, interval time.Duration) *RequeueSweeper {
	if batch <= 0 {
		batch = 50
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &RequeueSweeper{db: db, ingest: ingest, batch: batch, interval: interval}
}

// Interval 返回巡检间隔（供装配层日志/关闭逻辑参考）。
func (s *RequeueSweeper) Interval() time.Duration { return s.interval }

// Run 阻塞运行巡检循环，ctx 取消时退出（纳入优雅关闭）。
// 装配层 go s.Run(ctx) 启动，把 cancel 收入 Wired.Closers。
func (s *RequeueSweeper) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n := s.sweepOnce(ctx); n > 0 {
				slog.Info("requeue sweeper: re-enqueued", "count", n)
			}
		}
	}
}

// sweepOnce 回灌一批 requeued RawEvent，返回成功入队条数。
// 入队失败的保持 requeued（下轮再试）；成功的重置回 received（交归一化 worker 推进）。
func (s *RequeueSweeper) sweepOnce(ctx context.Context) int {
	if s.ingest == nil || s.ingest.queue == nil {
		return 0 // 无队列不回灌（测试桩/未装配）
	}
	rows, err := s.db.RawEvent.Query().
		Where(rawevent.StatusEQ(rawevent.StatusRequeued)).
		Order(ent.Asc(rawevent.FieldReceivedAt)). // 老的先回灌（FIFO，尽量不乱序）
		Limit(s.batch).
		WithIntegration().
		All(ctx)
	if err != nil {
		slog.Warn("requeue sweeper: query failed", "error", err)
		return 0
	}
	var done int
	for _, r := range rows {
		integ := r.Edges.Integration
		if integ == nil {
			// 无接入点归属无法归一化，跳过（保持 requeued，人工介入）。
			continue
		}
		// 先重置回 received 再入队（若入队失败下面回滚回 requeued）。
		if err := s.db.RawEvent.UpdateOneID(r.ID).
			SetStatus(rawevent.StatusReceived).
			SetError("").
			Exec(ctx); err != nil {
			continue
		}
		if err := s.ingest.enqueueNormalize(ctx, r.ID, integ.ID, integ.Type.String()); err != nil {
			// 入队仍失败：回滚回 requeued，下轮再试。
			_ = s.db.RawEvent.UpdateOneID(r.ID).
				SetStatus(rawevent.StatusRequeued).
				SetError("requeue enqueue failed: " + err.Error()).
				Exec(ctx)
			continue
		}
		done++
	}
	return done
}
