// suppression_expires_test.go B15：SuppressionRule.expires_at 可通过 API 设置/清除。
//
// 复用 handler_isolation_test.go 的 isoSetup / newIsolatedHandler / reqAsUser харness。
package notification

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/kevin/vigil/ent/role"
	"github.com/kevin/vigil/ent/rolebinding"
	"github.com/kevin/vigil/ent/suppressionrule"
	"github.com/kevin/vigil/internal/auth"
)

// grantSuppressionWrite 给 userA 追加 suppression 写权限（create/update），返回其 team。
// isoSetup 里的 viewer 角色只有 view，写端点需另授权。
func grantSuppressionWrite(t *testing.T, d isoData) {
	t.Helper()
	ctx := t.Context()
	writerRole, err := d.c.Role.Create().
		SetName("supp-writer").
		SetScopeLevel(role.ScopeLevelTeam).
		SetPermissions([]string{
			string(auth.PermSuppressionView),
			string(auth.PermSuppressionCreate),
			string(auth.PermSuppressionUpdate),
		}).
		Save(ctx)
	if err != nil {
		t.Fatalf("create writer role: %v", err)
	}
	if _, err := d.c.RoleBinding.Create().
		SetUserID(d.userA).
		SetRoleID(writerRole.ID).
		SetScopeLevel(rolebinding.ScopeLevelTeam).
		SetTeamID(strconv.Itoa(d.teamA)).
		SetGrantedAt(time.Now()).
		Save(ctx); err != nil {
		t.Fatalf("bind writer: %v", err)
	}
}

// TestSuppression_CreateWithExpiresAt B15：POST 带 expires_at 落库并生效。
func TestSuppression_CreateWithExpiresAt(t *testing.T) {
	d := isoSetup(t)
	grantSuppressionWrite(t, d)
	e := newIsolatedHandler(d)

	exp := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	body := `{"name":"maint","match_labels":{"env":"maint"},"action":"suppress","team_id":` +
		strconv.Itoa(d.teamA) + `,"expires_at":"` + exp + `"}`
	rec := reqAsUser(e, http.MethodPost, "/api/v1/suppression-rules", d.userA, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create suppression: got %d body=%s", rec.Code, rec.Body.String())
	}
	// 回读：expires_at 应落库。
	r, err := d.c.SuppressionRule.Query().Where(suppressionrule.NameEQ("maint")).Only(t.Context())
	if err != nil {
		t.Fatalf("query created rule: %v", err)
	}
	if r.ExpiresAt == nil {
		t.Fatalf("expires_at not persisted")
	}
}

// TestSuppression_CreateInvalidExpiresAt B15：非法 expires_at 返 400。
func TestSuppression_CreateInvalidExpiresAt(t *testing.T) {
	d := isoSetup(t)
	grantSuppressionWrite(t, d)
	e := newIsolatedHandler(d)

	body := `{"name":"bad","match_labels":{"env":"maint"},"action":"suppress","team_id":` +
		strconv.Itoa(d.teamA) + `,"expires_at":"not-a-time"}`
	rec := reqAsUser(e, http.MethodPost, "/api/v1/suppression-rules", d.userA, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid expires_at: got %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
}

// TestSuppression_UpdateSetAndClearExpiresAt B15：PATCH 设置后再传空串清除。
func TestSuppression_UpdateSetAndClearExpiresAt(t *testing.T) {
	d := isoSetup(t)
	grantSuppressionWrite(t, d)
	e := newIsolatedHandler(d)

	// 设置 expires_at（针对 teamA 已有的 suppA 规则）。
	exp := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	body := `{"expires_at":"` + exp + `"}`
	rec := reqAsUser(e, http.MethodPatch, "/api/v1/suppression-rules/"+strconv.Itoa(d.suppA), d.userA, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("set expires_at: got %d body=%s", rec.Code, rec.Body.String())
	}
	r, _ := d.c.SuppressionRule.Get(t.Context(), d.suppA)
	if r.ExpiresAt == nil {
		t.Fatalf("expires_at should be set")
	}

	// 清除（空串 → ClearExpiresAt）。
	rec2 := reqAsUser(e, http.MethodPatch, "/api/v1/suppression-rules/"+strconv.Itoa(d.suppA), d.userA, `{"expires_at":""}`)
	if rec2.Code != http.StatusOK {
		t.Fatalf("clear expires_at: got %d body=%s", rec2.Code, rec2.Body.String())
	}
	r2, _ := d.c.SuppressionRule.Get(t.Context(), d.suppA)
	if r2.ExpiresAt != nil {
		t.Fatalf("expires_at should be cleared, got %v", r2.ExpiresAt)
	}
}
