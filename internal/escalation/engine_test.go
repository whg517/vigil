package escalation

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/schema"
	"github.com/kevin/vigil/internal/config"
	"github.com/kevin/vigil/internal/queue"
	"github.com/kevin/vigil/internal/timeline"

	"github.com/alicebob/miniredis/v2"
	"github.com/hibiken/asynq"
	_ "github.com/mattn/go-sqlite3"
)

// TestEscalationTaskID 验证任务 ID 生成的稳定性与唯一性（用于幂等与取消）。
func TestEscalationTaskID(t *testing.T) {
	cases := []struct {
		incID, levelIdx, repeat int
		want                    string
	}{
		{42, 0, 0, "esc:42:0:0"},
		{42, 1, 0, "esc:42:1:0"},
		{42, 0, 2, "esc:42:0:2"},
		{1, 3, 1, "esc:1:3:1"},
	}
	for _, c := range cases {
		if got := escalationTaskID(c.incID, c.levelIdx, c.repeat); got != c.want {
			t.Errorf("escalationTaskID(%d,%d,%d): got %q, want %q", c.incID, c.levelIdx, c.repeat, got, c.want)
		}
	}
	// 唯一性：不同 (inc,level,repeat) 不撞
	seen := map[string]bool{}
	for inc := 1; inc <= 5; inc++ {
		for lv := 0; lv < 3; lv++ {
			for rp := 0; rp < 3; rp++ {
				id := escalationTaskID(inc, lv, rp)
				if seen[id] {
					t.Errorf("task id collision: %s", id)
				}
				seen[id] = true
			}
		}
	}
}

// TestNewEngine_NilDeps 验证构造函数对 nil 依赖的容错（测试场景）。
func TestNewEngine_NilDeps(t *testing.T) {
	e := NewEngine(nil, nil, nil, nil, nil)
	if e == nil {
		t.Fatal("NewEngine returned nil")
	}
	// inspector 在 redisOpt=nil 时应返回 nil
	if ins := e.inspector(); ins != nil {
		t.Errorf("inspector should be nil when redisOpt is nil")
	}
}

// TestCancelOnAck_NoRedis 验证无 Redis 时 CancelOnAck 不 panic（状态守卫兜底）。
func TestCancelOnAck_NoRedis(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:esc_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })

	e := NewEngine(c, nil, nil, nil, nil)
	// 无 Redis（inspector=nil），应安全返回 nil
	if err := e.CancelOnAck(context.TODO(), 1, nil, 0); err != nil {
		t.Errorf("CancelOnAck with no redis: %v", err)
	}
}

// —— 集成测试辅助 ——

// fakeNotifier 记录 NotifyEscalation 调用，用于断言升级是否触发通知。
type fakeNotifier struct {
	calls []fakeNotifyCall
}

type fakeNotifyCall struct {
	IncID    int
	Level    int
	Target   []NotifyTarget
	Channels []string
}

func (f *fakeNotifier) NotifyEscalation(_ context.Context, inc *ent.Incident, level int, targets []NotifyTarget, channels []string) error {
	f.calls = append(f.calls, fakeNotifyCall{IncID: inc.ID, Level: level, Target: targets, Channels: channels})
	return nil
}

// escTestEnv 集成测试环境：sqlite + miniredis + asynq queue + engine。
type escTestEnv struct {
	client   *ent.Client
	mr       *miniredis.Miniredis
	q        *queue.Queue
	engine   *Engine
	notifier *fakeNotifier
}

// newEscEnv 构造升级引擎测试环境。
// seedPolicy 的 levels 决定升级链结构。
func newEscEnv(t *testing.T, levels []schema.EscalationLevel, repeatTimes int) *escTestEnv {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:esc_int_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })

	mr := miniredis.RunT(t)
	redisOpt := asynq.RedisClientOpt{Addr: mr.Addr()}
	// 用真实 config 喂给 queue.New（仅 Redis 部分有意义）
	cfg := &config.Config{}
	cfg.Redis.Addr = mr.Addr()
	q := queue.New(cfg)
	t.Cleanup(func() { _ = q.Close() })

	notifier := &fakeNotifier{}
	engine := NewEngine(c, q, nil, notifier, &redisOpt)
	engine.SetRecorder(timeline.NewRecorder(c))

	// seed team + policy + incident
	ctx := context.Background()
	team, _ := c.Team.Create().SetName("T").SetSlug("t").Save(ctx)
	policy, _ := c.EscalationPolicy.Create().
		SetName("p").
		SetRepeatTimes(repeatTimes).
		SetLevels(levels).
		SetTeamID(team.ID).
		Save(ctx)
	inc, _ := c.Incident.Create().
		SetNumber("INC-0001").
		SetTitle("test").
		SetSeverity(incident.SeverityCritical).
		SetStatus(incident.StatusTriggered).
		SetTeamID(team.ID).
		SetEscalationPolicyID(policy.ID).
		Save(ctx)
	_ = inc

	return &escTestEnv{client: c, mr: mr, q: q, engine: engine, notifier: notifier}
}

// incID 取 seeded incident 的 id。
func (e *escTestEnv) incID(t *testing.T) int {
	t.Helper()
	inc, err := e.client.Incident.Query().Only(context.Background())
	if err != nil {
		t.Fatalf("get incident: %v", err)
	}
	return inc.ID
}

// runEscTask 构造并执行一个升级任务（绕过队列直接调 HandleTask，便于同步断言）。
func (e *escTestEnv) runEscTask(t *testing.T, incID, levelIdx, repeatSeq int) {
	t.Helper()
	payload, _ := json.Marshal(escalationTask{IncidentID: incID, LevelIdx: levelIdx, RepeatSeq: repeatSeq})
	if err := e.engine.HandleTask(context.Background(), asynq.NewTask(TaskEscalation, payload)); err != nil {
		t.Fatalf("HandleTask(%d,%d,%d): %v", incID, levelIdx, repeatSeq, err)
	}
}

// TestHandleTask_StateGuard_Acked 已 ack 的 incident 触发升级任务时不动作（状态守卫）。
func TestHandleTask_StateGuard_Acked(t *testing.T) {
	levels := []schema.EscalationLevel{
		{Level: 1, DelayMinutes: 0, Targets: []schema.Target{{Type: "user", TargetID: "1"}}},
	}
	env := newEscEnv(t, levels, 0)
	id := env.incID(t)
	// 把 incident 置为 acked
	if err := env.client.Incident.UpdateOneID(id).SetStatus(incident.StatusAcked).Exec(context.Background()); err != nil {
		t.Fatalf("set acked: %v", err)
	}

	env.runEscTask(t, id, 0, 0)

	if len(env.notifier.calls) != 0 {
		t.Errorf("acked incident should not trigger notification, got %d calls", len(env.notifier.calls))
	}
}

// TestHandleTask_StateGuard_Resolved 已 resolved 的 incident 不动作。
func TestHandleTask_StateGuard_Resolved(t *testing.T) {
	levels := []schema.EscalationLevel{
		{Level: 1, DelayMinutes: 0, Targets: []schema.Target{{Type: "user", TargetID: "1"}}},
	}
	env := newEscEnv(t, levels, 0)
	id := env.incID(t)
	now := time.Now()
	if err := env.client.Incident.UpdateOneID(id).
		SetStatus(incident.StatusResolved).SetResolvedAt(now).Exec(context.Background()); err != nil {
		t.Fatalf("set resolved: %v", err)
	}

	env.runEscTask(t, id, 0, 0)

	if len(env.notifier.calls) != 0 {
		t.Errorf("resolved incident should not trigger notification, got %d calls", len(env.notifier.calls))
	}
}

// TestHandleTask_NormalPath triggered incident 触发 level[0]：
// 推进状态到 escalated、current_level=1、记时间线、调 notifier。
func TestHandleTask_NormalPath(t *testing.T) {
	levels := []schema.EscalationLevel{
		{Level: 1, DelayMinutes: 0, Targets: []schema.Target{{Type: "user", TargetID: "1"}}},
		{Level: 2, DelayMinutes: 5, Targets: []schema.Target{{Type: "user", TargetID: "2"}}},
	}
	env := newEscEnv(t, levels, 0)
	id := env.incID(t)

	env.runEscTask(t, id, 0, 0)

	// incident 状态推进
	inc, _ := env.client.Incident.Get(context.Background(), id)
	if inc.Status != incident.StatusEscalated {
		t.Errorf("status: got %s, want escalated", inc.Status)
	}
	if inc.CurrentLevel != 1 {
		t.Errorf("current_level: got %d, want 1", inc.CurrentLevel)
	}
	if inc.EscalatedCount != 1 {
		t.Errorf("escalated_count: got %d, want 1", inc.EscalatedCount)
	}
	// notifier 被调用（level 0）
	if len(env.notifier.calls) != 1 || env.notifier.calls[0].Level != 0 {
		t.Errorf("notifier calls: got %+v, want 1 call at level 0", env.notifier.calls)
	}
	// 时间线有 escalated 记录
	items, _ := env.client.Incident.Query().Where(incident.IDEQ(id)).QueryTimeline().All(context.Background())
	if len(items) == 0 {
		t.Error("expected timeline item for escalation")
	}
}

// TestHandleTask_PerLevelChannels 验证 B6：各层按自身 notify_channels 发通知，
// 而非固定用全局默认通道。level 0 配 [im]，level 1 配 [im,phone,sms]，
// notifier 收到的 channels 应与该层配置一致。
func TestHandleTask_PerLevelChannels(t *testing.T) {
	levels := []schema.EscalationLevel{
		{Level: 1, DelayMinutes: 0, Targets: []schema.Target{{Type: "user", TargetID: "1"}}, NotifyChannel: []string{"im"}},
		{Level: 2, DelayMinutes: 0, Targets: []schema.Target{{Type: "user", TargetID: "1"}}, NotifyChannel: []string{"im", "phone", "sms"}},
	}
	env := newEscEnv(t, levels, 0)
	id := env.incID(t)

	// 触发 level 0
	env.runEscTask(t, id, 0, 0)
	// 触发 level 1（模拟自动链推进）
	env.runEscTask(t, id, 1, 0)

	if len(env.notifier.calls) != 2 {
		t.Fatalf("notifier calls: got %d, want 2", len(env.notifier.calls))
	}
	// level 0：channels=[im]
	if got := env.notifier.calls[0].Channels; len(got) != 1 || got[0] != "im" {
		t.Errorf("level 0 channels: got %v, want [im]", got)
	}
	// level 1：channels=[im,phone,sms]
	if got := env.notifier.calls[1].Channels; len(got) != 3 || got[0] != "im" || got[1] != "phone" || got[2] != "sms" {
		t.Errorf("level 1 channels: got %v, want [im phone sms]", got)
	}
}

// TestHandleTask_LevelIdxOutOfBounds levelIdx 超过 policy levels 时不动作（幂等）。
func TestHandleTask_LevelIdxOutOfBounds(t *testing.T) {
	levels := []schema.EscalationLevel{
		{Level: 1, DelayMinutes: 0, Targets: []schema.Target{{Type: "user", TargetID: "1"}}},
	}
	env := newEscEnv(t, levels, 0)
	id := env.incID(t)

	// levelIdx=5 超出唯一 level
	env.runEscTask(t, id, 5, 0)

	if len(env.notifier.calls) != 0 {
		t.Errorf("out-of-bounds level should not notify, got %d", len(env.notifier.calls))
	}
}

// TestRepeatTimesSemantics 锁定 C6：repeat_times 是策略级——每层通知 repeat_times+1 次，
// 重复间隔 = 该层 delay_minutes；重复用尽后才推进下一层。
//
// 语义（ADR-0016 + audit C6）：
//   - repeat_times=2 表示某层未 ack 时，除首次外再重复 2 次 = 共 3 次通知；
//   - 每次重复由上一次 HandleTask 结束时 scheduleLevel(同层, repeatSeq+1) 延迟 delay 入队；
//   - repeatSeq 达到 repeat_times 后不再重复，改 scheduleLevel(下一层, 0) 推进。
func TestRepeatTimesSemantics(t *testing.T) {
	// 单层 + repeat_times=2；delay=10min（重复间隔应 = 该层 delay）。
	levels := []schema.EscalationLevel{
		{Level: 1, DelayMinutes: 10, Targets: []schema.Target{{Type: "user", TargetID: "1"}}},
	}
	env := newEscEnv(t, levels, 2) // repeatTimes=2
	id := env.incID(t)

	inspector := asynq.NewInspector(asynq.RedisClientOpt{Addr: env.mr.Addr()})
	defer func() { _ = inspector.Close() }()

	// 第 1 次通知（repeatSeq=0）→ 应排下一次重复 (level 0, repeatSeq 1)
	env.runEscTask(t, id, 0, 0)
	assertScheduledTaskID(t, inspector, escalationTaskID(id, 0, 1))

	// 第 2 次通知（repeatSeq=1）→ 应排 (level 0, repeatSeq 2)
	env.runEscTask(t, id, 0, 1)
	assertScheduledTaskID(t, inspector, escalationTaskID(id, 0, 2))

	// 第 3 次通知（repeatSeq=2）→ repeatSeq 已达 repeat_times，不再重复；
	// 单层无下一层故不排任何 level 1 任务（越界，scheduleLevel 直接返回不入队）。
	// 注：测试直接调 HandleTask 不消费队列，前两步排的 repeat 任务仍残留 scheduled，
	// 故这里不断言总数为 0，而是断言"没有推进到下一层"（无 level 1 任务）。
	env.runEscTask(t, id, 0, 2)
	scheduled, _ := inspector.ListScheduledTasks("critical", asynq.PageSize(50))
	for _, task := range scheduled {
		if task.ID == escalationTaskID(id, 1, 0) {
			t.Errorf("repeat exhausted on single level should NOT advance to next level, found %s", task.ID)
		}
	}

	// 共 3 次通知（首次 + 2 次重复 = repeat_times+1）
	if len(env.notifier.calls) != 3 {
		t.Errorf("notify count: got %d, want 3 (repeat_times=2 → repeat+1)", len(env.notifier.calls))
	}
}

// assertScheduledTaskID 断言 critical 队列的 scheduled 任务中含指定 TaskID（delay>0 的任务落 scheduled）。
func assertScheduledTaskID(t *testing.T, inspector *asynq.Inspector, wantID string) {
	t.Helper()
	scheduled, err := inspector.ListScheduledTasks("critical", asynq.PageSize(50))
	if err != nil {
		t.Fatalf("list scheduled: %v", err)
	}
	for _, task := range scheduled {
		if task.ID == wantID {
			return
		}
	}
	var ids []string
	for _, task := range scheduled {
		ids = append(ids, task.ID)
	}
	t.Errorf("scheduled task %q not found; got %v", wantID, ids)
}

// TestTriggerLevelNow 入队即时升级任务到真实 asynq（miniredis），
// 验证任务确实被 enqueue（Inspector 能查到）。
func TestTriggerLevelNow(t *testing.T) {
	levels := []schema.EscalationLevel{
		{Level: 1, DelayMinutes: 0, Targets: []schema.Target{{Type: "user", TargetID: "1"}}},
	}
	env := newEscEnv(t, levels, 0)
	id := env.incID(t)

	if err := env.engine.TriggerLevelNow(context.Background(), id, 0); err != nil {
		t.Fatalf("TriggerLevelNow: %v", err)
	}

	// 用 Inspector 查 critical 队列应有 1 个待处理任务
	inspector := asynq.NewInspector(asynq.RedisClientOpt{Addr: env.mr.Addr()})
	defer func() { _ = inspector.Close() }()
	tasks, err := inspector.ListPendingTasks("critical", asynq.PageSize(10))
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("pending tasks: got %d, want 1", len(tasks))
	}
	if tasks[0].Type != TaskEscalation {
		t.Errorf("task type: got %s, want %s", tasks[0].Type, TaskEscalation)
	}
}

// TestCancelOnAck_RealInspector 有真实 Inspector 时，
// StartEscalation 入队的任务被 CancelOnAck 删除（状态守卫之外的主动取消）。
func TestCancelOnAck_RealInspector(t *testing.T) {
	levels := []schema.EscalationLevel{
		{Level: 1, DelayMinutes: 10, Targets: []schema.Target{{Type: "user", TargetID: "1"}}},
		{Level: 2, DelayMinutes: 10, Targets: []schema.Target{{Type: "user", TargetID: "2"}}},
	}
	env := newEscEnv(t, levels, 1)
	id := env.incID(t)

	// 启动升级链（入队延迟任务）
	if err := env.engine.StartEscalation(context.Background(), id, levels); err != nil {
		t.Fatalf("StartEscalation: %v", err)
	}
	inspector := asynq.NewInspector(asynq.RedisClientOpt{Addr: env.mr.Addr()})
	defer func() { _ = inspector.Close() }()
	// delay>0 的任务在 scheduled 队列（未到触发时间），pending 为空
	scheduled, _ := inspector.ListScheduledTasks("critical", asynq.PageSize(20))
	if len(scheduled) == 0 {
		t.Fatal("expected scheduled tasks after StartEscalation")
	}

	// CancelOnAck 删除所有待触发任务
	if err := env.engine.CancelOnAck(context.Background(), id, levels, 1); err != nil {
		t.Fatalf("CancelOnAck: %v", err)
	}
	after, _ := inspector.ListScheduledTasks("critical", asynq.PageSize(20))
	if len(after) != 0 {
		t.Errorf("scheduled after cancel: got %d, want 0", len(after))
	}
}

// TestCancelLevelPending 验证 B6b：只删指定 level 的 pending 任务（含各 repeat 序号），
// 不误删其它 level——手动跳级取消当前层延迟任务，避免"立即触发 + 延迟到点"同层重复。
func TestCancelLevelPending(t *testing.T) {
	levels := []schema.EscalationLevel{
		{Level: 1, DelayMinutes: 10, Targets: []schema.Target{{Type: "user", TargetID: "1"}}},
		{Level: 2, DelayMinutes: 10, Targets: []schema.Target{{Type: "user", TargetID: "2"}}},
	}
	env := newEscEnv(t, levels, 2) // repeatTimes=2
	id := env.incID(t)
	ctx := context.Background()

	// 手动入队 level 1 的一条延迟任务（模拟 level[0] 处理后排的下一层延迟任务）
	if err := env.engine.scheduleLevel(ctx, id, 1, levels, 0); err != nil {
		t.Fatalf("scheduleLevel: %v", err)
	}
	// 再入队 level 0 的一条（模拟同时存在的其它层任务，应不被误删）
	if err := env.engine.scheduleLevel(ctx, id, 0, levels, 0); err != nil {
		t.Fatalf("scheduleLevel level0: %v", err)
	}

	inspector := asynq.NewInspector(asynq.RedisClientOpt{Addr: env.mr.Addr()})
	defer func() { _ = inspector.Close() }()

	// 取消 level 1 的 pending（repeatTimes=2 → 清 repeat 0..2）
	if err := env.engine.CancelLevelPending(ctx, id, 1, 2); err != nil {
		t.Fatalf("CancelLevelPending: %v", err)
	}

	scheduled, _ := inspector.ListScheduledTasks("critical", asynq.PageSize(50))
	for _, task := range scheduled {
		if task.ID == escalationTaskID(id, 1, 0) {
			t.Errorf("level 1 task should be cancelled, still present: %s", task.ID)
		}
	}
	// level 0 的任务应仍在（未被误删）
	foundLevel0 := false
	for _, task := range scheduled {
		if task.ID == escalationTaskID(id, 0, 0) {
			foundLevel0 = true
		}
	}
	if !foundLevel0 {
		t.Error("level 0 task should NOT be cancelled by CancelLevelPending(level=1)")
	}
}

// TestScheduleLevel_TaskIDConflictIdempotent 同 (inc,level,repeat) 重复入队：
// 第二次撞 TaskID（ErrTaskIDConflict）应按成功处理（幂等场景），且队列只有一个任务。
// 修复点：原实现注释说"属幂等场景，忽略"、代码却原样返回错误。
func TestScheduleLevel_TaskIDConflictIdempotent(t *testing.T) {
	levels := []schema.EscalationLevel{
		{Level: 1, DelayMinutes: 10, Targets: []schema.Target{{Type: "user", TargetID: "1"}}},
	}
	env := newEscEnv(t, levels, 0)
	id := env.incID(t)
	ctx := context.Background()

	if err := env.engine.scheduleLevel(ctx, id, 0, levels, 0); err != nil {
		t.Fatalf("first scheduleLevel: %v", err)
	}
	// 第二次同 TaskID：多副本 sweeper 并发重排 / 重投补排的等价场景，应吸收冲突返回 nil
	if err := env.engine.scheduleLevel(ctx, id, 0, levels, 0); err != nil {
		t.Fatalf("second scheduleLevel should absorb ErrTaskIDConflict, got: %v", err)
	}

	inspector := asynq.NewInspector(asynq.RedisClientOpt{Addr: env.mr.Addr()})
	defer func() { _ = inspector.Close() }()
	scheduled, _ := inspector.ListScheduledTasks("critical", asynq.PageSize(10))
	if len(scheduled) != 1 {
		t.Errorf("scheduled tasks: got %d, want 1 (conflict must not duplicate)", len(scheduled))
	}
}

// TestHandleTask_RedeliveryNoDuplicateNotify at-least-once 重投防重复副作用：
// 同一任务（同 payload → 同标记键）执行两次，第二次应跳过通知/计数/时间线，
// 但仍推进状态 + 续排下一层（链的连续性不因重投判定中断）。
func TestHandleTask_RedeliveryNoDuplicateNotify(t *testing.T) {
	levels := []schema.EscalationLevel{
		{Level: 1, DelayMinutes: 0, Targets: []schema.Target{{Type: "user", TargetID: "1"}}},
		{Level: 2, DelayMinutes: 10, Targets: []schema.Target{{Type: "user", TargetID: "2"}}},
	}
	env := newEscEnv(t, levels, 0)
	id := env.incID(t)

	// 首投 + 模拟 worker 崩溃后 asynq 重投（同 payload，直接调 HandleTask 走标记键回退路径）
	env.runEscTask(t, id, 0, 0)
	env.runEscTask(t, id, 0, 0)

	// 通知只发一次（重投不轰炸）
	if len(env.notifier.calls) != 1 {
		t.Errorf("notifier calls: got %d, want 1 (redelivery must not re-notify)", len(env.notifier.calls))
	}
	// escalated_count 只自增一次（重投不重复计数）
	inc, _ := env.client.Incident.Get(context.Background(), id)
	if inc.EscalatedCount != 1 {
		t.Errorf("escalated_count: got %d, want 1", inc.EscalatedCount)
	}
	// 状态推进照常（重投也补齐状态，防"通知后落库前崩溃"导致状态脱节）
	if inc.Status != incident.StatusEscalated || inc.CurrentLevel != 1 {
		t.Errorf("status/current_level: got %s/%d, want escalated/1", inc.Status, inc.CurrentLevel)
	}
	// 时间线只记一条（重投不刷屏）
	items, _ := env.client.Incident.Query().Where(incident.IDEQ(id)).QueryTimeline().All(context.Background())
	if len(items) != 1 {
		t.Errorf("timeline items: got %d, want 1", len(items))
	}
	// 下一层任务仍被续排（重投跳过副作用但不跳过链推进）
	inspector := asynq.NewInspector(asynq.RedisClientOpt{Addr: env.mr.Addr()})
	defer func() { _ = inspector.Close() }()
	assertScheduledTaskID(t, inspector, escalationTaskID(id, 1, 0))
}

// TestHandleTask_TimelineMetaNotifiedUserIDs 升级时间线 meta 应记录解析出的 target
// user id 列表（不只人数）——排班蓝图事后被改时仍可追溯"当时实际该叫谁"。
func TestHandleTask_TimelineMetaNotifiedUserIDs(t *testing.T) {
	env := newEscEnv(t, nil, 0)
	ctx := context.Background()
	id := env.incID(t)
	u, _ := env.client.User.Create().SetUsername("oncall").SetEmail("oncall@x").SetName("值班").Save(ctx)
	levels := []schema.EscalationLevel{
		{Level: 1, DelayMinutes: 0, Targets: []schema.Target{{Type: "user", TargetID: itoa(u.ID)}}},
	}
	env.client.EscalationPolicy.Update().SetLevels(levels).ExecX(ctx)

	env.runEscTask(t, id, 0, 0)

	items, _ := env.client.Incident.Query().Where(incident.IDEQ(id)).QueryTimeline().All(ctx)
	if len(items) != 1 {
		t.Fatalf("timeline items: got %d, want 1", len(items))
	}
	rawIDs, ok := items[0].Detail["notified_user_ids"]
	if !ok {
		t.Fatalf("timeline detail missing notified_user_ids: %+v", items[0].Detail)
	}
	// JSON 回读数字为 float64
	list, ok := rawIDs.([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("notified_user_ids: got %#v, want 1-elem list", rawIDs)
	}
	if got, want := list[0], float64(u.ID); got != want {
		t.Errorf("notified_user_ids[0]: got %v, want %v", got, want)
	}
}

// TestResolveTargets_UserTarget user 类型 target 解析成 NotifyTarget（去重）。
func TestResolveTargets_UserTarget(t *testing.T) {
	env := newEscEnv(t, nil, 0)
	ctx := context.Background()
	// 建两个用户
	u1, _ := env.client.User.Create().SetUsername("u1").SetEmail("u1@x").Save(ctx)
	u2, _ := env.client.User.Create().SetUsername("u2").SetEmail("u2@x").Save(ctx)

	targets := []schema.Target{
		{Type: "user", TargetID: itoa(u1.ID)},
		{Type: "user", TargetID: itoa(u2.ID)},
		{Type: "user", TargetID: itoa(u1.ID)}, // 重复，应去重
		{Type: "user", TargetID: "99999"},     // 不存在，跳过（记 warn）
	}
	got, err := env.engine.resolveTargets(ctx, targets)
	if err != nil {
		t.Fatalf("resolveTargets: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("resolved count: got %d, want 2 (dedup + skip missing)", len(got))
	}
	// 不存在的 target 不应导致结果包含错误项
	for _, nt := range got {
		if nt.UserID == 99999 {
			t.Error("missing user should not appear in results")
		}
	}
}

// TestResolveTargets_TeamTarget team 类型 target 解算为团队全体在职成员（B9）。
// 原实现占位 UserID=0、source=team，导致邮件/电话按 user_id 解号解不出人——team 型通知全丢。
// 现应展开为逐成员 NotifyTarget（真实 UserID），使各通道对全体成员逐一送达。
func TestResolveTargets_TeamTarget(t *testing.T) {
	env := newEscEnv(t, nil, 0)
	ctx := context.Background()
	// 建团队 + 三个成员（其中一个 disabled，应被排除）
	team, _ := env.client.Team.Create().SetName("Pay").SetSlug("pay").Save(ctx)
	m1, _ := env.client.User.Create().SetUsername("m1").SetEmail("m1@x").SetName("M1").AddTeamIDs(team.ID).Save(ctx)
	m2, _ := env.client.User.Create().SetUsername("m2").SetEmail("m2@x").SetName("M2").AddTeamIDs(team.ID).Save(ctx)
	// disabled 成员不应被解算通知（B9：查 User.status 跳过禁用）
	env.client.User.Create().SetUsername("m3").SetEmail("m3@x").SetName("M3").
		SetStatus("disabled").AddTeamIDs(team.ID).SaveX(ctx)

	got, err := env.engine.resolveTargets(ctx, []schema.Target{
		{Type: "team", TargetID: itoa(team.ID)},
	})
	if err != nil {
		t.Fatalf("resolveTargets: %v", err)
	}
	// 应解出 2 个在职成员（disabled 被排除），每个带真实 UserID + source=team
	if len(got) != 2 {
		t.Fatalf("team target: got %d targets, want 2 active members", len(got))
	}
	ids := map[int]bool{}
	for _, nt := range got {
		if nt.Source != "team" {
			t.Errorf("team member source: got %q, want team", nt.Source)
		}
		if nt.UserID == 0 {
			t.Error("team member should have real UserID, not 0 placeholder")
		}
		ids[nt.UserID] = true
	}
	if !ids[m1.ID] || !ids[m2.ID] {
		t.Errorf("team members: got ids %v, want m1=%d m2=%d", ids, m1.ID, m2.ID)
	}
}

// TestResolveTargets_TeamTarget_Empty 空团队（无在职成员）解出 0 个目标（不 panic，记 warn）。
func TestResolveTargets_TeamTarget_Empty(t *testing.T) {
	env := newEscEnv(t, nil, 0)
	ctx := context.Background()
	team, _ := env.client.Team.Create().SetName("Empty").SetSlug("empty").Save(ctx)
	got, err := env.engine.resolveTargets(ctx, []schema.Target{
		{Type: "team", TargetID: itoa(team.ID)},
	})
	if err != nil {
		t.Fatalf("resolveTargets: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty team: got %d, want 0", len(got))
	}
}

// itoa 整数转字符串（避免引入 strconv）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
