// Command vigil 是 Vigil 告警处置平台的入口。
//
// 启动流程：load config → init logger → app.Bootstrap（装配 store/queue/各引擎/路由）→
//
//	start queue worker + http server → 等待退出信号 → 优雅关闭（queue→http→store）。
//
// 装配逻辑抽到 internal/app，供生产入口与进程内集成测试共用同一套装配；
// 本文件只负责生命周期编排（信号监听、后台服务启动、多组件有序关闭）。
//
// 优雅退出：捕获 SIGINT/SIGTERM，按序关闭 queue → server → store。
//
// OpenAPI 全局信息（swag v2 --v3.1 解析）。
// spec 由 handler 上的注解经 `go generate ./cmd/vigil/...` 生成到 internal/server/gen。
//
// @title          Vigil API
// @version        0.1.0
// @description    Vigil 告警处置平台 REST API。
// @description    认证：业务 API 要求 Bearer JWT（/auth/login 换取）。鉴权中间件另接受 X-Vigil-User-ID 头作为本地/回退身份（可伪造，仅限受信网络，生产禁用），该回退方案不在 securitySchemes 中声明。
// @license.name   MIT
// @servers.url    /api/v1
//
// @securitydefinitions.bearerauth bearerAuth
// @description    业务 API 推荐（唯一声明）方案：HTTP Bearer JWT（/auth/login 换取）。
// @bearerformat   JWT
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/internal/app"
	"github.com/kevin/vigil/internal/config"
	"github.com/kevin/vigil/internal/logger"
	"github.com/kevin/vigil/internal/migrate"

	_ "github.com/lib/pq" // 注册 postgres 驱动（ent dialect "postgres" 用）
	"go.uber.org/zap"
)

func main() {
	// 子命令分发：migrate 把 ent schema 应用到 PG（生产可换 atlas 版本化迁移）
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		if err := runMigrate(); err != nil {
			fmt.Fprintln(os.Stderr, "migrate failed:", err)
			os.Exit(1)
		}
		fmt.Println("migrate: schema applied")
		return
	}
	if err := run(); err != nil {
		// 用 os.Exit 而非 panic：避免栈追踪泄露到 stderr，行为可预期
		fmt.Fprintln(os.Stderr, "vigil run failed:", err)
		os.Exit(1)
	}
}

// runMigrate 执行版本化迁移到 PostgreSQL。
// 先应用 migrations/*.sql（版本追踪），再 ent auto-migrate 补充同步。
func runMigrate() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// 打开 sql.DB（原生 SQL，用于版本追踪表 + 迁移文件 apply）
	sqlDB, err := sql.Open("postgres", cfg.DB.DSN())
	if err != nil {
		return fmt.Errorf("open sql db: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()
	// 打开 ent.Client（schema 同步）
	entDB, err := ent.Open("postgres", cfg.DB.DSN())
	if err != nil {
		return fmt.Errorf("open ent db: %w", err)
	}
	defer func() { _ = entDB.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := migrate.Run(ctx, sqlDB, entDB); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

func run() error {
	// 1. 加载配置
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// 2. 初始化日志
	log, err := logger.New(cfg.App.Env, cfg.App.LogLevel)
	if err != nil {
		return err
	}
	defer func() { _ = log.Sync() }()

	log.Info("vigil starting",
		zap.String("env", cfg.App.Env),
		zap.String("addr", cfg.HTTP.Addr),
	)

	// 3. 捕获退出信号（装配期间持有的 ctx 生命周期需覆盖 App 全程）
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 4. 装配全部组件（store/queue/各引擎/HTTP 路由），不启动阻塞服务
	a, err := app.Bootstrap(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer func() { _ = a.Store.Close() }()
	defer func() { _ = a.Queue.Close() }()

	// 5. 启动 HTTP server 与 queue worker（各自 goroutine，致命错误汇总到 errCh）
	// errCh 收集 http server 与 queue server 的致命错误，任一出错即触发退出。
	// 容量 2：两个后台服务各可能上报一次，避免发送阻塞。
	errCh := make(chan error, 2)
	go func() {
		log.Info("http server listening", zap.String("addr", cfg.HTTP.Addr))
		if err := a.Server.Start(); err != nil && !errors.Is(err, context.Canceled) {
			errCh <- fmt.Errorf("http server: %w", err)
		}
	}()

	// queue.Start 非阻塞（asynq 内建 goroutine），但仍放 goroutine 保持与 http 启动对称、
	// 便于失败上报统一走 errCh。
	go func() {
		if err := a.Queue.Start(); err != nil {
			errCh <- fmt.Errorf("queue server: %w", err)
		}
	}()

	// 6. 等待退出信号或启动错误
	select {
	case <-ctx.Done():
		log.Info("shutdown signal received")
	case err := <-errCh:
		// http 或 queue 致命错误：记日志后走优雅关闭流程（不再 return err 直接退出，
		// 让 defer 关闭 store/queue，避免资源泄漏）
		log.Error("fatal server error, shutting down", zap.Error(err))
	}

	// 7. 优雅关闭：先停 queue（停止消费新任务），等待 webhook 出口在途推送，再停 http
	a.Queue.Shutdown()
	log.Info("queue stopped")

	// 等待 webhook 出口的异步推送完成（避免进程退出时丢失在途通知）
	if a.WebhookDispatcher.HasSubscriptions() {
		a.WebhookDispatcher.Close()
		log.Info("webhook out drained")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.Server.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown failed", zap.Error(err))
		return err
	}
	log.Info("vigil stopped")
	return nil
}
