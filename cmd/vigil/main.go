// Command vigil 是 Vigil 告警处置平台的入口。
//
// 启动流程：load config → init logger → 构造叶子依赖（store/queue/bus）→
//
//	server.Wire（装配各引擎/handler + 注册路由）→ start queue worker + http server →
//	等待退出信号 → 优雅关闭（queue→webhook drain→http→store）。
//
// 装配逻辑收敛在 internal/server.Wire，供生产入口与进程内集成测试共用同一套装配；
// 本文件只负责叶子构造、生命周期编排（信号监听、后台服务启动、多组件有序关闭）。
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
	"bufio"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/internal/config"
	domainevent "github.com/kevin/vigil/internal/event"
	"github.com/kevin/vigil/internal/logger"
	"github.com/kevin/vigil/internal/migrate"
	"github.com/kevin/vigil/internal/queue"
	"github.com/kevin/vigil/internal/server"
	"github.com/kevin/vigil/internal/store"

	_ "github.com/lib/pq" // 注册 postgres 驱动（ent dialect "postgres" 用）
	"go.uber.org/zap"
)

func main() {
	// 子命令分发：migrate 把 ent schema 应用到 PG（生产可换 atlas 版本化迁移）
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		if err := runMigrateCmd(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "migrate failed:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		// 用 os.Exit 而非 panic：避免栈追踪泄露到 stderr，行为可预期
		fmt.Fprintln(os.Stderr, "vigil run failed:", err)
		os.Exit(1)
	}
}

// runMigrateCmd 分发 migrate 子命令：
//
//	vigil migrate            应用未应用的版本化迁移 + ent auto-migrate（前进）
//	vigil migrate status     展示迁移版本状态（已应用/当前/待应用），只读
//	vigil migrate down ...   逆向【有 down 脚本的版本化 SQL 迁移】（不逆向 ent 结构变更）
func runMigrateCmd(args []string) error {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "status":
		return runMigrateStatus()
	case "down":
		return runMigrateDown(args[1:])
	case "", "up":
		if err := runMigrate(); err != nil {
			return err
		}
		fmt.Println("migrate: schema applied")
		return nil
	default:
		return fmt.Errorf("未知子命令 %q（可用: <空>|up|status|down）", sub)
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

// openSQLDB 打开原生 sql.DB（status/down 只需版本追踪表，无需 ent.Client）。
func openSQLDB() (*sql.DB, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	sqlDB, err := sql.Open("postgres", cfg.DB.DSN())
	if err != nil {
		return nil, fmt.Errorf("open sql db: %w", err)
	}
	return sqlDB, nil
}

// runMigrateStatus 打印迁移版本状态（只读）。
func runMigrateStatus() error {
	sqlDB, err := openSQLDB()
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	report, err := migrate.Status(ctx, sqlDB)
	if err != nil {
		return err
	}
	return report.Render(os.Stdout)
}

// runMigrateDown 逆向【有 down 脚本的版本化 SQL 迁移】。
//
//	--to <version>  逆向所有晚于该版本的已应用版本（保留该版本）；缺省=只回滚最近 1 个
//	--dry-run       只打印将执行什么，不落库
//	--force         跳过破坏性步骤的交互确认（自动化）
func runMigrateDown(args []string) error {
	fs := flag.NewFlagSet("migrate down", flag.ContinueOnError)
	to := fs.String("to", "", "逆向到指定版本（保留该版本及更早）；缺省只回滚最近一个已应用版本")
	dryRun := fs.Bool("dry-run", false, "只打印将执行什么，不落库")
	force := fs.Bool("force", false, "跳过破坏性步骤的交互确认")
	if err := fs.Parse(args); err != nil {
		return err
	}

	sqlDB, err := openSQLDB()
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 交互确认：从 stdin 读一行，等于 "yes"（忽略大小写与空白）才放行。
	confirm := func(prompt string) bool {
		fmt.Print(prompt)
		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		return strings.EqualFold(strings.TrimSpace(line), "yes")
	}

	return migrate.Down(ctx, sqlDB, migrate.DownOptions{
		To:     *to,
		DryRun: *dryRun,
		Force:  *force,
	}, os.Stdout, confirm)
}

func run() error {
	// 1. 加载配置 + 日志
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log, err := logger.New(cfg.App.Env, cfg.App.LogLevel)
	if err != nil {
		return err
	}
	defer func() { _ = log.Sync() }()

	log.Info("vigil starting",
		zap.String("env", cfg.App.Env),
		zap.String("addr", cfg.HTTP.Addr),
	)

	// 2. 捕获退出信号（ctx 生命周期覆盖 App 全程）
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 3. 构造叶子依赖（store/queue/bus），交给 server.Wire 装配全部组件 + 路由。
	st, err := store.New(ctx, cfg)
	if err != nil {
		log.Error("open store failed", zap.Error(err))
		return err
	}
	defer func() { _ = st.Close() }()
	log.Info("store ready (postgres + redis)")

	q := queue.New(cfg)
	defer func() { _ = q.Close() }()
	log.Info("queue ready (asynq)")

	bus := domainevent.New()

	wired, err := server.Wire(ctx, cfg, log, st, q, bus)
	if err != nil {
		return err
	}

	// 4. 启动 HTTP server 与 queue worker（各自 goroutine，致命错误汇总到 errCh）
	errCh := make(chan error, 2)
	go func() {
		log.Info("http server listening", zap.String("addr", cfg.HTTP.Addr))
		if err := wired.Server.Start(); err != nil && !errors.Is(err, context.Canceled) {
			errCh <- fmt.Errorf("http server: %w", err)
		}
	}()
	go func() {
		if err := q.Start(); err != nil {
			errCh <- fmt.Errorf("queue server: %w", err)
		}
	}()

	// 5. 等待退出信号或启动错误
	select {
	case <-ctx.Done():
		log.Info("shutdown signal received")
	case err := <-errCh:
		// http 或 queue 致命错误：记日志后走优雅关闭流程（让 defer 关闭 store/queue）
		log.Error("fatal server error, shutting down", zap.Error(err))
	}

	// 6. 优雅关闭：先停 queue（停止消费新任务），drain webhook 出口在途推送，再停 http
	q.Shutdown()
	log.Info("queue stopped")

	// 等待 webhook 出口的异步推送完成（避免进程退出时丢失在途通知）
	if wired.WebhookDispatcher.HasSubscriptions() {
		wired.WebhookDispatcher.Close()
		log.Info("webhook out drained")
	}

	// 停止后台 goroutine（QA 审计 C3：通知聚合 flush ticker 等），并最后 flush 一次
	wired.Close()
	log.Info("background workers stopped")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := wired.Server.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown failed", zap.Error(err))
		return err
	}
	log.Info("vigil stopped")
	return nil
}
