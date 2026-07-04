package server

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/alicebob/miniredis/v2"
	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/internal/config"
	"github.com/kevin/vigil/internal/store"
	"github.com/redis/go-redis/v9"

	"github.com/labstack/echo/v5"
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

// TestHealth_SchemaNotMigrated schema 未迁移（核心表缺失）时返回 503。
// 用未跑 migrate 的裸 sqlite（不经 enttest 建表），核心表 users 不存在，
// 探针应把 schema check 置 down 并整体返回 503——未 migrate 实例不应被判就绪。
func TestHealth_SchemaNotMigrated(t *testing.T) {
	rawDB, err := sql.Open("sqlite3", "file:server_test_nomigrate?mode=memory&cache=shared&_fk=1")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = rawDB.Close() })
	// 用裸 *sql.DB 构造 ent client，但不建表（不调 Schema.Create）。
	drv := entsql.OpenDB(dialect.SQLite, rawDB)
	db := ent.NewClient(ent.Driver(drv))
	t.Cleanup(func() { _ = db.Close() })

	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rc.Close() })

	st := &store.Store{DB: db, Redis: rc}
	s := New(&config.Config{}, st)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("health with schema not migrated: status %d, want 503; body=%s", rec.Code, rec.Body.String())
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

// newLifecycleServer 构造绑定到固定测试端口的 Server，用于 Start/Shutdown 生命周期测试。
// 用固定端口（而非 :0）是因为 Echo v5 不暴露实际监听地址，固定端口便于轮询连接就绪。
func newLifecycleServer(t *testing.T, addr string) *Server {
	t.Helper()
	db := enttest.Open(t, "sqlite3", "file:lc_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = db.Close() })
	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rc.Close() })

	st := &store.Store{DB: db, Redis: rc}
	cfg := &config.Config{}
	cfg.HTTP.Addr = addr
	return New(cfg, st)
}

// waitForListen 轮询 dial addr 直到 TCP 连接成功（Server 真正开始接受连接）。
// 用于桥接「Start 是异步阻塞」与「测试要等 Serve 就绪」的鸿沟。
func waitForListen(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server 未在 %v 内在 %s 开始监听", timeout, addr)
}

// TestStartShutdown_Graceful 验证 v5 迁移的核心：
// Start 用独立 startCtx 启动 StartConfig.Start，Shutdown 取消它后框架内建优雅关闭，
// Start goroutine 正常返回 nil。覆盖「正常关闭」路径。
func TestStartShutdown_Graceful(t *testing.T) {
	// 选一个不太可能冲突的端口；测试间串行执行，冲突风险低。
	addr := "127.0.0.1:38765"
	s := newLifecycleServer(t, addr)

	startErr := make(chan error, 1)
	go func() { startErr <- s.Start() }()

	waitForListen(t, addr, 3*time.Second)

	// 发起一次请求确认 Serve 真的工作。
	resp, err := http.Get(fmt.Sprintf("http://%s/health", addr))
	if err != nil {
		t.Fatalf("请求 /health 失败（Serve 未就绪？）: %v", err)
	}
	_ = resp.Body.Close()

	// 触发优雅关闭。
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown 返回错误: %v", err)
	}

	// Start 应在 Shutdown 后返回 nil（v5 过滤了 http.ErrServerClosed）。
	select {
	case err := <-startErr:
		if err != nil {
			t.Errorf("Start 在 Shutdown 后应返回 nil，got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("Start goroutine 未在 Shutdown 后 3s 内退出（startDone 信号失效？）")
	}
}

// TestShutdown_WithoutStart 验证 Start 未调用时 Shutdown 是安全 no-op。
// 覆盖 m3 的边界：startDone==nil 时不应 panic、不应卡在 select。
func TestShutdown_WithoutStart(t *testing.T) {
	s := newLifecycleServer(t, "127.0.0.1:0")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Errorf("未 Start 时 Shutdown 应返回 nil，got %v", err)
	}
}

// 确认 echo 引用（避免未使用 import）
var _ = echo.New
