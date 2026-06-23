package app

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/notificationrule"
	"github.com/kevin/vigil/ent/user"
	"github.com/kevin/vigil/internal/ai"
	"github.com/kevin/vigil/internal/analytics"
	"github.com/kevin/vigil/internal/auth"
	"github.com/kevin/vigil/internal/config"
	"github.com/kevin/vigil/internal/escalation"
	"github.com/kevin/vigil/internal/im"
	"github.com/kevin/vigil/internal/im/dingtalk"
	"github.com/kevin/vigil/internal/im/feishu"
	"github.com/kevin/vigil/internal/incident"
	"github.com/kevin/vigil/internal/ingestion"
	"github.com/kevin/vigil/internal/integration"
	"github.com/kevin/vigil/internal/middleware"
	"github.com/kevin/vigil/internal/notification"
	"github.com/kevin/vigil/internal/postmortem"
	"github.com/kevin/vigil/internal/queue"
	"github.com/kevin/vigil/internal/runbook"
	"github.com/kevin/vigil/internal/schedule"
	"github.com/kevin/vigil/internal/server"
	"github.com/kevin/vigil/internal/service"
	"github.com/kevin/vigil/internal/store"
	"github.com/kevin/vigil/internal/timeline"
	"github.com/kevin/vigil/internal/triage"
	"github.com/kevin/vigil/internal/webhook"
	"github.com/kevin/vigil/internal/ws"

	"github.com/hibiken/asynq"
	"go.uber.org/zap"
)

// Bootstrap 装配 Vigil 全部运行时组件并挂载 HTTP 路由，但不启动阻塞型服务。
//
// 装配顺序与依赖关系对应 architecture.md §分层：
//
//	store → queue → ingestion → schedule → notification → escalation →
//	timeline → auth(RBAC/audit) → incident.Service → webhook/IM → triage → server。
//
// 启动（HTTP server、Asynq worker）与优雅关闭由调用方负责：
//   - 生产入口：见 cmd/vigil/main.go，按 queue→webhook→http 顺序关闭。
//   - 测试：在进程内自行 Start/Shutdown。
//
// ctx 仅用于装配期间的初始化操作（如 resolveEmails 闭包持有），不绑定信号；
// 调用方传入的 ctx 生命周期应覆盖 App 的整个使用期。
func Bootstrap(ctx context.Context, cfg *config.Config, log *zap.Logger) (*App, error) {
	// 4. 打开数据存储
	st, err := store.New(ctx, cfg)
	if err != nil {
		log.Error("open store failed", zap.Error(err))
		return nil, err
	}
	log.Info("store ready (postgres + redis)")

	// 4.1 种子内置角色（鉴权生效的前提，幂等）
	if err := auth.SeedBuiltinRoles(ctx, st.DB); err != nil {
		log.Warn("seed builtin roles failed", zap.Error(err))
	} else {
		log.Info("builtin roles seeded")
	}

	// 4.2 JWT 签发器 + 默认管理员种子（能力域 13 登录态）。
	// JWTSecret 为空时登录链路降级（拒绝签发），仅靠 X-Vigil-User-ID 兼容。
	// 配置了 secret 才 seed 默认 admin（避免无登录态时建无用账号）。
	jwtSigner := auth.NewJWTSigner(
		cfg.Auth.JWTSecret,
		cfg.Auth.EffectiveAccessTokenTTL(),
		cfg.Auth.EffectiveRefreshTokenTTL(),
	)
	if !jwtSigner.Available() {
		log.Warn("auth jwt secret not set; login disabled (set VIGIL_AUTH_JWT_SECRET)")
	} else {
		if created, err := auth.SeedDefaultAdmin(ctx, st.DB); err != nil {
			log.Warn("seed default admin failed", zap.Error(err))
		} else if created {
			log.Warn("default admin created (username=admin password=changeme) — CHANGE IMMEDIATELY")
		}
	}

	// 4.3 身份解析聚合器（能力域 13）：统一 JWT / API Key / X-Vigil-User-ID 三轨。
	// 中间件通过它解析身份，避免给每个中间件函数逐个传 signer/verifier。
	apiKeyVerifier := auth.NewAPIKeyVerifier(st.DB)
	identityResolver := auth.NewIdentityResolver(jwtSigner, apiKeyVerifier)

	// 5. 初始化异步任务队列
	q := queue.New(cfg)
	log.Info("queue ready (asynq)")

	// 5.1 初始化接入（能力域 1-2）：适配器注册表 + webhook handler + 归一化 worker
	adapterRegistry := ingestion.NewAdapterRegistry()
	ingestHandler := ingestion.NewHandler(st.DB, q)
	// 限流（M1.7）：按 Integration 维度 Redis 滑动窗口，超限 429 但 payload 仍落库。
	// 背压：队列积压超阈值返回 503（payload 仍落库，恢复后回灌）。无 Redis 时降级跳过。
	ingestHandler.SetLimiter(middleware.NewLimiter(st.Redis), cfg.Ingestion.RateLimitPerMin)
	ingestHandler.SetBackpressureChecker(middleware.NewBackpressureChecker(st.Redis, cfg.Ingestion.BackpressureDepth))
	// 归一化 worker 持有 queue，归一化成功后入队分诊任务（流水线串接）
	normalizeWorker := ingestion.NewNormalizeWorker(st.DB, adapterRegistry, q)
	q.Register(ingestion.TaskNormalize, normalizeWorker.Handle)

	// 5.2 排班引擎（能力域 5）—— escalation 依赖它
	schedEngine := schedule.NewEngine(st.DB, st.Redis)

	// 5.3 通知（能力域 7）：通道注册表 + Webhook/邮件通道 + 分发器
	// 含静默时段（M7.8）+ 通知聚合（M7.9）—— "少打扰"核心。
	notifReg := notification.NewRegistry()
	// Webhook 通道 URL：复用出口 webhook 配置（VIGIL_WEBHOOK_OUT_URLS），
	// 两者语义一致（都是把 incident 推给外部订阅者）。
	// 完整实现后续按 team/service 配置解析（待 schema 加 webhook 配置字段）。
	notifWebhookURLs := parseWebhookURLs(cfg.Webhook.OutURLs)
	notifReg.Register(notification.NewWebhookChannel(func(inc *ent.Incident) []string {
		return notifWebhookURLs
	}))
	// 邮件通道（能力域 7 M7.3）：SMTP 配置后真实发送，未配置降级跳过。
	// GetEmails 从 targets 解析 email（当前简化：从 incident.team 成员查 email，后续按 NotificationRule 细化）。
	emailChan := &notification.EmailChannel{
		Config: notification.SMTPConfig{
			Host: cfg.Notification.SMTP.Host, Port: cfg.Notification.SMTP.Port,
			Username: cfg.Notification.SMTP.Username, Password: cfg.Notification.SMTP.Password,
		},
		From:      cfg.Notification.SMTP.From,
		GetEmails: func(targets []notification.Target) []string { return resolveEmails(ctx, st.DB, targets) },
	}
	notifReg.Register(emailChan)
	if emailChan.Available() {
		log.Info("email channel ready (smtp)")
	}
	// 电话/SMS 通道（能力域 7 M7.2，占位）：转发 webhook 供用户对接云语音 API。
	phoneChan := &notification.PhoneChannel{
		Config:    notification.VoiceProviderConfig{WebhookURL: cfg.Notification.Phone.WebhookURL, From: cfg.Notification.Phone.From},
		GetPhones: func(targets []notification.Target) []string { return resolvePhones(ctx, st.DB, targets) },
	}
	smsChan := &notification.SMSChannel{
		Config:    notification.VoiceProviderConfig{WebhookURL: cfg.Notification.SMS.WebhookURL, From: cfg.Notification.SMS.From},
		GetPhones: func(targets []notification.Target) []string { return resolvePhones(ctx, st.DB, targets) },
	}
	notifReg.Register(phoneChan)
	notifReg.Register(smsChan)
	// 默认通道含 im（IMChannel 在 5.6.1 注册，notifier 实时查 registry，晚注册也能生效）
	// 无 webhook URL 配置时不把 webhook 放默认通道，避免无效空跑
	defaultChans := []string{"im", "email"}
	if len(notifWebhookURLs) > 0 {
		defaultChans = append([]string{"webhook"}, defaultChans...)
	}
	notifier := notification.NewNotifier(notifReg, defaultChans)
	// 接通送达结果记录：当前用结构化日志（后续加 Notification 记录表后落库）。
	// 不接的话 SetResultRecorder 永不被调用，送达结果（成功/失败/目标）完全丢失。
	notifier.SetResultRecorder(func(incID int, r notification.SendResult) {
		if r.Success {
			log.Info("notification delivered",
				zap.Int("incident", incID),
				zap.String("channel", r.Channel),
				zap.String("target", r.Target))
		} else {
			log.Warn("notification failed",
				zap.Int("incident", incID),
				zap.String("channel", r.Channel),
				zap.String("target", r.Target),
				zap.String("error", r.Error))
		}
	})
	// 通知聚合器（M7.9）：30s 窗口内对同一 target 合并；critical 不聚合。
	// 无 Redis 时聚合器 Add 立即返回 sendNow（降级为不聚合，保证送达）。
	notifAggregator := notification.NewAggregator(st.Redis, 30*time.Second)
	notifier.SetAggregator(notifAggregator)
	// 静默时段解析（M7.8）：按 incident.team 查适用的 NotificationRule.quiet_hours。
	// 本期简化：取该 team 第一条 enabled 且配了 quiet_hours 的规则。
	notifier.SetQuietHoursResolver(func(inc *ent.Incident) *notification.QuietHours {
		if inc == nil {
			return nil
		}
		rules, err := st.DB.NotificationRule.Query().
			Where(notificationrule.EnabledEQ(true)).All(context.Background())
		if err != nil {
			return nil
		}
		for _, r := range rules {
			if qh := notification.ParseQuietHoursPublic(r.QuietHours); qh != nil && qh.Enabled {
				return qh
			}
		}
		return nil
	})

	// 5.3.1 通知模板系统（能力域 7 M7.5）：内置默认模板 seed + 注入 notifier。
	// 渲染失败由 TemplateEngine 内部降级（FormatTitle/Summary 兜底），不丢通知。
	notifTemplates := notification.NewTemplateEngine(st.DB)
	if err := notifTemplates.SeedBuiltinTemplates(ctx); err != nil {
		log.Warn("seed notification templates failed", zap.Error(err))
	} else {
		log.Info("notification templates seeded")
	}
	// 按 incident.team 查 NotificationRule.template_id 解析适用模板名（本期简化：取首条 enabled 规则）。
	notifier.SetTemplateEngine(notifTemplates, func(inc *ent.Incident) string {
		if inc == nil {
			return ""
		}
		r, err := st.DB.NotificationRule.Query().Where(notificationrule.EnabledEQ(true)).First(ctx)
		if err != nil || r == nil {
			return ""
		}
		return r.TemplateID
	})

	// 5.4 升级引擎（能力域 6）：Asynq 延迟任务驱动升级链，注入通知分发器 + 时间线记录器
	escRedisOpt := &asynq.RedisClientOpt{Addr: cfg.Redis.Addr, Password: cfg.Redis.Password, DB: cfg.Redis.DB}
	escEngine := escalation.NewEngine(st.DB, q, schedEngine, notifier, escRedisOpt)
	q.Register(escalation.TaskEscalation, escEngine.HandleTask)

	// 5.5 时间线（能力域 10）：统一 Recorder，供 escalation/runbook 写入
	timelineRecorder := timeline.NewRecorder(st.DB)
	escEngine.SetRecorder(timelineRecorder)
	escEngine.SetLogger(log) // 升级 target 解析失败时记告警日志

	// 5.6 RBAC 鉴权器（能力域 13）——提前创建，供 incident.Service 与 IM 层共用（同一鉴权链路）
	authz := auth.NewAuthorizer(st.DB)

	// 5.6.1 审计日志记录器（能力域 13 M13.5）：关键写操作埋点（角色变更/API Key/登录等）。
	// 解耦组件，各 handler 注入后调用 MustRecord（best-effort，失败不阻塞业务）。
	auditRecorder := auth.NewAuditRecorder(st.DB)

	// 5.7 事件动作服务（能力域 8 复用层）：IM 与 Web 共用的 ack/resolve/escalate/add_responder 入口。
	// 注入 recorder + escEngine；OnIncidentChanged 回调后续接 IM 卡片刷新（见 5.8）。
	incService := incident.NewService(st.DB, timelineRecorder, escEngine)

	// 5.7.1 Webhook 出口（能力域 14）：incident 生命周期事件推给外部订阅者。
	// 配置 VIGIL_WEBHOOK_OUT_URLS（逗号分隔）后启用。
	webhookURLs := parseWebhookURLs(cfg.Webhook.OutURLs)
	webhookDisp := webhook.NewDispatcher(webhookURLs)
	if webhookDisp.HasSubscriptions() {
		log.Info("webhook out enabled", zap.Int("subscriptions", len(webhookURLs)))
	}

	// 5.8 IM 协同（能力域 8 ★）：平台适配器注册表 + 账号映射 + 卡片渲染 + 回调 handler。
	// 飞书/钉钉为真实接入平台（P0）；企微留 NoopBot 待 PoC。凭证未配置时 Available()==false（降级）。
	imRegistry := im.NewRegistry()
	feishuBot := feishu.New(feishu.Config{
		AppID:             cfg.IM.Feishu.AppID,
		AppSecret:         cfg.IM.Feishu.AppSecret,
		VerificationToken: cfg.IM.Feishu.VerificationToken,
		EncryptKey:        cfg.IM.Feishu.EncryptKey,
		BaseURL:           cfg.IM.Feishu.BaseURL,
	})
	dingtalkBot := dingtalk.New(dingtalk.Config{
		AppKey:    cfg.IM.Dingtalk.AppKey,
		AppSecret: cfg.IM.Dingtalk.AppSecret,
		RobotCode: cfg.IM.Dingtalk.RobotCode,
		Token:     cfg.IM.Dingtalk.Token,
		AesKey:    cfg.IM.Dingtalk.AesKey,
		OapiBase:  cfg.IM.Dingtalk.OapiBase,
		APIBase:   cfg.IM.Dingtalk.APIBase,
	})
	imRegistry.Register(feishuBot)
	imRegistry.Register(dingtalkBot)
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

	// WebSocket hub（能力域 8 §状态双向同步）：incident 变更实时推给 Web 订阅者。
	// 单实例内存 hub；多实例时换 Redis pub/sub（架构 §6.4 预留）。
	wsHub := ws.NewHub()

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
		// WebSocket 推送（Web 端实时刷新，IM→Web 双向同步）
		wsHub.BroadcastIncident(inc.ID, string(action), inc)
	})
	if feishuBot.Available() {
		log.Info("im ready (feishu bot online)")
	} else {
		log.Info("im disabled (feishu credentials not configured)")
	}
	if dingtalkBot.Available() {
		log.Info("im ready (dingtalk bot online)")
	} else {
		log.Info("im disabled (dingtalk credentials not configured)")
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

	// 公开路由组（自带鉴权，不走 RBAC）：webhook 接入、IM 回调、登录换 token
	public := srv.PublicGroup()
	ingestHandler.Register(public) // 告警 webhook（token 鉴权）
	imHandler.Register(public)     // IM 平台回调（平台签名校验）
	// WebSocket 实时推送（能力域 8）：GET /ws/incidents/:id 订阅 incident 变更
	ws.NewHandler(wsHub).Register(public)
	log.Info("websocket ready (/ws/incidents/:id)")
	// 登录态 API（能力域 13）：login/refresh 走 public（换取 token 无需已登录）
	authHandler := auth.NewAuthHandler(st.DB, jwtSigner)
	authHandler.SetAuditRecorder(auditRecorder) // 登录成功/失败记审计（安全溯源）
	authHandler.RegisterPublic(public)

	// 业务路由组（受鉴权开关控制）：身份解析中间件（三轨：JWT/APIKey/header）
	v1 := srv.APIGroup()
	v1.Use(auth.RequireUser(cfg.Auth.Enabled, identityResolver))
	// me 走 v1（RequireUser 保护，需已登录）
	authHandler.RegisterProtected(v1)
	// IM 平台状态（只读，业务侧）：GET /im/platforms
	imHandler.RegisterStatus(v1)
	// API Key 管理（能力域 13 M13.7）：CRUD + 创建时返回明文仅一次；记审计
	apiKeyHandler := auth.NewAPIKeyHandler(st.DB)
	apiKeyHandler.SetAuditRecorder(auditRecorder)
	apiKeyHandler.Register(v1)
	schedule.NewHandler(schedEngine, st.DB).Register(v1)
	// 服务目录（能力域 4/13）：Service CRUD（此前仅 schema 无 handler）
	service.NewHandler(st.DB).Register(v1)
	// 接入点管理（能力域 1）：Integration CRUD，创建返回 webhook 鉴权 token 一次
	integration.NewHandler(st.DB).Register(v1)
	// 升级策略管理（能力域 6）：EscalationPolicy CRUD（engine 已有，此处补管理 API）
	escalation.NewPolicyHandler(st.DB).Register(v1)
	// Incident API（能力域 14 集成入口 + 8 IM/Web 操作）：list/get/ack/resolve/escalate
	incident.NewHandler(st.DB, incService).Register(v1)
	// RBAC（能力域 13）：角色/绑定管理；记审计（角色变更/授权是敏感操作）
	rbacHandler := auth.NewHandler(st.DB)
	rbacHandler.SetAuditRecorder(auditRecorder)
	rbacHandler.Register(v1)
	// 用户/团队管理（能力域 13）：User/Team CRUD（RBAC 角色绑定的 user_id/team_id 依赖这些列表）
	auth.NewUserHandler(st.DB).Register(v1)
	auth.NewTeamHandler(st.DB).Register(v1)
	// 审计日志查询（能力域 13 M13.5）：只读 + 筛选（权限点 admin.audit.view）
	auth.NewAuditHandler(st.DB).Register(v1)
	// Runbook（能力域 9）：处置手册 + 受控执行，注入时间线记录器
	runbookEngine := runbook.NewEngine(st.DB, runbook.NewRegistry())
	runbookEngine.SetTimelineRecorder(timelineRecorder)
	// on_failure=escalate 触发器：包装 incident.Service.Escalate（系统触发，actorID=0）
	runbookEngine.SetEscalationTrigger(runbookEscalator{inc: incService})
	runbook.NewHandler(st.DB, runbookEngine).Register(v1)
	// 时间线（能力域 10）：查询 + 手动追加 API
	timeline.NewHandler(timelineRecorder).Register(v1)
	// 复盘（能力域 12）：草稿生成 + 状态机 + 改进项
	// AI 起草：配置了 GLM key 则用 AI，否则降级（设计基线第 7 条）
	var pmLLM postmortem.LLMProvider
	glmProvider := ai.Provider(ai.NewGLMProvider(cfg.LLM.APIKey, cfg.LLM.Model, cfg.LLM.BaseURL))
	// LLM 成本控制（能力域 11，缓存/限流/配额）：包装 GLM，所有 Complete/Embed 走成本闸。
	// 无 Redis 时降级为透传（缓存/限流/配额全跳过，仅保证调用可达）。
	glmProvider = ai.NewCostController(glmProvider, st.Redis, "org:default", ai.CostConfig{
		CacheTTL:        time.Duration(cfg.LLM.Cost.CacheTTLSeconds) * time.Second,
		DisableCache:    cfg.LLM.Cost.DisableCache,
		RateLimitPerMin: cfg.LLM.Cost.RateLimitPerMin,
		TokenQuota:      cfg.LLM.Cost.TokenQuota,
	})
	if glmProvider.Available() {
		pmLLM = ai.NewPostmortemDraftAdapter(glmProvider)
		log.Info("ai llm ready (glm)")
	} else {
		log.Info("ai llm disabled (no api key), postmortem uses fallback drafts")
	}
	postmortemEngine := postmortem.NewEngine(st.DB, pmLLM)
	// 知识沉淀（M12.6）：published 复盘计算 embedding 入库，供相似检索反哺。
	// glmProvider 实现 ai.Provider（含 Embed），LLM 不可用时 embedder 为 nil（降级不入库）。
	if glmProvider.Available() {
		postmortemEngine.SetEmbedder(glmProvider)
	}
	postmortem.NewHandler(st.DB, postmortemEngine).Register(v1)
	// AI 诊断（能力域 11）：根因线索 + 相似事件 + human-in-the-loop
	aiDiagEngine := ai.NewDiagnoseEngine(st.DB, glmProvider)
	// 注入 raw SQL 执行器：FindSimilar 用 pgvector 余弦距离检索相似事件（M11.4）。
	// 无 *sql.DB 或无 pgvector 扩展时，FindSimilar 自动降级回 LIKE 文本匹配。
	if st.SQL != nil {
		aiDiagEngine.SetSQLRunner(func(ctx context.Context, query string, args []any, scan func(*sql.Rows) error) error {
			rows, err := st.SQL.QueryContext(ctx, query, args...)
			if err != nil {
				return err
			}
			defer func() { _ = rows.Close() }()
			for rows.Next() {
				if err := scan(rows); err != nil {
					return err
				}
			}
			return rows.Err()
		})
	}
	ai.NewHandler(aiDiagEngine).Register(v1)
	// 报表（能力域 15）：告警/事件/团队负载/复盘/趋势 度量
	analytics.NewHandler(analytics.NewEngine(st.DB)).Register(v1)
	// 通知配置（能力域 7 + 3 抑制）：NotificationRule / SuppressionRule / Template CRUD + dry-run test
	// 权限点 notification.rule.* / notification.template.* / suppression.* 由调用方在装配时按角色授权。
	notifHandler := notification.NewHandler(st.DB, notifier, notifAggregator)
	notifHandler.SetTemplateEngine(notifTemplates)
	notifHandler.Register(v1)

	return &App{
		Cfg:               cfg,
		Log:               log,
		Store:             st,
		Queue:             q,
		Server:            srv,
		TriageEngine:      triageEngine,
		EscalationEngine:  escEngine,
		IncidentService:   incService,
		RunbookEngine:     runbookEngine,
		AIDiagnoseEngine:  aiDiagEngine,
		WebhookDispatcher: webhookDisp,
	}, nil
}

// parseWebhookURLs 把逗号分隔的 webhook URL 字符串解析为去空的 URL 切片。
// 供通知通道与出口分发器共用配置（VIGIL_WEBHOOK_OUT_URLS）。
func parseWebhookURLs(csv string) []string {
	if csv == "" {
		return nil
	}
	var urls []string
	for _, u := range strings.Split(csv, ",") {
		if u = strings.TrimSpace(u); u != "" {
			urls = append(urls, u)
		}
	}
	return urls
}

// resolveEmails 从 targets 的 user_id 批量查 User.email（邮件通道用）。
// 查询失败的 user 跳过，不阻塞其他目标。
func resolveEmails(ctx context.Context, db *ent.Client, targets []notification.Target) []string {
	var uids []int
	for _, t := range targets {
		if t.UserID > 0 {
			uids = append(uids, t.UserID)
		}
	}
	if len(uids) == 0 {
		return nil
	}
	users, err := db.User.Query().Where(user.IDIn(uids...)).All(ctx)
	if err != nil {
		return nil
	}
	var emails []string
	for _, u := range users {
		if u.Email != "" {
			emails = append(emails, u.Email)
		}
	}
	return emails
}

// resolvePhones 从 targets 的 user_id 批量查 User.phone（电话/SMS 通道用）。
func resolvePhones(ctx context.Context, db *ent.Client, targets []notification.Target) []string {
	var uids []int
	for _, t := range targets {
		if t.UserID > 0 {
			uids = append(uids, t.UserID)
		}
	}
	if len(uids) == 0 {
		return nil
	}
	users, err := db.User.Query().Where(user.IDIn(uids...)).All(ctx)
	if err != nil {
		return nil
	}
	var phones []string
	for _, u := range users {
		if u.Phone != "" {
			phones = append(phones, u.Phone)
		}
	}
	return phones
}

// runbookEscalator 实现 runbook.EscalationTrigger，包装 incident.Service.Escalate。
// on_failure=escalate 时触发该 incident 的立即升级（系统触发，actorID=0，source=runbook）。
type runbookEscalator struct {
	inc *incident.Service
}

func (r runbookEscalator) Trigger(ctx context.Context, incID int, reason string) error {
	_, err := r.inc.Escalate(ctx, incID, 0, incident.SourceRunbook)
	return err
}
