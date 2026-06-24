// Package server 实现 HTTP 接入层（Echo）。
//
// 对应 architecture.md §接入层 + §6.3 可观测性：
// · REST API + WebSocket
// · /health 健康检查（依赖连通性）
// · /metrics Prometheus 指标（后续接入）
//
// 业务 handler 后续按能力域挂载到 group。
package server

import (
	"context"
	"net/http"
	"time"

	"github.com/kevin/vigil/internal/config"
	"github.com/kevin/vigil/internal/metrics"
	"github.com/kevin/vigil/internal/store"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Server HTTP 服务器，聚合接入层依赖。
type Server struct {
	echo   *echo.Echo
	cfg    *config.Config
	store  *store.Store
	v1     *echo.Group // /api/v1 业务路由组（需鉴权）
	public *echo.Group // /api/v1 公开路由组（webhook 接入/IM 回调，自带鉴权，不走 RBAC）

	// v5 优雅关闭：Start 创建独立 startCtx 并在返回时关闭 startDone；
	// Shutdown 取消 startCtx 触发框架内建关闭，并等待 startDone 确认 Serve 真正退出。
	startCtx    context.Context
	startCancel context.CancelFunc
	startDone   chan struct{}
}

// New 创建 Server 并注册基础路由（health 等）。
func New(cfg *config.Config, st *store.Store) *Server {
	e := echo.New()

	s := &Server{echo: e, cfg: cfg, store: st}

	// 中间件链（顺序：兜底类最外 → 观测类 → 业务类最内）。
	// Recover 最外：任何 handler panic 都能恢复，防进程崩溃（v5 不再默认注册）。
	e.Use(middleware.Recover())
	// RequestID：生成 X-Request-ID，串联错误日志/排障。
	e.Use(middleware.RequestID())
	// HTTP 指标采集（所有路由）。
	e.Use(metrics.EchoMiddleware())

	// 基础路由（无需鉴权）
	s.registerBase()
	s.registerOpenAPI() // OpenAPI spec + Swagger UI（/openapi.yaml, /docs）

	// API v1 路由组
	s.v1 = e.Group("/api/v1")     // 业务路由（鉴权中间件由 Wire 挂载）
	s.public = e.Group("/api/v1") // 公开路由（webhook 接入/IM 回调，用各自 token/签名鉴权）

	return s
}

// APIGroup 返回 /api/v1 业务路由组（需鉴权），供各能力域挂载业务路由。
func (s *Server) APIGroup() *echo.Group {
	return s.v1
}

// PublicGroup 返回 /api/v1 公开路由组（不走 RBAC，用于 webhook 接入、IM 回调等自带鉴权的入口）。
func (s *Server) PublicGroup() *echo.Group {
	return s.public
}

// registerBase 注册基础路由（健康检查、指标）。
func (s *Server) registerBase() {
	s.echo.GET("/health", s.health)
	s.echo.GET("/metrics", s.metrics) // Prometheus 指标端点
}

// health 健康检查：检查 PostgreSQL + Redis 连通性。
func (s *Server) health(c *echo.Context) error {
	ctx, cancel := context.WithTimeout(c.Request().Context(), 3*time.Second)
	defer cancel()

	status := http.StatusOK
	checks := map[string]string{}

	// 检查 Redis
	if err := s.store.Redis.Ping(ctx).Err(); err != nil {
		checks["redis"] = "down: " + err.Error()
		status = http.StatusServiceUnavailable
	} else {
		checks["redis"] = "up"
	}

	// PostgreSQL 连通性：轻量查询验证（运行时探活，非依赖初始化）。
	// DB 挂了健康检查必须能反映出来，供 K8s liveness/readiness 判断。
	if _, err := s.store.DB.User.Query().Limit(1).Count(ctx); err != nil {
		checks["postgres"] = "down: " + err.Error()
		status = http.StatusServiceUnavailable
	} else {
		checks["postgres"] = "up"
	}

	resp := map[string]any{
		"status":  http.StatusText(status),
		"checks":  checks,
		"version": "0.1.0",
	}
	return c.JSON(status, resp)
}

// metrics Prometheus 指标端点（Go runtime + 业务 + HTTP 指标）。
func (s *Server) metrics(c *echo.Context) error {
	promhttp.Handler().ServeHTTP(c.Response(), c.Request())
	return nil
}

// Start 启动 HTTP 服务（阻塞）。
//
// Echo v5 通过 StartConfig.Start(ctx, e) 内建优雅关闭：ctx 取消时框架自动等待
// GracefulTimeout 后关闭 listener。这里用一个独立于信号的 startCtx，让调用方
// （cmd/vigil）能在 Shutdown 中按"queue→webhook→http"的顺序主动触发关闭，而不是
// 收到信号立即停 http（破坏多组件有序关闭）。返回时关闭 startDone 通知 Shutdown。
func (s *Server) Start() error {
	s.startCtx, s.startCancel = context.WithCancel(context.Background())
	s.startDone = make(chan struct{})
	defer close(s.startDone)
	defer s.startCancel()

	sc := echo.StartConfig{
		Address:         s.cfg.HTTP.Addr,
		HideBanner:      true,
		HidePort:        true,
		GracefulTimeout: 10 * time.Second,
	}
	return sc.Start(s.startCtx, s.echo)
}

// Shutdown 优雅关闭：取消 Start 的生命周期 ctx，触发 v5 内建优雅关闭并等待 Serve 退出。
//
// 传入的 ctx 仅作为等待 Serve 退出的超时上限（超时返回其 Err，如 DeadlineExceeded）；
// 真正的关闭宽限由 StartConfig.GracefulTimeout（当前 10s）控制。
//
// 边界：若 Start 从未被调用（startDone==nil），本方法为安全 no-op——直接返回 nil，
// 既不等待也不消费 ctx。这使得构造后未启动的 Server 也能安全 Shutdown。
func (s *Server) Shutdown(ctx context.Context) error {
	if s.startCancel != nil {
		s.startCancel()
	}
	if s.startDone != nil {
		select {
		case <-s.startDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
