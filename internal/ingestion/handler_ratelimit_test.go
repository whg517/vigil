package ingestion

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/internal/middleware"
	"github.com/redis/go-redis/v9"

	"github.com/labstack/echo/v5"
	_ "github.com/mattn/go-sqlite3"
)

// newRateLimitTestHandler 构造带 miniredis 限流器的 handler + 一个有效 Integration(token=test-token)。
// dsn 用于隔离每个测试的内存库。返回 handler 和 ent client（供测试断言查询）。
func newRateLimitTestHandler(t *testing.T, dsn string, rateLimit int) (*Handler, *ent.Client) {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:"+dsn+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	team, _ := c.Team.Create().SetName("t").SetSlug("t1").Save(ctx)
	svc, _ := c.Service.Create().SetName("s").SetSlug("s1").SetTeamID(team.ID).Save(ctx)
	_, _ = c.Integration.Create().
		SetName("prom").SetType("prometheus").SetToken("test-token").
		SetTeamID(team.ID).SetServiceID(svc.ID).
		Save(ctx)

	h := NewHandler(c, nil) // queue=nil，测试不真正入队（handler 有 nil 守卫）
	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rc.Close() })
	h.SetLimiter(middleware.NewLimiter(rc), rateLimit)
	return h, c
}

func TestReceiveWebhook_RateLimitedReturns429(t *testing.T) {
	h, _ := newRateLimitTestHandler(t, "rl_429", 2) // 每分钟限 2 次
	e := echo.New()
	e.POST("/webhook/:token", h.receiveWebhook)

	body := []byte(`{"alerts":[{"status":"firing","labels":{"alertname":"x"}}]}`)

	// 前 2 次应 202（放行）
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/webhook/test-token", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Errorf("request %d: status %d, want 202", i+1, rec.Code)
		}
	}
	// 第 3 次应 429（超限）
	req := httptest.NewRequest(http.MethodPost, "/webhook/test-token", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("3rd request: status %d, want 429", rec.Code)
	}
}

func TestReceiveWebhook_RateLimitedPayloadStillPersisted(t *testing.T) {
	// 关键：限流时 payload 仍落 RawEvent（capabilities §3.3 不丢告警）
	h, c := newRateLimitTestHandler(t, "rl_persist", 1)
	e := echo.New()
	e.POST("/webhook/:token", h.receiveWebhook)
	body := []byte(`{"alerts":[{"status":"firing","labels":{"alertname":"x"}}]}`)

	// 第 1 次 202，第 2 次 429
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/webhook/test-token", bytes.NewReader(body)))
	req := httptest.NewRequest(http.MethodPost, "/webhook/test-token", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("2nd request status %d, want 429", rec.Code)
	}

	// 应有 2 条 RawEvent（两次都落库了，即使第 2 次被限流）
	cnt, err := c.RawEvent.Query().Count(context.Background())
	if err != nil {
		t.Fatalf("count raw events: %v", err)
	}
	if cnt != 2 {
		t.Errorf("raw event count=%d, want 2 (rate-limited payload must persist)", cnt)
	}
}

func TestReceiveWebhook_BackpressureReturns503(t *testing.T) {
	h, _ := newRateLimitTestHandler(t, "rl_bp", 100) // 限流宽松
	// 构造过载队列：塞 15 个任务到 asynq:critical，阈值 10
	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rc.Close() })
	ctx := context.Background()
	for i := 0; i < 15; i++ {
		if err := rc.LPush(ctx, "asynq:critical", "pending-task").Err(); err != nil {
			t.Fatalf("lpush: %v", err)
		}
	}
	h.SetBackpressureChecker(middleware.NewBackpressureChecker(rc, 10))

	e := echo.New()
	e.POST("/webhook/:token", h.receiveWebhook)
	req := httptest.NewRequest(http.MethodPost, "/webhook/test-token", bytes.NewReader([]byte(`{"alerts":[{"status":"firing"}]}`)))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("backpressure: status %d, want 503", rec.Code)
	}
}

func TestReceiveWebhook_NoLimiterAllowsAll(t *testing.T) {
	// 未注入 limiter（默认）→ 不限流，全放行
	c := enttest.Open(t, "sqlite3", "file:ingest_nolimit?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	team, _ := c.Team.Create().SetName("t").SetSlug("t2").Save(ctx)
	svc, _ := c.Service.Create().SetName("s").SetSlug("s2").SetTeamID(team.ID).Save(ctx)
	_, _ = c.Integration.Create().SetName("p").SetType("prometheus").SetToken("tk").SetTeamID(team.ID).SetServiceID(svc.ID).Save(ctx)

	h := NewHandler(c, nil)
	e := echo.New()
	e.POST("/webhook/:token", h.receiveWebhook)
	body := []byte(`{"alerts":[{"status":"firing","labels":{"alertname":"x"}}]}`)
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodPost, "/webhook/tk", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Errorf("request %d without limiter: status %d, want 202", i+1, rec.Code)
		}
	}
}
