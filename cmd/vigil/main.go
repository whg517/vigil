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

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/internal/auth"
	"github.com/kevin/vigil/internal/config"
	"github.com/kevin/vigil/internal/escalation"
	"github.com/kevin/vigil/internal/ingestion"
	"github.com/kevin/vigil/internal/logger"
	"github.com/kevin/vigil/internal/notification"
	"github.com/kevin/vigil/internal/queue"
	"github.com/kevin/vigil/internal/runbook"
	"github.com/kevin/vigil/internal/schedule"
	"github.com/kevin/vigil/internal/server"
	"github.com/kevin/vigil/internal/store"
	"github.com/kevin/vigil/internal/triage"

	"github.com/hibiken/asynq"
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
	// 归一化 worker 持有 queue，归一化成功后入队分诊任务（流水线串接）
	normalizeWorker := ingestion.NewNormalizeWorker(st.DB, adapterRegistry, q)
	q.Register(ingestion.TaskNormalize, normalizeWorker.Handle)

	// 5.2 排班引擎（能力域 5）—— escalation 依赖它
	schedEngine := schedule.NewEngine(st.DB, st.Redis)

	// 5.3 通知（能力域 7）：通道注册表 + Webhook/邮件通道 + 分发器
	notifReg := notification.NewRegistry()
	notifReg.Register(notification.NewWebhookChannel(func(inc *ent.Incident) []string {
		// TODO: 从团队/事件配置解析 webhook URL；暂返回空（无 URL 时不发送）
		return nil
	}))
	notifReg.Register(&notification.EmailChannel{})
	notifier := notification.NewNotifier(notifReg, []string{"webhook", "email"})

	// 5.4 升级引擎（能力域 6）：Asynq 延迟任务驱动升级链，注入通知分发器
	escRedisOpt := &asynq.RedisClientOpt{Addr: cfg.Redis.Addr, Password: cfg.Redis.Password, DB: cfg.Redis.DB}
	escEngine := escalation.NewEngine(st.DB, q, schedEngine, notifier, escRedisOpt)
	q.Register(escalation.TaskEscalation, escEngine.HandleTask)

	// 5.4 分诊（能力域 3-4）：创建 Incident 后注入"启动升级"回调
	triageEngine := triage.NewEngine(st.DB, st.Redis)
	triageEngine.OnIncidentCreated = func(ctx context.Context, inc *ent.Incident, svc *ent.Service) {
		// 查 Service 绑定的 EscalationPolicy，启动升级链
		policy, err := svc.QueryEscalationPolicy().Only(ctx)
		if err != nil || len(policy.Levels) == 0 {
			return // 无升级策略，跳过
		}
		// 绑定 policy 到 Incident（便于升级任务取 levels）
		_ = st.DB.Incident.UpdateOneID(inc.ID).SetEscalationPolicyID(policy.ID).Exec(ctx)
		if err := escEngine.StartEscalation(ctx, inc.ID, policy.Levels); err != nil {
			log.Warn("start escalation failed", zap.Int("incident", inc.ID), zap.Error(err))
		}
	}
	triageWorker := triage.NewWorker(triageEngine)
	q.Register(triage.TaskTriage, triageWorker.Handle)
	log.Info("pipeline ready (ingestion → triage → escalation → notification)")

	// 6. 启动 HTTP 服务
	srv := server.New(cfg, st)
	v1 := srv.APIGroup()
	ingestHandler.Register(v1)
	schedule.NewHandler(schedEngine).Register(v1)
	// RBAC（能力域 13）：角色/绑定管理 + 鉴权器
	authz := auth.NewAuthorizer(st.DB)
	auth.NewHandler(st.DB).Register(v1)
	_ = authz // TODO: 给业务路由挂鉴权中间件 Middleware(authz, PermXxx)
	// Runbook（能力域 9）：处置手册 + 受控执行
	runbookEngine := runbook.NewEngine(st.DB, runbook.NewRegistry())
	runbook.NewHandler(st.DB, runbookEngine).Register(v1)

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
