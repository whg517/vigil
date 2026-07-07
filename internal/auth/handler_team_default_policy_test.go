package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/schema"

	"github.com/labstack/echo/v5"
	_ "github.com/mattn/go-sqlite3"
)

// newTeamPolicyTest 起内存库 + TeamHandler（不注入 authz，专测默认策略读写逻辑）。
func newTeamPolicyTest(t *testing.T) (*ent.Client, *TeamHandler) {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:team_pol_"+t.Name()+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	return c, NewTeamHandler(c)
}

func mkPolicy(t *testing.T, c *ent.Client, teamID int, name string) *ent.EscalationPolicy {
	t.Helper()
	p, err := c.EscalationPolicy.Create().
		SetName(name).SetRepeatTimes(0).
		SetLevels([]schema.EscalationLevel{}).SetTeamID(teamID).Save(context.Background())
	if err != nil {
		t.Fatalf("create policy: %v", err)
	}
	return p
}

func patchTeam(h *TeamHandler, id int, body string) *httptest.ResponseRecorder {
	e := echo.New()
	e.PATCH("/teams/:id", h.updateTeam)
	req := httptest.NewRequest(http.MethodPatch, "/teams/"+strconv.Itoa(id), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// TestTeamSetDefaultPolicy_OK 设置本团队的策略为默认 → 200 且响应/库都回带该 id。
func TestTeamSetDefaultPolicy_OK(t *testing.T) {
	c, h := newTeamPolicyTest(t)
	ctx := context.Background()
	tm, _ := c.Team.Create().SetName("pay").SetSlug("pay").Save(ctx)
	pol := mkPolicy(t, c, tm.ID, "p1")

	rec := patchTeam(h, tm.ID, `{"default_escalation_policy_id":`+strconv.Itoa(pol.ID)+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		DefaultEscalationPolicyID *int `json:"default_escalation_policy_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.DefaultEscalationPolicyID == nil || *resp.DefaultEscalationPolicyID != pol.ID {
		t.Fatalf("response default policy: got %v, want %d", resp.DefaultEscalationPolicyID, pol.ID)
	}
	// 库里边已建立
	got, err := tm.QueryDefaultEscalationPolicy().Only(ctx)
	if err != nil || got.ID != pol.ID {
		t.Fatalf("db default policy: got %v err=%v, want %d", got, err, pol.ID)
	}
}

// TestTeamSetDefaultPolicy_CrossTeamRejected 设置他团队策略为默认 → 400 且状态不变（防越权绑定）。
func TestTeamSetDefaultPolicy_CrossTeamRejected(t *testing.T) {
	c, h := newTeamPolicyTest(t)
	ctx := context.Background()
	tmA, _ := c.Team.Create().SetName("A").SetSlug("a").Save(ctx)
	tmB, _ := c.Team.Create().SetName("B").SetSlug("b").Save(ctx)
	polB := mkPolicy(t, c, tmB.ID, "pb") // 属于 B

	rec := patchTeam(h, tmA.ID, `{"default_escalation_policy_id":`+strconv.Itoa(polB.ID)+`}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	// A 的默认策略仍未设置（回读状态，防 checkAccess 短路式误判）。
	if _, err := tmA.QueryDefaultEscalationPolicy().Only(ctx); !ent.IsNotFound(err) {
		t.Fatalf("team A default policy must remain unset, got err=%v", err)
	}
}

// TestTeamClearDefaultPolicy 传 0 清除默认策略 → 响应 null 且库中解绑。
func TestTeamClearDefaultPolicy(t *testing.T) {
	c, h := newTeamPolicyTest(t)
	ctx := context.Background()
	tm, _ := c.Team.Create().SetName("pay").SetSlug("pay").Save(ctx)
	pol := mkPolicy(t, c, tm.ID, "p1")
	if err := c.Team.UpdateOneID(tm.ID).SetDefaultEscalationPolicyID(pol.ID).Exec(ctx); err != nil {
		t.Fatalf("preset default: %v", err)
	}

	rec := patchTeam(h, tm.ID, `{"default_escalation_policy_id":0}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		DefaultEscalationPolicyID *int `json:"default_escalation_policy_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.DefaultEscalationPolicyID != nil {
		t.Fatalf("cleared response should be null, got %v", *resp.DefaultEscalationPolicyID)
	}
	if _, err := tm.QueryDefaultEscalationPolicy().Only(ctx); !ent.IsNotFound(err) {
		t.Fatalf("default policy should be cleared, got err=%v", err)
	}
}
