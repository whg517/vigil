// aggregator.go 定时聚合任务（T6.1，能力域 15 §B3）。
//
// 对应 docs/capabilities/10-integrations-analytics.md §B3：「聚合任务：定时 Asynq 任务（每小时/每日）」。
//
// 场景：大数据量下实时聚合（每次报表请求全表扫 Event/Incident）慢且抖动。本聚合器周期性
// 把各团队（及 org 全局）的指标预计算成 MetricsSnapshot 存库，报表端点可选读快照
// （source=snapshot）快速返回；默认仍读实时保准确。
//
// 设计：
//   - Snapshotter.Aggregate 对每个团队 + org 全局各算一份快照（复用 Engine 现有查询）。
//   - 幂等：同 (team, period, period_start) 先删后建（覆盖重跑），不产重复行。
//   - 触发：既支持 Asynq 定时任务（TaskAggregate），也支持装配层 ticker 直接周期调
//     （与 sweeper 同款 goroutine，纳入优雅关闭）。二者调同一 Aggregate，互不冲突（幂等）。
package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/metricssnapshot"
	"github.com/kevin/vigil/ent/team"

	"github.com/hibiken/asynq"
)

// TaskAggregate 定时聚合任务类型（低优先级队列，可延迟）。
const TaskAggregate = "vigil:metrics_aggregate"

// aggregatePayload 聚合任务载荷。Period 决定聚合粒度与窗口长度。
type aggregatePayload struct {
	Period string `json:"period"` // hourly | daily
}

// Snapshotter 指标快照聚合器。
type Snapshotter struct {
	db     *ent.Client
	engine *Engine
}

// NewSnapshotter 创建聚合器。
func NewSnapshotter(db *ent.Client) *Snapshotter {
	return &Snapshotter{db: db, engine: NewEngine(db)}
}

// periodWindow 返回给定粒度「上一个完整窗口」的起止时间。
//   - daily：昨天 00:00（本地）~ 今天 00:00。
//   - hourly：上一个整点 ~ 当前整点。
//
// 取「已完整结束的窗口」聚合，避免把进行中的窗口定格成不完整快照。
func periodWindow(p metricssnapshot.Period, now time.Time) (start, end time.Time) {
	switch p {
	case metricssnapshot.PeriodHourly:
		end = now.Truncate(time.Hour)
		start = end.Add(-time.Hour)
	default: // daily
		end = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		start = end.AddDate(0, 0, -1)
	}
	return start, end
}

// Aggregate 为「上一个完整窗口」预计算并写入各团队 + org 全局的指标快照。
// 幂等：重跑同窗口覆盖旧快照。返回写入的快照条数。
func (s *Snapshotter) Aggregate(ctx context.Context, period metricssnapshot.Period) (int, error) {
	if err := metricssnapshot.PeriodValidator(period); err != nil {
		return 0, fmt.Errorf("invalid period %q: %w", period, err)
	}
	start, end := periodWindow(period, time.Now())
	r := Range{Start: start, End: end}

	// 收集要聚合的 scope：org 全局（team=nil）+ 每个团队。
	teams, err := s.db.Team.Query().All(ctx)
	if err != nil {
		return 0, fmt.Errorf("list teams: %w", err)
	}

	var written int
	// org 全局快照（team=nil，scope=AllTeams）。
	if err := s.writeSnapshot(ctx, period, start, end, r, nil, AllTeams()); err != nil {
		return written, err
	}
	written++
	// 每团队快照（scope 限定单团队）。
	for _, t := range teams {
		tid := t.ID
		scope := Scope{OrgWide: false, TeamIDs: []int{tid}}
		if err := s.writeSnapshot(ctx, period, start, end, r, &tid, scope); err != nil {
			return written, err
		}
		written++
	}
	return written, nil
}

// writeSnapshot 计算单个 scope 的各维度指标并 upsert 成一条快照。
// teamID=nil 表示 org 全局快照。
func (s *Snapshotter) writeSnapshot(
	ctx context.Context,
	period metricssnapshot.Period,
	start, end time.Time,
	r Range,
	teamID *int,
	scope Scope,
) error {
	alert, err := s.engine.AlertMetrics(ctx, r, scope)
	if err != nil {
		return fmt.Errorf("alert metrics: %w", err)
	}
	inc, err := s.engine.IncidentMetrics(ctx, r, scope)
	if err != nil {
		return fmt.Errorf("incident metrics: %w", err)
	}
	pm, err := s.engine.PostmortemMetrics(ctx, r, scope)
	if err != nil {
		return fmt.Errorf("postmortem metrics: %w", err)
	}

	// 幂等：先删同 (team, period, period_start) 旧快照再建（覆盖重跑）。
	// org 全局（team=nil）在多数库唯一约束允许多 NULL 行，故这里显式删兜底去重。
	delQ := s.db.MetricsSnapshot.Delete().
		Where(
			metricssnapshot.PeriodEQ(period),
			metricssnapshot.PeriodStartEQ(start),
		)
	if teamID != nil {
		delQ = delQ.Where(metricssnapshot.HasTeamWith(team.IDEQ(*teamID)))
	} else {
		delQ = delQ.Where(metricssnapshot.Not(metricssnapshot.HasTeam()))
	}
	if _, err := delQ.Exec(ctx); err != nil {
		return fmt.Errorf("delete stale snapshot: %w", err)
	}

	create := s.db.MetricsSnapshot.Create().
		SetPeriod(period).
		SetPeriodStart(start).
		SetPeriodEnd(end).
		SetAlertsTotal(alert.Total).
		SetAlertsNotified(alert.Notified).
		SetAlertsUnrouted(alert.Unrouted).
		SetNoiseRate(alert.NoiseRate).
		SetIncidentsTotal(inc.Total).
		SetIncidentsResolved(inc.ResolvedCount).
		SetMttaSeconds(inc.MTTARatio).
		SetMttrSeconds(inc.MTTRatio).
		SetBySeverity(inc.BySeverity).
		SetByStatus(inc.ByStatus).
		SetPostmortemsTotal(pm.Total).
		SetPostmortemsPublished(pm.Published).
		SetCompletionRate(pm.CompletionRate)
	if teamID != nil {
		create = create.SetTeamID(*teamID)
	}
	if _, err := create.Save(ctx); err != nil {
		return fmt.Errorf("save snapshot: %w", err)
	}
	return nil
}

// —— 读快照路径（报表端点 source=snapshot 时用）——

// LatestAlertFromSnapshot 从快照读某 scope 的告警度量（返回最近一个 daily 快照的合计）。
//
// 快照按团队分行存储：team 级 scope 读该团队最新快照；org 级（OrgWide）读 org 全局快照（team=nil）。
// 无快照时返回 nil, nil（调用方降级到实时）。
func (s *Snapshotter) LatestAlertFromSnapshot(ctx context.Context, scope Scope, period metricssnapshot.Period) (*AlertMetrics, error) {
	snap, err := s.latestSnapshot(ctx, scope, period)
	if err != nil || snap == nil {
		return nil, err
	}
	return &AlertMetrics{
		Total:     snap.AlertsTotal,
		Notified:  snap.AlertsNotified,
		NoiseRate: snap.NoiseRate,
		Unrouted:  snap.AlertsUnrouted,
	}, nil
}

// LatestIncidentFromSnapshot 从快照读某 scope 的事件度量。无快照返回 nil, nil。
func (s *Snapshotter) LatestIncidentFromSnapshot(ctx context.Context, scope Scope, period metricssnapshot.Period) (*IncidentMetrics, error) {
	snap, err := s.latestSnapshot(ctx, scope, period)
	if err != nil || snap == nil {
		return nil, err
	}
	m := &IncidentMetrics{
		Total:         snap.IncidentsTotal,
		ResolvedCount: snap.IncidentsResolved,
		MTTARatio:     snap.MttaSeconds,
		MTTRatio:      snap.MttrSeconds,
		BySeverity:    map[string]int{},
		ByStatus:      map[string]int{},
	}
	// by_severity/by_status 是 JSON map；ent 直接反序列化为 map[string]int。
	if snap.BySeverity != nil {
		m.BySeverity = snap.BySeverity
	}
	if snap.ByStatus != nil {
		m.ByStatus = snap.ByStatus
	}
	return m, nil
}

// LatestPostmortemFromSnapshot 从快照读某 scope 的复盘度量。无快照返回 nil, nil。
func (s *Snapshotter) LatestPostmortemFromSnapshot(ctx context.Context, scope Scope, period metricssnapshot.Period) (*PostmortemMetrics, error) {
	snap, err := s.latestSnapshot(ctx, scope, period)
	if err != nil || snap == nil {
		return nil, err
	}
	return &PostmortemMetrics{
		Total:          snap.PostmortemsTotal,
		Published:      snap.PostmortemsPublished,
		CompletionRate: snap.CompletionRate,
	}, nil
}

// latestSnapshot 取给定 scope + 粒度的最近一条快照。
//   - OrgWide：读 org 全局快照（team=nil）。
//   - 单团队 scope：读该团队最新快照。
//   - 多团队/空 scope：快照按单团队分行，无法直接合并，返回 nil（调用方降级实时）。
func (s *Snapshotter) latestSnapshot(ctx context.Context, scope Scope, period metricssnapshot.Period) (*ent.MetricsSnapshot, error) {
	q := s.db.MetricsSnapshot.Query().
		Where(metricssnapshot.PeriodEQ(period)).
		Order(ent.Desc(metricssnapshot.FieldPeriodStart)).
		Limit(1)
	switch {
	case scope.OrgWide:
		q = q.Where(metricssnapshot.Not(metricssnapshot.HasTeam()))
	case len(scope.TeamIDs) == 1:
		q = q.Where(metricssnapshot.HasTeamWith(team.IDEQ(scope.TeamIDs[0])))
	default:
		// 多团队或空 scope：快照按单团队分行，跨团队合并不在快照口径内，降级实时。
		return nil, nil
	}
	snap, err := q.Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return snap, nil
}

// —— Asynq 任务与巡检 ——

// EnqueueAggregate 构造定时聚合任务。
func EnqueueAggregate(period metricssnapshot.Period) (*asynq.Task, error) {
	payload, err := json.Marshal(aggregatePayload{Period: string(period)})
	if err != nil {
		return nil, fmt.Errorf("marshal aggregate payload: %w", err)
	}
	// 低优先级队列：报表聚合可延迟，不与升级/接入争资源。
	return asynq.NewTask(TaskAggregate, payload, asynq.Queue("low")), nil
}

// HandleTask 消费聚合任务（Asynq worker）。
func (s *Snapshotter) HandleTask(ctx context.Context, t *asynq.Task) error {
	var p aggregatePayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("unmarshal aggregate payload: %w", err)
	}
	period := metricssnapshot.Period(p.Period)
	if period == "" {
		period = metricssnapshot.PeriodDaily
	}
	n, err := s.Aggregate(ctx, period)
	if err != nil {
		return fmt.Errorf("aggregate %s: %w", period, err)
	}
	slog.Info("metrics snapshot aggregated", "period", string(period), "snapshots", n)
	return nil
}

// Run 周期性直接触发聚合（装配层 ticker 兜底，与 sweeper 同款 goroutine，纳入优雅关闭）。
// interval<=0 用默认 1 小时。每轮聚合 daily 快照（幂等，重跑覆盖当日快照）。
// ctx 取消时退出。装配层 go s.Run(ctx, interval) 启动，把 cancel 收入 Wired.Closers。
func (s *Snapshotter) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n, err := s.Aggregate(ctx, metricssnapshot.PeriodDaily); err != nil {
				slog.Warn("metrics snapshot aggregate failed", "error", err)
			} else {
				slog.Info("metrics snapshot aggregated (ticker)", "snapshots", n)
			}
		}
	}
}
