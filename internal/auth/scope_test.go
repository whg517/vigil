// scope_test.go ScopeResolver 测试（ARCH-02/SEC-01 资源级 scope 反查）。
package auth

import (
	"context"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/incident"

	_ "github.com/mattn/go-sqlite3"
)

// newScopeClient 独立内存库。
func newScopeClient(t *testing.T) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:scope_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// seedTeamAndIncident 建 team + 归属该 team 的 incident，返回 (teamID, incidentID)。
func seedTeamAndIncident(t *testing.T, c *ent.Client, teamName string) (teamID, incID int) {
	t.Helper()
	ctx := context.Background()
	team, err := c.Team.Create().SetName(teamName).SetSlug(teamName + "-slug").Save(ctx)
	if err != nil {
		t.Fatalf("create team: %v", err)
	}
	inc, err := c.Incident.Create().
		SetNumber("INC-" + teamName).
		SetTitle(teamName + " incident").
		SetSeverity(incident.SeverityWarning).
		SetTeamID(team.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create incident: %v", err)
	}
	return team.ID, inc.ID
}

// TestResolve_DirectIncident 直接归属：incident → team。
func TestResolve_DirectIncident(t *testing.T) {
	c := newScopeClient(t)
	teamID, incID := seedTeamAndIncident(t, c, "pay")
	s := NewScopeResolver(c)

	got, err := s.Resolve(context.Background(), "incident", incID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got == nil || *got != teamID {
		t.Errorf("expected team %d, got %v", teamID, got)
	}
}

// TestResolve_NotFound 资源不存在 → nil（不阻断，避免存在性泄露）。
func TestResolve_NotFound(t *testing.T) {
	c := newScopeClient(t)
	s := NewScopeResolver(c)
	got, err := s.Resolve(context.Background(), "incident", 99999)
	if err != nil {
		t.Fatalf("Resolve should not error on missing: %v", err)
	}
	if got != nil {
		t.Errorf("missing resource should return nil, got %v", *got)
	}
}

// TestResolve_UnknownKind 未注册 kind → nil（视为 org 级）。
func TestResolve_UnknownKind(t *testing.T) {
	c := newScopeClient(t)
	s := NewScopeResolver(c)
	got, _ := s.Resolve(context.Background(), "unknown_kind", 1)
	if got != nil {
		t.Error("unknown kind should return nil")
	}
}

// TestResolve_PostmortemIndirect 间接归属：postmortem → incident → team。
func TestResolve_PostmortemIndirect(t *testing.T) {
	c := newScopeClient(t)
	teamID, incID := seedTeamAndIncident(t, c, "indirect")
	// 建 postmortem 挂到 incident（sections 是必填 JSON 字段）
	ctx := context.Background()
	pm, err := c.Postmortem.Create().
		SetSections(map[string]any{"summary": "test"}).
		SetIncidentID(incID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create postmortem: %v", err)
	}
	s := NewScopeResolver(c)
	got, err := s.Resolve(ctx, "postmortem", pm.ID)
	if err != nil {
		t.Fatalf("Resolve postmortem: %v", err)
	}
	if got == nil || *got != teamID {
		t.Errorf("expected team %d, got %v", teamID, got)
	}
}

// TestResolve_ActionItemIndirect 三级回溯：action_item → postmortem → incident → team。
func TestResolve_ActionItemIndirect(t *testing.T) {
	c := newScopeClient(t)
	teamID, incID := seedTeamAndIncident(t, c, "threelevel")
	ctx := context.Background()
	pm, err := c.Postmortem.Create().SetSections(map[string]any{"summary": "t"}).SetIncidentID(incID).Save(ctx)
	if err != nil {
		t.Fatalf("create postmortem: %v", err)
	}
	ai, err := c.ActionItem.Create().SetDescription("fix it").SetPostmortemID(pm.ID).Save(ctx)
	if err != nil {
		t.Fatalf("create action item: %v", err)
	}
	s := NewScopeResolver(c)
	got, _ := s.Resolve(ctx, "action_item", ai.ID)
	if got == nil || *got != teamID {
		t.Errorf("expected team %d via 3-level, got %v", teamID, got)
	}
}

// TestResolve_NoTeam 资源未挂 team → nil（org 级判定）。
func TestResolve_NoTeam(t *testing.T) {
	c := newScopeClient(t)
	ctx := context.Background()
	// 建 incident 但不设 team（incident.team 是 optional edge）
	inc, err := c.Incident.Create().
		SetNumber("INC-notam").
		SetTitle("no team").
		SetSeverity(incident.SeverityInfo).
		Save(ctx)
	if err != nil {
		t.Fatalf("create incident: %v", err)
	}
	s := NewScopeResolver(c)
	got, _ := s.Resolve(ctx, "incident", inc.ID)
	if got != nil {
		t.Errorf("incident without team should return nil, got %v", *got)
	}
}

// TestCheckResourceAccess_Anonymous uid<=0 放行（渐进阶段）。
func TestCheckResourceAccess_Anonymous(t *testing.T) {
	c := newScopeClient(t)
	s := NewScopeResolver(c)
	authz := NewAuthorizer(c)
	allowed, err := CheckResourceAccess(context.Background(), authz, s, 0, PermIncidentView, "incident", 1)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !allowed {
		t.Error("uid<=0 (anonymous) should be allowed in progressive phase")
	}
}
