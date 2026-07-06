// handler_impact_test.go 服务拓扑完整影响面契约测试（N2.4 / M4.4）。
//
// 覆盖 GET /services/:id/impact 的传递闭包语义：
//   - 多层依赖闭包（A→B→C，查 A 的 downstream_deps 含 C；查 C 的 upstream_impact 含 A）；
//   - 环检测（A→B→A 不死循环，cycle_detected=true）；
//   - depth 层级标注；深度限制 truncated；team scope 隔离。
package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/role"
	"github.com/kevin/vigil/ent/rolebinding"
	"github.com/kevin/vigil/internal/auth"

	"github.com/labstack/echo/v5"
	_ "github.com/mattn/go-sqlite3"
)

// impactResult 解析 impact 响应（顺序无关，用 id→depth 映射比对）。
type impactResult struct {
	ServiceID      int `json:"service_id"`
	UpstreamImpact []struct {
		ID    int `json:"id"`
		Depth int `json:"depth"`
	} `json:"upstream_impact"`
	DownstreamDeps []struct {
		ID    int `json:"id"`
		Depth int `json:"depth"`
	} `json:"downstream_deps"`
	CycleDetected bool `json:"cycle_detected"`
	Truncated     bool `json:"truncated"`
}

func decodeImpact(t *testing.T, rec *httptest.ResponseRecorder) impactResult {
	t.Helper()
	var r impactResult
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatalf("decode impact: %v (body=%s)", err, rec.Body.String())
	}
	return r
}

func upstreamDepths(r impactResult) map[int]int {
	m := map[int]int{}
	for _, n := range r.UpstreamImpact {
		m[n.ID] = n.Depth
	}
	return m
}

func downstreamDepths(r impactResult) map[int]int {
	m := map[int]int{}
	for _, n := range r.DownstreamDeps {
		m[n.ID] = n.Depth
	}
	return m
}

// TestImpact_TransitiveClosure A→B→C：A 依赖 B，B 依赖 C。
// 查 A 的下游依赖链应含 B(1)、C(2)；查 C 的上游影响面应含 B(1)、A(2)。
func TestImpact_TransitiveClosure(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:svc_impact_closure?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := t.Context()
	a := c.Service.Create().SetName("A").SetSlug("a").SaveX(ctx)
	b := c.Service.Create().SetName("B").SetSlug("b").SaveX(ctx)
	cc := c.Service.Create().SetName("C").SetSlug("c").SaveX(ctx)
	// A → B → C（depends_on 链）。
	c.Service.UpdateOneID(a.ID).AddDependsOnIDs(b.ID).ExecX(ctx)
	c.Service.UpdateOneID(b.ID).AddDependsOnIDs(cc.ID).ExecX(ctx)

	h := NewHandler(c)
	e := echo.New()
	h.Register(e.Group("/api/v1"))

	// A 的下游依赖链：B(depth1), C(depth2)；上游影响面空。
	recA := doJSON(e, http.MethodGet, "/api/v1/services/"+strconv.Itoa(a.ID)+"/impact", "")
	if recA.Code != http.StatusOK {
		t.Fatalf("impact A: got %d, want 200 (body=%s)", recA.Code, recA.Body.String())
	}
	rA := decodeImpact(t, recA)
	down := downstreamDepths(rA)
	if down[b.ID] != 1 || down[cc.ID] != 2 || len(down) != 2 {
		t.Errorf("A downstream_deps: got %v, want {B:1,C:2}", down)
	}
	if len(rA.UpstreamImpact) != 0 {
		t.Errorf("A upstream_impact should be empty, got %+v", rA.UpstreamImpact)
	}
	if rA.CycleDetected {
		t.Error("A: cycle_detected should be false for acyclic chain")
	}

	// C 的上游影响面：B(depth1), A(depth2)——C 故障连带影响 B 与 A。
	recC := doJSON(e, http.MethodGet, "/api/v1/services/"+strconv.Itoa(cc.ID)+"/impact", "")
	rC := decodeImpact(t, recC)
	up := upstreamDepths(rC)
	if up[b.ID] != 1 || up[a.ID] != 2 || len(up) != 2 {
		t.Errorf("C upstream_impact: got %v, want {B:1,A:2}", up)
	}
	if len(rC.DownstreamDeps) != 0 {
		t.Errorf("C downstream_deps should be empty, got %+v", rC.DownstreamDeps)
	}
}

// TestImpact_CycleDetection A→B→A：图有环，BFS 不死循环并置 cycle_detected。
func TestImpact_CycleDetection(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:svc_impact_cycle?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := t.Context()
	a := c.Service.Create().SetName("A").SetSlug("a").SaveX(ctx)
	b := c.Service.Create().SetName("B").SetSlug("b").SaveX(ctx)
	// A → B → A（环）。
	c.Service.UpdateOneID(a.ID).AddDependsOnIDs(b.ID).ExecX(ctx)
	c.Service.UpdateOneID(b.ID).AddDependsOnIDs(a.ID).ExecX(ctx)

	h := NewHandler(c)
	e := echo.New()
	h.Register(e.Group("/api/v1"))

	rec := doJSON(e, http.MethodGet, "/api/v1/services/"+strconv.Itoa(a.ID)+"/impact", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("impact A (cycle): got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	r := decodeImpact(t, rec)
	if !r.CycleDetected {
		t.Error("cycle_detected should be true for A→B→A")
	}
	// 环下 B 仍应被收入下游（depth1），起点 A 因 visited 不重复计入。
	down := downstreamDepths(r)
	if down[b.ID] != 1 {
		t.Errorf("downstream should contain B at depth1 despite cycle, got %v", down)
	}
	if _, ok := down[a.ID]; ok {
		t.Errorf("start node A must not appear in its own closure, got %v", down)
	}
}

// TestImpact_SelfLoopSafe 起点自身不因回边被算进影响面（visited 含起点）。
// 拓扑 A→B，B→A（环），确认 A 不出现在 A 的闭包里。
func TestImpact_SelfLoopSafe(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:svc_impact_selfloop?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := t.Context()
	a := c.Service.Create().SetName("A").SetSlug("a").SaveX(ctx)
	b := c.Service.Create().SetName("B").SetSlug("b").SaveX(ctx)
	c.Service.UpdateOneID(a.ID).AddDependsOnIDs(b.ID).ExecX(ctx)
	c.Service.UpdateOneID(b.ID).AddDependsOnIDs(a.ID).ExecX(ctx)

	h := NewHandler(c)
	e := echo.New()
	h.Register(e.Group("/api/v1"))
	rec := doJSON(e, http.MethodGet, "/api/v1/services/"+strconv.Itoa(a.ID)+"/impact", "")
	r := decodeImpact(t, rec)
	if _, ok := downstreamDepths(r)[a.ID]; ok {
		t.Error("A must not be in its own downstream closure")
	}
}

// TestImpact_DiamondDedup 菱形依赖 A→B, A→C, B→D, C→D：D 只计一次（visited 去重），取最短 depth。
func TestImpact_DiamondDedup(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:svc_impact_diamond?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := t.Context()
	a := c.Service.Create().SetName("A").SetSlug("a").SaveX(ctx)
	b := c.Service.Create().SetName("B").SetSlug("b").SaveX(ctx)
	cc := c.Service.Create().SetName("C").SetSlug("c").SaveX(ctx)
	d := c.Service.Create().SetName("D").SetSlug("d").SaveX(ctx)
	c.Service.UpdateOneID(a.ID).AddDependsOnIDs(b.ID, cc.ID).ExecX(ctx)
	c.Service.UpdateOneID(b.ID).AddDependsOnIDs(d.ID).ExecX(ctx)
	c.Service.UpdateOneID(cc.ID).AddDependsOnIDs(d.ID).ExecX(ctx)

	h := NewHandler(c)
	e := echo.New()
	h.Register(e.Group("/api/v1"))
	rec := doJSON(e, http.MethodGet, "/api/v1/services/"+strconv.Itoa(a.ID)+"/impact", "")
	r := decodeImpact(t, rec)
	down := downstreamDepths(r)
	// B、C 在 depth1；D 去重后仅一条，depth2（BFS 最短跳数）。
	if down[b.ID] != 1 || down[cc.ID] != 1 || down[d.ID] != 2 || len(down) != 3 {
		t.Errorf("diamond downstream: got %v, want {B:1,C:1,D:2}", down)
	}
	// 确认 D 只出现一次（去重）。
	dCount := 0
	for _, n := range r.DownstreamDeps {
		if n.ID == d.ID {
			dCount++
		}
	}
	if dCount != 1 {
		t.Errorf("D should appear exactly once (dedup), got %d", dCount)
	}
}

// TestImpact_NotFound 不存在的服务返回 404。
func TestImpact_NotFound(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:svc_impact_404?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	h := NewHandler(c)
	e := echo.New()
	h.Register(e.Group("/api/v1"))
	rec := doJSON(e, http.MethodGet, "/api/v1/services/99999/impact", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("impact unknown: got %d, want 404", rec.Code)
	}
}

// TestImpact_TeamScopeIsolation team 级用户不能看其他 team 服务的影响面 → 403。
func TestImpact_TeamScopeIsolation(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:svc_impact_scope?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := t.Context()

	ta := c.Team.Create().SetName("pay").SetSlug("pay").SaveX(ctx)
	tb := c.Team.Create().SetName("order").SetSlug("order").SaveX(ctx)
	viewer := c.Role.Create().SetName("viewer").SetScopeLevel(role.ScopeLevelTeam).
		SetPermissions([]string{string(auth.PermServiceView)}).SaveX(ctx)
	ua := c.User.Create().SetUsername("alice").SetEmail("a@x.com").SaveX(ctx)
	c.RoleBinding.Create().SetUserID(ua.ID).SetRoleID(viewer.ID).
		SetScopeLevel(rolebinding.ScopeLevelTeam).SetTeamID(strconv.Itoa(ta.ID)).
		SetGrantedAt(time.Now()).SaveX(ctx)
	// svcB 属 teamB，alice（teamA）无权查看。
	svcB := c.Service.Create().SetName("svc-b").SetSlug("svc-b").SetTeamID(tb.ID).SaveX(ctx)

	h := NewHandler(c)
	h.SetAuthorizer(auth.NewAuthorizer(c))
	h.SetScopeResolver(auth.NewScopeResolver(c))
	e := echo.New()
	v1 := e.Group("/api/v1", auth.RequireUser(false, auth.NewIdentityResolver(nil, nil, true, nil)))
	h.Register(v1)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/services/"+strconv.Itoa(svcB.ID)+"/impact", nil)
	req.Header.Set("X-Vigil-User-ID", strconv.Itoa(ua.ID))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-team impact: got %d, want 403", rec.Code)
	}
}
