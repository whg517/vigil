// handler_authz_test.go 复盘 handler 资源级鉴权测试（SEC-01）。
//
// 验证权限点误用修复：只读角色（subscriber / responder，仅 postmortem.view）
// 不得推动状态机、删除复盘或生成草稿。修复前这些用例会返回 2xx（越权），
// 修复后必须返回 403。对照的正向基线（有 create/update/publish 的角色）应通过。
package postmortem

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/postmortem"
	"github.com/kevin/vigil/ent/role"
	"github.com/kevin/vigil/ent/rolebinding"
	"github.com/kevin/vigil/internal/auth"

	"github.com/labstack/echo/v5"
	_ "github.com/mattn/go-sqlite3"
)

// authzData 鉴权测试场景：
//   - team + incident 归属 team
//   - draftPM：draft 状态复盘（用于流转 in_review / 打回）
//   - reviewPM：in_review 状态复盘（用于流转 published / archived 前置）
//   - subscriberUID：仅 postmortem.view（只读干系人）
//   - responderUID：仅 postmortem.view（一线值班，对复盘只读）
//   - leadUID：postmortem.view + create + update + publish（值班长，正向基线）
type authzData struct {
	c             *ent.Client
	teamID        int
	freshIncID    int // 无复盘的事件，供 generateDraft 测试
	draftPM       int
	reviewPM      int
	subscriberUID int
	responderUID  int
	leadUID       int
}

func authzSetup(t *testing.T) authzData {
	t.Helper()
	// 每个测试用独立命名的内存库，避免 cache=shared 跨用例数据污染。
	c := enttest.Open(t, "sqlite3", "file:pm_authz_"+t.Name()+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()

	team, err := c.Team.Create().SetName("pay").SetSlug("pay").Save(ctx)
	if err != nil {
		t.Fatalf("create team: %v", err)
	}
	// 每个事件至多一个复盘（incident_postmortem 唯一约束），故用三个独立事件。
	mkInc := func(num string) *ent.Incident {
		inc, err := c.Incident.Create().
			SetNumber(num).SetTitle("支付5xx").SetSeverity("critical").
			SetStatus("resolved").SetTeamID(team.ID).Save(ctx)
		if err != nil {
			t.Fatalf("create incident %s: %v", num, err)
		}
		return inc
	}
	incDraft, incReview, incFresh := mkInc("INC-1"), mkInc("INC-2"), mkInc("INC-3")

	draftPM, err := c.Postmortem.Create().
		SetIncidentID(incDraft.ID).SetStatus(postmortem.StatusDraft).
		SetGeneratedBy(postmortem.GeneratedByHuman).SetSections(map[string]any{}).Save(ctx)
	if err != nil {
		t.Fatalf("create draft pm: %v", err)
	}
	reviewPM, err := c.Postmortem.Create().
		SetIncidentID(incReview.ID).SetStatus(postmortem.StatusInReview).
		SetGeneratedBy(postmortem.GeneratedByHuman).SetSections(map[string]any{}).Save(ctx)
	if err != nil {
		t.Fatalf("create review pm: %v", err)
	}

	// 只读角色：仅 postmortem.view（对应 seed 的 subscriber / responder 对复盘的权限）。
	viewRole, err := c.Role.Create().
		SetName("pm_viewer").SetScopeLevel(role.ScopeLevelTeam).
		SetPermissions([]string{string(auth.PermPostmortemView)}).Save(ctx)
	if err != nil {
		t.Fatalf("create view role: %v", err)
	}
	// 编辑角色：view + create + update + publish（对应 seed 的 responder_lead）。
	leadRole, err := c.Role.Create().
		SetName("pm_lead").SetScopeLevel(role.ScopeLevelTeam).
		SetPermissions([]string{
			string(auth.PermPostmortemView), string(auth.PermPostmortemCreate),
			string(auth.PermPostmortemUpdate), string(auth.PermPostmortemPublish),
		}).Save(ctx)
	if err != nil {
		t.Fatalf("create lead role: %v", err)
	}

	mkUser := func(name string, roleID int) int {
		u, err := c.User.Create().SetUsername(name).SetEmail(name + "@x.com").Save(ctx)
		if err != nil {
			t.Fatalf("create user %s: %v", name, err)
		}
		if _, err := c.RoleBinding.Create().
			SetUserID(u.ID).SetRoleID(roleID).
			SetScopeLevel(rolebinding.ScopeLevelTeam).SetTeamID(strconv.Itoa(team.ID)).
			SetGrantedAt(time.Now()).Save(ctx); err != nil {
			t.Fatalf("bind user %s: %v", name, err)
		}
		return u.ID
	}

	return authzData{
		c:             c,
		teamID:        team.ID,
		freshIncID:    incFresh.ID,
		draftPM:       draftPM.ID,
		reviewPM:      reviewPM.ID,
		subscriberUID: mkUser("subscriber", viewRole.ID),
		responderUID:  mkUser("responder", viewRole.ID),
		leadUID:       mkUser("lead", leadRole.ID),
	}
}

// newAuthzHandler 构造带真实 authz+scope 的 handler 与 echo（身份解析用 X-Vigil-User-ID）。
func newAuthzHandler(d authzData) *echo.Echo {
	h := NewHandler(d.c, NewEngine(d.c, nil))
	h.SetAuthorizer(auth.NewAuthorizer(d.c))
	h.SetScopeResolver(auth.NewScopeResolver(d.c))
	e := echo.New()
	v1 := e.Group("/api/v1", auth.RequireUser(false, auth.NewIdentityResolver(nil, nil, true)))
	h.Register(v1)
	return e
}

// doAs 以 uid 身份发请求（可选 JSON body）。
func doAs(e *echo.Echo, method, path string, uid int, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Header.Set("X-Vigil-User-ID", strconv.Itoa(uid))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, r)
	return rec
}

// —— transition：只读角色不得推动状态机 ——

// TestTransition_SubscriberInReview_403 subscriber（仅 view）把 draft→in_review → 403。
func TestTransition_SubscriberInReview_403(t *testing.T) {
	d := authzSetup(t)
	e := newAuthzHandler(d)
	rec := doAs(e, http.MethodPatch, "/api/v1/postmortems/"+strconv.Itoa(d.draftPM)+"/transition",
		d.subscriberUID, `{"status":"in_review"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("subscriber transition→in_review: got %d, want 403 (body=%s)", rec.Code, rec.Body.String())
	}
}

// TestTransition_ResponderPublish_403 responder（仅 view）把 in_review→published → 403。
func TestTransition_ResponderPublish_403(t *testing.T) {
	d := authzSetup(t)
	e := newAuthzHandler(d)
	rec := doAs(e, http.MethodPatch, "/api/v1/postmortems/"+strconv.Itoa(d.reviewPM)+"/transition",
		d.responderUID, `{"status":"published"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("responder transition→published: got %d, want 403 (body=%s)", rec.Code, rec.Body.String())
	}
}

// TestTransition_ResponderReopenDraft_403 responder（仅 view）把 in_review 打回 draft → 403。
func TestTransition_ResponderReopenDraft_403(t *testing.T) {
	d := authzSetup(t)
	e := newAuthzHandler(d)
	rec := doAs(e, http.MethodPatch, "/api/v1/postmortems/"+strconv.Itoa(d.reviewPM)+"/transition",
		d.responderUID, `{"status":"draft"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("responder transition→draft(reopen): got %d, want 403 (body=%s)", rec.Code, rec.Body.String())
	}
}

// TestTransition_LeadInReview_OK 值班长（有 update）流转 draft→in_review → 200（正向基线）。
func TestTransition_LeadInReview_OK(t *testing.T) {
	d := authzSetup(t)
	e := newAuthzHandler(d)
	rec := doAs(e, http.MethodPatch, "/api/v1/postmortems/"+strconv.Itoa(d.draftPM)+"/transition",
		d.leadUID, `{"status":"in_review"}`)
	if rec.Code != http.StatusOK {
		t.Errorf("lead transition→in_review: got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
}

// TestTransition_LeadPublish_OK 值班长（有 publish）流转 in_review→published → 200（正向基线）。
func TestTransition_LeadPublish_OK(t *testing.T) {
	d := authzSetup(t)
	e := newAuthzHandler(d)
	rec := doAs(e, http.MethodPatch, "/api/v1/postmortems/"+strconv.Itoa(d.reviewPM)+"/transition",
		d.leadUID, `{"status":"published"}`)
	if rec.Code != http.StatusOK {
		t.Errorf("lead transition→published: got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
}

// —— delete：只读角色不得删除复盘 ——

// TestDelete_SubscriberPostmortem_403 subscriber（仅 view）删除复盘 → 403。
func TestDelete_SubscriberPostmortem_403(t *testing.T) {
	d := authzSetup(t)
	e := newAuthzHandler(d)
	rec := doAs(e, http.MethodDelete, "/api/v1/postmortems/"+strconv.Itoa(d.draftPM), d.subscriberUID, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("subscriber delete postmortem: got %d, want 403 (body=%s)", rec.Code, rec.Body.String())
	}
	// 断言复盘未被真正删除（只读角色越权删除防线；验证 403 后 handler 已中止，未落库）。
	if exists, _ := d.c.Postmortem.Query().Where(postmortem.IDEQ(d.draftPM)).Exist(context.Background()); !exists {
		t.Error("postmortem was deleted despite 403 (checkAccess did not short-circuit)")
	}
}

// TestDelete_ResponderPostmortem_403 responder（仅 view）删除复盘 → 403。
func TestDelete_ResponderPostmortem_403(t *testing.T) {
	d := authzSetup(t)
	e := newAuthzHandler(d)
	rec := doAs(e, http.MethodDelete, "/api/v1/postmortems/"+strconv.Itoa(d.draftPM), d.responderUID, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("responder delete postmortem: got %d, want 403 (body=%s)", rec.Code, rec.Body.String())
	}
}

// TestDelete_LeadPostmortem_OK 值班长（有 update）删除复盘 → 204（正向基线）。
func TestDelete_LeadPostmortem_OK(t *testing.T) {
	d := authzSetup(t)
	e := newAuthzHandler(d)
	rec := doAs(e, http.MethodDelete, "/api/v1/postmortems/"+strconv.Itoa(d.draftPM), d.leadUID, "")
	if rec.Code != http.StatusNoContent {
		t.Errorf("lead delete postmortem: got %d, want 204 (body=%s)", rec.Code, rec.Body.String())
	}
}

// —— generateDraft：只读角色不得生成/覆盖草稿 ——

// TestGenerateDraft_SubscriberIncident_403 subscriber（仅 view）为 incident 生成草稿 → 403。
func TestGenerateDraft_SubscriberIncident_403(t *testing.T) {
	d := authzSetup(t)
	e := newAuthzHandler(d)
	rec := doAs(e, http.MethodPost, "/api/v1/incidents/"+strconv.Itoa(d.freshIncID)+"/postmortem/draft",
		d.subscriberUID, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("subscriber generate draft: got %d, want 403 (body=%s)", rec.Code, rec.Body.String())
	}
	// 断言未生成草稿（越权草稿覆盖防线：handler 已中止，未落库）。
	if exists, _ := d.c.Postmortem.Query().
		Where(postmortem.HasIncidentWith(incident.IDEQ(d.freshIncID))).
		Exist(context.Background()); exists {
		t.Error("draft was generated despite 403 (checkAccess did not short-circuit)")
	}
}

// TestGenerateDraft_ResponderIncident_403 responder（仅 view）为 incident 生成草稿 → 403。
func TestGenerateDraft_ResponderIncident_403(t *testing.T) {
	d := authzSetup(t)
	e := newAuthzHandler(d)
	rec := doAs(e, http.MethodPost, "/api/v1/incidents/"+strconv.Itoa(d.freshIncID)+"/postmortem/draft",
		d.responderUID, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("responder generate draft: got %d, want 403 (body=%s)", rec.Code, rec.Body.String())
	}
}

// TestGenerateDraft_LeadIncident_OK 值班长（有 create）生成草稿 → 201（正向基线）。
func TestGenerateDraft_LeadIncident_OK(t *testing.T) {
	d := authzSetup(t)
	e := newAuthzHandler(d)
	rec := doAs(e, http.MethodPost, "/api/v1/incidents/"+strconv.Itoa(d.freshIncID)+"/postmortem/draft",
		d.leadUID, "")
	if rec.Code != http.StatusCreated {
		t.Errorf("lead generate draft: got %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
}
