// Package escalation 实现能力域 6：升级策略。
//
// 设计见 ADR-0016（Asynq 延迟任务 + 状态守卫）：
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
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/schema"
	"github.com/kevin/vigil/ent/team"
	"github.com/kevin/vigil/ent/timelineitem"
	"github.com/kevin/vigil/ent/user"
	domainevent "github.com/kevin/vigil/internal/event"
	"github.com/kevin/vigil/internal/metrics"
	"github.com/kevin/vigil/internal/queue"
	"github.com/kevin/vigil/internal/schedule"
	"github.com/kevin/vigil/internal/timeline"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
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
	// bus 领域事件总线。B10：自动升级（计时器到点触发）后发布 IncidentEscalated 事件，
	// 驱动 WS 推送 / IM 卡片刷新 / 出站 webhook 感知升级（原先只有手动/runbook escalate 走
	// incident.Service 发事件，自动升级对多端全盲）。为 nil 时跳过发布（降级/测试）。
	bus *domainevent.Bus
	// redisCli 通用 Redis 客户端（从 redisOpt 派生），用于 HandleTask 的一次性通知标记
	// （at-least-once 重投防重复通知/重复计数，见 ADR-0016「HandleTask 重投幂等」）。
	// nil（无 Redis）时降级为原 at-least-once 行为：可能重复通知，但绝不丢通知。
	redisCli redis.UniversalClient
}

// SetRecorder 注入时间线记录器。
func (e *Engine) SetRecorder(r *timeline.Recorder) {
	e.recorder = r
}

// SetBus 注入领域事件总线（装配时调用）。为 nil 时自动升级不发布事件（多端不感知）。
func (e *Engine) SetBus(b *domainevent.Bus) {
	e.bus = b
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
//
// B6：channels 为本层 EscalationLevel.notify_channels（如 [im,phone,sms]），
// 通知引擎据此选择送达通道，而非固定用全局默认通道——不同层可差异化升级强度
// （level 1 只 im、level 3 加 phone/sms）。channels 为空时由实现方降级到默认通道。
type Notifier interface {
	NotifyEscalation(ctx context.Context, inc *ent.Incident, level int, targets []NotifyTarget, channels []string) error
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
	e := &Engine{db: db, queue: q, sched: sched, notifier: notifier, redisOpt: redisOpt}
	// 从同一 Redis 连接信息派生通用客户端，供 HandleTask 一次性通知标记用——
	// 不新增装配入口（复用 redisOpt），保证标记与升级任务落在同一 Redis 实例。
	if redisOpt != nil {
		if cli, ok := redisOpt.MakeRedisClient().(redis.UniversalClient); ok {
			e.redisCli = cli
		}
	}
	return e
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
		asynq.MaxRetry(escalationMaxRetry), // ADR-0016：显式高重试，升级任务绝不能丢
		asynq.Retention(5 * time.Minute),   // 触发后保留 5 分钟便于排查
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
	opts := []asynq.Option{
		asynq.Queue("critical"),
		asynq.TaskID(escalationTaskID(incID, levelIdx, repeatSeq)),
		asynq.MaxRetry(escalationMaxRetry), // ADR-0016：显式高重试，升级任务绝不能丢
	}
	if delay > 0 {
		opts = append(opts, asynq.ProcessIn(delay))
	}
	if _, err := e.queue.Client.EnqueueContext(ctx, task, opts...); err != nil {
		// TaskID 重复（同 key 任务已在队）是幂等场景：任务重投补排、对账 sweeper 多副本
		// 并发重排、reopen 残留等都会撞同一 TaskID——同 key 任务已存在即目标达成，按成功
		// 处理。原实现注释说忽略、代码却原样返回错误，导致上层把幂等命中当失败重试/告警。
		if errors.Is(err, asynq.ErrTaskIDConflict) {
			return nil
		}
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
		slog.Info("escalation: skip, incident not active", "incident_id", p.IncidentID, "status", incSt)
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

	// 4. 一次性通知标记（ADR-0016「HandleTask 重投幂等」）：asynq 是 at-least-once，
	// worker 在「已通知、已入队下一层、尚未 ack」窗口崩溃会重投同一任务，状态守卫不拦
	// （incident 仍 escalated）——通知/计数/时间线/事件会重复执行（通知轰炸）。
	// 用键含本任务 TaskID 的 Redis 标记判重：重投时标记命中，跳过这四步副作用，
	// 只保留状态推进 + 下一步排程（链的连续性是硬约束，无论重投与否都必须续排）。
	firstRun := e.claimNotifyOnce(ctx, &p)

	// 5. 通知（若 Notifier 已接入）。失败不阻塞升级链。
	// B6：透传本层 notify_channels，让各层按配置走对应通道（level 1 只 im，末级加 phone/sms）。
	if firstRun {
		if e.notifier != nil {
			// 通知投递已 Asynq 化（ADR-0017 修订）：这里的调用主体是「落 pending 行 + 入队
			// 投递任务」，瞬时失败由通知任务自行退避重试，不阻塞升级链。返回的 error 是
			// 「聚合且同步兜底均异常」级别的罕见故障，告警日志留痕（原实现静默丢弃）。
			if nerr := e.notifier.NotifyEscalation(ctx, inc, p.LevelIdx, targets, level.NotifyChannel); nerr != nil {
				e.log().Warn("escalation: notify dispatch failed",
					zap.Int("incident_id", p.IncidentID), zap.Int("level", p.LevelIdx), zap.Error(nerr))
			}
		}
		// 标记「先通知后落」而非先占后通知：宁可极小崩溃窗口（通知后、落标记前）重投
		// 重复通知一次，不可先占标记再崩导致重投跳过通知——升级通知静默丢失违背升级链
		// 的存在意义（at-least-once 优先于 at-most-once）。
		e.markNotified(ctx, &p)
	}

	// 6. 记时间线 + 更新 Incident 升级状态
	// 用 Save 拿回更新后的 incident 快照（供领域事件载荷携带最新状态/level）。
	// escalated_count 只在首投自增：它是"实际通知了几轮"的统计口径，重投不代表新一轮通知。
	// 状态/current_level 重投也照常写（幂等赋值）——首投可能在通知后、落库前崩溃，
	// 重投必须补齐状态推进，否则链推进与 DB 状态脱节（对账 sweeper 会按错误层级重排）。
	upd := e.db.Incident.UpdateOneID(inc.ID).
		SetStatus(incident.StatusEscalated).
		SetCurrentLevel(p.LevelIdx + 1)
	if firstRun {
		upd = upd.SetEscalatedCount(inc.EscalatedCount + 1)
	}
	updated, err := upd.Save(ctx)
	if err != nil {
		return fmt.Errorf("update incident: %w", err)
	}
	slog.Info("escalation: level processed", "incident_id", p.IncidentID, "level", p.LevelIdx+1, "first_run", firstRun)
	if firstRun {
		// 埋点：升级触发次数（重投不重复计数）
		metrics.EscalationsTriggered.Inc()
		// 通过统一 Recorder 记时间线（消除直接 ent 调用），失败不阻塞主流程。
		// meta 除人数外记录解析出的 target user id 列表——排班蓝图事后被改时，
		// 仍可追溯"当时实际该叫谁"（审计缺口：只记人数无法复原名单）。
		notifiedIDs := make([]int, 0, len(targets))
		for _, nt := range targets {
			notifiedIDs = append(notifiedIDs, nt.UserID)
		}
		if e.recorder != nil {
			_ = e.recorder.Record(ctx, inc.ID, timelineitem.TypeEscalated,
				fmt.Sprintf("升级到 level %d，通知 %d 人", p.LevelIdx+1, len(targets)),
				timeline.Actor{Kind: "system"}, timelineitem.SourceSystem,
				map[string]any{"level": p.LevelIdx + 1, "notified": len(targets), "notified_user_ids": notifiedIDs})
		}
		// B10：自动升级发布 IncidentEscalated 领域事件，驱动 WS/IM 卡片/出站 webhook 同步。
		// ActorID=0 表示系统触发（计时器到点），区别于手动升级（携带操作人 ID）。
		// Level 携带本次触发的 level 索引，与 incident.Service 手动升级事件语义一致。
		// SystemTriggered=true 打破反馈环：OnManualEscalate 据此跳过再触发（本 level 已处理完）。
		// 同步派发在本任务 goroutine 内完成，订阅方（webhook）自行异步化，不阻塞升级链。
		// 重投跳过（与通知同理：多端已同步过本 level，重复发布只造成 WS/webhook 重复推送）。
		if e.bus != nil {
			e.bus.Publish(ctx, domainevent.Event{
				Type:            domainevent.IncidentEscalated,
				Incident:        updated,
				Action:          domainevent.Action("escalate"),
				ActorID:         0,
				Level:           p.LevelIdx,
				SystemTriggered: true,
			})
		}
	}

	// 7. 安排下一步：repeat 或下一 level
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
			// team 通知全团队（末级兜底）：解算成团队全体在职成员（B9）。
			// 原实现仅占位 NotifyTarget{UserID:0, source:team}——邮件/电话按 user_id 解号，
			// UserID=0 时解不出任何人，team 型 target 的邮件/电话通知实际全丢，只有 IM 群卡片路径有效。
			// 这里展开为逐成员 NotifyTarget（真实 UserID），使各通道对全体成员逐一送达。
			teamID, _ := strconv.Atoi(t.TargetID)
			if teamID == 0 {
				e.log().Warn("escalation target: invalid team id",
					zap.String("target_id", t.TargetID))
				continue
			}
			members, err := e.db.User.Query().
				Where(user.HasTeamsWith(team.IDEQ(teamID)), user.StatusEQ(user.StatusActive)).
				All(ctx)
			if err != nil {
				e.log().Warn("escalation target: query team members failed",
					zap.Int("team_id", teamID), zap.Error(err))
				continue
			}
			if len(members) == 0 {
				// 空团队（无在职成员）是末级兜底失效的严重信号，必须可观测。
				e.log().Warn("escalation target: team has no active members",
					zap.Int("team_id", teamID))
				continue
			}
			for _, u := range members {
				if !seen[u.ID] {
					seen[u.ID] = true
					out = append(out, NotifyTarget{UserID: u.ID, Name: u.Name, Source: "team"})
				}
			}
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

// CancelLevelPending 取消某一 level 所有 repeat 序号的待触发升级任务（B6b）。
//
// 用于手动跳级：人主动 escalate 到 level N 时，自动升级链此前已为 level N 排了延迟任务
// （level[N-1] 处理后 scheduleLevel(N)），若不取消，手动立即触发 level N 之外，
// 那条延迟任务到点还会再触发一次 level N —— 同层重复通知（轰炸/困惑）。
// 这里删掉 level N 的所有 pending（repeat 0..repeatTimes），只保留手动的 now: 立即任务。
// 复用 CancelOnAck 的删除语义（inspector.DeleteTask，任务已触发/不存在则忽略）。
// 无 Redis（inspector=nil）时跳过——状态守卫无法防同层重复，但这是降级路径，可接受。
func (e *Engine) CancelLevelPending(ctx context.Context, incID, levelIdx, repeatTimes int) error {
	inspector := e.inspector()
	if inspector == nil {
		return nil
	}
	defer func() { _ = inspector.Close() }()
	for repeatSeq := 0; repeatSeq <= repeatTimes; repeatSeq++ {
		_ = inspector.DeleteTask("critical", escalationTaskID(incID, levelIdx, repeatSeq))
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

	// escalationMaxRetry 升级任务显式重试上限（scheduleLevel / TriggerLevelNow 共用）。
	// ADR-0016 要求「升级任务绝不能丢：高优先级队列 + 高 MaxRetry」，原实现从未显式配置、
	// 静默依赖 asynq 库默认值（恰为 25）——显式声明使语义自包含，不随库默认漂移。
	escalationMaxRetry = 25

	// notifyMarkerPrefix 一次性通知标记的键前缀（+ 本任务 TaskID）。
	notifyMarkerPrefix = "vigil:escalation:notified:"
	// notifyMarkerTTL 标记保留时长。重投发生在 asynq 租约过期/重试退避窗口内（分钟到小时级），
	// 24h 足以覆盖现实重投窗口；过期后极端晚到的重投会重复通知一次（可接受，标记不无限膨胀）。
	notifyMarkerTTL = 24 * time.Hour
)

// notifyMarkerKey 生成本任务的一次性通知标记键。
// 优先用 asynq 上下文里的真实 TaskID（now: 手动触发任务带纳秒时间戳，每次手动升级
// 都是独立任务，各自独立判重——不会误吸收用户显式的再次触发）；
// 直接调用（测试/无 asynq 上下文）时回退 payload 派生的幂等 ID（esc:{inc}:{level}:{seq}）。
// 无 Redis 时返回 ""（调用方降级为 at-least-once 原行为）。
func (e *Engine) notifyMarkerKey(ctx context.Context, p *escalationTask) string {
	if e.redisCli == nil {
		return ""
	}
	id, ok := asynq.GetTaskID(ctx)
	if !ok || id == "" {
		id = escalationTaskID(p.IncidentID, p.LevelIdx, p.RepeatSeq)
	}
	return notifyMarkerPrefix + id
}

// claimNotifyOnce 判断本任务是否首投（标记未命中）。
// Redis 不可用/探查失败时按首投处理——降级语义是「可能重复通知，绝不丢通知」。
func (e *Engine) claimNotifyOnce(ctx context.Context, p *escalationTask) bool {
	key := e.notifyMarkerKey(ctx, p)
	if key == "" {
		return true
	}
	n, err := e.redisCli.Exists(ctx, key).Result()
	if err != nil {
		e.log().Warn("escalation: notify marker probe failed, treat as first run",
			zap.Int("incident_id", p.IncidentID), zap.Error(err))
		return true
	}
	return n == 0
}

// markNotified 落一次性通知标记（通知完成后调用，见 HandleTask 步骤 5 的顺序论证）。
// SetNX 表达"一次性"语义；失败只记 warn（最坏退化为重投时再通知一次）。
func (e *Engine) markNotified(ctx context.Context, p *escalationTask) {
	key := e.notifyMarkerKey(ctx, p)
	if key == "" {
		return
	}
	if err := e.redisCli.SetNX(ctx, key, "1", notifyMarkerTTL).Err(); err != nil {
		e.log().Warn("escalation: set notify marker failed",
			zap.Int("incident_id", p.IncidentID), zap.Error(err))
	}
}

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
