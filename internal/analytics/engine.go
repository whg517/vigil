// Package analytics 实现能力域 15：数据报表。
//
// 对应 docs/capabilities/10-integrations-analytics.md §B：
// · 告警度量（接入量/降噪率/unrouted）
// · 事件度量（数量/severity 分布/MTTA/MTTR）
// · 团队负载（值班次数/夜间打扰/人均事件）
// · 复盘度量（完成率/action_item 闭环率）
// · 趋势（时间序列）
//
// 纯查询聚合，不修改数据。所有指标支持时间范围筛选。
package analytics

import (
	"context"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/event"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/postmortem"
)

// Engine 报表引擎。
type Engine struct {
	db *ent.Client
}

// NewEngine 创建报表引擎。
func NewEngine(db *ent.Client) *Engine {
	return &Engine{db: db}
}

// Range 时间范围筛选参数。Start/End 为零值时不限。
type Range struct {
	Start time.Time
	End   time.Time
}

// AlertMetrics 告警度量（能力域 15 §B1）。
type AlertMetrics struct {
	Total     int     `json:"total"`     // 接入总量
	Notified  int     `json:"notified"`  // 触发通知的（非噪音）
	NoiseRate float64 `json:"noiseRate"` // 降噪率 = 1 - Notified/Total（0~1）
	Unrouted  int     `json:"unrouted"`  // 未命中路由
}

// AlertMetrics 计算告警度量。
func (e *Engine) AlertMetrics(ctx context.Context, r Range) (*AlertMetrics, error) {
	q := e.db.Event.Query()
	if !r.Start.IsZero() {
		q = q.Where(event.ReceivedAtGTE(r.Start))
	}
	if !r.End.IsZero() {
		q = q.Where(event.ReceivedAtLTE(r.End))
	}
	total, err := q.Count(ctx)
	if err != nil {
		return nil, err
	}
	// 非噪音 = is_noise=false
	notified, err := q.Clone().Where(event.IsNoiseEQ(false)).Count(ctx)
	if err != nil {
		return nil, err
	}
	// unrouted = service edge 为空（未命中路由，等待人工分诊）。
	// 用 event.Not(event.HasService()) 判定「无关联 service」。
	unrouted, err := q.Clone().Where(event.Not(event.HasService())).Count(ctx)
	if err != nil {
		return nil, err
	}
	m := &AlertMetrics{Total: total, Notified: notified, Unrouted: unrouted}
	if total > 0 {
		m.NoiseRate = 1 - float64(notified)/float64(total)
	}
	return m, nil
}

// IncidentMetrics 事件度量（能力域 15 §B2）。
type IncidentMetrics struct {
	Total         int            `json:"total"`
	BySeverity    map[string]int `json:"bySeverity"` // critical/warning/info 各数量
	ByStatus      map[string]int `json:"byStatus"`
	MTTARatio     float64        `json:"mttaratio"`     // 平均确认时长（秒），无数据为 0
	MTTRatio      float64        `json:"mttratio"`      // 平均解决时长（秒）
	ResolvedCount int            `json:"resolvedCount"` // 已解决数（用于 MTTR 计算）
}

// IncidentMetrics 计算事件度量。MTTA = acked_at - created_at，MTTR = resolved_at - created_at。
func (e *Engine) IncidentMetrics(ctx context.Context, r Range) (*IncidentMetrics, error) {
	q := e.db.Incident.Query()
	if !r.Start.IsZero() {
		q = q.Where(incident.CreatedAtGTE(r.Start))
	}
	if !r.End.IsZero() {
		q = q.Where(incident.CreatedAtLTE(r.End))
	}
	all, err := q.All(ctx)
	if err != nil {
		return nil, err
	}
	m := &IncidentMetrics{
		Total:      len(all),
		BySeverity: map[string]int{},
		ByStatus:   map[string]int{},
	}
	var ackSum, resolveSum float64
	ackCount := 0
	for _, inc := range all {
		m.BySeverity[string(inc.Severity)]++
		m.ByStatus[string(inc.Status)]++
		// MTTA: 已确认的 acked_at - created_at
		if inc.AckedAt != nil {
			ackCount++
			ackSum += inc.AckedAt.Sub(inc.CreatedAt).Seconds()
		}
		// MTTR: resolved 的 resolved_at - created_at
		if inc.ResolvedAt != nil {
			m.ResolvedCount++
			resolveSum += inc.ResolvedAt.Sub(inc.CreatedAt).Seconds()
		}
	}
	if ackCount > 0 {
		m.MTTARatio = ackSum / float64(ackCount)
	}
	if m.ResolvedCount > 0 {
		m.MTTRatio = resolveSum / float64(m.ResolvedCount)
	}
	return m, nil
}

// TeamLoad 团队负载（能力域 15 §B3）。
type TeamLoad struct {
	TeamID    int    `json:"teamID"`
	TeamName  string `json:"teamName"`
	Incidents int    `json:"incidents"` // 该团队事件数
}

// TeamLoad 计算各团队事件负载。
func (e *Engine) TeamLoad(ctx context.Context, r Range) ([]TeamLoad, error) {
	teams, err := e.db.Team.Query().All(ctx)
	if err != nil {
		return nil, err
	}
	var out []TeamLoad
	for _, t := range teams {
		q := t.QueryIncidents()
		if !r.Start.IsZero() {
			q = q.Where(incident.CreatedAtGTE(r.Start))
		}
		if !r.End.IsZero() {
			q = q.Where(incident.CreatedAtLTE(r.End))
		}
		cnt, err := q.Count(ctx)
		if err != nil {
			continue
		}
		out = append(out, TeamLoad{TeamID: t.ID, TeamName: t.Name, Incidents: cnt})
	}
	return out, nil
}

// PostmortemMetrics 复盘度量（能力域 15 §B4）。
type PostmortemMetrics struct {
	Total          int     `json:"total"`
	Published      int     `json:"published"`
	CompletionRate float64 `json:"completionRate"` // published/total
}

// PostmortemMetrics 计算复盘度量。
func (e *Engine) PostmortemMetrics(ctx context.Context, r Range) (*PostmortemMetrics, error) {
	q := e.db.Postmortem.Query()
	if !r.Start.IsZero() {
		q = q.Where(postmortem.CreatedAtGTE(r.Start))
	}
	if !r.End.IsZero() {
		q = q.Where(postmortem.CreatedAtLTE(r.End))
	}
	all, err := q.All(ctx)
	if err != nil {
		return nil, err
	}
	m := &PostmortemMetrics{Total: len(all)}
	for _, pm := range all {
		if postmortem.Status(pm.Status) == postmortem.StatusPublished || postmortem.Status(pm.Status) == postmortem.StatusArchived {
			m.Published++
		}
	}
	if m.Total > 0 {
		m.CompletionRate = float64(m.Published) / float64(m.Total)
	}
	return m, nil
}

// TrendPoint 趋势数据点。
type TrendPoint struct {
	Date      string `json:"date"` // YYYY-MM-DD
	Incidents int    `json:"incidents"`
	Events    int    `json:"events"`
}

// Trend 计算每日趋势（事件数 + 告警数）。
// days 为统计天数（从 End 向前数，End 为零值则用今天）。
func (e *Engine) Trend(ctx context.Context, days int, r Range) ([]TrendPoint, error) {
	if days <= 0 {
		days = 7
	}
	end := r.End
	if end.IsZero() {
		end = time.Now()
	}
	start := end.AddDate(0, 0, -days)

	allInc, err := e.db.Incident.Query().
		Where(incident.CreatedAtGTE(start), incident.CreatedAtLTE(end)).All(ctx)
	if err != nil {
		return nil, err
	}
	allEvt, err := e.db.Event.Query().
		Where(event.ReceivedAtGTE(start), event.ReceivedAtLTE(end)).All(ctx)
	if err != nil {
		return nil, err
	}
	// 按天聚合
	points := make([]TrendPoint, days)
	for i := 0; i < days; i++ {
		d := start.AddDate(0, 0, i)
		points[i] = TrendPoint{Date: d.Format("2006-01-02")}
	}
	idx := func(t time.Time) int { return int(t.Sub(start) / (24 * time.Hour)) }
	for _, inc := range allInc {
		i := idx(inc.CreatedAt)
		if i >= 0 && i < days {
			points[i].Incidents++
		}
	}
	for _, evt := range allEvt {
		i := idx(evt.ReceivedAt)
		if i >= 0 && i < days {
			points[i].Events++
		}
	}
	return points, nil
}

// Dashboard 仪表盘汇总（一次返回各维度概览，减少前端请求）。
type Dashboard struct {
	Alert      *AlertMetrics      `json:"alert"`
	Incident   *IncidentMetrics   `json:"incident"`
	Load       []TeamLoad         `json:"load"`
	Postmortem *PostmortemMetrics `json:"postmortem"`
}

// Dashboard 汇总仪表盘数据（近 N 天）。
func (e *Engine) Dashboard(ctx context.Context, days int) (*Dashboard, error) {
	if days <= 0 {
		days = 7
	}
	end := time.Now()
	start := end.AddDate(0, 0, -days)
	r := Range{Start: start, End: end}

	alert, err := e.AlertMetrics(ctx, r)
	if err != nil {
		return nil, err
	}
	inc, err := e.IncidentMetrics(ctx, r)
	if err != nil {
		return nil, err
	}
	load, err := e.TeamLoad(ctx, r)
	if err != nil {
		return nil, err
	}
	pm, err := e.PostmortemMetrics(ctx, r)
	if err != nil {
		return nil, err
	}
	return &Dashboard{Alert: alert, Incident: inc, Load: load, Postmortem: pm}, nil
}
