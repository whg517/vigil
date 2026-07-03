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
