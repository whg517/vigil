// suppression_kind_test.go 维护窗口独立操作流（backlog §2.7 / M3.2）：
// SuppressionRule.kind（adhoc|maintenance）+ time_window 计划起止校验 + list 按 kind 过滤。
//
// 复用 handler_isolation_test.go 的 isoSetup / newIsolatedHandler / reqAsUser harness，
// 以及 suppression_expires_test.go 的 grantSuppressionWrite（授 create/update 写权限）。
package notification

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/kevin/vigil/ent/suppressionrule"
)

// TestSuppression_CreateMaintenanceKind POST kind=maintenance 落库并回读。
func TestSuppression_CreateMaintenanceKind(t *testing.T) {
	d := isoSetup(t)
	grantSuppressionWrite(t, d)
	e := newIsolatedHandler(d)

	start := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	end := time.Now().Add(3 * time.Hour).UTC().Format(time.RFC3339)
	body := `{"name":"maint-win","kind":"maintenance","match_labels":{"env":"maint"},` +
		`"action":"suppress","team_id":` + strconv.Itoa(d.teamA) +
		`,"time_window":{"start":"` + start + `","end":"` + end + `"}}`
	rec := reqAsUser(e, http.MethodPost, "/api/v1/suppression-rules", d.userA, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create maintenance: got %d body=%s", rec.Code, rec.Body.String())
	}
	r, err := d.c.SuppressionRule.Query().Where(suppressionrule.NameEQ("maint-win")).Only(t.Context())
	if err != nil {
		t.Fatalf("query created rule: %v", err)
	}
	if r.Kind != suppressionrule.KindMaintenance {
		t.Fatalf("kind not persisted, got %s", r.Kind)
	}
	if s, _ := r.TimeWindow["start"].(string); s != start {
		t.Fatalf("time_window.start not persisted, got %q", s)
	}
}

// TestSuppression_CreateDefaultsAdhoc 不传 kind 默认 adhoc。
func TestSuppression_CreateDefaultsAdhoc(t *testing.T) {
	d := isoSetup(t)
	grantSuppressionWrite(t, d)
	e := newIsolatedHandler(d)

	body := `{"name":"noise","match_labels":{"env":"x"},"action":"suppress","team_id":` +
		strconv.Itoa(d.teamA) + `}`
	rec := reqAsUser(e, http.MethodPost, "/api/v1/suppression-rules", d.userA, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d body=%s", rec.Code, rec.Body.String())
	}
	r, _ := d.c.SuppressionRule.Query().Where(suppressionrule.NameEQ("noise")).Only(t.Context())
	if r.Kind != suppressionrule.KindAdhoc {
		t.Fatalf("default kind should be adhoc, got %s", r.Kind)
	}
}

// TestSuppression_CreateInvalidKind 非法 kind 返 400。
func TestSuppression_CreateInvalidKind(t *testing.T) {
	d := isoSetup(t)
	grantSuppressionWrite(t, d)
	e := newIsolatedHandler(d)

	body := `{"name":"bad","kind":"weird","match_labels":{"env":"x"},"action":"suppress","team_id":` +
		strconv.Itoa(d.teamA) + `}`
	rec := reqAsUser(e, http.MethodPost, "/api/v1/suppression-rules", d.userA, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid kind: got %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
}

// TestSuppression_CreateInvalidTimeWindow time_window start>=end 返 400。
func TestSuppression_CreateInvalidTimeWindow(t *testing.T) {
	d := isoSetup(t)
	grantSuppressionWrite(t, d)
	e := newIsolatedHandler(d)

	start := time.Now().Add(3 * time.Hour).UTC().Format(time.RFC3339)
	end := time.Now().Add(time.Hour).UTC().Format(time.RFC3339) // end < start
	body := `{"name":"bad-win","kind":"maintenance","match_labels":{"env":"x"},"action":"suppress","team_id":` +
		strconv.Itoa(d.teamA) + `,"time_window":{"start":"` + start + `","end":"` + end + `"}}`
	rec := reqAsUser(e, http.MethodPost, "/api/v1/suppression-rules", d.userA, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("start>=end: got %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
}

// TestSuppression_CreateTimeWindowMissingEnd 只给 start 不给 end 返 400。
func TestSuppression_CreateTimeWindowMissingEnd(t *testing.T) {
	d := isoSetup(t)
	grantSuppressionWrite(t, d)
	e := newIsolatedHandler(d)

	start := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	body := `{"name":"half-win","kind":"maintenance","match_labels":{"env":"x"},"action":"suppress","team_id":` +
		strconv.Itoa(d.teamA) + `,"time_window":{"start":"` + start + `"}}`
	rec := reqAsUser(e, http.MethodPost, "/api/v1/suppression-rules", d.userA, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing end: got %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
}

// TestSuppression_UpdateKind PATCH 切换 kind=maintenance。
func TestSuppression_UpdateKind(t *testing.T) {
	d := isoSetup(t)
	grantSuppressionWrite(t, d)
	e := newIsolatedHandler(d)

	rec := reqAsUser(e, http.MethodPatch, "/api/v1/suppression-rules/"+strconv.Itoa(d.suppA), d.userA,
		`{"kind":"maintenance"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update kind: got %d body=%s", rec.Code, rec.Body.String())
	}
	r, _ := d.c.SuppressionRule.Get(t.Context(), d.suppA)
	if r.Kind != suppressionrule.KindMaintenance {
		t.Fatalf("kind should be maintenance, got %s", r.Kind)
	}
}

// TestSuppression_ListFilterByKind GET ?kind=maintenance 只返回维护窗口规则。
func TestSuppression_ListFilterByKind(t *testing.T) {
	d := isoSetup(t)
	grantSuppressionWrite(t, d)
	e := newIsolatedHandler(d)

	// teamA 现有 suppA（adhoc 默认）。再建一条 maintenance。
	if _, err := d.c.SuppressionRule.Create().
		SetName("maint-a").SetMatchLabels(map[string]string{"env": "m"}).
		SetAction(suppressionrule.ActionSuppress).
		SetKind(suppressionrule.KindMaintenance).
		SetTeamID(d.teamA).Save(t.Context()); err != nil {
		t.Fatalf("seed maintenance rule: %v", err)
	}

	rec := reqAsUser(e, http.MethodGet, "/api/v1/suppression-rules?kind=maintenance", d.userA, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list?kind=maintenance: got %d body=%s", rec.Code, rec.Body.String())
	}
	var rules []struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rules); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rules) != 1 || rules[0].Name != "maint-a" || rules[0].Kind != "maintenance" {
		t.Fatalf("kind=maintenance filter wrong, got %+v", rules)
	}

	// ?kind=adhoc 只返回 adhoc（suppA）。
	rec2 := reqAsUser(e, http.MethodGet, "/api/v1/suppression-rules?kind=adhoc", d.userA, "")
	var adhoc []struct {
		Kind string `json:"kind"`
	}
	_ = json.Unmarshal(rec2.Body.Bytes(), &adhoc)
	for _, r := range adhoc {
		if r.Kind != "adhoc" {
			t.Fatalf("kind=adhoc filter leaked non-adhoc: %+v", adhoc)
		}
	}
}

// TestSuppression_ListInvalidKind 非法 ?kind= 返 400。
func TestSuppression_ListInvalidKind(t *testing.T) {
	d := isoSetup(t)
	grantSuppressionWrite(t, d)
	e := newIsolatedHandler(d)

	rec := reqAsUser(e, http.MethodGet, "/api/v1/suppression-rules?kind=weird", d.userA, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid kind filter: got %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
}
