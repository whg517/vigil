package incident

import (
	"context"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/auditlog"
	entincident "github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/predicate"
	"github.com/kevin/vigil/ent/role"
	"github.com/kevin/vigil/ent/rolebinding"
	"github.com/kevin/vigil/internal/auth"
	"github.com/kevin/vigil/internal/event"
	"github.com/kevin/vigil/internal/timeline"

	_ "github.com/mattn/go-sqlite3"
)

// roleNameEQ Role.name 等值谓词（测试局部封装，避免与生产文件的 role 包用法混淆）。
// 注：itoa 已在 handler_test.go 定义，本文件复用。
func roleNameEQ(name string) predicate.Role { return role.NameEQ(name) }

// tempGrantFixture 搭一套跨团队协同的最小环境：
//   - 两个 team：teamA（incident 归属）、teamB（被拉人所属）
//   - 内置角色 seed（含 responder）
//   - 一个 teamA 的 incident（默认 triggered）
//   - authz + auditRecorder + 注入 granter 的 Service
type tempGrantFixture struct {
	c     *ent.Client
	svc   *Service
	authz *auth.Authorizer
	teamA *ent.Team
	teamB *ent.Team
	inc   *ent.Incident
}

func newTempGrantFixture(t *testing.T, status entincident.Status) *tempGrantFixture {
	t.Helper()
	ctx := context.Background()
	c := newClient(t)

	// 内置角色（含 responder）——临时授权发放依赖 responder 角色存在。
	if err := auth.SeedBuiltinRoles(ctx, c); err != nil {
		t.Fatalf("seed roles: %v", err)
	}

	teamA, err := c.Team.Create().SetName("支付").SetSlug("pay").Save(ctx)
	if err != nil {
		t.Fatalf("create teamA: %v", err)
	}
	teamB, err := c.Team.Create().SetName("风控").SetSlug("risk").Save(ctx)
	if err != nil {
		t.Fatalf("create teamB: %v", err)
	}
	inc, err := c.Incident.Create().
		SetNumber("INC-TG-0001").
		SetTitle("支付 5xx").
		SetSeverity(entincident.SeverityWarning). // warning 不受复盘闸门约束，close 测试简单
		SetStatus(status).
		SetTeamID(teamA.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create incident: %v", err)
	}

	authz := auth.NewAuthorizer(c)
	audit := auth.NewAuditRecorder(c)
	bus := event.New()
	svc := NewService(c, timeline.NewRecorder(c), bus)
	svc.SetResponderGranter(NewResponderGranter(c, authz, audit, DefaultTempGrantTTL))
	svc.SubscribeRevocation(bus)

	return &tempGrantFixture{c: c, svc: svc, authz: authz, teamA: teamA, teamB: teamB, inc: inc}
}

// mkUser 建一个用户。
func mkUser(t *testing.T, c *ent.Client, username string) *ent.User {
	t.Helper()
	u, err := c.User.Create().SetUsername(username).SetEmail(username + "@x.com").Save(context.Background())
	if err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	return u
}

// grantResponder 给 user 在 team 上发一个常规（非临时）responder 绑定。
func grantResponder(t *testing.T, c *ent.Client, userID, teamID int) {
	t.Helper()
	ctx := context.Background()
	r, err := c.Role.Query().Where(roleNameEQ(auth.ResponderRoleName)).Only(ctx)
	if err != nil {
		t.Fatalf("query responder role: %v", err)
	}
	_, err = c.RoleBinding.Create().
		SetUserID(userID).
		SetRoleID(r.ID).
		SetScopeLevel(rolebinding.ScopeLevelTeam).
		SetTeamID(itoa(teamID)).
		Save(ctx)
	if err != nil {
		t.Fatalf("grant responder: %v", err)
	}
}

// canAck 判 user 是否对 teamID 有 incident.ack 权限（authz 实时查库）。
func (f *tempGrantFixture) canAck(t *testing.T, userID, teamID int) bool {
	t.Helper()
	ok, err := f.authz.Check(context.Background(), auth.AuthzRequest{
		UserID:     userID,
		Permission: auth.PermIncidentAck,
		TeamScope:  &teamID,
	})
	if err != nil {
		t.Fatalf("authz check: %v", err)
	}
	return ok
}

// TestAddResponder_CrossTeamGrantsTempAuthz 跨团队 @人：被拉人对 incident 所属 team
// 原本无权限 → AddResponder 后获得事件级临时 responder 授权（可 ack）。
func TestAddResponder_CrossTeamGrantsTempAuthz(t *testing.T) {
	f := newTempGrantFixture(t, entincident.StatusTriggered)
	ctx := context.Background()
	target := mkUser(t, f.c, "cross_team_bob")

	// 前置：被拉人对 teamA（incident 所属）无任何权限。
	if f.canAck(t, target.ID, f.teamA.ID) {
		t.Fatal("前置错误：被拉人本不应有 teamA 权限")
	}

	if _, err := f.svc.AddResponder(ctx, f.inc.ID, 1, target.ID, SourceIM); err != nil {
		t.Fatalf("AddResponder: %v", err)
	}

	// 断言 1：被拉人现在对 teamA 有 ack 权限（临时授权生效）。
	if !f.canAck(t, target.ID, f.teamA.ID) {
		t.Error("跨团队 @人后未获得 teamA 处置权限（临时授权未发放）")
	}
	// 断言 2：发出的绑定标了 source_incident_id、有 expires_at、是 team scope。
	b, err := f.c.RoleBinding.Query().
		Where(rolebinding.SourceIncidentIDEQ(f.inc.ID)).
		Only(ctx)
	if err != nil {
		t.Fatalf("query temp binding: %v", err)
	}
	if b.ScopeLevel != rolebinding.ScopeLevelTeam || b.TeamID != itoa(f.teamA.ID) {
		t.Errorf("临时授权 scope 错误：got scope=%s team=%s，want team=%d", b.ScopeLevel, b.TeamID, f.teamA.ID)
	}
	if b.ExpiresAt == nil {
		t.Error("临时授权无 expires_at 兜底")
	}
}

// TestAddResponder_NotGrantedToOtherTeam 不放宽软隔离：临时授权只对 incident 所属 team 有效，
// 被拉人在自己团队/其它团队不因此获得权限。
func TestAddResponder_NotGrantedToOtherTeam(t *testing.T) {
	f := newTempGrantFixture(t, entincident.StatusTriggered)
	ctx := context.Background()
	target := mkUser(t, f.c, "scoped_bob")

	if _, err := f.svc.AddResponder(ctx, f.inc.ID, 1, target.ID, SourceIM); err != nil {
		t.Fatalf("AddResponder: %v", err)
	}
	// teamA 有权（incident 所属），teamB 无权（未被授予）。
	if !f.canAck(t, target.ID, f.teamA.ID) {
		t.Error("应对 teamA 有权")
	}
	if f.canAck(t, target.ID, f.teamB.ID) {
		t.Error("软隔离被放宽：不该对 teamB 有权")
	}
	// 也不该有 org 级权限（临时授权是 team scope，不是全局）。
	if f.canAck(t, target.ID, 0) { // teamScope=0 探针（无匹配 team），仅 org 级会命中
		// 说明：Check(teamScope=&0) 仅 org 级绑定生效；有 org 权限才返回 true。
		t.Error("软隔离被放宽：临时授权不应给 org 级权限")
	}
}

// TestAddResponder_AlreadyInTeamNoDuplicate 被拉人已是该 team 成员（有 responder 绑定）
// → 不重复发放临时授权（source_incident_id 绑定数为 0）。
func TestAddResponder_AlreadyInTeamNoDuplicate(t *testing.T) {
	f := newTempGrantFixture(t, entincident.StatusTriggered)
	ctx := context.Background()
	target := mkUser(t, f.c, "teamA_member")
	// 预置：target 已是 teamA 的 responder（常规绑定）。
	grantResponder(t, f.c, target.ID, f.teamA.ID)

	if _, err := f.svc.AddResponder(ctx, f.inc.ID, 1, target.ID, SourceIM); err != nil {
		t.Fatalf("AddResponder: %v", err)
	}
	// 不应产生任何临时授权绑定。
	n, err := f.c.RoleBinding.Query().Where(rolebinding.SourceIncidentIDEQ(f.inc.ID)).Count(ctx)
	if err != nil {
		t.Fatalf("count temp bindings: %v", err)
	}
	if n != 0 {
		t.Errorf("已在该 team 的人被重复发临时授权：got %d 条，want 0", n)
	}
}

// TestAddResponder_RevokeOnClose incident 关闭 → 撤销该 incident 的临时授权，被拉人失去权限。
func TestAddResponder_RevokeOnClose(t *testing.T) {
	f := newTempGrantFixture(t, entincident.StatusResolved) // resolved 才能 close
	ctx := context.Background()
	target := mkUser(t, f.c, "revoke_bob")

	if _, err := f.svc.AddResponder(ctx, f.inc.ID, 1, target.ID, SourceIM); err != nil {
		t.Fatalf("AddResponder: %v", err)
	}
	if !f.canAck(t, target.ID, f.teamA.ID) {
		t.Fatal("授权后应有权")
	}

	// 关闭 incident（warning 不受复盘闸门约束）→ 订阅方撤销临时授权。
	if _, err := f.svc.Close(ctx, f.inc.ID, 1, SourceWeb); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// 断言：临时授权被删（authz 实时查库，立即失效）。
	if f.canAck(t, target.ID, f.teamA.ID) {
		t.Error("incident 关闭后临时授权未撤销（被拉人仍有权）")
	}
	n, _ := f.c.RoleBinding.Query().Where(rolebinding.SourceIncidentIDEQ(f.inc.ID)).Count(ctx)
	if n != 0 {
		t.Errorf("临时授权绑定未删除：残留 %d 条", n)
	}
}

// TestAddResponder_RevokeOnResolve incident 解决（协同结束）→ 撤销临时授权。
func TestAddResponder_RevokeOnResolve(t *testing.T) {
	f := newTempGrantFixture(t, entincident.StatusAcked) // acked → resolved
	ctx := context.Background()
	target := mkUser(t, f.c, "resolve_bob")

	if _, err := f.svc.AddResponder(ctx, f.inc.ID, 1, target.ID, SourceIM); err != nil {
		t.Fatalf("AddResponder: %v", err)
	}
	if !f.canAck(t, target.ID, f.teamA.ID) {
		t.Fatal("授权后应有权")
	}
	if _, err := f.svc.Resolve(ctx, f.inc.ID, 1, SourceWeb); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if f.canAck(t, target.ID, f.teamA.ID) {
		t.Error("incident resolved 后临时授权未撤销")
	}
}

// TestTempGrant_ExpiresAtFallback 兜底：即使漏撤销，expires_at 过期后 authz 也不再放行。
// 直接构造一条过期的临时绑定验证 authz 过滤。
func TestTempGrant_ExpiresAtFallback(t *testing.T) {
	f := newTempGrantFixture(t, entincident.StatusTriggered)
	ctx := context.Background()
	target := mkUser(t, f.c, "expired_bob")

	r, err := f.c.Role.Query().Where(roleNameEQ(auth.ResponderRoleName)).Only(ctx)
	if err != nil {
		t.Fatalf("query responder role: %v", err)
	}
	// 过期时间设在过去（模拟漏删 + 已过期）。
	_, err = f.c.RoleBinding.Create().
		SetUserID(target.ID).
		SetRoleID(r.ID).
		SetScopeLevel(rolebinding.ScopeLevelTeam).
		SetTeamID(itoa(f.teamA.ID)).
		SetSourceIncidentID(f.inc.ID).
		SetExpiresAt(time.Now().Add(-time.Hour)).
		Save(ctx)
	if err != nil {
		t.Fatalf("create expired binding: %v", err)
	}
	// authz 实时按 expires_at 过滤 → 过期绑定不生效。
	if f.canAck(t, target.ID, f.teamA.ID) {
		t.Error("过期的临时授权仍生效（expires_at 兜底失效）")
	}
}

// TestTempGrant_AuditTrail 发放与撤销都落审计（role.temp_grant / role.temp_revoke）。
func TestTempGrant_AuditTrail(t *testing.T) {
	f := newTempGrantFixture(t, entincident.StatusResolved)
	ctx := context.Background()
	target := mkUser(t, f.c, "audit_bob")

	if _, err := f.svc.AddResponder(ctx, f.inc.ID, 42, target.ID, SourceIM); err != nil {
		t.Fatalf("AddResponder: %v", err)
	}
	// 发放审计
	grantN, _ := f.c.AuditLog.Query().Where(auditlog.ActionEQ("role.temp_grant")).Count(ctx)
	if grantN != 1 {
		t.Errorf("发放审计条数：got %d, want 1", grantN)
	}

	if _, err := f.svc.Close(ctx, f.inc.ID, 1, SourceWeb); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// 撤销审计
	revokeN, _ := f.c.AuditLog.Query().Where(auditlog.ActionEQ("role.temp_revoke")).Count(ctx)
	if revokeN != 1 {
		t.Errorf("撤销审计条数：got %d, want 1", revokeN)
	}
}

// TestAddResponder_NilGranterDegrades 未注入 granter 时降级：仅加入 responders 名单，不发临时授权。
func TestAddResponder_NilGranterDegrades(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()
	if err := auth.SeedBuiltinRoles(ctx, c); err != nil {
		t.Fatalf("seed roles: %v", err)
	}
	team, _ := c.Team.Create().SetName("t").SetSlug("t").Save(ctx)
	inc, _ := c.Incident.Create().SetNumber("INC-NIL").SetTitle("x").
		SetSeverity(entincident.SeverityInfo).SetStatus(entincident.StatusTriggered).SetTeamID(team.ID).Save(ctx)
	target := mkUser(t, c, "nil_bob")

	svc := NewService(c, timeline.NewRecorder(c), nil) // 不注入 granter
	if _, err := svc.AddResponder(ctx, inc.ID, 1, target.ID, SourceIM); err != nil {
		t.Fatalf("AddResponder: %v", err)
	}
	// 加入了 responders 名单
	rs, _ := c.Incident.Query().Where(entincident.IDEQ(inc.ID)).QueryResponders().All(ctx)
	if len(rs) != 1 {
		t.Errorf("responder 未加入：got %d", len(rs))
	}
	// 但没有任何临时授权绑定
	n, _ := c.RoleBinding.Query().Where(rolebinding.SourceIncidentIDEQ(inc.ID)).Count(ctx)
	if n != 0 {
		t.Errorf("nil granter 不应发临时授权：got %d 条", n)
	}
}
