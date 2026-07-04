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
	"github.com/kevin/vigil/ent/predicate"
	"github.com/kevin/vigil/ent/service"
	"github.com/kevin/vigil/ent/team"
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

// Scope 团队数据隔离范围（S14）。
//
// 报表是团队软隔离的数据归属边界之一：团队 Leader 只应看到本团队指标，
// 跨团队数据不得出现在其报表里；org 级角色（org_admin）可看全组织。
//
// 语义（与 auth.VisibleTeamIDs 对齐）：
//   - OrgWide=true：不做团队过滤，聚合全组织（org_admin 等 org 级角色）。
//   - OrgWide=false 且 TeamIDs 非空：仅聚合这些 team 归属的数据。
//   - OrgWide=false 且 TeamIDs 为空：无任何可见 team，各指标应为空（0）。
//
// 未挂 team 的数据（如未路由 Event、无 team 归属的复盘）不属于任何团队，
// 因此团队 scope 下不计入；只有 OrgWide 视图才纳入。
type Scope struct {
	OrgWide bool
	TeamIDs []int
}

// AllTeams 返回看全组织的 scope（内部汇总/系统调用用，如 Dashboard 兜底）。
func AllTeams() Scope { return Scope{OrgWide: true} }

// empty 表示该 scope 无任何可见 team（非 org 级且 team 列表为空）→ 指标恒为空。
func (s Scope) empty() bool { return !s.OrgWide && len(s.TeamIDs) == 0 }

// eventTeamPred 返回 Event 的团队归属谓词（Event → service → team）。
// OrgWide 返回 nil（不过滤）。
func (s Scope) eventTeamPred() predicate.Event {
	if s.OrgWide {
		return nil
	}
	return event.HasServiceWith(service.HasTeamWith(team.IDIn(s.TeamIDs...)))
}

// incidentTeamPred 返回 Incident 的团队归属谓词（Incident → team）。
func (s Scope) incidentTeamPred() predicate.Incident {
	if s.OrgWide {
		return nil
	}
	return incident.HasTeamWith(team.IDIn(s.TeamIDs...))
}

// postmortemTeamPred 返回 Postmortem 的团队归属谓词（Postmortem → incident → team）。
func (s Scope) postmortemTeamPred() predicate.Postmortem {
	if s.OrgWide {
		return nil
	}
	return postmortem.HasIncidentWith(incident.HasTeamWith(team.IDIn(s.TeamIDs...)))
}

// AlertMetrics 告警度量（能力域 15 §B1）。
type AlertMetrics struct {
	Total     int     `json:"total"`     // 接入总量
	Notified  int     `json:"notified"`  // 触发通知的（非噪音）
	NoiseRate float64 `json:"noiseRate"` // 降噪率 = 1 - Notified/Total（0~1）
	Unrouted  int     `json:"unrouted"`  // 未命中路由
}

// AlertMetrics 计算告警度量。
func (e *Engine) AlertMetrics(ctx context.Context, r Range, scope Scope) (*AlertMetrics, error) {
	// 团队 scope 但无可见 team：无数据，直接返回空指标（避免误聚合全组织）。
	if scope.empty() {
		return &AlertMetrics{}, nil
	}
	q := e.db.Event.Query()
	if !r.Start.IsZero() {
		q = q.Where(event.ReceivedAtGTE(r.Start))
	}
	if !r.End.IsZero() {
		q = q.Where(event.ReceivedAtLTE(r.End))
	}
	if p := scope.eventTeamPred(); p != nil {
		q = q.Where(p)
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
	// unrouted = 未命中路由（无关联 service）且非噪音（C25）。
	// 被标噪的 Event 是「已被降噪判定」，不属于「等待人工分诊」的未路由口径；
	// 若把噪音也计入会让 unrouted 偏大、误导团队以为路由覆盖不足。
	unrouted, err := q.Clone().Where(event.Not(event.HasService()), event.IsNoiseEQ(false)).Count(ctx)
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
func (e *Engine) IncidentMetrics(ctx context.Context, r Range, scope Scope) (*IncidentMetrics, error) {
	if scope.empty() {
		return &IncidentMetrics{BySeverity: map[string]int{}, ByStatus: map[string]int{}}, nil
	}
	q := e.db.Incident.Query()
	if !r.Start.IsZero() {
		q = q.Where(incident.CreatedAtGTE(r.Start))
	}
	if !r.End.IsZero() {
		q = q.Where(incident.CreatedAtLTE(r.End))
	}
	if p := scope.incidentTeamPred(); p != nil {
		q = q.Where(p)
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
func (e *Engine) TeamLoad(ctx context.Context, r Range, scope Scope) ([]TeamLoad, error) {
	if scope.empty() {
		return nil, nil
	}
	tq := e.db.Team.Query()
	// 团队 scope：只列出可见 team（org 级不限）。
	if !scope.OrgWide {
		tq = tq.Where(team.IDIn(scope.TeamIDs...))
	}
	teams, err := tq.All(ctx)
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
func (e *Engine) PostmortemMetrics(ctx context.Context, r Range, scope Scope) (*PostmortemMetrics, error) {
	if scope.empty() {
		return &PostmortemMetrics{}, nil
	}
	q := e.db.Postmortem.Query()
	if !r.Start.IsZero() {
		q = q.Where(postmortem.CreatedAtGTE(r.Start))
	}
	if !r.End.IsZero() {
		q = q.Where(postmortem.CreatedAtLTE(r.End))
	}
	if p := scope.postmortemTeamPred(); p != nil {
		q = q.Where(p)
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
func (e *Engine) Trend(ctx context.Context, days int, r Range, scope Scope) ([]TrendPoint, error) {
	if days <= 0 {
		days = 7
	}
	end := r.End
	if end.IsZero() {
		end = time.Now()
	}
	start := end.AddDate(0, 0, -days)

	// 按天聚合骨架（即使无可见 team 也返回连续日期序列，前端图表不断档）。
	points := make([]TrendPoint, days)
	for i := 0; i < days; i++ {
		d := start.AddDate(0, 0, i)
		points[i] = TrendPoint{Date: d.Format("2006-01-02")}
	}
	if scope.empty() {
		return points, nil
	}

	incQ := e.db.Incident.Query().
		Where(incident.CreatedAtGTE(start), incident.CreatedAtLTE(end))
	if p := scope.incidentTeamPred(); p != nil {
		incQ = incQ.Where(p)
	}
	allInc, err := incQ.All(ctx)
	if err != nil {
		return nil, err
	}
	evtQ := e.db.Event.Query().
		Where(event.ReceivedAtGTE(start), event.ReceivedAtLTE(end))
	if p := scope.eventTeamPred(); p != nil {
		evtQ = evtQ.Where(p)
	}
	allEvt, err := evtQ.All(ctx)
	if err != nil {
		return nil, err
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
func (e *Engine) Dashboard(ctx context.Context, days int, scope Scope) (*Dashboard, error) {
	if days <= 0 {
		days = 7
	}
	end := time.Now()
	start := end.AddDate(0, 0, -days)
	r := Range{Start: start, End: end}

	alert, err := e.AlertMetrics(ctx, r, scope)
	if err != nil {
		return nil, err
	}
	inc, err := e.IncidentMetrics(ctx, r, scope)
	if err != nil {
		return nil, err
	}
	load, err := e.TeamLoad(ctx, r, scope)
	if err != nil {
		return nil, err
	}
	pm, err := e.PostmortemMetrics(ctx, r, scope)
	if err != nil {
		return nil, err
	}
	return &Dashboard{Alert: alert, Incident: inc, Load: load, Postmortem: pm}, nil
}
