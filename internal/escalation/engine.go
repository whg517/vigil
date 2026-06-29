// Package escalation 实现能力域 6：升级策略。
//
// 对应 docs/capabilities/03-scheduling-escalation.md §3：
// · Asynq 延迟任务驱动升级链（asynq.ProcessIn(delay)）
// · Incident 创建 → 入队 level[0] → 到期触发 → 入队 level[1] → ...
// · ack 即取消（DeleteTask + 状态守卫）
// · 每次升级记时间线（TimelineItem type=escalated）
//
// 通知（能力域 7）通过 Notifier 接口接入：升级触发时由 notifier.NotifyEscalation
// 送达 targets（IM/邮件/webhook/电话），notifier 为 nil 时降级为仅记时间线。
package escalation

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/schema"
	"github.com/kevin/vigil/ent/timelineitem"
	"github.com/kevin/vigil/internal/metrics"
	"github.com/kevin/vigil/internal/queue"
	"github.com/kevin/vigil/internal/schedule"
	"github.com/kevin/vigil/internal/timeline"

	"github.com/hibiken/asynq"
	"go.uber.org/zap"
)

// Engine 升级引擎。
type Engine struct {
	db       *ent.Client
	queue    *queue.Queue
	sched    *schedule.Engine
	notifier Notifier              // 通知接口（能力域 7 接入）；nil 则只记时间线
	redisOpt *asynq.RedisClientOpt // 用于创建 Inspector 删除待触发任务
	recorder *timeline.Recorder    // 时间线记录器（统一 Recorder）；nil 则不记
	logger   *zap.Logger           // 日志，nil 用 Nop
}

// SetRecorder 注入时间线记录器。
func (e *Engine) SetRecorder(r *timeline.Recorder) {
	e.recorder = r
}

// SetLogger 注入日志器。未注入时用 zap.NewNop()，不影响功能。
func (e *Engine) SetLogger(l *zap.Logger) {
	e.logger = l
}

// log 取日志器，未注入用 Nop（测试友好）。
func (e *Engine) log() *zap.Logger {
	if e.logger == nil {
		return zap.NewNop()
	}
	return e.logger
}

// Notifier 通知接口，由能力域 7 实现。升级触发时调用以送达 targets。
type Notifier interface {
	NotifyEscalation(ctx context.Context, inc *ent.Incident, level int, targets []NotifyTarget) error
}

// NotifyTarget 升级通知目标（已解析的"实际人"）。
type NotifyTarget struct {
	UserID int
	Name   string
	Source string // schedule | user | team
}

// NewEngine 创建升级引擎。notifier 可为 nil（无通知时仅记时间线）。
// redisOpt 用于创建 Inspector 删除待触发任务，可为 nil（降级靠状态守卫兜底）。
func NewEngine(db *ent.Client, q *queue.Queue, sched *schedule.Engine, notifier Notifier, redisOpt *asynq.RedisClientOpt) *Engine {
	return &Engine{db: db, queue: q, sched: sched, notifier: notifier, redisOpt: redisOpt}
}

// StartEscalation Incident 创建后调用：入队 level[0] 的延迟任务。
// policyLevels 为空时升级链为空，跳过（视为无需升级）。
func (e *Engine) StartEscalation(ctx context.Context, incID int, levels []schema.EscalationLevel) error {
	if len(levels) == 0 {
		return nil
	}
	return e.scheduleLevel(ctx, incID, 0, levels, 0)
}

// TriggerLevelNow 立即触发某 level 的升级（用于人工「我现在就需要更高层级介入」）。
// 与 scheduleLevel 的延迟入队不同：用 ProcessIn(0) 立即执行，
// 复用 HandleTask 的「通知 + 时间线 + 推进下一 level」逻辑。
// levelIdx 越界（无策略或超过末级）则不动作，幂等友好。
// taskID 用 now: 前缀，避免与已存在的延迟任务（esc: 前缀）TaskID 冲突。
func (e *Engine) TriggerLevelNow(ctx context.Context, incID, levelIdx int) error {
	if levelIdx < 0 {
		return nil
	}
	payload, _ := json.Marshal(escalationTask{IncidentID: incID, LevelIdx: levelIdx, RepeatSeq: 0})
	task := asynq.NewTask(TaskEscalation, payload)
	// TaskID 带 now: 前缀 + 时间戳，保证可重复触发（每次手动升级独立任务）
	opts := []asynq.Option{
		asynq.Queue("critical"),
		asynq.TaskID(fmt.Sprintf("now:%d:%d:%d", incID, levelIdx, time.Now().UnixNano())),
		asynq.ProcessIn(0),
		asynq.Retention(5 * time.Minute), // 触发后保留 5 分钟便于排查
	}
	if _, err := e.queue.Client.EnqueueContext(ctx, task, opts...); err != nil {
		return fmt.Errorf("enqueue immediate escalation level %d: %w", levelIdx, err)
	}
	return nil
}

// scheduleLevel 入队某 level 的延迟任务。
// repeatSeq：当前 level 的第几次重复通知（0=首次）。
func (e *Engine) scheduleLevel(ctx context.Context, incID, levelIdx int, levels []schema.EscalationLevel, repeatSeq int) error {
	if levelIdx >= len(levels) {
		return nil // 已到末级
	}
	level := levels[levelIdx]
	payload, _ := json.Marshal(escalationTask{
		IncidentID: incID,
		LevelIdx:   levelIdx,
		RepeatSeq:  repeatSeq,
	})
	task := asynq.NewTask(TaskEscalation, payload)
	delay := time.Duration(level.DelayMinutes) * time.Minute
	opts := []asynq.Option{asynq.Queue("critical"), asynq.TaskID(escalationTaskID(incID, levelIdx, repeatSeq))}
	if delay > 0 {
		opts = append(opts, asynq.ProcessIn(delay))
	}
	if _, err := e.queue.Client.EnqueueContext(ctx, task, opts...); err != nil {
		// 延迟任务可能因 TaskID 重复报错（同 key 已存在），属幂等场景，忽略
		return fmt.Errorf("enqueue escalation level %d: %w", levelIdx, err)
	}
	return nil
}

// HandleTask Asynq 任务处理：到期触发某 level 升级。
// 注册到 queue。包含状态守卫：已 ack/resolved 的 incident 不动作。
func (e *Engine) HandleTask(ctx context.Context, t *asynq.Task) error {
	var p escalationTask
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("unmarshal escalation task: %w", err)
	}

	// 1. 状态守卫：取 Incident，已 ack/resolved/closed 则不动作
	inc, err := e.db.Incident.Get(ctx, p.IncidentID)
	if err != nil {
		return fmt.Errorf("get incident %d: %w", p.IncidentID, err)
	}
	if incSt := incident.Status(inc.Status); incSt != incident.StatusTriggered && incSt != incident.StatusEscalated {
		// 已 ack/resolved/closed —— 取消所有后续升级
		return nil
	}

	// 2. 取 EscalationPolicy 的 levels
	policy, err := inc.QueryEscalationPolicy().Only(ctx)
	if err != nil {
		return fmt.Errorf("query escalation policy: %w", err)
	}
	if p.LevelIdx >= len(policy.Levels) {
		return nil
	}
	level := policy.Levels[p.LevelIdx]

	// 3. 解析 targets（schedule → 调排班算人；user/team 直接用）
	targets, err := e.resolveTargets(ctx, level.Targets)
	if err != nil {
		return fmt.Errorf("resolve targets: %w", err)
	}

	// 4. 通知（若 Notifier 已接入）。失败不阻塞升级链。
	if e.notifier != nil {
		_ = e.notifier.NotifyEscalation(ctx, inc, p.LevelIdx, targets)
	}

	// 5. 记时间线 + 更新 Incident 升级状态
	if err := e.db.Incident.UpdateOneID(inc.ID).
		SetStatus(incident.StatusEscalated).
		SetCurrentLevel(p.LevelIdx + 1).
		SetEscalatedCount(inc.EscalatedCount + 1).
		Exec(ctx); err != nil {
		return fmt.Errorf("update incident: %w", err)
	}
	// 埋点：升级触发次数
	metrics.EscalationsTriggered.Inc()
	// 通过统一 Recorder 记时间线（消除直接 ent 调用），失败不阻塞主流程
	if e.recorder != nil {
		_ = e.recorder.Record(ctx, inc.ID, timelineitem.TypeEscalated,
			fmt.Sprintf("升级到 level %d，通知 %d 人", p.LevelIdx+1, len(targets)),
			timeline.Actor{Kind: "system"}, timelineitem.SourceSystem,
			map[string]any{"level": p.LevelIdx + 1, "notified": len(targets)})
	}

	// 6. 安排下一步：repeat 或下一 level
	if p.RepeatSeq < policy.RepeatTimes {
		// 还有重复次数：安排同 level 再次通知
		return e.scheduleLevel(ctx, inc.ID, p.LevelIdx, policy.Levels, p.RepeatSeq+1)
	}
	// 进入下一 level
	return e.scheduleLevel(ctx, inc.ID, p.LevelIdx+1, policy.Levels, 0)
}

// resolveTargets 把 EscalationLevel.Targets 解析成实际通知人。
// 解析失败（排班查询错/用户不存在）改为告警日志而非静默吞错——
// 升级链上「该通知的人没通知到」是严重事故，必须可观测。
func (e *Engine) resolveTargets(ctx context.Context, targets []schema.Target) ([]NotifyTarget, error) {
	var out []NotifyTarget
	seen := map[int]bool{} // 去重
	for _, t := range targets {
		switch t.Type {
		case "schedule":
			schedID, _ := strconv.Atoi(t.TargetID)
			if schedID == 0 {
				e.log().Warn("escalation target: invalid schedule id",
					zap.String("target_id", t.TargetID))
				continue
			}
			if e.sched == nil {
				e.log().Warn("escalation target: schedule engine nil, skip schedule target",
					zap.Int("schedule_id", schedID))
				continue
			}
			res, err := e.sched.OncallNow(ctx, schedID)
			if err != nil {
				e.log().Warn("escalation target: query oncall failed",
					zap.Int("schedule_id", schedID), zap.Error(err))
				continue
			}
			for _, layer := range res.Layers {
				for _, u := range layer.Users {
					if !seen[u.ID] {
						seen[u.ID] = true
						out = append(out, NotifyTarget{UserID: u.ID, Name: u.Name, Source: "schedule"})
					}
				}
			}
		case "user":
			uid, _ := strconv.Atoi(t.TargetID)
			if uid == 0 {
				e.log().Warn("escalation target: invalid user id",
					zap.String("target_id", t.TargetID))
				continue
			}
			u, err := e.db.User.Get(ctx, uid)
			if err != nil {
				e.log().Warn("escalation target: user not found",
					zap.Int("user_id", uid), zap.Error(err))
				continue
			}
			if !seen[u.ID] {
				seen[u.ID] = true
				out = append(out, NotifyTarget{UserID: u.ID, Name: u.Name, Source: "user"})
			}
		case "team":
			// team 通知全团队：查成员。简化为标记 source=team，通知引擎处理
			out = append(out, NotifyTarget{Name: fmt.Sprintf("team:%s", t.TargetID), Source: "team"})
		}
	}
	return out, nil
}

// CancelOnAck 当 Incident 被 ack 时调用：取消所有待触发升级任务。
// repeatTimes 为 EscalationPolicy 级的重复次数。levels 为各级配置。
// 状态守卫兜底：即使删除失败，HandleTask 也会因 incident 已 ack 而不动作。
func (e *Engine) CancelOnAck(ctx context.Context, incID int, levels []schema.EscalationLevel, repeatTimes int) error {
	inspector := e.inspector()
	if inspector == nil {
		return nil // 无 Redis 连接信息，跳过（依赖状态守卫兜底）
	}
	defer func() { _ = inspector.Close() }()
	// 删除所有可能的待触发任务（level × repeat 组合）
	for levelIdx := 0; levelIdx < len(levels); levelIdx++ {
		for repeatSeq := 0; repeatSeq <= repeatTimes; repeatSeq++ {
			taskID := escalationTaskID(incID, levelIdx, repeatSeq)
			// 任务可能已触发/不存在，忽略错误（状态守卫兜底）
			_ = inspector.DeleteTask("critical", taskID)
		}
	}
	return nil
}

// inspector 创建 Asynq Inspector（用于删除待触发任务）。
// 从 queue 复用 Redis 连接信息；nil 时返回 nil。
func (e *Engine) inspector() *asynq.Inspector {
	if e.redisOpt == nil {
		return nil
	}
	return asynq.NewInspector(*e.redisOpt)
}

const (
	// TaskEscalation 升级任务类型名。
	TaskEscalation = "vigil:escalation"
)

// escalationTask 升级任务 payload。
type escalationTask struct {
	IncidentID int `json:"incident_id"`
	LevelIdx   int `json:"level_idx"`
	RepeatSeq  int `json:"repeat_seq"` // 当前 level 的重复序号
}

// escalationTaskID 生成稳定的任务 ID（用于幂等与取消）。
func escalationTaskID(incID, levelIdx, repeatSeq int) string {
	return fmt.Sprintf("esc:%d:%d:%d", incID, levelIdx, repeatSeq)
}
