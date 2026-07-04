// Package subscription 实现 T4.4：subscriber 定向订阅通知。
//
// 对应 docs/roadmap-completeness.md T4.4 与旅程 E.2「第一时间被告知」。
//
// 机制：让干系人（尤其 subscriber / 团队 Leader）订阅关注对象（team 或 service），
// 当其 Incident 生命周期变更（created/acked/resolved 等）时，除值班人/升级 target 外，
// 给订阅了该 team/service 的订阅者发定向通知。
//
// 定位：订阅是「多一类通知接收人来源」——复用 T2.2 通知链（notification.Notifier）分发与送达记录，
// 本包只负责「事件 → 解算订阅者 → 交给通知链定向送达」。quiet_hours 对非值班订阅者生效
// （notification.Notifier.NotifyTargeted 内一律按非值班人判定，夜间非 critical 抑制）。
package subscription

import (
	"context"
	"log/slog"

	"github.com/kevin/vigil/ent"
	entincident "github.com/kevin/vigil/ent/incident"
	entservice "github.com/kevin/vigil/ent/service"
	entsubscription "github.com/kevin/vigil/ent/subscription"
	entteam "github.com/kevin/vigil/ent/team"
	"github.com/kevin/vigil/ent/user"
	"github.com/kevin/vigil/internal/event"
	"github.com/kevin/vigil/internal/notification"
)

// targetedNotifier 收敛通知链的定向送达能力（便于测试打桩）。
// 由 notification.Notifier 实现（NotifyTargeted）。
type targetedNotifier interface {
	NotifyTargeted(ctx context.Context, inc *ent.Incident, targets []notification.Target, channels []string) error
}

// severityRank 严重度阈值序：critical > warning > info。数值越大越严重，用于 min_severity 过滤。
var severityRank = map[string]int{"info": 1, "warning": 2, "critical": 3}

// Notifier 订阅定向通知引擎：订阅 Incident 生命周期事件，给订阅了该 team/service 的干系人发定向通知。
type Notifier struct {
	db       *ent.Client
	notifier targetedNotifier
}

// NewNotifier 创建订阅定向通知引擎。notifier 为通知链（notification.Notifier）。
func NewNotifier(db *ent.Client, n targetedNotifier) *Notifier {
	return &Notifier{db: db, notifier: n}
}

// Subscribe 把本引擎挂到领域事件总线：Incident 全生命周期变更都触发订阅定向通知。
//
// 订阅事件集与 WS/webhook/IM 卡片同款——订阅者关心的是「状态变了」这件事本身。
// 注：created/acked/resolved/closed/reopened/escalated/responder_added 均纳入。
func (n *Notifier) Subscribe(bus *event.Bus) {
	for _, t := range []event.Type{
		event.IncidentCreated,
		event.IncidentAcked,
		event.IncidentEscalated,
		event.IncidentResolved,
		event.IncidentClosed,
		event.IncidentReopened,
		event.IncidentResponderAdded,
	} {
		bus.Subscribe(t, n.OnIncidentEvent)
	}
}

// OnIncidentEvent 处理 Incident 生命周期事件：解算订阅者 → 定向通知。
//
// best-effort：任一步失败仅记日志，不阻断事件派发（与事件总线扇出契约一致）。
// 事件载荷里的 *ent.Incident 是快照（team/service edge 未加载），故按 ID 重取带边的 incident。
func (n *Notifier) OnIncidentEvent(ctx context.Context, ev event.Event) error {
	if ev.Incident == nil || n.notifier == nil {
		return nil
	}
	inc, err := n.db.Incident.Query().
		Where(entincident.IDEQ(ev.Incident.ID)).
		WithTeam().
		WithService().
		Only(ctx)
	if err != nil {
		slog.Warn("subscription: load incident failed", "incident_id", ev.Incident.ID, "error", err)
		return nil
	}

	subs, err := n.resolveSubscriptions(ctx, inc)
	if err != nil {
		slog.Warn("subscription: resolve subscriptions failed", "incident_id", inc.ID, "error", err)
		return nil
	}
	if len(subs) == 0 {
		return nil
	}

	incSevRank := severityRank[string(inc.Severity)]
	for _, sub := range subs {
		usr := sub.Edges.User
		if usr == nil || usr.Status != user.StatusActive {
			continue // 订阅者被禁用/查不到：不发（禁用即失效，与 T0.3 一致）
		}
		// min_severity 过滤：Incident 严重度低于订阅阈值则不告知（屏蔽订阅者不关心的低级噪音）。
		if incSevRank < severityRank[string(sub.MinSeverity)] {
			continue
		}
		targets := []notification.Target{{UserID: usr.ID, Name: usr.Name, Source: "user"}}
		// 交给通知链定向送达（quiet_hours 在链内对非值班订阅者生效）。best-effort：失败记日志。
		if e := n.notifier.NotifyTargeted(ctx, inc, targets, sub.Channels); e != nil {
			slog.Warn("subscription: targeted notify failed",
				"incident_id", inc.ID, "user_id", usr.ID, "error", e)
		}
	}
	return nil
}

// resolveSubscriptions 解算该 Incident 的适用订阅（team 订阅 ∪ service 订阅，按订阅者去重）。
//
// 一个订阅者可能同时订阅了该 Incident 的 team 与 service（两条订阅命中同一 Incident）——
// 按 user 去重，只发一次，避免重复打扰；去重时保留第一条命中的订阅偏好（channels/min_severity）。
func (n *Notifier) resolveSubscriptions(ctx context.Context, inc *ent.Incident) ([]*ent.Subscription, error) {
	var all []*ent.Subscription

	// team 订阅：Incident 归属 team 被订阅。
	if inc.Edges.Team != nil {
		teamSubs, err := n.db.Subscription.Query().
			Where(entsubscription.HasTeamWith(entteam.IDEQ(inc.Edges.Team.ID))).
			WithUser().
			All(ctx)
		if err != nil {
			return nil, err
		}
		all = append(all, teamSubs...)
	}
	// service 订阅：Incident 归属 service 被订阅。
	if inc.Edges.Service != nil {
		svcSubs, err := n.db.Subscription.Query().
			Where(entsubscription.HasServiceWith(entservice.IDEQ(inc.Edges.Service.ID))).
			WithUser().
			All(ctx)
		if err != nil {
			return nil, err
		}
		all = append(all, svcSubs...)
	}

	// 按订阅者去重（同一人订了 team 又订了 service 只发一次）。
	seen := map[int]bool{}
	out := make([]*ent.Subscription, 0, len(all))
	for _, s := range all {
		if s.Edges.User == nil {
			continue
		}
		uid := s.Edges.User.ID
		if seen[uid] {
			continue
		}
		seen[uid] = true
		out = append(out, s)
	}
	return out, nil
}
