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
// @description    认证：业务 API 要求 Bearer JWT（/auth/login 换取）。X-Vigil-User-ID 头回退默认关闭，须 VIGIL_AUTH_HEADER_FALLBACK=true 显式开启（该头可伪造，仅限本地开发调试；生产环境无条件强制关闭），该回退方案不在 securitySchemes 中声明。
// @license.name   MIT
// @servers.url    /api/v1
//
// @securitydefinitions.bearerauth bearerAuth
// @description    业务 API 推荐（唯一声明）方案：HTTP Bearer JWT（/auth/login 换取）。
// @bearerformat   JWT
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/kevin/vigil/internal/config"
	domainevent "github.com/kevin/vigil/internal/event"
	"github.com/kevin/vigil/internal/logger"
	"github.com/kevin/vigil/internal/queue"
	"github.com/kevin/vigil/internal/schema"
	"github.com/kevin/vigil/internal/server"
	"github.com/kevin/vigil/internal/store"

	_ "github.com/lib/pq" // 注册 postgres 驱动（ent dialect "postgres" 用）
	"go.uber.org/zap"
)

func main() {
	// 子命令分发：migrate 把嵌入二进制的 atlas 版本化迁移文件 apply 到 PG
	// （shell out 调 atlas CLI；运行环境须装 atlas，Docker 镜像已内置）
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		if err := runMigrateCmd(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "migrate failed:", err)
			os.Exit(1)
		}
		return
	}
	// seed-demo 灌演示数据（幂等），见 seed_demo.go。
	if len(os.Args) > 1 && os.Args[1] == "seed-demo" {
		if err := runSeedDemoCmd(); err != nil {
			fmt.Fprintln(os.Stderr, "seed-demo failed:", err)
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
//	vigil migrate            apply 嵌入的 atlas 版本化迁移到 PG
//	vigil migrate status     打印当前数据库迁移版本状态（atlas migrate status 透传）
//
// 本项目不提供 down 回滚子命令：升级迁移失败一律通过备份恢复（scripts/restore.sh）完成。
// 实现方式：把 //go:embed 嵌入的迁移文件解压到临时目录，shell out 调 atlas CLI。
func runMigrateCmd(args []string) error {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "status":
		return runMigrateStatus()
	case "", "up":
		if err := runMigrate(); err != nil {
			return err
		}
		fmt.Println("migrate: schema applied")
		return nil
	default:
		return fmt.Errorf("未知子命令 %q（可用: <空>|up|status）", sub)
	}
}

// runMigrate shell out 调 atlas CLI apply 嵌入的迁移文件。
//
// 步骤：
//  1. 把 internal/schema 嵌入的 migrations 解压到临时目录（schema.Extract）
//  2. atlas migrate apply --dir file://<tmp> --url <cfg.DB.URL()>
//  3. atlas CLI 本身由运行环境提供（Docker 镜像内置 / 本地预装）
func runMigrate() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	dir, err := schema.Extract()
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	// #nosec G204 -- atlas 二进制名是常量，--dir 来自临时目录（schema.Extract 受控），
	// --url 来自 cfg.DB（envconfig 加载，非用户输入）。整个 cmd 由 vigil 进程自己组装。
	cmd := exec.Command("atlas", "migrate", "apply",
		"--dir", "file://"+dir,
		"--url", cfg.DB.URL(),
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("atlas migrate apply: %w", err)
	}
	return nil
}

// runMigrateStatus 透传 atlas migrate status。
func runMigrateStatus() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	dir, err := schema.Extract()
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	// #nosec G204 -- 同 runMigrate：atlas 二进制名常量，参数由 vigil 进程受控组装。
	cmd := exec.Command("atlas", "migrate", "status",
		"--dir", "file://"+dir,
		"--url", cfg.DB.URL(),
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
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
