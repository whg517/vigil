//go:build integration

package e2e

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/internal/app"
	"github.com/kevin/vigil/internal/config"
	"github.com/kevin/vigil/internal/migrate"

	_ "github.com/lib/pq" // 注册 postgres 驱动（ent dialect "postgres" 与 raw sql.DB 都依赖）
	"go.uber.org/zap"
)

// Env 持有一个进程内启动的完整 Vigil 实例，供集成测试驱动。
//
// 通过 Setup 创建：覆盖配置（随机端口 + 强制鉴权）→ migrate 建表 →
// app.Bootstrap 装配 → 启动 HTTP server + Asynq worker。
// 测试通过 BaseURL() 发 HTTP 请求，或 DB()/App() 直接断言库状态。
type Env struct {
	App     *app.App
	baseURL string
}

// Setup 启动一个完整的进程内 Vigil 实例并返回 Env。
//
// 流程：
//  1. 预分配一个空闲 TCP 端口（先 listen 拿端口，关闭后立即交给 server 用，
//     测试环境竞态概率极低）。
//  2. 用 t.Setenv 覆盖关键配置（随机端口、强制鉴权、JWT secret、固定 PG/Redis）。
//  3. app.Bootstrap 装配（含 store.New 的 PG/Redis ping；连不上 → t.Skip）。
//  4. migrate.Run 幂等建表（含 pgvector 扩展）。
//  5. 启动 queue worker（非阻塞）+ http server（goroutine）。
//  6. 轮询 /health 直到就绪。
//  7. t.Cleanup 注册优雅关闭（queue→http→store）。
func Setup(t *testing.T) *Env {
	t.Helper()

	// 1. 预分配空闲端口（:0 让 OS 分配 → 立即关闭交还给 server 用）
	addr, baseURL := allocateAddr(t)

	// 2. 覆盖配置（t.Setenv 会在测试结束后自动还原）
	// 连固定端口：由 docker-compose（本地）或 GitHub service container（CI）起的 PG/Redis。
	t.Setenv("VIGIL_DB_HOST", "localhost")
	t.Setenv("VIGIL_DB_PORT", "5432")
	t.Setenv("VIGIL_DB_USER", "vigil")
	t.Setenv("VIGIL_DB_PASSWORD", "vigil")
	t.Setenv("VIGIL_DB_NAME", "vigil")
	t.Setenv("VIGIL_DB_SSL_MODE", "disable")
	t.Setenv("VIGIL_REDIS_ADDR", "localhost:6379")
	t.Setenv("VIGIL_REDIS_DB", "0")
	t.Setenv("VIGIL_HTTP_ADDR", addr) // 预分配的端口
	// 强制鉴权：e2e 要验证 RBAC 与鉴权三轨，必须开启。
	t.Setenv("VIGIL_AUTH_ENABLED", "true")
	t.Setenv("VIGIL_AUTH_JWT_SECRET", "e2e-test-jwt-secret")
	t.Setenv("VIGIL_APP_ENV", "development")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	// 3. 装配（贯穿测试生命周期的 ctx，取消时触发装配期闭包回收）
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	log, _ := zap.NewDevelopment()

	a, err := app.Bootstrap(ctx, cfg, log)
	if err != nil {
		// 连不上 PG/Redis 是预期场景（依赖未起），跳过而非失败。
		if isConnErr(err) {
			t.Skipf("e2e: dependencies not available (run 'make dev-up'): %v", err)
		}
		t.Fatalf("bootstrap: %v", err)
	}

	// 4. 建表（幂等：已迁移会跳过；pre_0001_pgvector.sql 建 vector 扩展）
	if err := migrate.Run(ctx, a.Store.SQL, a.Store.DB); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// 4.1 清空 Redis：保证 dedup key / 聚合器 / asynq 残留任务不污染本测试。
	// 必须在 worker 启动前做（避免清掉运行中的任务）。
	if err := a.Store.Redis.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("flush redis: %v", err)
	}

	// 5. 启动 queue worker（非阻塞，asynq 内建 goroutine）
	if err := a.Queue.Start(); err != nil {
		t.Fatalf("start queue: %v", err)
	}

	// 6. 启动 http server（goroutine）
	serveErr := make(chan error, 1)
	go func() {
		if err := a.Server.Start(); err != nil {
			serveErr <- err
		}
	}()

	// 7. 轮询 /health 就绪
	env := &Env{App: a, baseURL: baseURL}
	env.waitHealthy(ctx, t)

	// 8. 优雅关闭（顺序与生产一致：queue → http → store）
	t.Cleanup(func() {
		cancel() // 取消装配期 ctx（回收 resolveEmails 等闭包持有的引用）
		a.Queue.Shutdown()
		shutdownCtx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		_ = a.Server.Shutdown(shutdownCtx)
		_ = a.Store.Close()
		_ = a.Queue.Close()
		select {
		case <-serveErr: // server 正常退出
		default:
		}
	})

	return env
}

// allocateAddr 预分配一个空闲 TCP 端口，返回 (监听地址, baseURL)。
// 通过先 listen 拿到 OS 分配的端口再立即关闭，交还给后续 server 使用。
// 测试环境竞态概率极低；若偶发端口被占，重新跑 Setup 即可。
func allocateAddr(t *testing.T) (addr, baseURL string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate port: %v", err)
	}
	addr = ln.Addr().String()
	_ = ln.Close()
	return addr, "http://" + addr
}

// BaseURL 返回实例的 API 基地址（如 http://127.0.0.1:xxxxx），不带 /api/v1 后缀。
func (e *Env) BaseURL() string { return e.baseURL }

// APIURL 返回完整的业务 API URL（拼接 /api/v1 + path）。
func (e *Env) APIURL(path string) string {
	return e.baseURL + "/api/v1" + path
}

// DB 返回 ent 客户端，供测试直接查库断言（流水线副作用、状态机等）。
func (e *Env) DB() *ent.Client { return e.App.Store.DB }

// waitHealthy 轮询 /health 直到返回 200 或超时（10s）。
func (e *Env) waitHealthy(ctx context.Context, t *testing.T) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(e.baseURL + "/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("health check cancelled: %v", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatalf("health check timed out (server not ready in 10s)")
}

// isConnErr 判断错误是否为依赖连接失败（PG/Redis 未起）。
// bootstrap 内 store.New 会包装 "ping postgres"/"ping redis" 前缀。
func isConnErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, sub := range []string{
		"ping postgres", "ping redis",
		"connection refused", "no such host",
		"dial tcp", "connect: connection",
	} {
		if strings.Contains(msg, sub) {
			return true
		}
	}
	return false
}
