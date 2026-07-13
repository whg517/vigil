package escalation

import (
	"context"
	"testing"
	"time"

	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/schema"

	"github.com/hibiken/asynq"
)

// newSweeperEnv 在 escTestEnv 基础上构造对账巡检器（复用同一 miniredis / engine / db）。
func newSweeperEnv(t *testing.T, levels []schema.EscalationLevel, repeatTimes int) (*escTestEnv, *Sweeper) {
	t.Helper()
	env := newEscEnv(t, levels, repeatTimes)
	sw := NewSweeper(env.client, env.engine, &asynq.RedisClientOpt{Addr: env.mr.Addr()}, time.Minute)
	return env, sw
}

// twoDelayedLevels 两层带延迟的升级链（延迟>0 使任务落 scheduled 态，便于 Inspector 断言）。
func twoDelayedLevels() []schema.EscalationLevel {
	return []schema.EscalationLevel{
		{Level: 1, DelayMinutes: 10, Targets: []schema.Target{{Type: "user", TargetID: "1"}}},
		{Level: 2, DelayMinutes: 10, Targets: []schema.Target{{Type: "user", TargetID: "2"}}},
	}
}

// TestSweeper_MissingTaskRescheduled 核心恢复路径：活跃 Incident 应有升级任务而队列全空
// （模拟 Redis 丢数据，队列 key 都不存在），巡检应从 current_level 重排；
// 第二轮巡检任务已在队，不再动作（幂等收敛）。
func TestSweeper_MissingTaskRescheduled(t *testing.T) {
	env, sw := newSweeperEnv(t, twoDelayedLevels(), 0)
	id := env.incID(t)
	ctx := context.Background()

	// 队列从未入过任务（等价 Redis 被清空后的状态：queue key 不存在）
	if n := sw.sweepOnce(ctx); n != 1 {
		t.Fatalf("sweepOnce: got %d rescheduled, want 1", n)
	}
	inspector := asynq.NewInspector(asynq.RedisClientOpt{Addr: env.mr.Addr()})
	defer func() { _ = inspector.Close() }()
	// current_level=0（未升级过）→ 重排 level[0] 首发
	assertScheduledTaskID(t, inspector, escalationTaskID(id, 0, 0))

	// 幂等收敛：任务已在队，第二轮不重复动作
	if n := sw.sweepOnce(ctx); n != 0 {
		t.Errorf("second sweepOnce: got %d, want 0 (task now present)", n)
	}
	scheduled, _ := inspector.ListScheduledTasks("critical", asynq.PageSize(10))
	if len(scheduled) != 1 {
		t.Errorf("scheduled tasks after two sweeps: got %d, want 1", len(scheduled))
	}
}

// TestSweeper_TaskExistsNoAction 任务健在（StartEscalation 已入队）时巡检不动作。
func TestSweeper_TaskExistsNoAction(t *testing.T) {
	env, sw := newSweeperEnv(t, twoDelayedLevels(), 0)
	id := env.incID(t)
	ctx := context.Background()

	if err := env.engine.StartEscalation(ctx, id, twoDelayedLevels()); err != nil {
		t.Fatalf("StartEscalation: %v", err)
	}
	if n := sw.sweepOnce(ctx); n != 0 {
		t.Errorf("sweepOnce with task present: got %d, want 0", n)
	}
	inspector := asynq.NewInspector(asynq.RedisClientOpt{Addr: env.mr.Addr()})
	defer func() { _ = inspector.Close() }()
	scheduled, _ := inspector.ListScheduledTasks("critical", asynq.PageSize(10))
	if len(scheduled) != 1 {
		t.Errorf("scheduled tasks: got %d, want 1 (sweeper must not duplicate)", len(scheduled))
	}
}

// TestSweeper_AckedIncidentNotRescheduled 已 ack 的 Incident 不在扫描条件内
// （状态守卫语义：ack 即停链），队列保持空。
func TestSweeper_AckedIncidentNotRescheduled(t *testing.T) {
	env, sw := newSweeperEnv(t, twoDelayedLevels(), 0)
	id := env.incID(t)
	ctx := context.Background()

	env.client.Incident.UpdateOneID(id).SetStatus(incident.StatusAcked).ExecX(ctx)

	if n := sw.sweepOnce(ctx); n != 0 {
		t.Errorf("sweepOnce on acked incident: got %d, want 0", n)
	}
	inspector := asynq.NewInspector(asynq.RedisClientOpt{Addr: env.mr.Addr()})
	defer func() { _ = inspector.Close() }()
	scheduled, err := inspector.ListScheduledTasks("critical", asynq.PageSize(10))
	if err == nil && len(scheduled) != 0 {
		t.Errorf("acked incident must not get tasks rescheduled, got %d", len(scheduled))
	}
}

// TestSweeper_MidChainRescheduleFromCurrentLevel 链推进到中途（level[0] 已处理，
// current_level=1）后任务丢失：应从 current_level（下一层）重排，而非从头。
func TestSweeper_MidChainRescheduleFromCurrentLevel(t *testing.T) {
	env, sw := newSweeperEnv(t, twoDelayedLevels(), 0)
	id := env.incID(t)
	ctx := context.Background()

	env.client.Incident.UpdateOneID(id).
		SetStatus(incident.StatusEscalated).
		SetCurrentLevel(1).
		SetEscalatedCount(1).
		ExecX(ctx)

	if n := sw.sweepOnce(ctx); n != 1 {
		t.Fatalf("sweepOnce: got %d, want 1", n)
	}
	inspector := asynq.NewInspector(asynq.RedisClientOpt{Addr: env.mr.Addr()})
	defer func() { _ = inspector.Close() }()
	assertScheduledTaskID(t, inspector, escalationTaskID(id, 1, 0))
	// 不应重排已处理过的 level[0]
	scheduled, _ := inspector.ListScheduledTasks("critical", asynq.PageSize(10))
	for _, task := range scheduled {
		if task.ID == escalationTaskID(id, 0, 0) {
			t.Errorf("level 0 already processed, must not be rescheduled: %s", task.ID)
		}
	}
}

// TestSweeper_ChainFinishedNotRescheduled 链已推进到末级处理完（current_level=len(levels)）：
// 无"应然"任务，不重排——此守卫兼防"末级重排→处理完→再重排"的自激通知循环。
func TestSweeper_ChainFinishedNotRescheduled(t *testing.T) {
	env, sw := newSweeperEnv(t, twoDelayedLevels(), 0)
	id := env.incID(t)
	ctx := context.Background()

	env.client.Incident.UpdateOneID(id).
		SetStatus(incident.StatusEscalated).
		SetCurrentLevel(2). // = len(levels)，末级已处理完
		SetEscalatedCount(2).
		ExecX(ctx)

	if n := sw.sweepOnce(ctx); n != 0 {
		t.Errorf("sweepOnce on finished chain: got %d, want 0 (anti self-excitation)", n)
	}
}

// TestSweeper_NoRedisNoop 无 Redis 连接信息（降级装配）时巡检安全空转。
func TestSweeper_NoRedisNoop(t *testing.T) {
	env := newEscEnv(t, twoDelayedLevels(), 0)
	sw := NewSweeper(env.client, env.engine, nil, time.Minute)
	if n := sw.sweepOnce(context.Background()); n != 0 {
		t.Errorf("sweepOnce without redis: got %d, want 0", n)
	}
}
