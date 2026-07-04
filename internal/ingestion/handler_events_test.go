// handler_events_test.go 开放 API 投递端点测试（T5.1）。
package ingestion

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
	"github.com/kevin/vigil/ent/event"

	"github.com/hibiken/asynq"
	"github.com/labstack/echo/v5"
	_ "github.com/mattn/go-sqlite3"
)

// eventsTestHandler 构造一个通用 JSON 接入点（type=webhook）+ handler（queue=nil，不真入队）。
func eventsTestHandler(t *testing.T, dsn string) (*Handler, *ent.Client, int) {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:"+dsn+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	team, _ := c.Team.Create().SetName("t").SetSlug("t-" + dsn).Save(ctx)
	svc, _ := c.Service.Create().SetName("s").SetSlug("s-" + dsn).SetTeamID(team.ID).Save(ctx)
	integ, _ := c.Integration.Create().
		SetName("api").SetType("webhook").SetToken("tok-" + dsn).
		SetTeamID(team.ID).SetServiceID(svc.ID).
		Save(ctx)
	// authz/scope 不注入 → checkIntegrationAccess 降级放行（专注测试投递语义）。
	h := NewHandler(c, nil)
	return h, c, integ.ID
}

func TestDeliverEvent_PersistsRawEventAnd202(t *testing.T) {
	h, c, integID := eventsTestHandler(t, "deliver_ok")
	e := echo.New()
	e.POST("/events", h.deliverEvent)

	body := []byte(`{"source_event_id":"evt-1","severity":"critical","summary":"disk full"}`)
	req := httptest.NewRequest(http.MethodPost, "/events?integration_id="+strconv.Itoa(integID), bytes.NewReader(body))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s, want 202", rec.Code, rec.Body.String())
	}
	// RawEvent 应落库（不丢），归属该接入点。
	n, err := c.RawEvent.Query().Count(context.Background())
	if err != nil {
		t.Fatalf("count raw: %v", err)
	}
	if n != 1 {
		t.Errorf("raw_event count=%d, want 1", n)
	}
}

func TestDeliverEvent_IntegrationIDFromBody(t *testing.T) {
	h, c, integID := eventsTestHandler(t, "deliver_body_id")
	e := echo.New()
	e.POST("/events", h.deliverEvent)

	// 不带 query，从 body 顶层 integration_id 解析。
	body := []byte(`{"integration_id":` + strconv.Itoa(integID) + `,"summary":"x"}`)
	req := httptest.NewRequest(http.MethodPost, "/events", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d, want 202", rec.Code)
	}
	if n, _ := c.RawEvent.Query().Count(context.Background()); n != 1 {
		t.Errorf("raw_event count=%d, want 1", n)
	}
}

func TestDeliverEvent_MissingIntegrationID(t *testing.T) {
	h, _, _ := eventsTestHandler(t, "deliver_no_id")
	e := echo.New()
	e.POST("/events", h.deliverEvent)

	req := httptest.NewRequest(http.MethodPost, "/events", bytes.NewReader([]byte(`{"summary":"x"}`)))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400 (missing integration_id)", rec.Code)
	}
}

func TestDeliverEvent_UnknownIntegration404(t *testing.T) {
	h, _, _ := eventsTestHandler(t, "deliver_404")
	e := echo.New()
	e.POST("/events", h.deliverEvent)

	req := httptest.NewRequest(http.MethodPost, "/events?integration_id=99999", bytes.NewReader([]byte(`{"summary":"x"}`)))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d, want 404", rec.Code)
	}
}

func TestDeliverEvent_DisabledIntegration401(t *testing.T) {
	h, c, integID := eventsTestHandler(t, "deliver_disabled")
	// 禁用接入点。
	_ = c.Integration.UpdateOneID(integID).SetEnabled(false).Exec(context.Background())
	e := echo.New()
	e.POST("/events", h.deliverEvent)

	req := httptest.NewRequest(http.MethodPost, "/events?integration_id="+strconv.Itoa(integID), bytes.NewReader([]byte(`{"summary":"x"}`)))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status=%d, want 401 (disabled)", rec.Code)
	}
}

// TestDeliverEvent_Idempotent 验证开放 API 走归一化后，重复 source_event_id 不产新 Event。
// 端到端：投递 → RawEvent → 直接跑 NormalizeWorker（模拟 worker 消费）两次同一 payload。
func TestDeliverEvent_Idempotent(t *testing.T) {
	h, c, integID := eventsTestHandler(t, "deliver_idem")
	ctx := context.Background()
	e := echo.New()
	e.POST("/events", h.deliverEvent)

	body := []byte(`{"source_event_id":"dup-1","severity":"critical","status":"firing","summary":"same alert"}`)

	deliver := func() int {
		req := httptest.NewRequest(http.MethodPost, "/events?integration_id="+strconv.Itoa(integID), bytes.NewReader(body))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("deliver status=%d", rec.Code)
		}
		var ack struct {
			RawEventID int `json:"raw_event_id"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &ack)
		return ack.RawEventID
	}

	// worker 复用生产同一 registry（通用 JSON 适配器 type=webhook）。
	worker := NewNormalizeWorker(c, NewAdapterRegistry(), nil)
	normalize := func(rawID int) {
		p, _ := json.Marshal(normalizePayload{RawEventID: rawID, IntegrationID: integID, SourceType: "webhook"})
		if err := worker.Handle(ctx, asynq.NewTask(TaskNormalize, p)); err != nil {
			t.Fatalf("normalize: %v", err)
		}
	}

	raw1 := deliver()
	normalize(raw1)
	raw2 := deliver()
	normalize(raw2)

	// 两次投递、两条 RawEvent，但归一化后 Event 只有 1 条（幂等：同 source+source_event_id+status）。
	rawN, _ := c.RawEvent.Query().Count(ctx)
	if rawN != 2 {
		t.Errorf("raw_event count=%d, want 2 (每次投递各落一条)", rawN)
	}
	evtN, _ := c.Event.Query().Where(event.SourceEventIDEQ("dup-1")).Count(ctx)
	if evtN != 1 {
		t.Errorf("event count=%d, want 1 (幂等去重)", evtN)
	}
}
