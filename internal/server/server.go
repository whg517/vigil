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

	"github.com/labstack/echo/v4"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Server HTTP 服务器，聚合接入层依赖。
type Server struct {
	echo   *echo.Echo
	cfg    *config.Config
	store  *store.Store
	v1     *echo.Group // /api/v1 业务路由组（需鉴权）
	public *echo.Group // /api/v1 公开路由组（webhook 接入/IM 回调，自带鉴权，不走 RBAC）
}

// New 创建 Server 并注册基础路由（health 等）。
func New(cfg *config.Config, st *store.Store) *Server {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

	s := &Server{echo: e, cfg: cfg, store: st}

	// 中间件：HTTP 指标采集（所有路由）
	e.Use(metrics.EchoMiddleware())

	// 基础路由（无需鉴权）
	s.registerBase()
	s.registerOpenAPI() // OpenAPI spec + Swagger UI（/openapi.yaml, /docs）

	// API v1 路由组
	s.v1 = e.Group("/api/v1")     // 业务路由（鉴权中间件由 main 挂载）
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
func (s *Server) health(c echo.Context) error {
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
func (s *Server) metrics(c echo.Context) error {
	promhttp.Handler().ServeHTTP(c.Response().Writer, c.Request())
	return nil
}

// Start 启动 HTTP 服务（阻塞）。
func (s *Server) Start() error {
	return s.echo.Start(s.cfg.HTTP.Addr)
}

// Shutdown 优雅关闭。
func (s *Server) Shutdown(ctx context.Context) error {
	return s.echo.Shutdown(ctx)
}
