// Package escalation 实现能力域 6：升级策略。
//
// 对应 docs/capabilities/03-scheduling-escalation.md §3：
// · Asynq 延迟任务驱动升级链（asynq.ProcessIn(delay)）
// · Incident 创建 → 入队 level[0] → 到期触发 → 入队 level[1] → ...
// · ack 即取消（DeleteTask + 状态守卫）
// · 每次升级记时间线（TimelineItem type=escalated）
//
// 通知（能力域 7）暂未实现，触发时先记时间线 + 返回 targets，
// 通知引擎接入后由 Notifier 承载实际送达。
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
	"github.com/kevin/vigil/internal/queue"
	"github.com/kevin/vigil/internal/schedule"

	"github.com/hibiken/asynq"
)

// Engine 升级引擎。
type Engine struct {
	db       *ent.Client
	queue    *queue.Queue
	sched    *schedule.Engine
	notifier Notifier            // 通知接口（能力域 7 接入）；nil 则只记时间线
	redisOpt *asynq.RedisClientOpt // 用于创建 Inspector 删除待触发任务
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

	// 4. 通知（若 Notifier 已接入）
	if e.notifier != nil {
		if err := e.notifier.NotifyEscalation(ctx, inc, p.LevelIdx, targets); err != nil {
			// 通知失败不阻塞升级链（记日志，继续）
		}
	}

	// 5. 记时间线 + 更新 Incident 升级状态
	now := time.Now()
	if err := e.db.Incident.UpdateOneID(inc.ID).
		SetStatus(incident.StatusEscalated).
		SetCurrentLevel(p.LevelIdx + 1).
		SetEscalatedCount(inc.EscalatedCount + 1).
		Exec(ctx); err != nil {
		return fmt.Errorf("update incident: %w", err)
	}
	if err := e.db.TimelineItem.Create().
		SetIncidentID(inc.ID).
		SetType("escalated").
		SetActor(map[string]string{"kind": "system"}).
		SetContent(fmt.Sprintf("升级到 level %d，通知 %d 人", p.LevelIdx+1, len(targets))).
		SetSource("system").
		SetTimestamp(now).
		Exec(ctx); err != nil {
		// 时间线失败不阻塞主流程
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
func (e *Engine) resolveTargets(ctx context.Context, targets []schema.Target) ([]NotifyTarget, error) {
	var out []NotifyTarget
	seen := map[int]bool{} // 去重
	for _, t := range targets {
		switch t.Type {
		case "schedule":
			schedID, _ := strconv.Atoi(t.TargetID)
			if schedID == 0 || e.sched == nil {
				continue
			}
			res, err := e.sched.OncallNow(ctx, schedID)
			if err != nil {
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
				continue
			}
			u, err := e.db.User.Get(ctx, uid)
			if err != nil {
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
	defer inspector.Close()
	// 删除所有可能的待触发任务（level × repeat 组合）
	for levelIdx := 0; levelIdx < len(levels); levelIdx++ {
		for repeatSeq := 0; repeatSeq <= repeatTimes; repeatSeq++ {
			taskID := escalationTaskID(incID, levelIdx, repeatSeq)
			if err := inspector.DeleteTask("critical", taskID); err != nil {
				// 任务可能已触发/不存在，忽略（状态守卫兜底）
			}
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
