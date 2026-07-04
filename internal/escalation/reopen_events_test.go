package escalation

import (
	"context"
	"testing"

	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/schema"
	domainevent "github.com/kevin/vigil/internal/event"

	"github.com/hibiken/asynq"
)

// countTasks 统计 critical 队列里 pending + scheduled 的任务数（不含已触发/已完成）。
func countTasks(t *testing.T, addr string) int {
	t.Helper()
	inspector := asynq.NewInspector(asynq.RedisClientOpt{Addr: addr})
	defer func() { _ = inspector.Close() }()
	pending, _ := inspector.ListPendingTasks("critical", asynq.PageSize(50))
	scheduled, _ := inspector.ListScheduledTasks("critical", asynq.PageSize(50))
	return len(pending) + len(scheduled)
}

// reopenToTriggered 把 seeded incident 打到 resolved 再置回 triggered（模拟 reopen 后的状态）。
// 同时把 current_level/escalated_count 抬高，验证 OnReopened 会归零。
func reopenToTriggered(t *testing.T, env *escTestEnv, id int) {
	t.Helper()
	if err := env.client.Incident.UpdateOneID(id).
		SetStatus(incident.StatusTriggered).
		SetCurrentLevel(2).
		SetEscalatedCount(3).
		Exec(context.Background()); err != nil {
		t.Fatalf("set reopened state: %v", err)
	}
}

// TestOnReopened_RestartsFromFirstLevel 验证 reopen 后升级链从首层（level 0）重启：
// ① 重排了首层计时器任务；② current_level/escalated_count 归零。
func TestOnReopened_RestartsFromFirstLevel(t *testing.T) {
	levels := []schema.EscalationLevel{
		{Level: 1, DelayMinutes: 5, Targets: []schema.Target{{Type: "user", TargetID: "1"}}},
		{Level: 2, DelayMinutes: 10, Targets: []schema.Target{{Type: "user", TargetID: "2"}}},
	}
	env := newEscEnv(t, levels, 0)
	id := env.incID(t)
	reopenToTriggered(t, env, id)

	inc, _ := env.client.Incident.Get(context.Background(), id)
	if err := env.engine.OnReopened(context.Background(), domainevent.Event{
		Type:     domainevent.IncidentReopened,
		Incident: inc,
	}); err != nil {
		t.Fatalf("OnReopened: %v", err)
	}

	// 首层 delay>0 → scheduled 队列应有恰好 1 个任务（level[0] 的计时器），不多不少。
	inspector := asynq.NewInspector(asynq.RedisClientOpt{Addr: env.mr.Addr()})
	defer func() { _ = inspector.Close() }()
	scheduled, _ := inspector.ListScheduledTasks("critical", asynq.PageSize(50))
	if len(scheduled) != 1 {
		t.Fatalf("scheduled tasks after reopen: got %d, want 1 (level 0 timer)", len(scheduled))
	}
	// 校验重排的是首层任务（TaskID esc:id:0:0）。
	if want := escalationTaskID(id, 0, 0); scheduled[0].ID != want {
		t.Errorf("scheduled task id: got %q, want %q (first level)", scheduled[0].ID, want)
	}

	// 升级层级归零。
	got, _ := env.client.Incident.Get(context.Background(), id)
	if got.CurrentLevel != 0 {
		t.Errorf("current_level after reopen: got %d, want 0", got.CurrentLevel)
	}
	if got.EscalatedCount != 0 {
		t.Errorf("escalated_count after reopen: got %d, want 0", got.EscalatedCount)
	}
}

// TestOnReopened_TimerFiresNotifiesAndAdvances 验证 reopen 重排的首层计时器到点后，
// HandleTask 正常重发通知 + 推进层级（复用现有升级逻辑）。
func TestOnReopened_TimerFiresNotifiesAndAdvances(t *testing.T) {
	levels := []schema.EscalationLevel{
		{Level: 1, DelayMinutes: 5, Targets: []schema.Target{{Type: "user", TargetID: "1"}}},
		{Level: 2, DelayMinutes: 10, Targets: []schema.Target{{Type: "user", TargetID: "2"}}},
	}
	env := newEscEnv(t, levels, 0)
	id := env.incID(t)
	// 建 target 用户，使 resolveTargets 能解析出人。
	env.client.User.Create().SetUsername("u1").SetEmail("u1@x").SaveX(context.Background())
	reopenToTriggered(t, env, id)

	inc, _ := env.client.Incident.Get(context.Background(), id)
	if err := env.engine.OnReopened(context.Background(), domainevent.Event{
		Type:     domainevent.IncidentReopened,
		Incident: inc,
	}); err != nil {
		t.Fatalf("OnReopened: %v", err)
	}

	// 模拟首层计时器到点：直接跑 level 0 的 HandleTask。
	env.runEscTask(t, id, 0, 0)

	// 重发通知（level 0）。
	if len(env.notifier.calls) != 1 || env.notifier.calls[0].Level != 0 {
		t.Errorf("notifier calls after reopen timer: got %+v, want 1 call at level 0", env.notifier.calls)
	}
	// 推进层级：status=escalated、current_level=1（从 0 推进到 1，证明确从首层起）。
	got, _ := env.client.Incident.Get(context.Background(), id)
	if got.Status != incident.StatusEscalated {
		t.Errorf("status: got %s, want escalated", got.Status)
	}
	if got.CurrentLevel != 1 {
		t.Errorf("current_level: got %d, want 1 (advanced from first level)", got.CurrentLevel)
	}
	if got.EscalatedCount != 1 {
		t.Errorf("escalated_count: got %d, want 1", got.EscalatedCount)
	}
}

// TestOnReopened_ThenAckCancels 验证 reopen 重启升级链后，ack 仍能取消它。
func TestOnReopened_ThenAckCancels(t *testing.T) {
	levels := []schema.EscalationLevel{
		{Level: 1, DelayMinutes: 5, Targets: []schema.Target{{Type: "user", TargetID: "1"}}},
		{Level: 2, DelayMinutes: 10, Targets: []schema.Target{{Type: "user", TargetID: "2"}}},
	}
	env := newEscEnv(t, levels, 1)
	id := env.incID(t)
	reopenToTriggered(t, env, id)

	inc, _ := env.client.Incident.Get(context.Background(), id)
	if err := env.engine.OnReopened(context.Background(), domainevent.Event{
		Type:     domainevent.IncidentReopened,
		Incident: inc,
	}); err != nil {
		t.Fatalf("OnReopened: %v", err)
	}
	if n := countTasks(t, env.mr.Addr()); n == 0 {
		t.Fatal("expected escalation tasks after reopen, got 0")
	}

	// ack → OnAcked 取消。用重取的 incident（带 policy edge 查询能力）。
	acked, _ := env.client.Incident.Get(context.Background(), id)
	if err := env.engine.OnAcked(context.Background(), domainevent.Event{
		Type:     domainevent.IncidentAcked,
		Incident: acked,
	}); err != nil {
		t.Fatalf("OnAcked after reopen: %v", err)
	}
	if n := countTasks(t, env.mr.Addr()); n != 0 {
		t.Errorf("tasks after ack: got %d, want 0 (reopened chain should be cancelable)", n)
	}
}

// TestOnReopened_NoDuplicateTasks 验证 reopen 前有残留 pending 任务时，
// 重启不产生重复升级任务（先清残留再排首层，同 TaskID 不叠加）。
func TestOnReopened_NoDuplicateTasks(t *testing.T) {
	levels := []schema.EscalationLevel{
		{Level: 1, DelayMinutes: 5, Targets: []schema.Target{{Type: "user", TargetID: "1"}}},
	}
	env := newEscEnv(t, levels, 0)
	id := env.incID(t)

	// 模拟残留：reopen 前先排一次首层任务（如上次响应周期未清干净）。
	if err := env.engine.StartEscalation(context.Background(), id, levels); err != nil {
		t.Fatalf("seed residual StartEscalation: %v", err)
	}
	if n := countTasks(t, env.mr.Addr()); n != 1 {
		t.Fatalf("residual tasks: got %d, want 1", n)
	}

	reopenToTriggered(t, env, id)
	inc, _ := env.client.Incident.Get(context.Background(), id)
	if err := env.engine.OnReopened(context.Background(), domainevent.Event{
		Type:     domainevent.IncidentReopened,
		Incident: inc,
	}); err != nil {
		t.Fatalf("OnReopened: %v", err)
	}

	// 清残留 + 重排首层后仍只应有 1 个 level[0] 任务，不是 2 个。
	if n := countTasks(t, env.mr.Addr()); n != 1 {
		t.Errorf("tasks after reopen with residual: got %d, want 1 (no duplicate)", n)
	}
}

// TestOnReopened_NoPolicy 无升级策略的 incident reopen 时安全跳过（不排任务、不改状态）。
func TestOnReopened_NoPolicy(t *testing.T) {
	env := newEscEnv(t, nil, 0)
	// 另建一个不绑策略的 incident。
	ctx := context.Background()
	team, _ := env.client.Team.Query().Only(ctx)
	inc := env.client.Incident.Create().
		SetNumber("INC-9999").
		SetTitle("no-policy").
		SetSeverity(incident.SeverityCritical).
		SetStatus(incident.StatusTriggered).
		SetTeamID(team.ID).
		SaveX(ctx)

	if err := env.engine.OnReopened(ctx, domainevent.Event{
		Type:     domainevent.IncidentReopened,
		Incident: inc,
	}); err != nil {
		t.Fatalf("OnReopened no-policy: %v", err)
	}
	if n := countTasks(t, env.mr.Addr()); n != 0 {
		t.Errorf("no-policy reopen should enqueue nothing, got %d", n)
	}
}
