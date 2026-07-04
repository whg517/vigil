// handler_rawevents_test.go RawEvent 查询/重放 + 回灌巡检测试（T5.5）。
package ingestion

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/rawevent"
	"github.com/kevin/vigil/internal/config"
	"github.com/kevin/vigil/internal/queue"

	"github.com/labstack/echo/v5"
	_ "github.com/mattn/go-sqlite3"
)

// rawEventTestEnv 构造：ent client + 一个接入点 + 若干 RawEvent（各状态）。
func rawEventTestEnv(t *testing.T, dsn string) (*ent.Client, int) {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:"+dsn+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	team, _ := c.Team.Create().SetName("t").SetSlug("t-" + dsn).Save(ctx)
	integ, _ := c.Integration.Create().
		SetName("api").SetType("webhook").SetToken("tok-" + dsn).
		SetTeamID(team.ID).Save(ctx)
	return c, integ.ID
}

func mkRaw(t *testing.T, c *ent.Client, integID int, status rawevent.Status, payload string) *ent.RawEvent {
	t.Helper()
	r, err := c.RawEvent.Create().
		SetPayload([]byte(payload)).
		SetStatus(status).
		SetIntegrationID(integID).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create raw: %v", err)
	}
	return r
}

func TestRawEventList_CountsAndFilter(t *testing.T) {
	c, integID := rawEventTestEnv(t, "raw_list")
	mkRaw(t, c, integID, rawevent.StatusReceived, `{}`)
	mkRaw(t, c, integID, rawevent.StatusParseFailed, `{bad`)
	mkRaw(t, c, integID, rawevent.StatusParseFailed, `{bad2`)
	mkRaw(t, c, integID, rawevent.StatusRequeued, `{}`)

	h := NewRawEventHandler(c, nil) // authz nil → 无隔离，查全部
	e := echo.New()
	e.GET("/raw-events", h.list)

	// 无过滤：counts 应含各状态分布
	req := httptest.NewRequest(http.MethodGet, "/raw-events", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp listResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Counts["parse_failed"] != 2 {
		t.Errorf("parse_failed count=%d, want 2", resp.Counts["parse_failed"])
	}
	if resp.Counts["requeued"] != 1 {
		t.Errorf("requeued count=%d, want 1", resp.Counts["requeued"])
	}
	if len(resp.Items) != 4 {
		t.Errorf("items=%d, want 4", len(resp.Items))
	}

	// status 过滤：只返 parse_failed 明细
	req2 := httptest.NewRequest(http.MethodGet, "/raw-events?status=parse_failed", nil)
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)
	var resp2 listResp
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp2)
	if len(resp2.Items) != 2 {
		t.Errorf("filtered items=%d, want 2", len(resp2.Items))
	}
}

func TestRawEventList_InvalidStatus400(t *testing.T) {
	c, _ := rawEventTestEnv(t, "raw_badstatus")
	h := NewRawEventHandler(c, nil)
	e := echo.New()
	e.GET("/raw-events", h.list)
	req := httptest.NewRequest(http.MethodGet, "/raw-events?status=bogus", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", rec.Code)
	}
}

// realQueue 构造一个连 miniredis 的真实 queue（供重放/回灌真入队）。
func realQueue(t *testing.T) *queue.Queue {
	t.Helper()
	mr := miniredis.RunT(t)
	q := queue.New(&config.Config{
		Redis: config.Redis{Addr: mr.Addr()},
		Asynq: config.Asynq{Concurrency: 1},
	})
	t.Cleanup(func() { _ = q.Close() })
	return q
}

func TestRawEventReplay_ResetsToReceivedAndEnqueues(t *testing.T) {
	c, integID := rawEventTestEnv(t, "raw_replay")
	raw := mkRaw(t, c, integID, rawevent.StatusParseFailed, `{"summary":"x"}`)

	ingest := NewHandler(c, realQueue(t))
	h := NewRawEventHandler(c, ingest)
	e := echo.New()
	e.POST("/raw-events/:id/replay", h.replay)

	req := httptest.NewRequest(http.MethodPost, "/raw-events/"+strconv.Itoa(raw.ID)+"/replay", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s, want 202", rec.Code, rec.Body.String())
	}
	// 重放后状态回 received（等 worker 消费），error 清空。
	got, _ := c.RawEvent.Get(context.Background(), raw.ID)
	if got.Status != rawevent.StatusReceived {
		t.Errorf("status=%s, want received", got.Status)
	}
	if got.Error != "" {
		t.Errorf("error=%q, want empty after replay", got.Error)
	}
}

func TestRawEventReplay_NotFound404(t *testing.T) {
	c, _ := rawEventTestEnv(t, "raw_replay_404")
	h := NewRawEventHandler(c, NewHandler(c, realQueue(t)))
	e := echo.New()
	e.POST("/raw-events/:id/replay", h.replay)
	req := httptest.NewRequest(http.MethodPost, "/raw-events/99999/replay", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d, want 404", rec.Code)
	}
}

// TestRequeueSweeper_ReenqueuesRequeued 验证巡检把 requeued 重置回 received 并入队。
func TestRequeueSweeper_ReenqueuesRequeued(t *testing.T) {
	c, integID := rawEventTestEnv(t, "sweep")
	ctx := context.Background()
	r1 := mkRaw(t, c, integID, rawevent.StatusRequeued, `{"summary":"a"}`)
	r2 := mkRaw(t, c, integID, rawevent.StatusRequeued, `{"summary":"b"}`)
	// normalized 的不应被回灌
	rOK := mkRaw(t, c, integID, rawevent.StatusNormalized, `{}`)

	ingest := NewHandler(c, realQueue(t))
	sw := NewRequeueSweeper(c, ingest, 10, time.Minute)
	n := sw.sweepOnce(ctx)
	if n != 2 {
		t.Fatalf("sweep re-enqueued=%d, want 2", n)
	}
	for _, id := range []int{r1.ID, r2.ID} {
		got, _ := c.RawEvent.Get(ctx, id)
		if got.Status != rawevent.StatusReceived {
			t.Errorf("raw %d status=%s, want received after sweep", id, got.Status)
		}
	}
	// normalized 的不受影响
	gotOK, _ := c.RawEvent.Get(ctx, rOK.ID)
	if gotOK.Status != rawevent.StatusNormalized {
		t.Errorf("normalized raw touched: status=%s", gotOK.Status)
	}
}
