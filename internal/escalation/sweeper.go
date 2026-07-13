// sweeper.go 升级对账巡检（reconciliation sweeper，ADR-0016「Redis 数据丢失与对账恢复」）。
//
// 场景：升级链全靠 Asynq 延迟任务存 Redis（ADR-0016）。Redis 丢数据（未开持久化的重启/
// 误 FLUSHALL/主从切换丢窗口）后，活跃 Incident 的升级计时器凭空消失——不再升级、不再通知、
// 且无任何报错，是最危险的静默失效。本巡检周期核对「DB 里的应然」与「Redis 里的实然」：
//
//   - 应然：status ∈ {triggered, escalated} 且绑定非空 levels 策略、current_level < len(levels)
//     的 Incident，按状态守卫语义必然应存在一个待触发升级任务（下一层首发或当前层重复）；
//     current_level ≥ len(levels) 表示链已推进到末级处理完，无"应然"任务（此守卫兼防
//     "末级重排→处理完→再重排"的自激循环）。
//   - 实然：Inspector 遍历 critical 队列 pending/scheduled/retry/active 四态，按 payload
//     解出 incident_id 集合，任一态存在该 Incident 的升级任务即视为链健在。
//   - 修复：缺失则从 current_level 层重排（repeatSeq=0）。repeatSeq 只活在任务 payload 里
//     随 Redis 一起丢，不接续旧序号——宁可升得更快（跳过当前层剩余重复），不可断链。
//
// 幂等/多副本安全：重排复用幂等 TaskID（esc:{inc}:{level}:{seq}），scheduleLevel 对
// ErrTaskIDConflict 按成功处理——多副本并发巡检、巡检与正常链推进赛跑，最坏撞 TaskID 被吸收。
// 巡检与 ack 赛跑时多排的任务由 HandleTask 状态守卫吸收，不误升级。
//
// 设计：goroutine ticker（与 ingestion.RequeueSweeper 同款，纳入优雅关闭），
// 在 internal/server/wire.go 经 Wired.goPeriodic 接线。
package escalation

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/incident"

	"github.com/hibiken/asynq"
)

// sweepPageSize Inspector 单页列举任务数（遍历四态队列时分页拉全量）。
const sweepPageSize = 500

// Sweeper 升级对账巡检器。
type Sweeper struct {
	db       *ent.Client
	engine   *Engine // 复用 scheduleLevel（幂等 TaskID + 冲突按成功）
	redisOpt *asynq.RedisClientOpt
	batch    int // 单轮扫描活跃 Incident 上限（防极端场景全表扫；活跃单按设计是少量）
	interval time.Duration
}

// NewSweeper 构造升级对账巡检器。interval<=0 用默认 2m，batch<=0 用默认 500。
// redisOpt 为 nil（无 Redis）时巡检空转——无任务存储可对账，也无从重排。
func NewSweeper(db *ent.Client, engine *Engine, redisOpt *asynq.RedisClientOpt, interval time.Duration) *Sweeper {
	if interval <= 0 {
		interval = 2 * time.Minute
	}
	return &Sweeper{db: db, engine: engine, redisOpt: redisOpt, batch: 500, interval: interval}
}

// Interval 返回巡检间隔（供装配层日志参考）。
func (s *Sweeper) Interval() time.Duration { return s.interval }

// Run 阻塞运行巡检循环，ctx 取消时退出（纳入优雅关闭）。
// 装配层 goPeriodic(ctx, s.Run) 启动。
func (s *Sweeper) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n := s.sweepOnce(ctx); n > 0 {
				slog.Warn("escalation sweeper: rescheduled missing escalation tasks", "count", n)
			}
		}
	}
}

// sweepOnce 执行一轮对账，返回重排的任务条数。
// 任一步 Redis/DB 探查失败则本轮跳过（记 warn），下一轮自动重试——宁可晚对账，不可误重排。
func (s *Sweeper) sweepOnce(ctx context.Context) int {
	if s.redisOpt == nil || s.engine == nil {
		return 0 // 无 Redis/引擎（测试桩/降级装配），无从对账
	}

	// 1. 实然：收集队列中仍存在升级任务的 incident id 集合。
	have, ok := s.collectQueuedIncidents(ctx)
	if !ok {
		return 0
	}

	// 2. 应然：扫描活跃且绑定了升级策略的 Incident（policy edge 随查随取，一次加载）。
	rows, err := s.db.Incident.Query().
		Where(
			incident.StatusIn(incident.StatusTriggered, incident.StatusEscalated),
			incident.HasEscalationPolicy(),
		).
		WithEscalationPolicy().
		Order(ent.Asc(incident.FieldID)).
		Limit(s.batch).
		All(ctx)
	if err != nil {
		slog.Warn("escalation sweeper: query active incidents failed", "error", err)
		return 0
	}

	// 3. 核对 + 修复：应然有、实然无 → 从 current_level 重排。
	var rescheduled int
	for _, inc := range rows {
		policy := inc.Edges.EscalationPolicy
		if policy == nil || len(policy.Levels) == 0 {
			continue // 空策略：无升级链，无"应然"任务
		}
		if inc.CurrentLevel >= len(policy.Levels) {
			continue // 链已推进到末级处理完（含手动跳级到末级），不重排（防自激循环）
		}
		if have[inc.ID] {
			continue // 任务在队（四态任一），链健在
		}
		// current_level 语义：HandleTask 每处理完一层置 LevelIdx+1，即"下一待触发层级索引"；
		// 从未升级过（=0）等价于 StartEscalation 的 level[0] 首发。
		if err := s.engine.scheduleLevel(ctx, inc.ID, inc.CurrentLevel, policy.Levels, 0); err != nil {
			slog.Warn("escalation sweeper: reschedule failed",
				"incident_id", inc.ID, "level", inc.CurrentLevel, "error", err)
			continue
		}
		slog.Warn("escalation sweeper: escalation task missing, rescheduled",
			"incident_id", inc.ID, "level", inc.CurrentLevel, "status", inc.Status)
		rescheduled++
	}
	return rescheduled
}

// collectQueuedIncidents 遍历 critical 队列的 pending/scheduled/retry/active 四态，
// 返回队列中存在升级任务的 incident id 集合。
//
// 为什么含 active：正在执行的任务处理完会自行排下一步，此刻重排会与其赛跑（虽有幂等
// TaskID 兜底，但能不赛就不赛）。archived/completed 不算"链健在"——archived 是重试耗尽
// 的死任务（链已断，应重排），completed 任务已完成使命。
// 按 payload 解 incident_id 而非解析 TaskID 字符串：esc:/now: 两种前缀统一覆盖，且不与
// ID 格式约定耦合。
func (s *Sweeper) collectQueuedIncidents(ctx context.Context) (map[int]bool, bool) {
	inspector := asynq.NewInspector(*s.redisOpt)
	defer func() { _ = inspector.Close() }()

	have := make(map[int]bool)
	lists := []func(string, ...asynq.ListOption) ([]*asynq.TaskInfo, error){
		inspector.ListPendingTasks,
		inspector.ListScheduledTasks,
		inspector.ListRetryTasks,
		inspector.ListActiveTasks,
	}
	for _, list := range lists {
		for page := 1; ; page++ {
			// ctx 取消则中止本轮（Inspector API 不吃 ctx，这里显式检查以响应优雅关闭）。
			if ctx.Err() != nil {
				return nil, false
			}
			tasks, err := list("critical", asynq.PageSize(sweepPageSize), asynq.Page(page))
			if err != nil {
				// 队列不存在 ≠ 探查失败：Redis 被整体清空（正是本巡检要修复的灾难场景）
				// 或从未入过队时，队列 key 本身不存在——必须视为"空队列"继续对账，
				// 否则 sweeper 永远无法修复彻底丢空的 Redis。
				if errors.Is(err, asynq.ErrQueueNotFound) {
					break
				}
				slog.Warn("escalation sweeper: list queue tasks failed", "error", err)
				return nil, false // Redis 探查失败：本轮跳过，宁可晚对账不可误重排
			}
			for _, ti := range tasks {
				if ti.Type != TaskEscalation {
					continue
				}
				var p escalationTask
				if err := json.Unmarshal(ti.Payload, &p); err != nil {
					continue
				}
				have[p.IncidentID] = true
			}
			if len(tasks) < sweepPageSize {
				break
			}
		}
	}
	return have, true
}
