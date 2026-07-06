package ws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/incident"
	entrole "github.com/kevin/vigil/ent/role"
	"github.com/kevin/vigil/ent/rolebinding"
	"github.com/kevin/vigil/internal/auth"
	"github.com/kevin/vigil/internal/metrics"
	"github.com/labstack/echo/v5"

	_ "github.com/mattn/go-sqlite3"
)

// —— 握手鉴权测试脚手架（T0.5）——
//
// WS 端点原先无鉴权，任意匿名连接可订阅任意 incident。以下测试固化修复：
// 无 token → 401；有 token 但无 incident.view（跨 team）→ 403；有权用户 → 握手成功且能收推送。

// wsTestEnv 一套鉴权依赖 + JWT 签发器。
type wsTestEnv struct {
	db       *ent.Client
	authz    *auth.Authorizer
	resolver *auth.IdentityResolver
	scope    *auth.ScopeResolver
	signer   *auth.JWTSigner
}

// newWSTestEnv 建内存库 + 全套鉴权依赖（与 wire.go 装配同款）。
func newWSTestEnv(t *testing.T) *wsTestEnv {
	t.Helper()
	db := enttest.Open(t, "sqlite3", "file:ws_auth_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = db.Close() })
	signer := auth.NewJWTSigner("test-secret-please-ignore", 15*time.Minute, 30*24*time.Hour)
	return &wsTestEnv{
		db:       db,
		authz:    auth.NewAuthorizer(db),
		resolver: auth.NewIdentityResolver(signer, nil, false, db), // headerFallback=false：只认 JWT
		scope:    auth.NewScopeResolver(db),
		signer:   signer,
	}
}

// seedTeamIncident 建 team + 归属该 team 的 incident，返回 (teamID, incidentID)。
func (env *wsTestEnv) seedTeamIncident(t *testing.T, name string) (teamID, incID int) {
	t.Helper()
	ctx := context.Background()
	team, err := env.db.Team.Create().SetName(name).SetSlug(name + "-slug").Save(ctx)
	if err != nil {
		t.Fatalf("create team: %v", err)
	}
	inc, err := env.db.Incident.Create().
		SetNumber("INC-" + name).
		SetTitle(name + " incident").
		SetSeverity(incident.SeverityWarning).
		SetTeamID(team.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create incident: %v", err)
	}
	return team.ID, inc.ID
}

// seedUserWithView 建用户 + 授予 incident.view（scope 由 teamID 决定：nil=org 级，否则 team 级）。
// 返回该用户签发的 access token（写进 ?token= 用）。
func (env *wsTestEnv) seedUserWithView(t *testing.T, username string, teamID *int) (uid int, token string) {
	t.Helper()
	ctx := context.Background()
	u, err := env.db.User.Create().SetUsername(username).SetEmail(username + "@x.com").Save(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	scope := entrole.ScopeLevelOrg
	bindScope := rolebinding.ScopeLevelOrg
	teamStr := ""
	if teamID != nil {
		scope = entrole.ScopeLevelTeam
		bindScope = rolebinding.ScopeLevelTeam
		teamStr = strconv.Itoa(*teamID)
	}
	rl, err := env.db.Role.Create().
		SetName(username + "-viewer").
		SetScopeLevel(scope).
		SetPermissions([]string{string(auth.PermIncidentView)}).
		Save(ctx)
	if err != nil {
		t.Fatalf("create role: %v", err)
	}
	rb := env.db.RoleBinding.Create().
		SetUserID(u.ID).
		SetRoleID(rl.ID).
		SetScopeLevel(bindScope).
		SetGrantedAt(time.Now())
	if teamStr != "" {
		rb.SetTeamID(teamStr)
	}
	if _, err := rb.Save(ctx); err != nil {
		t.Fatalf("create binding: %v", err)
	}
	// token_version 默认 0，与签发快照一致（不触发吊销）。
	tok, err := env.signer.GenerateAccessToken(u.ID, username, 0)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	return u.ID, tok
}

// seedUserWithPerm 建用户 + 授予指定权限点（scope 由 teamID 决定：nil=org 级，否则 team 级）。
// 返回该用户的 access token。用于看板握手（analytics.view）等非 incident.view 场景。
func (env *wsTestEnv) seedUserWithPerm(t *testing.T, username string, perm auth.Permission, teamID *int) (uid int, token string) {
	t.Helper()
	ctx := context.Background()
	u, err := env.db.User.Create().SetUsername(username).SetEmail(username + "@x.com").Save(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	scope := entrole.ScopeLevelOrg
	bindScope := rolebinding.ScopeLevelOrg
	teamStr := ""
	if teamID != nil {
		scope = entrole.ScopeLevelTeam
		bindScope = rolebinding.ScopeLevelTeam
		teamStr = strconv.Itoa(*teamID)
	}
	rl, err := env.db.Role.Create().
		SetName(username + "-role").
		SetScopeLevel(scope).
		SetPermissions([]string{string(perm)}).
		Save(ctx)
	if err != nil {
		t.Fatalf("create role: %v", err)
	}
	rb := env.db.RoleBinding.Create().
		SetUserID(u.ID).
		SetRoleID(rl.ID).
		SetScopeLevel(bindScope).
		SetGrantedAt(time.Now())
	if teamStr != "" {
		rb.SetTeamID(teamStr)
	}
	if _, err := rb.Save(ctx); err != nil {
		t.Fatalf("create binding: %v", err)
	}
	tok, err := env.signer.GenerateAccessToken(u.ID, username, 0)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	return u.ID, tok
}

// seedBareUser 建一个无任何角色绑定的用户，返回其 access token（用于"有身份无权限"用例）。
func (env *wsTestEnv) seedBareUser(t *testing.T, username string) string {
	t.Helper()
	u, err := env.db.User.Create().SetUsername(username).SetEmail(username + "@x.com").Save(context.Background())
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	tok, err := env.signer.GenerateAccessToken(u.ID, username, 0)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	return tok
}

// newHandler 用 env 装配一个带鉴权的 Handler。
func (env *wsTestEnv) newHandler() *Handler {
	return NewHandler(env.hubAndDeps())
}

// hubAndDeps 拆出参数，供 NewHandler 展开（顺带每次新建 hub）。
func (env *wsTestEnv) hubAndDeps() (*Hub, *auth.Authorizer, *auth.IdentityResolver, *auth.ScopeResolver) {
	return NewHub(), env.authz, env.resolver, env.scope
}

// dialWS 用 ?token= 拨号，返回 (conn, httpStatus, err)。conn 为 nil 表示握手失败。
func dialWS(t *testing.T, srvURL, path, token string) (*websocket.Conn, int, error) {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(srvURL, "http") + path
	if token != "" {
		wsURL += "?token=" + token
	}
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	conn, resp, err := dialer.Dial(wsURL, nil)
	status := 0
	if resp != nil {
		status = resp.StatusCode
		_ = resp.Body.Close()
	}
	return conn, status, err
}

// TestHandleIncident_NoToken 无 token → 401，不 Upgrade。
func TestHandleIncident_NoToken(t *testing.T) {
	env := newWSTestEnv(t)
	_, incID := env.seedTeamIncident(t, "pay")

	e := echo.New()
	env.newHandler().Register(e.Group(""))
	srv := httptest.NewServer(e)
	defer srv.Close()

	conn, status, err := dialWS(t, srv.URL, "/ws/incidents/"+strconv.Itoa(incID), "")
	if conn != nil {
		_ = conn.Close()
		t.Fatal("无 token 不应握手成功")
	}
	if err == nil {
		t.Fatal("无 token 应握手失败")
	}
	if status != http.StatusUnauthorized {
		t.Errorf("无 token 状态码 = %d, want 401", status)
	}
}

// TestHandleIncident_InvalidToken 伪造/无效 token → 401，不 Upgrade。
func TestHandleIncident_InvalidToken(t *testing.T) {
	env := newWSTestEnv(t)
	_, incID := env.seedTeamIncident(t, "pay")

	e := echo.New()
	env.newHandler().Register(e.Group(""))
	srv := httptest.NewServer(e)
	defer srv.Close()

	conn, status, err := dialWS(t, srv.URL, "/ws/incidents/"+strconv.Itoa(incID), "not-a-real-jwt")
	if conn != nil {
		_ = conn.Close()
		t.Fatal("无效 token 不应握手成功")
	}
	if err == nil {
		t.Fatal("无效 token 应握手失败")
	}
	if status != http.StatusUnauthorized {
		t.Errorf("无效 token 状态码 = %d, want 401", status)
	}
}

// TestHandleIncident_CrossTeamForbidden 有 token 但对该 incident 无 view（跨 team）→ 403。
func TestHandleIncident_CrossTeamForbidden(t *testing.T) {
	env := newWSTestEnv(t)
	_, incID := env.seedTeamIncident(t, "pay") // incident 归属 pay team
	// 建另一 team，用户仅在该 team 有 view → 对 pay 的 incident 无权。
	otherTeam, err := env.db.Team.Create().SetName("infra").SetSlug("infra-slug").Save(context.Background())
	if err != nil {
		t.Fatalf("create other team: %v", err)
	}
	_, token := env.seedUserWithView(t, "bob", &otherTeam.ID)

	e := echo.New()
	env.newHandler().Register(e.Group(""))
	srv := httptest.NewServer(e)
	defer srv.Close()

	conn, status, dErr := dialWS(t, srv.URL, "/ws/incidents/"+strconv.Itoa(incID), token)
	if conn != nil {
		_ = conn.Close()
		t.Fatal("跨 team 无权不应握手成功")
	}
	if dErr == nil {
		t.Fatal("跨 team 无权应握手失败")
	}
	if status != http.StatusForbidden {
		t.Errorf("跨 team 无权状态码 = %d, want 403", status)
	}
}

// TestHandleIncident_AuthenticatedNoPerm 有合法身份但无任何角色 → 403（身份≠授权）。
func TestHandleIncident_AuthenticatedNoPerm(t *testing.T) {
	env := newWSTestEnv(t)
	_, incID := env.seedTeamIncident(t, "pay")
	token := env.seedBareUser(t, "nobody")

	e := echo.New()
	env.newHandler().Register(e.Group(""))
	srv := httptest.NewServer(e)
	defer srv.Close()

	conn, status, dErr := dialWS(t, srv.URL, "/ws/incidents/"+strconv.Itoa(incID), token)
	if conn != nil {
		_ = conn.Close()
		t.Fatal("无角色用户不应握手成功")
	}
	if dErr == nil {
		t.Fatal("无角色用户应握手失败")
	}
	if status != http.StatusForbidden {
		t.Errorf("无权用户状态码 = %d, want 403", status)
	}
}

// TestHandleIncident_AuthorizedThroughMetricsMiddleware 有权用户握手成功且能收推送。
//
// 兼顾 v5 迁移回归（原 TestHandleIncident_UpgradeThroughMetricsMiddleware）：
// metrics.EchoMiddleware 把 c.Response() 包成 statusRecorder，gorilla Upgrade 需底层
// ResponseWriter 实现 http.Hijacker——靠嵌入方法提升 + Unwrap 链透传。此测试同时验证
// 鉴权通过后 Hijacker 透传仍生效（握手 → 订阅 → 收广播闭环）。
func TestHandleIncident_AuthorizedThroughMetricsMiddleware(t *testing.T) {
	env := newWSTestEnv(t)
	teamID, incID := env.seedTeamIncident(t, "pay")
	_, token := env.seedUserWithView(t, "alice", &teamID) // team 级 view，命中 pay team

	hub := NewHub()
	wsHandler := NewHandler(hub, env.authz, env.resolver, env.scope)

	e := echo.New()
	e.Use(metrics.EchoMiddleware()) // ★ 关键：模拟生产中间件链
	wsHandler.Register(e.Group(""))

	srv := httptest.NewServer(e)
	defer srv.Close()

	conn, status, err := dialWS(t, srv.URL, "/ws/incidents/"+strconv.Itoa(incID), token)
	if err != nil {
		t.Fatalf("有权用户 WebSocket 握手失败（Hijacker 未透传或鉴权误拒？）: %v [status=%d]", err, status)
	}
	defer conn.Close()

	if status != http.StatusSwitchingProtocols {
		t.Errorf("握手状态码 = %d, want 101", status)
	}

	// 等订阅注册到 hub（Subscribe 在 handleIncident 内异步完成）。
	// 用「广播 + 读」的带重试探测：读到即订阅已就绪。
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var got string
	for i := 0; i < 20; i++ {
		hub.BroadcastIncident(incID, "ack", nil)
		if _, msg, rerr := conn.ReadMessage(); rerr == nil {
			got = string(msg)
			break
		}
	}
	if got == "" {
		t.Fatal("3s 内未收到广播消息（订阅未就绪或写循环失效）")
	}
	if !strings.Contains(got, "incident_changed") {
		t.Errorf("收到的消息不含预期 type: %s", got)
	}
}

// TestHandleIncident_OrgScopeAuthorized org 级 view 用户对任意 team 的 incident 都能订阅。
func TestHandleIncident_OrgScopeAuthorized(t *testing.T) {
	env := newWSTestEnv(t)
	_, incID := env.seedTeamIncident(t, "pay")
	_, token := env.seedUserWithView(t, "admin", nil) // org 级 view

	e := echo.New()
	env.newHandler().Register(e.Group(""))
	srv := httptest.NewServer(e)
	defer srv.Close()

	conn, status, err := dialWS(t, srv.URL, "/ws/incidents/"+strconv.Itoa(incID), token)
	if err != nil {
		t.Fatalf("org 级用户握手失败: %v [status=%d]", err, status)
	}
	defer conn.Close()
	if status != http.StatusSwitchingProtocols {
		t.Errorf("握手状态码 = %d, want 101", status)
	}
}

// TestHandleIncident_InvalidID 非法 incident id 返回 400，不进入鉴权/Upgrade 路径。
func TestHandleIncident_InvalidID(t *testing.T) {
	env := newWSTestEnv(t)

	e := echo.New()
	env.newHandler().Register(e.Group(""))

	req := httptest.NewRequest(http.MethodGet, "/ws/incidents/abc", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("非法 id 状态码 = %d, want 400", rec.Code)
	}
}

// TestHandleIncident_FailClosedWithoutDeps 依赖缺失（nil authz/resolver）时握手被拒（fail-closed），
// 不静默退回无鉴权旧行为。
func TestHandleIncident_FailClosedWithoutDeps(t *testing.T) {
	h := NewHandler(NewHub(), nil, nil, nil)
	e := echo.New()
	h.Register(e.Group(""))
	srv := httptest.NewServer(e)
	defer srv.Close()

	conn, status, err := dialWS(t, srv.URL, "/ws/incidents/1", "whatever")
	if conn != nil {
		_ = conn.Close()
		t.Fatal("依赖缺失时不应握手成功")
	}
	if err == nil {
		t.Fatal("依赖缺失时应握手失败")
	}
	if status != http.StatusUnauthorized {
		t.Errorf("fail-closed 状态码 = %d, want 401", status)
	}
}

// —— 看板订阅握手 + 广播（P4·B3 值班大屏/实时看板）——

// TestHandleDashboard_NoToken 无 token → 401，不 Upgrade。
func TestHandleDashboard_NoToken(t *testing.T) {
	env := newWSTestEnv(t)
	e := echo.New()
	env.newHandler().Register(e.Group(""))
	srv := httptest.NewServer(e)
	defer srv.Close()

	conn, status, err := dialWS(t, srv.URL, "/ws/dashboard", "")
	if conn != nil {
		_ = conn.Close()
		t.Fatal("无 token 不应握手成功")
	}
	if err == nil || status != http.StatusUnauthorized {
		t.Errorf("无 token 应 401，got status=%d err=%v", status, err)
	}
}

// TestHandleDashboard_NoAnalyticsPerm 有身份但无 analytics.view → 403（org 级只读看板要求 org 级权限）。
func TestHandleDashboard_NoAnalyticsPerm(t *testing.T) {
	env := newWSTestEnv(t)
	token := env.seedBareUser(t, "nobody")

	e := echo.New()
	env.newHandler().Register(e.Group(""))
	srv := httptest.NewServer(e)
	defer srv.Close()

	conn, status, err := dialWS(t, srv.URL, "/ws/dashboard", token)
	if conn != nil {
		_ = conn.Close()
		t.Fatal("无 analytics.view 不应握手成功")
	}
	if err == nil || status != http.StatusForbidden {
		t.Errorf("无权应 403，got status=%d err=%v", status, err)
	}
}

// TestHandleDashboard_TeamScopeForbidden 仅 team 级 analytics.view → 403
// （看板是 org 级视图，team 级权限不足以订阅全组织看板）。
func TestHandleDashboard_TeamScopeForbidden(t *testing.T) {
	env := newWSTestEnv(t)
	teamID, _ := env.seedTeamIncident(t, "pay")
	_, token := env.seedUserWithPerm(t, "team-lead", auth.PermAnalyticsView, &teamID)

	e := echo.New()
	env.newHandler().Register(e.Group(""))
	srv := httptest.NewServer(e)
	defer srv.Close()

	conn, status, err := dialWS(t, srv.URL, "/ws/dashboard", token)
	if conn != nil {
		_ = conn.Close()
		t.Fatal("仅 team 级 analytics.view 不应订阅 org 级看板")
	}
	if err == nil || status != http.StatusForbidden {
		t.Errorf("team 级权限应 403，got status=%d err=%v", status, err)
	}
}

// TestHandleDashboard_OrgScopeReceivesBroadcast org 级 analytics.view 握手成功且收到看板增量推送。
func TestHandleDashboard_OrgScopeReceivesBroadcast(t *testing.T) {
	env := newWSTestEnv(t)
	_, token := env.seedUserWithPerm(t, "noc", auth.PermAnalyticsView, nil) // org 级

	hub := NewHub()
	wsHandler := NewHandler(hub, env.authz, env.resolver, env.scope)
	e := echo.New()
	wsHandler.Register(e.Group(""))
	srv := httptest.NewServer(e)
	defer srv.Close()

	conn, status, err := dialWS(t, srv.URL, "/ws/dashboard", token)
	if err != nil {
		t.Fatalf("org 级看板握手失败: %v [status=%d]", err, status)
	}
	defer conn.Close()
	if status != http.StatusSwitchingProtocols {
		t.Errorf("握手状态码 = %d, want 101", status)
	}

	// 探测订阅就绪：反复广播 + 读，读到即订阅已注册。
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var got string
	for i := 0; i < 20; i++ {
		hub.BroadcastDashboard("resolve", map[string]any{"incident_id": 1})
		if _, msg, rerr := conn.ReadMessage(); rerr == nil {
			got = string(msg)
			break
		}
	}
	if got == "" {
		t.Fatal("3s 内未收到看板广播（订阅未就绪或写循环失效）")
	}
	if !strings.Contains(got, "dashboard_update") {
		t.Errorf("收到的消息不含 dashboard_update: %s", got)
	}
}
