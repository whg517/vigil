// temp_grant.go 跨团队 @人 → 事件级临时授权（M8.3，ADR-0020）。
//
// 背景（软隔离铁律）：团队是数据归属边界，权限不跨团队继承。跨团队 @人拉入 incident
// 协同时，被 @的人若不在该 incident 所属 team、又无 org 级授权，就没有 ack/处置权限
// ——协作被软隔离边界挡住。
//
// 设计（拉人即授权，但不放宽软隔离）：
//   - 拉人时（AddResponder），若目标用户对该 incident 所属 team 无最小处置权限，
//     自动发放一个**事件级临时 RoleBinding**：role=responder、scope=该 incident 的 team、
//     带 expires_at（默认 24h 兜底）、标记 source_incident_id=该 incident id。
//   - 该授权**只对这一个 team 有效**（team scope，非 org），撤销/过期后 authz 实时查库立即失效
//     ——不给全局、不留后门，软隔离边界不被放宽。
//   - incident 关闭（closed/resolved/merged）时按 source_incident_id 精确撤销；
//     即使漏删，expires_at 也会兜底过期。
//
// 解耦：本组件经 SetResponderGranter 注入到 incident.Service（同 PostmortemGate 模式），
// Service 只调 ResponderGranter 接口，不直接依赖 auth 的鉴权/审计细节。
package incident

import (
	"context"
	"fmt"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/role"
	"github.com/kevin/vigil/ent/rolebinding"
	"github.com/kevin/vigil/internal/auth"
)

// DefaultTempGrantTTL 事件级临时授权默认有效期（兜底）。
// 语义：即使 incident 关闭联动撤销漏删，authz 实时按 expires_at 过滤，超期即失效。
// 24h 足够覆盖单个 incident 的典型协同处置时长；未过期时重复 @人幂等跳过（不刷新到期），
// 授权过期后再次拉入才会发放新的 24h 授权。
const DefaultTempGrantTTL = 24 * time.Hour

// ResponderGranter 事件级临时授权发放/撤销接口。
//
// 由 tempGranter 实现并经 incident.Service.SetResponderGranter 注入。
// 用接口而非直接依赖具体实现，与 PostmortemGate 同款解耦（便于测试注入 nil 降级）。
type ResponderGranter interface {
	// GrantForIncident 为 targetUserID 在 inc 所属 team 上确保有最小处置权限：
	// 若已有权限（本团队成员 / org 级授权 / 已有临时授权），不重复发放；
	// 否则发放一个 responder 角色、team scope、带 expires_at 与 source_incident_id 的临时 RoleBinding。
	// opUserID 为拉人操作者（记入 granted_by 与审计）。
	GrantForIncident(ctx context.Context, inc *ent.Incident, opUserID, targetUserID int) error
	// RevokeForIncident 撤销为 incID 发放的所有事件级临时授权（按 source_incident_id 精确删除）。
	RevokeForIncident(ctx context.Context, incID int) error
}

// tempGranter ResponderGranter 的默认实现。
type tempGranter struct {
	db    *ent.Client
	authz *auth.Authorizer
	audit *auth.AuditRecorder
	ttl   time.Duration
}

// NewResponderGranter 构造事件级临时授权发放器。
//
// ttl<=0 时用 DefaultTempGrantTTL。audit 可为 nil（降级：不落审计）。
// authz 用于判「目标用户是否已有该 team 处置权限」，避免给本就有权的人重复发临时授权。
func NewResponderGranter(db *ent.Client, authz *auth.Authorizer, audit *auth.AuditRecorder, ttl time.Duration) ResponderGranter {
	if ttl <= 0 {
		ttl = DefaultTempGrantTTL
	}
	return &tempGranter{db: db, authz: authz, audit: audit, ttl: ttl}
}

// GrantForIncident 见 ResponderGranter.GrantForIncident。
func (g *tempGranter) GrantForIncident(ctx context.Context, inc *ent.Incident, opUserID, targetUserID int) error {
	if inc == nil {
		return nil
	}
	// 取 incident 所属 team；无归属 team 则无从发 team scope 授权（跳过，软隔离对无 team 资源不适用）。
	team, err := inc.QueryTeam().Only(ctx)
	if err != nil || team == nil {
		return nil //nolint:nilerr // 无归属 team：不发临时授权（非错误，静默跳过）
	}
	teamID := team.ID

	// 幂等/不越权：若目标用户对该 team 已有最小处置权限（本团队成员角色 / org 级 / 已有临时授权），
	// 不重复发放——避免给本就有权的人堆叠冗余绑定，也满足「已在该 team 的人不重复发」。
	has, err := g.authz.Check(ctx, auth.AuthzRequest{
		UserID:     targetUserID,
		Permission: auth.PermIncidentAck, // 以「能否 ack」作为最小处置权限探针
		TeamScope:  &teamID,
	})
	if err != nil {
		return fmt.Errorf("check existing permission: %w", err)
	}
	if has {
		return nil // 已有权限，无需发放
	}

	// 定位内置 responder 角色（最小处置角色）。角色缺失（未 seed）视为配置异常，返回错误让上层记日志。
	r, err := g.db.Role.Query().Where(role.NameEQ(auth.ResponderRoleName)).Only(ctx)
	if err != nil {
		return fmt.Errorf("query %s role (run SeedBuiltinRoles first): %w", auth.ResponderRoleName, err)
	}

	expiresAt := time.Now().Add(g.ttl)
	_, err = g.db.RoleBinding.Create().
		SetUserID(targetUserID).
		SetRoleID(r.ID).
		SetScopeLevel(rolebinding.ScopeLevelTeam).
		SetTeamID(fmt.Sprintf("%d", teamID)).
		SetSourceIncidentID(inc.ID).
		SetExpiresAt(expiresAt).
		SetGrantedBy(fmt.Sprintf("%d", opUserID)).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("create temp role binding: %w", err)
	}

	// 审计（发放留痕，M13.5）：who 拉了 whom、授到哪个 team、来源 incident、到期时间。
	g.recordAudit(ctx, opUserID, "role.temp_grant", targetUserID, map[string]any{
		"reason":      "事件级临时授权",
		"incident_id": inc.ID,
		"team_id":     teamID,
		"role":        auth.ResponderRoleName,
		"expires_at":  expiresAt.Format(time.RFC3339),
		"target_user": targetUserID,
	})
	return nil
}

// RevokeForIncident 见 ResponderGranter.RevokeForIncident。
func (g *tempGranter) RevokeForIncident(ctx context.Context, incID int) error {
	if incID <= 0 {
		return nil
	}
	// 按 source_incident_id 精确删除该 incident 发出的所有临时授权（可能拉了多人）。
	// 删除数用于审计留痕（撤销了几条）。
	n, err := g.db.RoleBinding.Delete().
		Where(rolebinding.SourceIncidentIDEQ(incID)).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("revoke temp role bindings for incident %d: %w", incID, err)
	}
	if n > 0 {
		g.recordAudit(ctx, 0, "role.temp_revoke", 0, map[string]any{
			"reason":      "事件关闭撤销临时授权",
			"incident_id": incID,
			"revoked":     n,
		})
	}
	return nil
}

// recordAudit best-effort 记一条审计（audit 为 nil 时静默跳过；失败不阻塞主流程）。
func (g *tempGranter) recordAudit(ctx context.Context, actorID int, action string, resourceID int, detail map[string]any) {
	if g.audit == nil {
		return
	}
	g.audit.MustRecord(ctx, auth.AuditEntry{
		ActorUserID:  actorID,
		Action:       action,
		ResourceType: "role_binding",
		ResourceID:   resourceID,
		Result:       auth.AuditResultSuccess,
		Detail:       detail,
	})
}
