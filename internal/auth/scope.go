// scope.go 资源级鉴权 scope 解析器（ARCH-02 + SEC-01）。
//
// 背景：parseTeamScope 仅从 path param :team_id 取值，但业务路由都是 /resource/:id
// 形式（不带 team_id）→ teamScope 恒 nil → RBAC 只做 org 级判定 → 水平越权。
//
// ScopeResolver 按 (资源类型, 资源id) 反查归属 team_id，供资源级鉴权使用。
// 仿照 IM 已有的 incidentTeamScope（im/handler.go）模式，抽象成可复用、可注册的形式。
//
// 反查语义：
//   - 直接归属 team 的资源（incident/runbook/service/...）：QueryTeam().Only
//   - 间接归属（postmortem/action_item/ai_insight）：多级 join 回溯到 incident.team
//   - 资源无 team 归属（builtin 模板/未挂 team）：返回 nil（→ org 级判定）
//   - 资源不存在：返回 nil（不阻断，交后续 Get 返回 404，避免存在性泄露）
//
// 性能：每次反查 1 条 join 查询，操作类请求可接受；后续可加缓存。
package auth

import (
	"context"
	"fmt"

	"github.com/kevin/vigil/ent"
)

// ScopeResolver 按 (资源类型, 资源id) 反查归属 team_id。
//
// 零值不可用，须用 NewScopeResolver 构造并注册所需资源类型。
// 未注册的 kind 在 Resolve 时返回 (nil, nil)（视为无 team 归属，走 org 级判定）。
type ScopeResolver struct {
	db        *ent.Client
	resolvers map[string]func(ctx context.Context, id int) (*int, error)
}

// teamQuerier 收敛"能查 Team 的实体"的最小接口（ent 生成的具体实体都满足）。
// 用接口而非泛型，避免对每种 ent 类型重复约束。
type teamQuerier interface {
	QueryTeam() *ent.TeamQuery
}

// NewScopeResolver 创建 scope 解析器并注册全部业务资源的反查函数。
// db 为 nil 时所有 Resolve 返回 nil（降级，测试桩场景）。
func NewScopeResolver(db *ent.Client) *ScopeResolver {
	s := &ScopeResolver{db: db, resolvers: make(map[string]func(ctx context.Context, id int) (*int, error))}
	if db == nil {
		return s
	}
	// —— 直接归属 team 的资源 ——
	s.resolvers["incident"] = resolveDirect(db.Incident.Get) // *ent.Incident 实现 teamQuerier
	s.resolvers["runbook"] = resolveDirect(db.Runbook.Get)
	s.resolvers["service"] = resolveDirect(db.Service.Get)
	s.resolvers["integration"] = resolveDirect(db.Integration.Get)
	s.resolvers["ticket_integration"] = resolveDirect(db.TicketIntegration.Get)
	s.resolvers["schedule"] = resolveDirect(db.Schedule.Get)
	s.resolvers["escalation_policy"] = resolveDirect(db.EscalationPolicy.Get)
	s.resolvers["notification_rule"] = resolveDirect(db.NotificationRule.Get)
	s.resolvers["suppression_rule"] = resolveDirect(db.SuppressionRule.Get)
	s.resolvers["notification_template"] = resolveDirect(db.NotificationTemplate.Get)
	// —— 间接归属 team（经 incident）——
	s.resolvers["postmortem"] = s.resolvePostmortemTeam
	s.resolvers["action_item"] = s.resolveActionItemTeam
	s.resolvers["ai_insight"] = s.resolveAIInsightTeam
	s.resolvers["timeline_item"] = s.resolveTimelineTeam
	return s
}

// Resolve 按 kind+id 反查资源归属的 team_id。
// 返回 (teamID, err)：teamID 为 nil 表示无 team 归属/资源不存在/未注册 kind。
// 调用方应将 nil 传给 authz.Check 的 TeamScope（走 org 级判定）。
func (s *ScopeResolver) Resolve(ctx context.Context, kind string, id int) (*int, error) {
	if s == nil || s.resolvers == nil {
		return nil, nil
	}
	fn, ok := s.resolvers[kind]
	if !ok {
		return nil, nil // 未注册 kind：不阻断（视为 org 级）
	}
	return fn(ctx, id)
}

// resolveDirect 直接归属 team 资源的反查工厂。
// 用 Go 泛型收敛：T 是具体 ent 实体的指针类型（如 *ent.Incident），
// 约束为实现 teamQuerier（ent 的 QueryTeam 是指针接收者，故 T 须是指针）。
// getter 形如 db.Incident.Get（func(ctx, id) (T, error)，T=*ent.Incident）。
func resolveDirect[T teamQuerier](getter func(ctx context.Context, id int) (T, error)) func(ctx context.Context, id int) (*int, error) {
	return func(ctx context.Context, id int) (*int, error) {
		obj, err := getter(ctx, id)
		if err != nil {
			return nil, nil
		}
		return queryTeamID(ctx, obj.QueryTeam())
	}
}

// resolvePostmortemTeam postmortem → incident → team（间接归属）。
func (s *ScopeResolver) resolvePostmortemTeam(ctx context.Context, id int) (*int, error) {
	pm, err := s.db.Postmortem.Get(ctx, id)
	if err != nil {
		return nil, nil
	}
	inc, err := pm.QueryIncident().Only(ctx)
	if err != nil {
		return nil, nil
	}
	return queryTeamID(ctx, inc.QueryTeam())
}

// resolveActionItemTeam action_item → postmortem → incident → team（三级回溯）。
func (s *ScopeResolver) resolveActionItemTeam(ctx context.Context, id int) (*int, error) {
	ai, err := s.db.ActionItem.Get(ctx, id)
	if err != nil {
		return nil, nil
	}
	pm, err := ai.QueryPostmortem().Only(ctx)
	if err != nil {
		return nil, nil
	}
	inc, err := pm.QueryIncident().Only(ctx)
	if err != nil {
		return nil, nil
	}
	return queryTeamID(ctx, inc.QueryTeam())
}

// resolveAIInsightTeam ai_insight → incident → team（间接归属）。
func (s *ScopeResolver) resolveAIInsightTeam(ctx context.Context, id int) (*int, error) {
	ins, err := s.db.AIInsight.Get(ctx, id)
	if err != nil {
		return nil, nil
	}
	inc, err := ins.QueryIncident().Only(ctx)
	if err != nil {
		return nil, nil
	}
	return queryTeamID(ctx, inc.QueryTeam())
}

// resolveTimelineTeam timeline_item → incident → team（间接归属）。
func (s *ScopeResolver) resolveTimelineTeam(ctx context.Context, id int) (*int, error) {
	ti, err := s.db.TimelineItem.Get(ctx, id)
	if err != nil {
		return nil, nil
	}
	inc, err := ti.QueryIncident().Only(ctx)
	if err != nil {
		return nil, nil
	}
	return queryTeamID(ctx, inc.QueryTeam())
}

// queryTeamID 从 TeamQuery 取单个 team 的 ID（无 team 归属返回 nil）。
func queryTeamID(ctx context.Context, q *ent.TeamQuery) (*int, error) {
	t, err := q.Only(ctx)
	if err != nil || t == nil {
		return nil, nil
	}
	id := t.ID
	return &id, nil
}

// CheckResourceAccess 资源级鉴权统一入口（收口 handler 调用）。
// 反查资源归属 team → authz.Check。
//
// 返回 (allowed, err)：
//   - uid<=0（匿名/系统，渐进阶段）→ 放行（与 RequireUser enforce=false 一致）
//   - 资源无 team 归属（teamID=nil）→ org 级判定
//   - 鉴权失败或错误 → (false, err)
//
// handler 用法：
//
//	allowed, err := auth.CheckResourceAccess(ctx, h.authz, h.scope, uid, auth.PermIncidentView, "incident", id)
//	if err != nil { return errs.Internal(c, log, err) }
//	if !allowed { return errs.Forbidden(c, "no access to this resource") }
func CheckResourceAccess(ctx context.Context, authz *Authorizer, scope *ScopeResolver, uid int, perm Permission, kind string, id int) (bool, error) {
	if uid <= 0 {
		return true, nil // 匿名/系统：渐进阶段放行
	}
	teamID, err := scope.Resolve(ctx, kind, id)
	if err != nil {
		return false, fmt.Errorf("resolve scope: %w", err)
	}
	return authz.Check(ctx, AuthzRequest{
		UserID:     uid,
		Permission: perm,
		TeamScope:  teamID,
	})
}
