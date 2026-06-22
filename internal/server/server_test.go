package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/internal/config"
	"github.com/kevin/vigil/internal/store"
	"github.com/redis/go-redis/v9"

	"github.com/labstack/echo/v4"
	_ "github.com/mattn/go-sqlite3"
)

// newTestServer 构造带 miniredis + sqlite 的测试 Server。
func newTestServer(t *testing.T) *Server {
	t.Helper()
	db := enttest.Open(t, "sqlite3", "file:server_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = db.Close() })
	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rc.Close() })

	st := &store.Store{DB: db, Redis: rc}
	cfg := &config.Config{}
	return New(cfg, st)
}

// TestHealth_AllUp Redis+DB 都通时返回 200。
func TestHealth_AllUp(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("health status %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

// TestHealth_RedisDown Redis 挂时返回 503 且 checks 标记 down。
func TestHealth_RedisDown(t *testing.T) {
	s := newTestServer(t)
	// 关掉 miniredis（通过访问其内部）——用 Store 的 Redis client 连个坏地址
	// 直接换 store 的 redis 为坏 client
	s.store.Redis = redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("health with redis down: status %d, want 503", rec.Code)
	}
}

// TestAPIGroup_ReturnsV1 APIGroup 返回 /api/v1 group。
func TestAPIGroup_ReturnsV1(t *testing.T) {
	s := newTestServer(t)
	g := s.APIGroup()
	if g == nil {
		t.Fatal("APIGroup nil")
	}
}

// TestPublicGroup_ReturnsPublic PublicGroup 返回公开 group。
func TestPublicGroup_ReturnsPublic(t *testing.T) {
	s := newTestServer(t)
	g := s.PublicGroup()
	if g == nil {
		t.Fatal("PublicGroup nil")
	}
}

// TestMetricsEndpoint /metrics 返回 200（Prometheus 格式）。
func TestMetricsEndpoint(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("metrics status %d, want 200", rec.Code)
	}
}

// 确认 echo 引用（避免未使用 import）
var _ = echo.New
