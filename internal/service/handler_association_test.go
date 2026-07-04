// handler_association_test.go Service↔Schedule/Runbook 关联端点契约测试（T2.5 / B13 / M14）。
//
// 覆盖：create 收 schedule_ids/runbook_ids 并落库；get 回带关联 id；update 全量替换语义
// （nil 不改 / [] 清空 / [x] 替换）。无 authz 注入（checkAccess 降级放行），聚焦请求/响应契约。
package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"

	"github.com/labstack/echo/v5"
	_ "github.com/mattn/go-sqlite3"
)

type assocData struct {
	c              *ent.Client
	e              *echo.Echo
	sched1, sched2 int
	rb1, rb2       int
}

func assocSetup(t *testing.T) assocData {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:svc_assoc_"+t.Name()+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := t.Context()

	s1 := c.Schedule.Create().SetName("sched-1").SetType("calendar").SaveX(ctx)
	s2 := c.Schedule.Create().SetName("sched-2").SetType("rotation").SaveX(ctx)
	r1 := c.Runbook.Create().SetName("rb-1").SetType("document").SaveX(ctx)
	r2 := c.Runbook.Create().SetName("rb-2").SetType("executable").SaveX(ctx)

	h := NewHandler(c) // 不注入 authz：checkAccess 降级放行，聚焦契约
	e := echo.New()
	h.Register(e.Group("/api/v1"))
	return assocData{c: c, e: e, sched1: s1.ID, sched2: s2.ID, rb1: r1.ID, rb2: r2.ID}
}

func doJSON(e *echo.Echo, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// decodeAssoc 解析响应中的 schedule_ids/runbook_ids（顺序无关，用 set 比对）。
func decodeAssoc(t *testing.T, rec *httptest.ResponseRecorder) (map[int]bool, map[int]bool) {
	t.Helper()
	var resp struct {
		ID          int   `json:"id"`
		ScheduleIDs []int `json:"schedule_ids"`
		RunbookIDs  []int `json:"runbook_ids"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode resp: %v (body=%s)", err, rec.Body.String())
	}
	return toSet(resp.ScheduleIDs), toSet(resp.RunbookIDs)
}

func toSet(ids []int) map[int]bool {
	m := make(map[int]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

// TestCreateWithAssociations 创建时收 schedule_ids/runbook_ids 并回带。
func TestCreateWithAssociations(t *testing.T) {
	d := assocSetup(t)
	body := `{"name":"svc","slug":"svc","schedule_ids":[` +
		strconv.Itoa(d.sched1) + `,` + strconv.Itoa(d.sched2) + `],"runbook_ids":[` +
		strconv.Itoa(d.rb1) + `]}`
	rec := doJSON(d.e, http.MethodPost, "/api/v1/services", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	scheds, rbs := decodeAssoc(t, rec)
	if !scheds[d.sched1] || !scheds[d.sched2] || len(scheds) != 2 {
		t.Errorf("schedule_ids: got %v, want {%d,%d}", scheds, d.sched1, d.sched2)
	}
	if !rbs[d.rb1] || len(rbs) != 1 {
		t.Errorf("runbook_ids: got %v, want {%d}", rbs, d.rb1)
	}
}

// TestGetReturnsAssociations get 回带已建立的关联 id。
func TestGetReturnsAssociations(t *testing.T) {
	d := assocSetup(t)
	svc := d.c.Service.Create().SetName("svc").SetSlug("svc").
		AddScheduleIDs(d.sched1).AddRunbookIDs(d.rb1, d.rb2).SaveX(t.Context())
	rec := doJSON(d.e, http.MethodGet, "/api/v1/services/"+strconv.Itoa(svc.ID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: got %d, want 200", rec.Code)
	}
	scheds, rbs := decodeAssoc(t, rec)
	if !scheds[d.sched1] || len(scheds) != 1 {
		t.Errorf("schedule_ids: got %v, want {%d}", scheds, d.sched1)
	}
	if !rbs[d.rb1] || !rbs[d.rb2] || len(rbs) != 2 {
		t.Errorf("runbook_ids: got %v, want {%d,%d}", rbs, d.rb1, d.rb2)
	}
}

// TestUpdateReplacesAssociations update 全量替换：[sched1,rb1] → [sched2],runbooks 清空。
func TestUpdateReplacesAssociations(t *testing.T) {
	d := assocSetup(t)
	svc := d.c.Service.Create().SetName("svc").SetSlug("svc").
		AddScheduleIDs(d.sched1).AddRunbookIDs(d.rb1).SaveX(t.Context())
	// schedule_ids 替换为 [sched2]，runbook_ids 传 [] 清空。
	body := `{"schedule_ids":[` + strconv.Itoa(d.sched2) + `],"runbook_ids":[]}`
	rec := doJSON(d.e, http.MethodPatch, "/api/v1/services/"+strconv.Itoa(svc.ID), body)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	scheds, rbs := decodeAssoc(t, rec)
	if !scheds[d.sched2] || scheds[d.sched1] || len(scheds) != 1 {
		t.Errorf("schedule_ids after replace: got %v, want {%d}", scheds, d.sched2)
	}
	if len(rbs) != 0 {
		t.Errorf("runbook_ids after clear: got %v, want {}", rbs)
	}
}

// TestUpdateNilLeavesAssociationsUntouched update 不带关联字段时不动已有关联。
func TestUpdateNilLeavesAssociationsUntouched(t *testing.T) {
	d := assocSetup(t)
	svc := d.c.Service.Create().SetName("svc").SetSlug("svc").
		AddScheduleIDs(d.sched1).AddRunbookIDs(d.rb1).SaveX(t.Context())
	// 仅改 name，不带 schedule_ids/runbook_ids。
	rec := doJSON(d.e, http.MethodPatch, "/api/v1/services/"+strconv.Itoa(svc.ID), `{"name":"renamed"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: got %d, want 200", rec.Code)
	}
	scheds, rbs := decodeAssoc(t, rec)
	if !scheds[d.sched1] || len(scheds) != 1 {
		t.Errorf("schedule_ids should be untouched: got %v, want {%d}", scheds, d.sched1)
	}
	if !rbs[d.rb1] || len(rbs) != 1 {
		t.Errorf("runbook_ids should be untouched: got %v, want {%d}", rbs, d.rb1)
	}
}
