// wire.go 集中装配 Vigil 全部运行时组件并挂载 HTTP 路由。
//
// 替代原 internal/app/bootstrap.go 的「上帝装配函数」。装配逻辑收敛在 server 包的
// Wire 函数里：按依赖顺序构造各域引擎/handler（顺序传递，无 deps 容器），
// 就地注册路由 + 订阅事件，返回生命周期句柄。
//
// 设计要点（架构基线）：
//   - 无 deps/Build/编排：依赖图就是函数内局部变量的声明顺序，编译器天然保证。
//   - 无状态对象（timeline/authz/incService 等）各用各的，不共享。
//   - 域间协作走领域事件总线（event.Bus），消除构建期依赖环。
//   - 横切策略（audit/template/限流）在构造时注入，不再用 setter。
//
// 启动（HTTP server、Asynq worker）与优雅关闭由调用方负责（cmd/vigil/main.go、e2e）。
package server

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
	domainevent "github.com/kevin/vigil/internal/event"
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
	"github.com/kevin/vigil/internal/service"
	"github.com/kevin/vigil/internal/store"
	"github.com/kevin/vigil/internal/timeline"
	"github.com/kevin/vigil/internal/triage"
	"github.com/kevin/vigil/internal/webhook"
	"github.com/kevin/vigil/internal/ws"

	"github.com/hibiken/asynq"
	"go.uber.org/zap"
)

// Wired 装配产物：调用方（main/e2e）生命周期管理需要的句柄。
//
// 只暴露「需要跨域或被关闭逻辑触达」的对象。各域 handler/引擎在 Wire 内部构造后
// 即被路由/事件订阅引用，无需外暴露。Server 是 HTTP 入口，WebhookDispatcher 需要
// 在优雅关闭时 drain 在途推送。
type Wired struct {
	Server            *Server
	WebhookDispatcher *webhook.Dispatcher
}

// Wire 装配全部组件并挂载路由，返回生命周期句柄。不启动阻塞服务。
//
// ctx 用于装配期初始化（如 resolveEmails 闭包、seed）。调用方传入的 ctx 生命周期
// 应覆盖整个使用期。装配顺序对应架构分层，依赖图通过局部变量声明顺序自然满足。
func Wire(ctx context.Context, cfg *config.Config, log *zap.Logger, st *store.Store, q *queue.Queue, bus *domainevent.Bus) (*Wired, error) {
	// —— 基础设施 seed（鉴权生效前提，幂等）——
	if err := auth.SeedBuiltinRoles(ctx, st.DB); err != nil {
		log.Warn("seed builtin roles failed", zap.Error(err))
	} else {
		log.Info("builtin roles seeded")
	}

	// —— 登录态：JWT 签发器 + 默认管理员 ——
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
	// 身份解析（三轨：JWT/APIKey/X-Vigil-User-ID），中间件用。
	apiKeyVerifier := auth.NewAPIKeyVerifier(st.DB)
	identityResolver := auth.NewIdentityResolver(jwtSigner, apiKeyVerifier)

	// —— 接入（能力域 1-2）：webhook 接收 + 归一化 worker ——
	adapterRegistry := ingestion.NewAdapterRegistry()
	ingestHandler := ingestion.NewHandler(st.DB, q)
	// 限流/背压（无 Redis 时降级跳过；payload 仍落库，不丢告警）。
	ingestHandler.SetLimiter(middleware.NewLimiter(st.Redis), cfg.Ingestion.RateLimitPerMin)
	ingestHandler.SetBackpressureChecker(middleware.NewBackpressureChecker(st.Redis, cfg.Ingestion.BackpressureDepth))
	normalizeWorker := ingestion.NewNormalizeWorker(st.DB, adapterRegistry, q)
	q.Register(ingestion.TaskNormalize, normalizeWorker.Handle)

	// —— 通知（能力域 7）：通道注册表 + 分发器（含静默/聚合/模板）——
	notifWebhookURLs := parseWebhookURLs(cfg.Webhook.OutURLs)
	notifier, notifAggregator, notifTemplates := buildNotifier(ctx, cfg, log, st, notifWebhookURLs)

	// —— 升级引擎（能力域 6）：Asynq 延迟任务驱动升级链 ——
	schedEngine := schedule.NewEngine(st.DB, st.Redis) // escalation 依赖它
	escRedisOpt := &asynq.RedisClientOpt{Addr: cfg.Redis.Addr, Password: cfg.Redis.Password, DB: cfg.Redis.DB}
	timelineRecorder := timeline.NewRecorder(st.DB) // 统一时间线 Recorder
	escEngine := escalation.NewEngine(st.DB, q, schedEngine, notifier, escRedisOpt)
	escEngine.SetRecorder(timelineRecorder)
	escEngine.SetLogger(log)
	q.Register(escalation.TaskEscalation, escEngine.HandleTask)
	// escalation 订阅 incident 事件：ack 取消升级、创建启动链、手动升级触发 level。
	bus.Subscribe(domainevent.IncidentCreated, escEngine.OnCreated)
	bus.Subscribe(domainevent.IncidentAcked, escEngine.OnAcked)
	bus.Subscribe(domainevent.IncidentEscalated, escEngine.OnManualEscalate)

	// —— 鉴权（能力域 13）：RBAC + 审计 ——
	authz := auth.NewAuthorizer(st.DB)
	auditRecorder := auth.NewAuditRecorder(st.DB)

	// —— 事件动作服务（能力域 8 复用层）：发布事件，不持 escalation ——
	incService := incident.NewService(st.DB, timelineRecorder, bus)

	// —— Webhook 出口（能力域 14）——
	webhookURLs := parseWebhookURLs(cfg.Webhook.OutURLs)
	webhookDisp := webhook.NewDispatcher(webhookURLs)
	if webhookDisp.HasSubscriptions() {
		log.Info("webhook out enabled", zap.Int("subscriptions", len(webhookURLs)))
	}

	// —— IM 协同（能力域 8）：平台适配器 + 账号映射 + 卡片 + 回调 handler ——
	imRegistry, feishuBot, dingtalkBot := buildIMRegistry(cfg)
	imMapper := im.NewMapper(st.DB)
	imCards := im.NewCardStore()
	// 卡片渲染器：按权限渲染按钮（无权不显示，IM 不成权限后门）。
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
	// IM 也作为 notification 通道（升级通知走 IM 卡片送达，IM-first 闭环）。
	notifReg := notifier.Registry() // 注册 IMChannel 到同一 registry（晚注册也能生效，notifier 实时查）
	notifReg.Register(im.NewIMChannel(imRegistry, imCards, func(inc *ent.Incident, targets []notification.Target) string {
		return cfg.IM.OncallChannel // 值班群 channel（空则不发送）
	}))
	logIMStatus(log, feishuBot, dingtalkBot)

	// —— WebSocket hub（能力域 8 状态同步）——
	wsHub := ws.NewHub()

	// —— incident 变更事件订阅：IM 卡片刷新 / Webhook 出口 / WebSocket 推送 ——
	cardRefresher := im.NewCardRefresher(imRegistry, imCards)
	for _, typ := range []domainevent.Type{
		domainevent.IncidentAcked,
		domainevent.IncidentResolved,
		domainevent.IncidentReopened,
		domainevent.IncidentEscalated,
		domainevent.IncidentResponderAdded,
	} {
		bus.Subscribe(typ, cardRefresher.OnIncidentEvent)
		bus.Subscribe(typ, webhookDisp.OnIncidentEvent)
		bus.Subscribe(typ, wsHub.OnIncidentEvent)
	}

	// —— 分诊（能力域 3-4）：创建 Incident 后发事件（escalation 已订阅启动升级链）——
	triageEngine := triage.NewEngine(st.DB, st.Redis)
	triageEngine.SetBus(bus)
	q.Register(triage.TaskTriage, triage.NewWorker(triageEngine).Handle)
	log.Info("pipeline ready (ingestion → triage → escalation → notification)")

	// —— HTTP server + 路由注册 ——
	srv := New(cfg, st)
	public := srv.PublicGroup()
	v1 := srv.APIGroup()
	v1.Use(auth.RequireUser(cfg.Auth.Enabled, identityResolver))

	// 公开路由（自带鉴权，不走 RBAC）
	ingestHandler.Register(public)
	imHandler.Register(public)
	ws.NewHandler(wsHub).Register(public)
	log.Info("websocket ready (/ws/incidents/:id)")
	authHandler := auth.NewAuthHandler(st.DB, jwtSigner)
	authHandler.SetAuditRecorder(auditRecorder)
	authHandler.RegisterPublic(public)

	// 业务路由（受 RequireUser 保护）
	authHandler.RegisterProtected(v1)
	imHandler.RegisterStatus(v1)
	apiKeyHandler := auth.NewAPIKeyHandler(st.DB)
	apiKeyHandler.SetAuditRecorder(auditRecorder)
	apiKeyHandler.Register(v1)
	schedule.NewHandler(schedEngine, st.DB).Register(v1)
	service.NewHandler(st.DB).Register(v1)
	integration.NewHandler(st.DB).Register(v1)
	escalation.NewPolicyHandler(st.DB).Register(v1)
	incident.NewHandler(st.DB, incService).Register(v1)
	rbacHandler := auth.NewHandler(st.DB)
	rbacHandler.SetAuditRecorder(auditRecorder)
	rbacHandler.Register(v1)
	auth.NewUserHandler(st.DB).Register(v1)
	auth.NewTeamHandler(st.DB).Register(v1)
	auth.NewAuditHandler(st.DB).Register(v1)
	// Runbook（能力域 9）：注入时间线 + 升级触发器（包装 incService.Escalate）。
	runbookEngine := runbook.NewEngine(st.DB, runbook.NewRegistry())
	runbookEngine.SetTimelineRecorder(timelineRecorder)
	runbookEngine.SetEscalationTrigger(runbookEscalator{inc: incService})
	runbook.NewHandler(st.DB, runbookEngine).Register(v1)
	timeline.NewHandler(timelineRecorder).Register(v1)
	// 复盘（能力域 12）+ AI 诊断（能力域 11）：共享 GLM provider（成本控制包装）。
	glmProvider := buildGLMProvider(cfg, log, st)
	postmortemEngine := postmortem.NewEngine(st.DB, postmortemLLM(glmProvider, log))
	if glmProvider.Available() {
		postmortemEngine.SetEmbedder(glmProvider) // 知识沉淀：published 复盘计算 embedding
	}
	postmortem.NewHandler(st.DB, postmortemEngine).Register(v1)
	aiDiagEngine := ai.NewDiagnoseEngine(st.DB, glmProvider)
	if st.SQL != nil {
		aiDiagEngine.SetSQLRunner(pgvectorRunner(st.SQL))
	}
	ai.NewHandler(aiDiagEngine).Register(v1)
	analytics.NewHandler(analytics.NewEngine(st.DB)).Register(v1)
	// 通知配置（能力域 7 + 3 抑制）：Rule/Suppression/Template CRUD + dry-run。
	notifHandler := notification.NewHandler(st.DB, notifier, notifAggregator)
	notifHandler.SetTemplateEngine(notifTemplates)
	notifHandler.Register(v1)

	return &Wired{Server: srv, WebhookDispatcher: webhookDisp}, nil
}

// buildNotifier 构造通知分发器（能力域 7），含通道注册、送达记录、聚合、静默、模板。
// 返回 notifier + aggregator + templates（notifHandler 需要后两者做 CRUD/preview）。
func buildNotifier(ctx context.Context, cfg *config.Config, log *zap.Logger, st *store.Store, notifWebhookURLs []string) (*notification.Notifier, *notification.Aggregator, *notification.TemplateEngine) {
	reg := notification.NewRegistry()
	// Webhook 通道（复用出口 webhook 配置 URL）。
	reg.Register(notification.NewWebhookChannel(func(*ent.Incident) []string { return notifWebhookURLs }))
	// 邮件通道（SMTP 配置后真实发送，未配置降级跳过）。
	emailChan := &notification.EmailChannel{
		Config: notification.SMTPConfig{
			Host: cfg.Notification.SMTP.Host, Port: cfg.Notification.SMTP.Port,
			Username: cfg.Notification.SMTP.Username, Password: cfg.Notification.SMTP.Password,
		},
		From:      cfg.Notification.SMTP.From,
		GetEmails: func(targets []notification.Target) []string { return resolveEmails(ctx, st.DB, targets) },
	}
	reg.Register(emailChan)
	if emailChan.Available() {
		log.Info("email channel ready (smtp)")
	}
	// 电话/SMS 通道（占位：转发 webhook 供用户对接云语音 API）。
	phoneChan := &notification.PhoneChannel{
		Config:    notification.VoiceProviderConfig{WebhookURL: cfg.Notification.Phone.WebhookURL, From: cfg.Notification.Phone.From},
		GetPhones: func(targets []notification.Target) []string { return resolvePhones(ctx, st.DB, targets) },
	}
	smsChan := &notification.SMSChannel{
		Config:    notification.VoiceProviderConfig{WebhookURL: cfg.Notification.SMS.WebhookURL, From: cfg.Notification.SMS.From},
		GetPhones: func(targets []notification.Target) []string { return resolvePhones(ctx, st.DB, targets) },
	}
	reg.Register(phoneChan)
	reg.Register(smsChan)
	// 默认通道含 im（IMChannel 在 Wire 后注册，notifier 实时查 registry，晚注册也能生效）。
	defaultChans := []string{"im", "email"}
	if len(notifWebhookURLs) > 0 {
		defaultChans = append([]string{"webhook"}, defaultChans...)
	}
	notifier := notification.NewNotifier(reg, defaultChans)
	// 送达结果记录（当前结构化日志，后续加 Notification 表后落库）。
	notifier.SetResultRecorder(func(incID int, r notification.SendResult) {
		if r.Success {
			log.Info("notification delivered",
				zap.Int("incident", incID), zap.String("channel", r.Channel), zap.String("target", r.Target))
		} else {
			log.Warn("notification failed",
				zap.Int("incident", incID), zap.String("channel", r.Channel),
				zap.String("target", r.Target), zap.String("error", r.Error))
		}
	})
	// 聚合（M7.9）：30s 窗口内对同一 target 合并；critical 不聚合。无 Redis 降级为不聚合。
	notifAggregator := notification.NewAggregator(st.Redis, 30*time.Second)
	notifier.SetAggregator(notifAggregator)
	// 静默时段（M7.8）：按 incident.team 查适用的 NotificationRule.quiet_hours（本期简化：取首条 enabled）。
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
	// 模板系统（M7.5）：内置默认模板 seed + 注入 notifier（渲染失败内部降级兜底）。
	notifTemplates := notification.NewTemplateEngine(st.DB)
	if err := notifTemplates.SeedBuiltinTemplates(ctx); err != nil {
		log.Warn("seed notification templates failed", zap.Error(err))
	} else {
		log.Info("notification templates seeded")
	}
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
	return notifier, notifAggregator, notifTemplates
}

// buildIMRegistry 构造 IM 平台适配器注册表（飞书/钉钉 P0，企微 Noop 待 PoC）。
// 返回 registry + 各 adapter（供日志判断 Available）。
func buildIMRegistry(cfg *config.Config) (*im.Registry, *feishu.Adapter, *dingtalk.Adapter) {
	reg := im.NewRegistry()
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
	reg.Register(feishuBot)
	reg.Register(dingtalkBot)
	reg.Register(im.NewNoopBot("wecom"))
	return reg, feishuBot, dingtalkBot
}

// logIMStatus 记录各 IM 平台就绪状态。
func logIMStatus(log *zap.Logger, feishuBot *feishu.Adapter, dingtalkBot *dingtalk.Adapter) {
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
}

// buildGLMProvider 构造 GLM LLM provider（含成本控制包装：缓存/限流/配额）。
// 无 Redis 时成本控制降级为透传（仅保证调用可达）。
func buildGLMProvider(cfg *config.Config, log *zap.Logger, st *store.Store) ai.Provider {
	p := ai.Provider(ai.NewGLMProvider(cfg.LLM.APIKey, cfg.LLM.Model, cfg.LLM.BaseURL))
	p = ai.NewCostController(p, st.Redis, "org:default", ai.CostConfig{
		CacheTTL:        time.Duration(cfg.LLM.Cost.CacheTTLSeconds) * time.Second,
		DisableCache:    cfg.LLM.Cost.DisableCache,
		RateLimitPerMin: cfg.LLM.Cost.RateLimitPerMin,
		TokenQuota:      cfg.LLM.Cost.TokenQuota,
	})
	if p.Available() {
		log.Info("ai llm ready (glm)")
	} else {
		log.Info("ai llm disabled (no api key), postmortem uses fallback drafts")
	}
	return p
}

// postmortemLLM 把 GLM provider 适配成复盘草稿 LLM（不可用时返回 nil 走兜底）。
func postmortemLLM(glmProvider ai.Provider, log *zap.Logger) postmortem.LLMProvider {
	if !glmProvider.Available() {
		return nil
	}
	return ai.NewPostmortemDraftAdapter(glmProvider)
}

// pgvectorRunner 返回 raw SQL 执行器：供 ai.DiagnoseEngine.FindSimilar 做 pgvector 相似检索。
// 无 pgvector 扩展时 FindSimilar 自动降级回 LIKE 文本匹配。
func pgvectorRunner(sqlDB *sql.DB) func(ctx context.Context, query string, args []any, scan func(*sql.Rows) error) error {
	return func(ctx context.Context, query string, args []any, scan func(*sql.Rows) error) error {
		rows, err := sqlDB.QueryContext(ctx, query, args...)
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
	}
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
