// Command vigil 是 Vigil 告警处置平台的入口。
//
// 启动流程：load config → init logger → open store (pg+redis) →
//           init queue → start http server → start queue worker。
// 优雅退出：捕获 SIGINT/SIGTERM，按序关闭 queue → server → store。
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/internal/ai"
	"github.com/kevin/vigil/internal/analytics"
	"github.com/kevin/vigil/internal/auth"
	"github.com/kevin/vigil/internal/config"
	"github.com/kevin/vigil/internal/escalation"
	"github.com/kevin/vigil/internal/im"
	"github.com/kevin/vigil/internal/im/feishu"
	"github.com/kevin/vigil/internal/ingestion"
	"github.com/kevin/vigil/internal/incident"
	"github.com/kevin/vigil/internal/logger"
	"github.com/kevin/vigil/internal/notification"
	"github.com/kevin/vigil/internal/postmortem"
	"github.com/kevin/vigil/internal/queue"
	"github.com/kevin/vigil/internal/runbook"
	"github.com/kevin/vigil/internal/schedule"
	"github.com/kevin/vigil/internal/server"
	"github.com/kevin/vigil/internal/store"
	"github.com/kevin/vigil/internal/timeline"
	"github.com/kevin/vigil/internal/triage"
	"github.com/kevin/vigil/internal/webhook"

	"github.com/hibiken/asynq"
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
		panic(err)
	}
}

// runMigrate 应用 ent schema 到 PostgreSQL（auto-migration）。
func runMigrate() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	db, err := ent.Open("postgres", cfg.DB.DSN())
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.Schema.Create(ctx); err != nil {
		return fmt.Errorf("apply schema: %w", err)
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

	// 4.1 种子内置角色（鉴权生效的前提，幂等）
	if err := auth.SeedBuiltinRoles(ctx, st.DB); err != nil {
		log.Warn("seed builtin roles failed", zap.Error(err))
	} else {
		log.Info("builtin roles seeded")
	}

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
	// 默认通道含 im（IMChannel 在 5.6.1 注册，notifier 实时查 registry，晚注册也能生效）
	notifier := notification.NewNotifier(notifReg, []string{"im", "webhook", "email"})

	// 5.4 升级引擎（能力域 6）：Asynq 延迟任务驱动升级链，注入通知分发器 + 时间线记录器
	escRedisOpt := &asynq.RedisClientOpt{Addr: cfg.Redis.Addr, Password: cfg.Redis.Password, DB: cfg.Redis.DB}
	escEngine := escalation.NewEngine(st.DB, q, schedEngine, notifier, escRedisOpt)
	q.Register(escalation.TaskEscalation, escEngine.HandleTask)

	// 5.5 时间线（能力域 10）：统一 Recorder，供 escalation/runbook 写入
	timelineRecorder := timeline.NewRecorder(st.DB)
	escEngine.SetRecorder(timelineRecorder)

	// 5.6 RBAC 鉴权器（能力域 13）——提前创建，供 incident.Service 与 IM 层共用（同一鉴权链路）
	authz := auth.NewAuthorizer(st.DB)

	// 5.7 事件动作服务（能力域 8 复用层）：IM 与 Web 共用的 ack/resolve/escalate/add_responder 入口。
	// 注入 recorder + escEngine；OnIncidentChanged 回调后续接 IM 卡片刷新（见 5.8）。
	incService := incident.NewService(st.DB, timelineRecorder, escEngine)

	// 5.7.1 Webhook 出口（能力域 14）：incident 生命周期事件推给外部订阅者。
	// 配置 VIGIL_WEBHOOK_OUT_URLS（逗号分隔）后启用。
	var webhookURLs []string
	if cfg.Webhook.OutURLs != "" {
		for _, u := range strings.Split(cfg.Webhook.OutURLs, ",") {
			if u = strings.TrimSpace(u); u != "" {
				webhookURLs = append(webhookURLs, u)
			}
		}
	}
	webhookDisp := webhook.NewDispatcher(webhookURLs)
	if webhookDisp.HasSubscriptions() {
		log.Info("webhook out enabled", zap.Int("subscriptions", len(webhookURLs)))
	}

	// 5.8 IM 协同（能力域 8 ★）：平台适配器注册表 + 账号映射 + 卡片渲染 + 回调 handler。
	// 飞书为唯一真实接入平台；钉钉/企微留 NoopBot 待 PoC。凭证未配置时 Available()==false（降级）。
	imRegistry := im.NewRegistry()
	feishuBot := feishu.New(feishu.Config{
		AppID:             cfg.IM.Feishu.AppID,
		AppSecret:         cfg.IM.Feishu.AppSecret,
		VerificationToken: cfg.IM.Feishu.VerificationToken,
		EncryptKey:        cfg.IM.Feishu.EncryptKey,
		BaseURL:           cfg.IM.Feishu.BaseURL,
	})
	imRegistry.Register(feishuBot)
	imRegistry.Register(im.NewNoopBot("dingtalk"))
	imRegistry.Register(im.NewNoopBot("wecom"))

	imMapper := im.NewMapper(st.DB)
	imCards := im.NewCardStore()
	// 卡片渲染器：注入 authz.CheckAny 做按权限渲染按钮（无权按钮不显示，IM 不成权限后门）
	imRenderer := im.NewRenderer(func(userID int, teamScope *int, perms []string) (map[string]bool, error) {
		pp := make([]auth.Permission, 0, len(perms))
		for _, p := range perms {
			pp = append(pp, auth.Permission(p))
		}
		granted, err := authz.CheckAny(context.Background(), userID, teamScope, pp)
		if err != nil {
			return nil, err
		}
		out := make(map[string]bool, len(granted))
		for p, ok := range granted {
			out[string(p)] = ok
		}
		return out, nil
	})
	imHandler := im.NewHandler(st.DB, imRegistry, imMapper, authz, incService, imRenderer, imCards)

	// 状态变更回调：incident 状态一变即刷新已发卡片（§8 双向同步之 Web→IM / IM 内自更新）
	incService.SetOnIncidentChanged(func(ctx context.Context, inc *ent.Incident, action incident.Action) {
		// IM 卡片刷新（Web→IM 双向同步）
		for _, bot := range imRegistry.Available() {
			if cardID, ok := imCards.Get(inc.ID, bot.Platform()); ok {
				card := im.BuildCard(inc, "")
				_ = bot.UpdateCard(ctx, cardID, card)
			}
		}
		// Webhook 出口推送（incident 生命周期事件给外部订阅者）
		webhookDisp.OnIncidentChanged(ctx, inc, action)
	})
	if feishuBot.Available() {
		log.Info("im ready (feishu bot online)")
	} else {
		log.Info("im disabled (feishu credentials not configured)")
	}

	// 5.6.1 集成缺口补全：把 IM 适配成 notification 通道，
	// 让升级通知通过 IM 卡片送达（IM-first 闭环）。
	// getChannel：当前简化为返回配置的值班群（VIGIL_IM_ONCALL_CHANNEL），
	// 完整实现按 target user.im_accounts 解析私聊。
	notifReg.Register(im.NewIMChannel(imRegistry, imCards, func(inc *ent.Incident, targets []notification.Target) string {
		return cfg.IM.OncallChannel // 值班群 channel（空则不发送）
	}))

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

	// 公开路由组（自带鉴权，不走 RBAC）：webhook 接入、IM 回调
	public := srv.PublicGroup()
	ingestHandler.Register(public)   // 告警 webhook（token 鉴权）
	imHandler.Register(public)       // IM 平台回调（平台签名校验）

	// 业务路由组（受鉴权开关控制）：身份解析中间件
	v1 := srv.APIGroup()
	v1.Use(auth.RequireUser(cfg.Auth.Enabled))
	schedule.NewHandler(schedEngine).Register(v1)
	// Incident API（能力域 14 集成入口 + 8 IM/Web 操作）：list/get/ack/resolve/escalate
	incident.NewHandler(st.DB, incService).Register(v1)
	// RBAC（能力域 13）：角色/绑定管理
	auth.NewHandler(st.DB).Register(v1)
	// Runbook（能力域 9）：处置手册 + 受控执行，注入时间线记录器
	runbookEngine := runbook.NewEngine(st.DB, runbook.NewRegistry())
	runbookEngine.SetTimelineRecorder(timelineRecorder)
	runbook.NewHandler(st.DB, runbookEngine).Register(v1)
	// 时间线（能力域 10）：查询 + 手动追加 API
	timeline.NewHandler(timelineRecorder).Register(v1)
	// 复盘（能力域 12）：草稿生成 + 状态机 + 改进项
	// AI 起草：配置了 GLM key 则用 AI，否则降级（设计基线第 7 条）
	var pmLLM postmortem.LLMProvider
	glmProvider := ai.NewGLMProvider(cfg.LLM.APIKey, cfg.LLM.Model, cfg.LLM.BaseURL)
	if glmProvider.Available() {
		pmLLM = ai.NewPostmortemDraftAdapter(glmProvider)
		log.Info("ai llm ready (glm)")
	} else {
		log.Info("ai llm disabled (no api key), postmortem uses fallback drafts")
	}
	postmortemEngine := postmortem.NewEngine(st.DB, pmLLM)
	postmortem.NewHandler(st.DB, postmortemEngine).Register(v1)
	// AI 诊断（能力域 11）：根因线索 + 相似事件 + human-in-the-loop
	aiDiagEngine := ai.NewDiagnoseEngine(st.DB, glmProvider)
	ai.NewHandler(aiDiagEngine).Register(v1)
	// 报表（能力域 15）：告警/事件/团队负载/复盘/趋势 度量
	analytics.NewHandler(analytics.NewEngine(st.DB)).Register(v1)

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
