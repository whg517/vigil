// Command vigil 是 Vigil 告警处置平台的入口。
//
// 启动流程：load config → init logger → open store (pg+redis) →
//           init queue → start http server → start queue worker。
// 优雅退出：捕获 SIGINT/SIGTERM，按序关闭 queue → server → store。
package main

import (
	"context"
	"errors"
	"os/signal"
	"syscall"
	"time"

	"github.com/kevin/vigil/internal/config"
	"github.com/kevin/vigil/internal/ingestion"
	"github.com/kevin/vigil/internal/logger"
	"github.com/kevin/vigil/internal/queue"
	"github.com/kevin/vigil/internal/server"
	"github.com/kevin/vigil/internal/store"

	"go.uber.org/zap"
)

func main() {
	if err := run(); err != nil {
		panic(err)
	}
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
	defer log.Sync()

	log.Info("vigil starting",
		zap.String("env", cfg.App.Env),
		zap.String("addr", cfg.HTTP.Addr),
	)

	// 3. 捕获退出信号
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 4. 打开数据存储
	st, err := store.New(ctx, cfg)
	if err != nil {
		log.Error("open store failed", zap.Error(err))
		return err
	}
	defer st.Close()
	log.Info("store ready (postgres + redis)")

	// 5. 初始化异步任务队列
	q := queue.New(cfg)
	defer q.Close()
	log.Info("queue ready (asynq)")

	// 5.1 初始化接入（能力域 1-2）：适配器注册表 + webhook handler + 归一化 worker
	adapterRegistry := ingestion.NewAdapterRegistry()
	ingestHandler := ingestion.NewHandler(st.DB, q)
	normalizeWorker := ingestion.NewNormalizeWorker(st.DB, adapterRegistry)
	// 归一化任务注册到 queue
	q.Register(ingestion.TaskNormalize, normalizeWorker.Handle)
	log.Info("ingestion ready (webhook + normalize worker)")

	// 6. 启动 HTTP 服务
	srv := server.New(cfg, st)
	// 挂载 webhook 接入路由（/api/v1/webhook/:token）
	ingestHandler.Register(srv.APIGroup())

	errCh := make(chan error, 1)
	go func() {
		log.Info("http server listening", zap.String("addr", cfg.HTTP.Addr))
		if err := srv.Start(); err != nil && !errors.Is(err, context.Canceled) {
			errCh <- err
		}
		close(errCh)
	}()

	// 7. 启动异步任务消费（goroutine，不阻塞主流程）
	// 业务 handler 由各能力域在启动时通过 q.Register 注册
	go func() {
		if err := q.Start(); err != nil {
			log.Error("queue server error", zap.Error(err))
		}
	}()

	// 8. 等待退出信号或启动错误
	select {
	case <-ctx.Done():
		log.Info("shutdown signal received")
	case err := <-errCh:
		if err != nil {
			log.Error("http server error", zap.Error(err))
			return err
		}
	}

	// 9. 优雅关闭：先停 queue（停止消费新任务），再停 http，store 由 defer 关闭
	q.Shutdown()
	log.Info("queue stopped")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown failed", zap.Error(err))
		return err
	}
	log.Info("vigil stopped")
	return nil
}
