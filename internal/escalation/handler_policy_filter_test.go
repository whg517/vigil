// handler_policy_filter_test.go 升级策略列表按 team 过滤（团队默认策略选择器用）。
package escalation

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/schema"

	"github.com/labstack/echo/v5"
	_ "github.com/mattn/go-sqlite3"
)

// newOpenPolicyHandler 起一个不注入 authz 的策略 handler（开放，专测 team_id 过滤逻辑）。
func newOpenPolicyHandler(c *ent.Client) *echo.Echo {
	h := NewPolicyHandler(c)
	e := echo.New()
	v1 := e.Group("/api/v1")
	h.Register(v1)
	return e
}

func mkFilterPolicy(t *testing.T, c *ent.Client, teamID int, name string) *ent.EscalationPolicy {
	t.Helper()
	p, err := c.EscalationPolicy.Create().
		SetName(name).SetRepeatTimes(0).
		SetLevels([]schema.EscalationLevel{}).SetTeamID(teamID).Save(t.Context())
	if err != nil {
		t.Fatalf("create policy %s: %v", name, err)
	}
	return p
}

// TestPolicyListTeamFilter ?team_id= 只返回该团队策略；非法值 400。
func TestPolicyListTeamFilter(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:esc_filter?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := t.Context()
	ta, _ := c.Team.Create().SetName("pay").SetSlug("pay").Save(ctx)
	tb, _ := c.Team.Create().SetName("order").SetSlug("order").Save(ctx)
	polA := mkFilterPolicy(t, c, ta.ID, "pa")
	mkFilterPolicy(t, c, tb.ID, "pb")
	e := newOpenPolicyHandler(c)

	// 按 teamA 过滤 → 只得 polA。
	rec := reqAsUser(e, http.MethodGet, "/api/v1/escalation-policies?team_id="+strconv.Itoa(ta.ID), 0, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("filter status = %d, want 200", rec.Code)
	}
	var got []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got) != 1 || got[0].ID != polA.ID {
		t.Fatalf("team_id filter: got %+v, want only polA(%d)", got, polA.ID)
	}

	// 非法 team_id → 400。
	rec = reqAsUser(e, http.MethodGet, "/api/v1/escalation-policies?team_id=abc", 0, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid team_id: got %d, want 400", rec.Code)
	}

	// 无 team_id → 返回全部（2 条）。
	rec = reqAsUser(e, http.MethodGet, "/api/v1/escalation-policies", 0, "")
	var all []struct{ ID int }
	_ = json.Unmarshal(rec.Body.Bytes(), &all)
	if len(all) != 2 {
		t.Fatalf("no filter: got %d policies, want 2", len(all))
	}
}
