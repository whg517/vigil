// handler_test.go 排班 handler 测试，重点验证 FIX-D（create 建 Rotation 后 oncall 非空）。
package schedule

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"

	"github.com/labstack/echo/v5"
	_ "github.com/mattn/go-sqlite3"
)

// newHandlerTestClient 独立内存库。
func newHandlerTestClient(t *testing.T) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:sched_handler_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestHandler_CreateBuildsRotation FIX-D：POST /schedules 含 participants 的 layer，
// 应建 Rotation 实体，使后续 GET /oncall 返回非空 Layers。
func TestHandler_CreateBuildsRotation(t *testing.T) {
	c := newHandlerTestClient(t)
	ctx := context.Background()
	u1, _ := c.User.Create().SetUsername("u1").SetEmail("u1@x.com").Save(ctx)
	u2, _ := c.User.Create().SetUsername("u2").SetEmail("u2@x.com").Save(ctx)

	h := NewHandler(NewEngine(c, nil), c)
	e := echo.New()
	v1 := e.Group("/api/v1")
	h.Register(v1)

	body := map[string]any{
		"name": "支付值班", "type": "rotation", "timezone": "UTC",
		"layers": []map[string]any{
			{
				"name": "一线", "priority": 0,
				"participants":  []int{u1.ID, u2.ID},
				"rotation_type": "daily",
				"shift_length":  "24h",
				"handoff_time":  "00:00",
				"start_date":    "2026-06-01T00:00:00Z",
			},
		},
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/schedules", bytes.NewReader(bodyBytes))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create schedule: got %d, body=%s", rec.Code, rec.Body.String())
	}
	var sched ent.Schedule
	if err := json.Unmarshal(rec.Body.Bytes(), &sched); err != nil {
		t.Fatalf("decode schedule: %v", err)
	}

	// 验证 Rotation 实体已创建
	rotCnt, _ := c.Rotation.Query().Count(ctx)
	if rotCnt == 0 {
		t.Error("FIX-D: create schedule should build Rotation entity, got 0")
	}

	// GET oncall 应返回非空 Layers + 在班人
	oncReq := httptest.NewRequest(http.MethodGet, "/api/v1/schedules/"+strconv.Itoa(sched.ID)+"/oncall", nil)
	oncRec := httptest.NewRecorder()
	e.ServeHTTP(oncRec, oncReq)
	if oncRec.Code != http.StatusOK {
		t.Fatalf("oncall: got %d, body=%s", oncRec.Code, oncRec.Body.String())
	}
	oncDetail := struct {
		Layers []struct {
			Users []struct{ ID int } `json:"Users"`
		} `json:"Layers"`
	}{}
	if err := json.Unmarshal(oncRec.Body.Bytes(), &oncDetail); err != nil {
		t.Fatalf("decode oncall: %v", err)
	}
	if len(oncDetail.Layers) == 0 {
		t.Error("FIX-D: oncall should return non-empty Layers after create with participants")
	}
	totalUsers := 0
	for _, l := range oncDetail.Layers {
		totalUsers += len(l.Users)
	}
	if totalUsers == 0 {
		t.Error("oncall layers should contain on-call users")
	}
}

// seedHandlerSchedule 建一个含单层轮换的 Schedule（u1/u2），返回 schedule 与两个 user。
func seedHandlerSchedule(t *testing.T, c *ent.Client) (*ent.Schedule, *ent.User, *ent.User) {
	t.Helper()
	ctx := context.Background()
	u1, _ := c.User.Create().SetUsername("h1").SetEmail("h1@x.com").Save(ctx)
	u2, _ := c.User.Create().SetUsername("h2").SetEmail("h2@x.com").Save(ctx)
	start := mustTime(t, "2026-06-01T00:00:00Z")
	rot, _ := c.Rotation.Create().SetName("一线").SetShiftLength("24h").
		SetHandoffTime("00:00").SetRotationType("daily").SetStartDate(start).
		AddParticipantIDs(u1.ID, u2.ID).Save(ctx)
	sched, _ := c.Schedule.Create().SetName("值班").SetType("rotation").
		SetTimezone("UTC").AddRotationIDs(rot.ID).Save(ctx)
	return sched, u1, u2
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return v
}

// TestHandler_OverrideLifecycle C5/M8：创建换班 → oncall 反映 override=true → 列表 → 删除。
func TestHandler_OverrideLifecycle(t *testing.T) {
	c := newHandlerTestClient(t)
	ctx := context.Background()
	sched, _, _ := seedHandlerSchedule(t, c)
	u3, _ := c.User.Create().SetUsername("h3").SetEmail("h3@x.com").SetName("替班").Save(ctx)

	h := NewHandler(NewEngine(c, nil), c)
	e := echo.New()
	h.Register(e.Group("/api/v1"))
	base := "/api/v1/schedules/" + strconv.Itoa(sched.ID) + "/overrides"

	// 创建：6/1 全天换给 u3。
	body := map[string]any{
		"user_id": u3.ID, "reason": "请假",
		"start_time": "2026-06-01T00:00:00Z", "end_time": "2026-06-02T00:00:00Z",
	}
	bb, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, base, bytes.NewReader(bb))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create override: got %d, body=%s", rec.Code, rec.Body.String())
	}
	var created overrideView
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode override: %v", err)
	}
	if created.UserID != u3.ID || created.ScheduleID != sched.ID {
		t.Errorf("override view: got user=%d sched=%d, want %d/%d", created.UserID, created.ScheduleID, u3.ID, sched.ID)
	}

	// oncall 时段内：u3 顶替，override=true。
	oncReq := httptest.NewRequest(http.MethodGet, "/api/v1/schedules/"+strconv.Itoa(sched.ID)+"/oncall?time=2026-06-01T12:00:00Z", nil)
	oncRec := httptest.NewRecorder()
	e.ServeHTTP(oncRec, oncReq)
	if oncRec.Code != http.StatusOK {
		t.Fatalf("oncall: got %d", oncRec.Code)
	}
	var onc OncallResult
	if err := json.Unmarshal(oncRec.Body.Bytes(), &onc); err != nil {
		t.Fatalf("decode oncall: %v", err)
	}
	if len(onc.Layers) == 0 || len(onc.Layers[0].Users) == 0 || !onc.Layers[0].Users[0].Override {
		t.Fatalf("oncall should reflect override=true, got %+v", onc.Layers)
	}

	// list：应含一条。
	listReq := httptest.NewRequest(http.MethodGet, base, nil)
	listRec := httptest.NewRecorder()
	e.ServeHTTP(listRec, listReq)
	var list []overrideView
	if err := json.Unmarshal(listRec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list overrides: got %d, want 1", len(list))
	}

	// delete：204，之后 list 为空。
	delReq := httptest.NewRequest(http.MethodDelete, base+"/"+strconv.Itoa(created.ID), nil)
	delRec := httptest.NewRecorder()
	e.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("delete override: got %d, body=%s", delRec.Code, delRec.Body.String())
	}
	cnt, _ := c.Override.Query().Count(ctx)
	if cnt != 0 {
		t.Errorf("after delete: override count %d, want 0", cnt)
	}
}

// TestHandler_OverrideValidation C5：缺时段/时段颠倒/缺 user_id 返 400。
func TestHandler_OverrideValidation(t *testing.T) {
	c := newHandlerTestClient(t)
	sched, _, _ := seedHandlerSchedule(t, c)
	h := NewHandler(NewEngine(c, nil), c)
	e := echo.New()
	h.Register(e.Group("/api/v1"))
	base := "/api/v1/schedules/" + strconv.Itoa(sched.ID) + "/overrides"

	cases := []string{
		`{"user_id":0,"start_time":"2026-06-01T00:00:00Z","end_time":"2026-06-02T00:00:00Z"}`, // 缺 user_id
		`{"user_id":1}`, // 缺时段
		`{"user_id":1,"start_time":"2026-06-02T00:00:00Z","end_time":"2026-06-01T00:00:00Z"}`, // 时段颠倒
	}
	for i, body := range cases {
		req := httptest.NewRequest(http.MethodPost, base, bytes.NewReader([]byte(body)))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("case %d: got %d, want 400 (body=%s)", i, rec.Code, rec.Body.String())
		}
	}
}

// TestHandler_PatchRebuildsRotation B21：PATCH layers 重建 Rotation（改参与人无需删重建）。
func TestHandler_PatchRebuildsRotation(t *testing.T) {
	c := newHandlerTestClient(t)
	ctx := context.Background()
	sched, _, _ := seedHandlerSchedule(t, c)
	u3, _ := c.User.Create().SetUsername("hp3").SetEmail("hp3@x.com").Save(ctx)

	oldRotIDs, _ := sched.QueryRotations().IDs(ctx)

	h := NewHandler(NewEngine(c, nil), c)
	e := echo.New()
	h.Register(e.Group("/api/v1"))

	// PATCH：把参与人改成只有 u3。
	body := map[string]any{
		"layers": []map[string]any{
			{
				"name": "一线", "priority": 0,
				"participants": []int{u3.ID}, "rotation_type": "daily",
				"shift_length": "24h", "handoff_time": "00:00",
				"start_date": "2026-06-01T00:00:00Z",
			},
		},
	}
	bb, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/schedules/"+strconv.Itoa(sched.ID), bytes.NewReader(bb))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: got %d, body=%s", rec.Code, rec.Body.String())
	}

	// 旧 Rotation 应已删除，新 Rotation 只含 u3。
	for _, oldID := range oldRotIDs {
		if _, err := c.Rotation.Get(ctx, oldID); !ent.IsNotFound(err) {
			t.Errorf("old rotation %d should be deleted after PATCH (err=%v)", oldID, err)
		}
	}
	eng := NewEngine(c, nil)
	res, err := eng.Oncall(ctx, sched.ID, mustTime(t, "2026-06-05T12:00:00Z"))
	if err != nil {
		t.Fatalf("oncall: %v", err)
	}
	if len(res.Layers) == 0 || len(res.Layers[0].Users) == 0 || res.Layers[0].Users[0].ID != u3.ID {
		t.Errorf("after PATCH oncall should resolve u3, got %+v", res.Layers)
	}
}

// TestHandler_CreateNoParticipants FIX-D：layer 无 participants 时不建 Rotation（不报错）。
func TestHandler_CreateNoParticipants(t *testing.T) {
	c := newHandlerTestClient(t)
	h := NewHandler(NewEngine(c, nil), c)
	e := echo.New()
	h.Register(e.Group("/api/v1"))

	body := `{"name":"空排班","type":"rotation","layers":[{"name":"L0","priority":0}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/schedules", bytes.NewReader([]byte(body)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d, body=%s", rec.Code, rec.Body.String())
	}
	// 无 participants 的 layer 不建 Rotation
	rotCnt, _ := c.Rotation.Query().Count(context.Background())
	if rotCnt != 0 {
		t.Errorf("layer without participants should not create Rotation, got %d", rotCnt)
	}
}
