// retention.go Event/RawEvent 保留清理巡检（T6.2，能力域 15 平台化长尾 / M15）。
//
// 背景：Event 是海量不可变的原始信号（设计基线第 2 条：Event/Incident 分离，Event 只追加
// 不修改）。无保留策略时长期堆积会持续吃存储，且大表拖慢查询。本巡检周期性删除超过保留期的
// 旧 Event/RawEvent 释放存储。
//
// ★ 安全约束（本文件强制保证）：
//   - **保护活跃 Incident 的证据**：只删「关联的 Incident 已 closed，或无关联 Incident」的 Event。
//     活跃处理单元（triggered/escalated/acked/resolved——任何未 closed 态）引用的 Event 是处置/
//     复盘的证据，绝不删；即使已过保留期也保留到 Incident closed。
//   - **批量分页删除**：每批限量（config.Retention.BatchSize），逐批删避免一次删百万行的大事务锁表。
//   - **RawEvent 独立更短保留期**：原始 payload 字节体积大、价值随时间衰减快，默认保留期短于 Event，
//     且 RawEvent 无 Incident 关联（仅接入点归属），按 created_at 直接过期即可清。
//
// 触发：既支持 Asynq 定时任务（TaskCleanup，可外部编排），也支持装配层 ticker 直接周期调
// （与 ingestion.RequeueSweeper / analytics.Snapshotter 同款 goroutine，纳入优雅关闭）。
// 二者调同一 Sweep，幂等（无可删即 no-op）。
package event

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/kevin/vigil/ent"
	entevent "github.com/kevin/vigil/ent/event"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/rawevent"

	"github.com/hibiken/asynq"
)

// TaskCleanup 定时清理任务类型（低优先级队列，可延迟）。
const TaskCleanup = "vigil:event_cleanup"

// RetentionSweeper 周期性清理超保留期的 Event/RawEvent。
type RetentionSweeper struct {
	db *ent.Client
	// eventTTL/rawEventTTL 保留期。<=0 表示对应清理不启用（永不删）。
	eventTTL    time.Duration
	rawEventTTL time.Duration
	batch       int
	interval    time.Duration
}

// NewRetentionSweeper 构造清理巡检器。
//
// eventDays/rawEventDays<=0 表示对应清理不启用（该类型永不删）；batch<=0 用默认 500；
// interval<=0 用默认 6h。天数在此转为 Duration（内部统一按时间比较）。
func NewRetentionSweeper(db *ent.Client, eventDays, rawEventDays, batch int, interval time.Duration) *RetentionSweeper {
	if batch <= 0 {
		batch = 500
	}
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	s := &RetentionSweeper{db: db, batch: batch, interval: interval}
	if eventDays > 0 {
		s.eventTTL = time.Duration(eventDays) * 24 * time.Hour
	}
	if rawEventDays > 0 {
		s.rawEventTTL = time.Duration(rawEventDays) * 24 * time.Hour
	}
	return s
}

// Interval 返回巡检间隔（供装配层日志/关闭逻辑参考）。
func (s *RetentionSweeper) Interval() time.Duration { return s.interval }

// Enabled 是否有任一类型启用了清理（两者都未配则无需启动巡检）。
func (s *RetentionSweeper) Enabled() bool { return s.eventTTL > 0 || s.rawEventTTL > 0 }

// Run 阻塞运行巡检循环，ctx 取消时退出（纳入优雅关闭）。
// 装配层 go s.Run(ctx) 启动，把 cancel 收入 Wired.Closers。
func (s *RetentionSweeper) Run(ctx context.Context) {
	if !s.Enabled() {
		return // 未配任何保留期，不启动（永不删，向后兼容）
	}
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ev, raw, err := s.Sweep(ctx)
			if err != nil {
				slog.Warn("retention sweeper failed", "error", err)
				continue
			}
			if ev > 0 || raw > 0 {
				slog.Info("retention sweep", "events_deleted", ev, "raw_events_deleted", raw)
			}
		}
	}
}

// Sweep 执行一轮清理，返回删除的 Event / RawEvent 条数。幂等（无可删即 0）。
// 两类清理各自独立（一类失败不影响另一类），任一出错回传首个错误。
func (s *RetentionSweeper) Sweep(ctx context.Context) (eventsDeleted, rawEventsDeleted int, err error) {
	now := time.Now()
	var firstErr error
	if s.eventTTL > 0 {
		n, e := s.sweepEvents(ctx, now.Add(-s.eventTTL))
		eventsDeleted = n
		if e != nil {
			firstErr = e
		}
	}
	if s.rawEventTTL > 0 {
		n, e := s.sweepRawEvents(ctx, now.Add(-s.rawEventTTL))
		rawEventsDeleted = n
		if e != nil && firstErr == nil {
			firstErr = e
		}
	}
	return eventsDeleted, rawEventsDeleted, firstErr
}

// sweepEvents 分页删除 created_at 早于 cutoff 且未被活跃 Incident 引用的 Event。
//
// 保护条件（★ 安全核心）：删除集 = created_at < cutoff AND (无关联 Incident OR 关联 Incident 已 closed)。
// 换言之，只要 Event 关联的 Incident 尚未 closed（triggered/escalated/acked/resolved 任一活跃态），
// 该 Event 就是活跃处置证据，即使已过保留期也保留。
//
// 分页：每批取一批 id 再按 id 删（限量 batch），删满一批继续下一批直到不足一批（无更多可删）。
func (s *RetentionSweeper) sweepEvents(ctx context.Context, cutoff time.Time) (int, error) {
	// 「未被活跃 Incident 引用」谓词：无 incident 边，或有 incident 边但其 status=closed。
	protectedByActive := entevent.Or(
		entevent.Not(entevent.HasIncident()),
		entevent.HasIncidentWith(incident.StatusEQ(incident.StatusClosed)),
	)
	var total int
	for {
		ids, err := s.db.Event.Query().
			Where(
				entevent.CreatedAtLT(cutoff),
				protectedByActive,
			).
			Order(ent.Asc(entevent.FieldCreatedAt)). // 老的先删（FIFO）
			Limit(s.batch).
			IDs(ctx)
		if err != nil {
			return total, fmt.Errorf("query stale events: %w", err)
		}
		if len(ids) == 0 {
			break
		}
		n, err := s.db.Event.Delete().Where(entevent.IDIn(ids...)).Exec(ctx)
		if err != nil {
			return total, fmt.Errorf("delete stale events: %w", err)
		}
		total += n
		if len(ids) < s.batch {
			break // 不足一批：已无更多可删
		}
	}
	return total, nil
}

// sweepRawEvents 分页删除 created_at 早于 cutoff 的 RawEvent。
//
// RawEvent 是原始 payload 暂存，无 Incident 关联（仅接入点归属），过重放窗口即可清；
// 但不删仍待回灌的 requeued（尚未成功归一化，删了会丢告警）——只删终态（normalized/parse_failed/received）。
// 分页同 sweepEvents。
func (s *RetentionSweeper) sweepRawEvents(ctx context.Context, cutoff time.Time) (int, error) {
	var total int
	for {
		ids, err := s.db.RawEvent.Query().
			Where(
				rawevent.CreatedAtLT(cutoff),
				// requeued 尚未成功归一化，仍需回灌，删了会丢告警——排除，只清终态。
				rawevent.StatusNEQ(rawevent.StatusRequeued),
			).
			Order(ent.Asc(rawevent.FieldCreatedAt)).
			Limit(s.batch).
			IDs(ctx)
		if err != nil {
			return total, fmt.Errorf("query stale raw_events: %w", err)
		}
		if len(ids) == 0 {
			break
		}
		n, err := s.db.RawEvent.Delete().Where(rawevent.IDIn(ids...)).Exec(ctx)
		if err != nil {
			return total, fmt.Errorf("delete stale raw_events: %w", err)
		}
		total += n
		if len(ids) < s.batch {
			break
		}
	}
	return total, nil
}

// —— Asynq 任务 ——

// EnqueueCleanup 构造定时清理任务（低优先级队列，可延迟，不与升级/接入争资源）。
func EnqueueCleanup() (*asynq.Task, error) {
	// 无载荷（保留期由装配时配置固化）；预留 JSON 以便未来传参。
	payload, err := json.Marshal(struct{}{})
	if err != nil {
		return nil, fmt.Errorf("marshal cleanup payload: %w", err)
	}
	return asynq.NewTask(TaskCleanup, payload, asynq.Queue("low")), nil
}

// HandleTask 消费清理任务（Asynq worker，与 ticker 调同一 Sweep，幂等互不冲突）。
func (s *RetentionSweeper) HandleTask(ctx context.Context, _ *asynq.Task) error {
	ev, raw, err := s.Sweep(ctx)
	if err != nil {
		return fmt.Errorf("event cleanup: %w", err)
	}
	slog.Info("event cleanup task", "events_deleted", ev, "raw_events_deleted", raw)
	return nil
}
