// handler_test.go 出站 webhook 投递查询/重放端点测试（T5.2）。
package webhook

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/webhookdelivery"
	"github.com/kevin/vigil/internal/httputil"

	"github.com/labstack/echo/v5"
	_ "github.com/mattn/go-sqlite3"
)

func deliveryTestClient(t *testing.T, dsn string) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:"+dsn+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func mkDelivery(t *testing.T, c *ent.Client, url, event string, status webhookdelivery.Status, payload string) *ent.WebhookDelivery {
	t.Helper()
	d, err := c.WebhookDelivery.Create().
		SetURL(url).SetEvent(event).SetStatus(status).SetPayload([]byte(payload)).
		SetLastError("boom").SetLastStatusCode(500).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create delivery: %v", err)
	}
	return d
}

func TestWebhookDeliveryList_CountsAndFilter(t *testing.T) {
	c := deliveryTestClient(t, "wd_list")
	mkDelivery(t, c, "http://x", "incident.created", webhookdelivery.StatusFailed, `{}`)
	mkDelivery(t, c, "http://x", "incident.acked", webhookdelivery.StatusFailed, `{}`)
	mkDelivery(t, c, "http://x", "incident.resolved", webhookdelivery.StatusSuccess, `{}`)

	h := NewHandler(c, nil)
	e := echo.New()
	e.GET("/webhook-deliveries", h.list)

	// 无过滤：counts 分布
	req := httptest.NewRequest(http.MethodGet, "/webhook-deliveries", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp listResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Counts["failed"] != 2 || resp.Counts["success"] != 1 {
		t.Errorf("counts=%v", resp.Counts)
	}
	if len(resp.Items) != 3 {
		t.Errorf("items=%d, want 3", len(resp.Items))
	}
	// payload 不回显（view 无 payload 字段），但排障字段可见
	if resp.Items[0].LastError == "" || resp.Items[0].LastStatusCode == 0 {
		t.Error("死信应可见 last_error/last_status_code")
	}

	// status=failed 过滤：仅死信明细
	req = httptest.NewRequest(http.MethodGet, "/webhook-deliveries?status=failed", nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Items) != 2 {
		t.Errorf("failed items=%d, want 2", len(resp.Items))
	}

	// 非法 status → 400
	req = httptest.NewRequest(http.MethodGet, "/webhook-deliveries?status=bogus", nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("非法 status 应 400，实际 %d", rec.Code)
	}
}

func TestWebhookDeliveryReplay_Success(t *testing.T) {
	c := deliveryTestClient(t, "wd_replay_ok")
	// 目标端这次成功接住
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := mkDelivery(t, c, srv.URL, "incident.created", webhookdelivery.StatusFailed, `{"event":"incident.created"}`)

	h := NewHandler(c, NewDispatcher(nil))
	e := echo.New()
	e.POST("/webhook-deliveries/:id/replay", h.replay)

	req := httptest.NewRequest(http.MethodPost, "/webhook-deliveries/"+strconv.Itoa(d.ID)+"/replay", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("replay 应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	var ack httputil.AckResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &ack)
	if ack.Status != "replayed" || ack.ID != d.ID {
		t.Errorf("ack=%+v", ack)
	}
	// 记录应被回写为 success + attempts 累加 + 清错
	got, _ := c.WebhookDelivery.Get(context.Background(), d.ID)
	if got.Status != webhookdelivery.StatusSuccess {
		t.Errorf("重放成功后 status 应为 success，实际 %s", got.Status)
	}
	if got.Attempts != 1 {
		t.Errorf("attempts 应累加为 1，实际 %d", got.Attempts)
	}
	if got.LastError != "" {
		t.Errorf("成功后 last_error 应清空，实际 %q", got.LastError)
	}
}

func TestWebhookDeliveryReplay_StillFailing(t *testing.T) {
	c := deliveryTestClient(t, "wd_replay_fail")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway) // 仍失败
	}))
	defer srv.Close()

	d := mkDelivery(t, c, srv.URL, "incident.created", webhookdelivery.StatusFailed, `{}`)

	h := NewHandler(c, NewDispatcher(nil))
	e := echo.New()
	e.POST("/webhook-deliveries/:id/replay", h.replay)

	req := httptest.NewRequest(http.MethodPost, "/webhook-deliveries/"+strconv.Itoa(d.ID)+"/replay", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("重放仍失败应 502，实际 %d", rec.Code)
	}
	got, _ := c.WebhookDelivery.Get(context.Background(), d.ID)
	if got.Status != webhookdelivery.StatusFailed || got.Attempts != 1 {
		t.Errorf("失败后应仍 failed 且 attempts=1，实际 status=%s attempts=%d", got.Status, got.Attempts)
	}
	if got.LastStatusCode != http.StatusBadGateway {
		t.Errorf("last_status_code 应记 502，实际 %d", got.LastStatusCode)
	}
}

func TestWebhookDeliveryReplay_NotFound(t *testing.T) {
	c := deliveryTestClient(t, "wd_replay_404")
	h := NewHandler(c, NewDispatcher(nil))
	e := echo.New()
	e.POST("/webhook-deliveries/:id/replay", h.replay)

	req := httptest.NewRequest(http.MethodPost, "/webhook-deliveries/9999/replay", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("不存在应 404，实际 %d", rec.Code)
	}
}
