// handler_isolation_test.go 跨 team 数据隔离测试（ARCH-02/SEC-01）。
//
// 覆盖 SuppressionRule / NotificationTemplate / NotificationRule 三类配置写入口：
// team 级权限用户不能查看/改写/删除其他 team 的规则与模板。除断言 403 外，对写端点回读
// 资源状态，专治 checkAccess 短路失效（d98843a 修复类）——「报 403 却已落库」的越权。
package notification

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/notificationrule"
	"github.com/kevin/vigil/ent/notificationtemplate"
	"github.com/kevin/vigil/ent/role"
	"github.com/kevin/vigil/ent/rolebinding"
	"github.com/kevin/vigil/ent/suppressionrule"
	"github.com/kevin/vigil/internal/auth"

	"github.com/labstack/echo/v5"
	_ "github.com/mattn/go-sqlite3"
)

// isoData 隔离场景：teamA/teamB 各持一份 suppression / template / rule；
// userA 仅 teamA 全套权限，userB 仅 teamB。
type isoData struct {
	c            *ent.Client
	teamA, teamB int
	suppA, suppB int
	tmplA, tmplB int
	ruleA, ruleB int
	userA, userB int
}

func isoSetup(t *testing.T) isoData {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:notif_iso?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := t.Context()

	ta, err := c.Team.Create().SetName("pay").SetSlug("pay").Save(ctx)
	if err != nil {
		t.Fatalf("create teamA: %v", err)
	}
	tb, err := c.Team.Create().SetName("order").SetSlug("order").Save(ctx)
	if err != nil {
		t.Fatalf("create teamB: %v", err)
	}

	// viewer 角色：三类资源的 view 权限（team 级）。
	viewerRole, err := c.Role.Create().
		SetName("viewer").
		SetScopeLevel(role.ScopeLevelTeam).
		SetPermissions([]string{
			string(auth.PermSuppressionView),
			string(auth.PermNotificationTemplateView),
			string(auth.PermNotificationRuleView),
		}).
		Save(ctx)
	if err != nil {
		t.Fatalf("create role: %v", err)
	}

	ua, err := c.User.Create().SetUsername("alice").SetEmail("a@x.com").Save(ctx)
	if err != nil {
		t.Fatalf("create userA: %v", err)
	}
	ub, err := c.User.Create().SetUsername("bob").SetEmail("b@x.com").Save(ctx)
	if err != nil {
		t.Fatalf("create userB: %v", err)
	}
	for _, p := range []struct{ uid, tid int }{{ua.ID, ta.ID}, {ub.ID, tb.ID}} {
		if _, err := c.RoleBinding.Create().
			SetUserID(p.uid).
			SetRoleID(viewerRole.ID).
			SetScopeLevel(rolebinding.ScopeLevelTeam).
			SetTeamID(strconv.Itoa(p.tid)).
			SetGrantedAt(time.Now()).
			Save(ctx); err != nil {
			t.Fatalf("create binding: %v", err)
		}
	}

	mkSupp := func(name string, tid int) int {
		r, err := c.SuppressionRule.Create().
			SetName(name).SetMatchLabels(map[string]string{"k": "v"}).
			SetAction(suppressionrule.ActionSuppress).SetTeamID(tid).Save(ctx)
		if err != nil {
			t.Fatalf("create suppression %s: %v", name, err)
		}
		return r.ID
	}
	mkTmpl := func(name string, tid int) int {
		tm, err := c.NotificationTemplate.Create().
			SetName(name).SetChannel(notificationtemplate.ChannelIm).
			SetFormat(notificationtemplate.FormatText).
			SetTitleTemplate("t").SetBodyTemplate("b").SetTeamID(tid).Save(ctx)
		if err != nil {
			t.Fatalf("create template %s: %v", name, err)
		}
		return tm.ID
	}
	mkRule := func(name string, tid int) int {
		r, err := c.NotificationRule.Create().
			SetName(name).SetCondition(map[string]any{}).
			SetChannels([]string{"im"}).SetTeamID(tid).Save(ctx)
		if err != nil {
			t.Fatalf("create rule %s: %v", name, err)
		}
		return r.ID
	}

	return isoData{
		c: c, teamA: ta.ID, teamB: tb.ID,
		suppA: mkSupp("supp-a", ta.ID), suppB: mkSupp("supp-b", tb.ID),
		tmplA: mkTmpl("tmpl-a", ta.ID), tmplB: mkTmpl("tmpl-b", tb.ID),
		ruleA: mkRule("rule-a", ta.ID), ruleB: mkRule("rule-b", tb.ID),
		userA: ua.ID, userB: ub.ID,
	}
}

// grantWriter 给 uid 授予 teamID 范围的三类资源写权限，验证跨 team 仍被拦。
func grantWriter(t *testing.T, d isoData, uid, teamID int) {
	t.Helper()
	ctx := t.Context()
	wr, err := d.c.Role.Create().
		SetName("writer-" + strconv.Itoa(teamID) + "-" + strconv.Itoa(uid)).
		SetScopeLevel(role.ScopeLevelTeam).
		SetPermissions([]string{
			string(auth.PermSuppressionView), string(auth.PermSuppressionUpdate), string(auth.PermSuppressionDelete),
			string(auth.PermNotificationTemplateView), string(auth.PermNotificationTemplateUpdate), string(auth.PermNotificationTemplateDelete),
			string(auth.PermNotificationRuleView), string(auth.PermNotificationRuleUpdate), string(auth.PermNotificationRuleDelete),
		}).
		Save(ctx)
	if err != nil {
		t.Fatalf("create writer role: %v", err)
	}
	if _, err := d.c.RoleBinding.Create().
		SetUserID(uid).SetRoleID(wr.ID).
		SetScopeLevel(rolebinding.ScopeLevelTeam).
		SetTeamID(strconv.Itoa(teamID)).SetGrantedAt(time.Now()).Save(ctx); err != nil {
		t.Fatalf("create writer binding: %v", err)
	}
}

func newIsolatedHandler(d isoData) *echo.Echo {
	h := NewHandler(d.c, nil, nil)
	h.SetTemplateEngine(NewTemplateEngine(d.c))
	h.SetAuthorizer(auth.NewAuthorizer(d.c))
	h.SetScopeResolver(auth.NewScopeResolver(d.c))
	e := echo.New()
	v1 := e.Group("/api/v1", auth.RequireUser(false, auth.NewIdentityResolver(nil, nil, true, nil)))
	h.Register(v1)
	return e
}

func reqAsUser(e *echo.Echo, method, path string, uid int, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Vigil-User-ID", strconv.Itoa(uid))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// ===== SuppressionRule =====

func TestIsolation_GetOwnTeamSuppression_OK(t *testing.T) {
	d := isoSetup(t)
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodGet, "/api/v1/suppression-rules/"+strconv.Itoa(d.suppA), d.userA, "")
	if rec.Code != http.StatusOK {
		t.Errorf("userA get own suppression: got %d, want 200 (baseline)", rec.Code)
	}
}

func TestIsolation_GetOtherTeamSuppression_403(t *testing.T) {
	d := isoSetup(t)
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodGet, "/api/v1/suppression-rules/"+strconv.Itoa(d.suppB), d.userA, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("userA get teamB suppression: got %d, want 403 (cross-team isolation FAILED)", rec.Code)
	}
}

func TestIsolation_SuppressionListOnlyVisibleTeam(t *testing.T) {
	d := isoSetup(t)
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodGet, "/api/v1/suppression-rules", d.userA, "")
	body := rec.Body.String()
	if strings.Contains(body, `"supp-b"`) || !strings.Contains(body, `"supp-a"`) {
		t.Errorf("userA suppression list should contain only supp-a, got: %s (cross-team leak!)", body)
	}
}

// TestIsolation_CrossTeamUpdateSuppression_StateUnchanged ★ 回读校验短路失效。
func TestIsolation_CrossTeamUpdateSuppression_StateUnchanged(t *testing.T) {
	d := isoSetup(t)
	grantWriter(t, d, d.userA, d.teamA)
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodPatch, "/api/v1/suppression-rules/"+strconv.Itoa(d.suppB), d.userA, `{"name":"hacked"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("userA patch teamB suppression: got %d, want 403", rec.Code)
	}
	r, err := d.c.SuppressionRule.Get(t.Context(), d.suppB)
	if err != nil {
		t.Fatalf("reload suppB: %v", err)
	}
	if r.Name != "supp-b" {
		t.Errorf("suppB name mutated despite 403: got %q, want supp-b (short-circuit FAILED)", r.Name)
	}
}

func TestIsolation_CrossTeamDeleteSuppression_StateUnchanged(t *testing.T) {
	d := isoSetup(t)
	grantWriter(t, d, d.userA, d.teamA)
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodDelete, "/api/v1/suppression-rules/"+strconv.Itoa(d.suppB), d.userA, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("userA delete teamB suppression: got %d, want 403", rec.Code)
	}
	if exist, err := d.c.SuppressionRule.Query().Where(suppressionrule.IDEQ(d.suppB)).Exist(t.Context()); err != nil {
		t.Fatalf("check suppB exist: %v", err)
	} else if !exist {
		t.Error("suppB deleted despite 403 (short-circuit FAILED)")
	}
}

// ===== NotificationTemplate =====

func TestIsolation_GetOwnTeamTemplate_OK(t *testing.T) {
	d := isoSetup(t)
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodGet, "/api/v1/notification-templates/"+strconv.Itoa(d.tmplA), d.userA, "")
	if rec.Code != http.StatusOK {
		t.Errorf("userA get own template: got %d, want 200 (baseline)", rec.Code)
	}
}

func TestIsolation_GetOtherTeamTemplate_403(t *testing.T) {
	d := isoSetup(t)
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodGet, "/api/v1/notification-templates/"+strconv.Itoa(d.tmplB), d.userA, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("userA get teamB template: got %d, want 403 (cross-team isolation FAILED)", rec.Code)
	}
}

func TestIsolation_GetOtherTeamTemplate_Symmetric(t *testing.T) {
	d := isoSetup(t)
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodGet, "/api/v1/notification-templates/"+strconv.Itoa(d.tmplA), d.userB, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("userB get teamA template: got %d, want 403", rec.Code)
	}
}

func TestIsolation_TemplateListOnlyVisibleTeam(t *testing.T) {
	d := isoSetup(t)
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodGet, "/api/v1/notification-templates", d.userA, "")
	body := rec.Body.String()
	if strings.Contains(body, `"tmpl-b"`) || !strings.Contains(body, `"tmpl-a"`) {
		t.Errorf("userA template list should contain only tmpl-a, got: %s (cross-team leak!)", body)
	}
}

// TestIsolation_CrossTeamUpdateTemplate_StateUnchanged ★ 回读校验短路失效。
func TestIsolation_CrossTeamUpdateTemplate_StateUnchanged(t *testing.T) {
	d := isoSetup(t)
	grantWriter(t, d, d.userA, d.teamA)
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodPatch, "/api/v1/notification-templates/"+strconv.Itoa(d.tmplB), d.userA, `{"title_template":"hacked"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("userA patch teamB template: got %d, want 403", rec.Code)
	}
	tm, err := d.c.NotificationTemplate.Get(t.Context(), d.tmplB)
	if err != nil {
		t.Fatalf("reload tmplB: %v", err)
	}
	if tm.TitleTemplate != "t" {
		t.Errorf("tmplB title mutated despite 403: got %q, want t (short-circuit FAILED)", tm.TitleTemplate)
	}
}

func TestIsolation_CrossTeamDeleteTemplate_StateUnchanged(t *testing.T) {
	d := isoSetup(t)
	grantWriter(t, d, d.userA, d.teamA)
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodDelete, "/api/v1/notification-templates/"+strconv.Itoa(d.tmplB), d.userA, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("userA delete teamB template: got %d, want 403", rec.Code)
	}
	if exist, err := d.c.NotificationTemplate.Query().Where(notificationtemplate.IDEQ(d.tmplB)).Exist(t.Context()); err != nil {
		t.Fatalf("check tmplB exist: %v", err)
	} else if !exist {
		t.Error("tmplB deleted despite 403 (short-circuit FAILED)")
	}
}

// ===== NotificationRule =====

func TestIsolation_GetOtherTeamRule_403(t *testing.T) {
	d := isoSetup(t)
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodGet, "/api/v1/notification-rules/"+strconv.Itoa(d.ruleB), d.userA, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("userA get teamB rule: got %d, want 403 (cross-team isolation FAILED)", rec.Code)
	}
}

func TestIsolation_RuleListOnlyVisibleTeam(t *testing.T) {
	d := isoSetup(t)
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodGet, "/api/v1/notification-rules", d.userA, "")
	body := rec.Body.String()
	if strings.Contains(body, `"rule-b"`) || !strings.Contains(body, `"rule-a"`) {
		t.Errorf("userA rule list should contain only rule-a, got: %s (cross-team leak!)", body)
	}
}

// TestIsolation_CrossTeamUpdateRule_StateUnchanged ★ 回读校验短路失效。
func TestIsolation_CrossTeamUpdateRule_StateUnchanged(t *testing.T) {
	d := isoSetup(t)
	grantWriter(t, d, d.userA, d.teamA)
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodPatch, "/api/v1/notification-rules/"+strconv.Itoa(d.ruleB), d.userA, `{"name":"hacked"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("userA patch teamB rule: got %d, want 403", rec.Code)
	}
	r, err := d.c.NotificationRule.Get(t.Context(), d.ruleB)
	if err != nil {
		t.Fatalf("reload ruleB: %v", err)
	}
	if r.Name != "rule-b" {
		t.Errorf("ruleB name mutated despite 403: got %q, want rule-b (short-circuit FAILED)", r.Name)
	}
}

func TestIsolation_CrossTeamDeleteRule_StateUnchanged(t *testing.T) {
	d := isoSetup(t)
	grantWriter(t, d, d.userA, d.teamA)
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodDelete, "/api/v1/notification-rules/"+strconv.Itoa(d.ruleB), d.userA, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("userA delete teamB rule: got %d, want 403", rec.Code)
	}
	if exist, err := d.c.NotificationRule.Query().Where(notificationrule.IDEQ(d.ruleB)).Exist(t.Context()); err != nil {
		t.Fatalf("check ruleB exist: %v", err)
	} else if !exist {
		t.Error("ruleB deleted despite 403 (short-circuit FAILED)")
	}
}

// TestIsolation_OrgWideUserSeesAll org 级用户不被隔离。
func TestIsolation_OrgWideUserSeesAll(t *testing.T) {
	d := isoSetup(t)
	ctx := t.Context()
	orgRole, err := d.c.Role.Create().
		SetName("admin").SetScopeLevel(role.ScopeLevelOrg).
		SetPermissions([]string{string(auth.PermSuppressionView)}).Save(ctx)
	if err != nil {
		t.Fatalf("create org role: %v", err)
	}
	if _, err := d.c.RoleBinding.Create().
		SetUserID(d.userA).SetRoleID(orgRole.ID).
		SetScopeLevel(rolebinding.ScopeLevelOrg).SetGrantedAt(time.Now()).Save(ctx); err != nil {
		t.Fatalf("create org binding: %v", err)
	}
	e := newIsolatedHandler(d)
	rec := reqAsUser(e, http.MethodGet, "/api/v1/suppression-rules/"+strconv.Itoa(d.suppB), d.userA, "")
	if rec.Code != http.StatusOK {
		t.Errorf("org-wide userA should access teamB suppression, got %d", rec.Code)
	}
}
