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
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/notificationrule"
	"github.com/kevin/vigil/ent/role"
	"github.com/kevin/vigil/ent/rolebinding"
	"github.com/kevin/vigil/ent/user"
	"github.com/kevin/vigil/internal/ai"
	"github.com/kevin/vigil/internal/analytics"
	"github.com/kevin/vigil/internal/auth"
	"github.com/kevin/vigil/internal/config"
	"github.com/kevin/vigil/internal/errs"
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

	"github.com/labstack/echo/v5"
)

// Wired 装配产物：调用方（main/e2e）生命周期管理需要的句柄。
//
// 只暴露「需要跨域或被关闭逻辑触达」的对象。各域 handler/引擎在 Wire 内部构造后
// 即被路由/事件订阅引用，无需外暴露。Server 是 HTTP 入口，WebhookDispatcher 需要
// 在优雅关闭时 drain 在途推送。Closers 收集需在关闭时停止的后台 goroutine。
type Wired struct {
	Server            *Server
	WebhookDispatcher *webhook.Dispatcher
	// Closers 在优雅关闭时被调用（QA 审计 C3：通知聚合 flush ticker 等）。
	Closers []func()
}

// Close 依次调用所有 closer（幂等，忽略 nil）。
func (w *Wired) Close() {
	for _, c := range w.Closers {
		if c != nil {
			c()
		}
	}
}

// Wire 装配全部组件并挂载路由，返回生命周期句柄。不启动阻塞服务。
//
// ctx 用于装配期初始化（如 resolveEmails 闭包、seed）。调用方传入的 ctx 生命周期
// 应覆盖整个使用期。装配顺序对应架构分层，依赖图通过局部变量声明顺序自然满足。
func Wire(ctx context.Context, cfg *config.Config, log *zap.Logger, st *store.Store, q *queue.Queue, bus *domainevent.Bus) (*Wired, error) {
	// FIX-3：注入全局 logger，使 errs.Internal(c, nil, err) 也能记录详细 err（消除可观测性回归）。
	errs.SetLogger(log)

	// —— 基础设施 seed（鉴权生效前提，幂等）——
	// T0.1：seed 失败必须让装配返回错误、进程退出，而非 Warn 续跑。
	// 内置角色是鉴权生效的前提，seed 失败（非幂等重复）却带病启动会导致所有请求被拒
	// （无角色可授），且探针全绿——比直接崩溃更难排查。SeedBuiltinRoles 内部已把
	// 唯一约束冲突（重复启动）视为幂等跳过，故这里的 err 一定是真失败。
	if err := auth.SeedBuiltinRoles(ctx, st.DB); err != nil {
		return nil, fmt.Errorf("seed builtin roles: %w", err)
	}
	log.Info("builtin roles seeded")

	// —— 登录态：JWT 签发器 + 默认管理员 ——
	jwtSigner := auth.NewJWTSigner(
		cfg.Auth.JWTSecret,
		cfg.Auth.EffectiveAccessTokenTTL(),
		cfg.Auth.EffectiveRefreshTokenTTL(),
	)
	if !jwtSigner.Available() {
		log.Warn("auth jwt secret not set; login disabled (set VIGIL_AUTH_JWT_SECRET)")
	} else {
		// T0.1：seed 失败让装配返回错误、进程退出，而非 Warn 续跑。
		// SeedDefaultAdmin 对「admin 已存在」（唯一约束冲突）返回 (false, nil) 视为幂等，
		// 故这里的 err 一定是真失败（如角色绑定失败），带病启动会让首个管理员无法登录/配置。
		if created, err := auth.SeedDefaultAdmin(ctx, st.DB); err != nil {
			return nil, fmt.Errorf("seed default admin: %w", err)
		} else if created {
			log.Warn("default admin created (username=admin password=changeme) — CHANGE IMMEDIATELY")
		}
	}
	// 身份解析（三轨：JWT/APIKey/X-Vigil-User-ID），中间件用。
	// SEC-02：生产环境禁用 X-Vigil-User-ID 头回退（可伪造），仅承认 JWT/API Key。
	apiKeyVerifier := auth.NewAPIKeyVerifier(st.DB)
	// T0.4：传入 db，使 JWT 分支校验 token_version（改密后旧 token 立即失效）。
	identityResolver := auth.NewIdentityResolver(jwtSigner, apiKeyVerifier, !cfg.App.IsProduction(), st.DB)

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
	schedEngine.SetLogger(log)
	// C4：空班检测告警 team_admin（无 team 归属/无 team_admin 时兜底 org_admin）。
	schedEngine.SetEmptyShiftAlerter(&emptyShiftAlerter{db: st.DB, notifier: notifier, log: log})
	escRedisOpt := &asynq.RedisClientOpt{Addr: cfg.Redis.Addr, Password: cfg.Redis.Password, DB: cfg.Redis.DB}
	timelineRecorder := timeline.NewRecorder(st.DB) // 统一时间线 Recorder
	escEngine := escalation.NewEngine(st.DB, q, schedEngine, notifier, escRedisOpt)
	escEngine.SetRecorder(timelineRecorder)
	escEngine.SetLogger(log)
	// B10：注入事件总线，使自动升级（计时器到点触发）也发布 IncidentEscalated 领域事件，
	// 驱动 WS 推送 / IM 卡片刷新 / 出站 webhook（原先自动升级对多端全盲）。
	escEngine.SetBus(bus)
	q.Register(escalation.TaskEscalation, escEngine.HandleTask)
	// escalation 订阅 incident 事件：ack 取消升级、创建启动链、手动升级触发 level。
	bus.Subscribe(domainevent.IncidentCreated, escEngine.OnCreated)
	bus.Subscribe(domainevent.IncidentAcked, escEngine.OnAcked)
	bus.Subscribe(domainevent.IncidentEscalated, escEngine.OnManualEscalate)
	// reopen（resolved/closed → triggered）后从首层重启升级链：不订阅则 incident 静默停在
	// triggered，不重发通知、不再升级，等于「重开了但没人管」。见 escEngine.OnReopened。
	bus.Subscribe(domainevent.IncidentReopened, escEngine.OnReopened)

	// —— 鉴权（能力域 13）：RBAC + 审计 ——
	authz := auth.NewAuthorizer(st.DB)
	auditRecorder := auth.NewAuditRecorder(st.DB)
	// ARCH-02/SEC-01：资源级鉴权 scope 解析器（资源→team 反查）。
	// 供各业务 handler 经 SetScopeResolver 注入后做资源级判定（checkAccess）。
	scopeResolver := auth.NewScopeResolver(st.DB)

	// —— 事件动作服务（能力域 8 复用层）：发布事件，不持 escalation ——
	incService := incident.NewService(st.DB, timelineRecorder, bus)
	// 操作审计（IncidentAction，审计 B4/B5）：订阅处置事件，每个动作落一条审计记录。
	// 事件驱动——与时间线/通知/升级各订阅方并列，系统自动动作（自动恢复/自动升级）同样留痕（via=automation）。
	incidentActionRecorder := incident.NewActionRecorder(st.DB)
	incidentActionRecorder.Subscribe(bus)

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
	// S9：IM 越权拒绝落审计（IM 是主交互面，越权探测须与 Web 同样留痕）。
	imHandler := im.NewHandler(st.DB, imRegistry, imMapper, authz, incService, imRenderer, imCards, auditRecorder)
	// IM 也作为 notification 通道（升级通知走 IM 卡片送达，IM-first 闭环）。
	notifReg := notifier.Registry() // 注册 IMChannel 到同一 registry（晚注册也能生效，notifier 实时查）
	// FIX-H：OncallChannel 可能仍是占位配置（如 "# 值班群 chat_id..."），
	// 占位值会被 IMChannel 当作真实 channel 尝试发送而失败。识别占位特征后置空降级（不发送）。
	oncall := cfg.IM.OncallChannel
	if oncall == "" || strings.HasPrefix(oncall, "#") ||
		strings.Contains(oncall, "chat_id") || strings.Contains(oncall, "openConversationId") {
		oncall = "" // 占位配置，置空降级（不发送）
	}
	imChannel := im.NewIMChannel(imRegistry, imCards, func(inc *ent.Incident, targets []notification.Target) string {
		return oncall // 值班群 channel（空则不发送）
	})
	// QA 审计 C5：注入渲染器，使通知主路径卡片按接收者权限渲染操作按钮
	// （旧实现主路径卡片零按钮，值班人收到告警只能看不能点，IM 差异化核心失效）。
	imChannel.SetRenderer(imRenderer)
	notifReg.Register(imChannel)
	logIMStatus(log, feishuBot, dingtalkBot)

	// —— WebSocket hub（能力域 8 状态同步）——
	wsHub := ws.NewHub()
	// B11：把 WS hub 注入时间线记录器，使新增时间线条目（升级/自动恢复/runbook 等各域写入）
	// 实时广播 timeline_added，Web 详情页时间线无需轮询即刷新。
	timelineRecorder.SetBroadcaster(wsHub)

	// —— incident 变更事件订阅：IM 卡片刷新 / Webhook 出口 / WebSocket 推送 ——
	// B10：补 IncidentCreated 到多端同步订阅集。原先它只被 escalation 订阅（启动升级链），
	// 未接 ws/webhook/卡片 → 新告警建单后 Web 列表不实时刷新、出站 webhook 感知不到新单。
	// 加入后：incident 创建（triage）、自动升级（escalation 计时器）、自动恢复（triage handleResolved）
	// 三类系统触发的变更也统一驱动 WS 推送 + 出站 webhook + IM 卡片刷新。
	cardRefresher := im.NewCardRefresher(imRegistry, imCards)
	for _, typ := range []domainevent.Type{
		domainevent.IncidentCreated,
		domainevent.IncidentAcked,
		domainevent.IncidentResolved,
		domainevent.IncidentClosed,
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
	// C9：去重/聚合窗口从配置注入（替代硬编码 5min），支持按告警源特性调窗防裂单。
	triageEngine.SetWindows(cfg.Triage.EffectiveDedupWindow(), cfg.Triage.EffectiveAggregateWindow())
	triageEngine.SetBus(bus)
	// B3：注入时间线记录器，使自动恢复（handleResolved）写 status_changed 时间线。
	triageEngine.SetRecorder(timelineRecorder)
	// C3：注入未路由兜底通知器——critical 级 Event 无 Service 匹配时兜底通知 org_admin，
	// 避免高危故障因路由未命中而完全静默（既不建单、不升级、不通知）。
	triageEngine.SetUnroutedNotifier(&unroutedFallbackNotifier{db: st.DB, notifier: notifier, log: log})
	q.Register(triage.TaskTriage, triage.NewWorker(triageEngine).Handle)
	log.Info("pipeline ready (ingestion → triage → escalation → notification)")

	// —— HTTP server + 路由注册 ——
	srv := New(cfg, st)
	public := srv.PublicGroup()
	v1 := srv.APIGroup()

	// QA 审计 C1：路由级 RBAC 守卫。原实现只在 v1 挂 RequireUser（仅身份解析），
	// RequirePermPerRoute 定义了从未被调用——所有写路由对任意登录用户敞开。
	// RouteGuard 按 (method,path) 查权限点命中鉴权，未登记路由保持现状（渐进启用）。
	// 组级中间件先做身份解析 + 强制改密检查（C8），再挂守卫。
	// SEC-02：生产环境强制鉴权（EffectiveEnabled），杜绝业务 API 裸奔。
	routeGuard := auth.NewRouteGuard(authz, identityResolver)
	registerSensitiveRoutePerms(routeGuard)
	v1.Use(auth.RequireUserWithGuard(cfg.Auth.EffectiveEnabled(cfg.App.IsProduction()), identityResolver, forcePasswordGuard(st.DB)))
	v1.Use(routeGuard.Middleware())

	// 公开路由（自带鉴权，不走 RBAC）
	ingestHandler.Register(public)
	imHandler.Register(public)
	// T0.5：WS 端点握手鉴权（?token= JWT + incident.view 团队软隔离）。
	// 仍挂 public 组——鉴权在 handler 内按 query token 完成，RouteGuard 中间件读不到 query，无法复用。
	ws.NewHandler(wsHub, authz, identityResolver, scopeResolver).Register(public)
	log.Info("websocket ready (/ws/incidents/:id)")
	authHandler := auth.NewAuthHandler(st.DB, jwtSigner)
	authHandler.SetAuditRecorder(auditRecorder)
	// SEC-04：登录限流/锁定（无 Redis 时降级跳过，依赖审计日志事后追溯）。
	authHandler.SetLoginGuard(auth.NewLoginGuard(st.Redis))
	authHandler.RegisterPublic(public)
	// 测试专用 reset 端点：仅 development 环境挂载（生产禁用，零暴露）。
	// 供前端 Playwright e2e 在每个 spec 前清空数据，保证用例隔离。
	if !cfg.App.IsProduction() {
		srv.registerTestReset()
		log.Info("test reset endpoint enabled (development only) at /api/v1/__test__/reset")
	}

	// 业务路由（受 RequireUser + RouteGuard 保护）
	authHandler.RegisterProtected(v1)
	imHandler.RegisterStatus(v1)
	apiKeyHandler := auth.NewAPIKeyHandler(st.DB)
	apiKeyHandler.SetAuditRecorder(auditRecorder)
	apiKeyHandler.Register(v1)
	scheduleH := schedule.NewHandler(schedEngine, st.DB)
	scheduleH.SetAuthorizer(authz)
	scheduleH.SetScopeResolver(scopeResolver)
	scheduleH.Register(v1)
	serviceH := service.NewHandler(st.DB)
	serviceH.SetAuthorizer(authz)
	serviceH.SetScopeResolver(scopeResolver)
	serviceH.Register(v1)
	integrationH := integration.NewHandler(st.DB)
	integrationH.SetAuthorizer(authz)
	integrationH.SetScopeResolver(scopeResolver)
	integrationH.SetAuditRecorder(auditRecorder) // C21：接入点配置变更留痕
	integrationH.Register(v1)
	escalationH := escalation.NewPolicyHandler(st.DB)
	escalationH.SetAuthorizer(authz)
	escalationH.SetScopeResolver(scopeResolver)
	escalationH.Register(v1)
	incidentH := incident.NewHandler(st.DB, incService)
	incidentH.SetActionRecorder(incidentActionRecorder) // GET /incidents/:id/actions 复用同一审计器
	incidentH.SetAuthorizer(authz)
	incidentH.SetScopeResolver(scopeResolver)
	incidentH.Register(v1)
	// M6：未路由 Event 人工重路由端点（POST /events/:id/reroute），复用分诊引擎聚合/建单。
	triageH := triage.NewHandler(st.DB, triageEngine)
	triageH.SetAuthorizer(authz)
	triageH.SetScopeResolver(scopeResolver)
	triageH.Register(v1)
	rbacHandler := auth.NewHandler(st.DB)
	rbacHandler.SetAuditRecorder(auditRecorder)
	rbacHandler.Register(v1)
	// QA 审计 C6：UserHandler 注入 IM 账号绑定器/查询器（imMapper 实现），
	// 补齐 POST /users/:id/im-accounts 绑定端点（原 Mapper.BindAccount 全仓 0 调用方）。
	userHandler := auth.NewUserHandler(st.DB)
	// 审计 S2：注入鉴权器，使 PATCH /users/:id 改 status 时叠加 user.disable 校验。
	userHandler.SetAuthorizer(authz)
	userHandler.SetAuditRecorder(auditRecorder) // C21：用户启停留痕
	userHandler.SetIMAccountBinder(imMapper)
	userHandler.SetIMAccountResolver(imMapperResolver{m: imMapper})
	userHandler.Register(v1)
	// T2.7：TeamHandler 注入审计器（成员增删留痕）+ 鉴权器（成员管理团队软隔离，跨团队拒）。
	teamHandler := auth.NewTeamHandler(st.DB)
	teamHandler.SetAuditRecorder(auditRecorder)
	teamHandler.SetAuthorizer(authz)
	teamHandler.Register(v1)
	auth.NewAuditHandler(st.DB).Register(v1)
	// Runbook（能力域 9）：注入时间线 + 升级触发器（包装 incService.Escalate）。
	runbookEngine := runbook.NewEngine(st.DB, runbook.NewRegistry())
	runbookEngine.SetTimelineRecorder(timelineRecorder)
	runbookEngine.SetEscalationTrigger(runbookEscalator{inc: incService})
	// 并发保护（C.5.1 / audit S10）：(runbook, incident) 维度执行锁，防连点/并发重复触发写步骤。
	// 无 Redis 时降级为无锁（TTL 用默认兜底值）。
	runbookEngine.SetRedis(st.Redis, 0)
	runbookH := runbook.NewHandler(st.DB, runbookEngine)
	runbookH.SetAuthorizer(authz)
	runbookH.SetScopeResolver(scopeResolver)
	runbookH.SetAuditRecorder(auditRecorder) // S10/C14：Runbook 执行留痕
	runbookH.Register(v1)
	timelineH := timeline.NewHandler(timelineRecorder)
	timelineH.SetAuthorizer(authz)
	timelineH.SetScopeResolver(scopeResolver)
	timelineH.Register(v1)
	// 复盘（能力域 12）+ AI 诊断（能力域 11）：共享 GLM provider（成本控制包装）。
	glmProvider := buildGLMProvider(cfg, log, st)
	postmortemEngine := postmortem.NewEngine(st.DB, postmortemLLM(glmProvider, log))
	if glmProvider.Available() {
		postmortemEngine.SetEmbedder(glmProvider) // 知识沉淀：published 复盘计算 embedding
	}
	// 复盘发布 → 关联 incident 推进到 closed 终态（收口闭环）。
	// 走 incService.Close（同一状态机/时间线/领域事件），复盘不反向依赖 incident 包。
	postmortemEngine.SetIncidentCloser(incidentCloser{inc: incService})
	postmortemH := postmortem.NewHandler(st.DB, postmortemEngine)
	postmortemH.SetAuthorizer(authz)
	postmortemH.SetScopeResolver(scopeResolver)
	postmortemH.Register(v1)
	aiDiagEngine := ai.NewDiagnoseEngine(st.DB, glmProvider)
	aiDiagEngine.SetRecorder(timelineRecorder) // 诊断产出 AI 洞察后写 ai_insight 时间线（原先零写入）
	if st.SQL != nil {
		aiDiagEngine.SetSQLRunner(pgvectorRunner(st.SQL))
	}
	// T3.2 分诊 AI：与诊断链共享 GLM provider + 时间线记录器，复用相似检索（dedup 建议）。
	// 建单后异步触发（不阻塞分诊），也经 POST /incidents/:id/triage-ai 手动触发。
	triageAIEngine := ai.NewTriageAIEngine(st.DB, glmProvider)
	triageAIEngine.SetRecorder(timelineRecorder)  // 产出建议后写 ai_insight 时间线
	triageAIEngine.SetSimilarFinder(aiDiagEngine) // dedup 建议复用诊断链的 pgvector/LIKE 相似检索
	// 注入分诊引擎：新建 Incident 后异步跑 severity/dedup 建议（analyzer 内部 LLM 不可用自行降级）。
	triageEngine.SetAIAnalyzer(triageAIAnalyzerAdapter{e: triageAIEngine})

	// T3.3 处置 Copilot：与诊断链共享 GLM provider + 时间线记录器，复用相似检索（runbook 推荐）。
	// 经 POST /incidents/:id/ai-copilot 手动触发。★ 安全红线：推荐仅呈现/高亮，accept 不触发执行——
	// 执行仍走 Runbook 两档安全（写操作 require_approval），AI 推荐不绕过审批。
	copilotEngine := ai.NewCopilotEngine(st.DB, glmProvider)
	copilotEngine.SetRecorder(timelineRecorder)  // 产出建议后写 ai_insight 时间线
	copilotEngine.SetSimilarFinder(aiDiagEngine) // runbook 推荐复用诊断链的相似检索（取历史处置痕迹）

	aiH := ai.NewHandler(aiDiagEngine)
	aiH.SetTriageAI(triageAIEngine) // 启用手动触发端点（T3.2）
	aiH.SetCopilot(copilotEngine)   // 启用处置 Copilot 手动触发端点（T3.3）
	aiH.SetAuthorizer(authz)
	aiH.SetScopeResolver(scopeResolver)
	aiH.SetAuditRecorder(auditRecorder) // S11：AI 建议采纳/拒绝留痕（谁在何时改判）
	aiH.Register(v1)
	// 报表（能力域 15）：注入 authz 启用团队 scope 数据隔离（S14）——
	// team 级 Leader 只见本团队指标，org 级角色看全组织。
	analytics.NewHandler(analytics.NewEngine(st.DB)).SetAuthorizer(authz).Register(v1)
	// 通知配置（能力域 7 + 3 抑制）：Rule/Suppression/Template CRUD + dry-run。
	notifHandler := notification.NewHandler(st.DB, notifier, notifAggregator)
	notifHandler.SetTemplateEngine(notifTemplates)
	notifHandler.SetAuthorizer(authz)
	notifHandler.SetScopeResolver(scopeResolver)
	notifHandler.Register(v1)

	// QA 审计 C3：通知聚合 flush ticker。原实现 FlushAggregated 从未被调用 → 非 critical
	// 聚合通知成死信（永滞 Redis）。周期扫 pending targets 合并发送，间隔 ≤ 聚合窗口。
	flushCtx, flushCancel := context.WithCancel(ctx)
	wired := &Wired{Server: srv, WebhookDispatcher: webhookDisp}
	if notifAggregator != nil && st.Redis != nil {
		interval := notifAggregator.Window() / 2
		if interval <= 0 {
			interval = 15 * time.Second
		}
		go runAggregationFlusher(flushCtx, notifier, interval, log)
		wired.Closers = append(wired.Closers, flushCancel)
		log.Info("notification aggregation flusher started", zap.Duration("interval", interval))
	} else {
		flushCancel()
	}
	return wired, nil
}

// runAggregationFlusher 周期扫描 pending 聚合队列并 flush 合并发送。
// ctx 取消时退出（纳入优雅关闭）。单次 FlushAll 失败不中断 ticker（仅记日志）。
func runAggregationFlusher(ctx context.Context, n *notification.Notifier, interval time.Duration, log *zap.Logger) {
	// 启动后先等一个窗口再首次扫描（让首批通知在窗口内聚合，避免过早 flush 空跑）
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			// 关闭前最后 flush 一次，尽量不丢在途通知
			_, _ = n.FlushAll(context.Background())
			return
		case <-timer.C:
			flushed, err := n.FlushAll(ctx)
			if err != nil {
				log.Warn("aggregation flush error", zap.Error(err))
			} else if flushed > 0 {
				log.Info("aggregation flushed", zap.Int("targets", flushed))
			}
			timer.Reset(interval)
		}
	}
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
	// 默认降级链（B8/C12）：IM 优先（IM-first），失败降级邮件，再降级电话/短信（强打扰兜底）。
	// 顺序即「主通道失败才启用下一通道」的降级层次，而非并联各发一份。
	// IMChannel 在 Wire 后注册，notifier 实时查 registry，晚注册也能生效。
	// 注：electricity/短信仅在配了云语音 webhook 时可用（未配则 Send 返回空，链自动跳过）。
	defaultChans := []string{"im", "email", "phone", "sms"}
	if len(notifWebhookURLs) > 0 {
		defaultChans = append([]string{"webhook"}, defaultChans...)
	}
	notifier := notification.NewNotifier(reg, defaultChans)
	// 送达结果记录（结构化日志/metrics 层）。
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
	// 送达三态落库（B22/M13）：每次 sent/failed/suppressed 落一条 Notification，
	// 使被静默/失败的通知可查、可补发，全通道失败可被兜底告警发现。
	notifier.SetDeliveryRecorder(notification.NewDeliveryRecorder(st.DB))
	// 通知规则评估（B7/C12）：按 incident 的 condition 匹配规则，取其 channels/template/quiet_hours。
	notifier.SetRuleResolver(notification.NewRuleResolver(st.DB))
	// 全通道失败兜底告警（B22）：某 target 整条降级链全失败时，兜底通知 org_admin（走非 IM 通道）。
	// 复用 unroutedFallbackNotifier 的 org_admin 解算能力，避免高危故障静默无人知。
	notifier.SetAllFailedHook(buildAllFailedHook(ctx, st.DB, notifier, log))
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

// unroutedFallbackNotifier 实现 triage.UnroutedNotifier（C3）。
//
// 未路由的 critical Event 无 Service/Incident，无法走升级链——本适配器解算 org_admin
// 收件人，用 Notifier.NotifyUnrouted 走 email/phone/sms/webhook 兜底通知（不走 IM 卡片，
// 无单可渲染）。org_admin 是"全组织可见"角色，无 Service 归属的漏网告警交给他们兜底最合理。
type unroutedFallbackNotifier struct {
	db       *ent.Client
	notifier *notification.Notifier
	log      *zap.Logger
}

// NotifyUnroutedCritical 兜底通知 org_admin：critical Event 路由未命中时调用。
func (u *unroutedFallbackNotifier) NotifyUnroutedCritical(ctx context.Context, evt *ent.Event) error {
	admins, err := u.resolveOrgAdmins(ctx)
	if err != nil {
		return fmt.Errorf("resolve org admins: %w", err)
	}
	if len(admins) == 0 {
		// 无 org_admin 可通知是配置缺陷（应至少有一个内置管理员），记 warn 便于运维发现。
		u.log.Warn("unrouted critical: no org_admin to notify", zap.Int("event_id", evt.ID))
		return nil
	}
	targets := make([]notification.Target, 0, len(admins))
	for _, a := range admins {
		targets = append(targets, notification.Target{UserID: a.ID, Name: a.Name, Source: "user"})
	}
	title := fmt.Sprintf("[CRITICAL] 未路由告警：%s", evt.Summary)
	summary := fmt.Sprintf("收到 critical 告警但无匹配 Service，已入未路由池待人工分诊。dedup_key=%s。请尽快确认归属或手动建单。", evt.DedupKey)
	return u.notifier.NotifyUnrouted(ctx, targets, title, summary, nil)
}

// resolveOrgAdmins 解算持有 org 级 org_admin 角色绑定的在职用户（去重）。
func (u *unroutedFallbackNotifier) resolveOrgAdmins(ctx context.Context) ([]*ent.User, error) {
	now := time.Now()
	bindings, err := u.db.RoleBinding.Query().
		Where(
			rolebinding.HasRoleWith(role.NameEQ("org_admin")),
			rolebinding.ScopeLevelEQ(rolebinding.ScopeLevelOrg),
			rolebinding.Or(rolebinding.ExpiresAtIsNil(), rolebinding.ExpiresAtGTE(now)),
		).
		WithUser().
		All(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[int]bool{}
	var out []*ent.User
	for _, b := range bindings {
		usr := b.Edges.User
		if usr == nil || seen[usr.ID] || usr.Status != user.StatusActive {
			continue
		}
		seen[usr.ID] = true
		out = append(out, usr)
	}
	return out, nil
}

// buildAllFailedHook 构造「整条降级链对某 target 全失败」时的兜底告警回调（B22）。
//
// 场景：某响应者的 IM/邮件/电话/短信全部发送失败（网络/配置故障），若无兜底则该事件
// 「没人被通知到」却静默无人知——这是升级链兜底失效的严重信号。本回调解算 org_admin，
// 走 NotifyUnrouted（非 IM 通道，不再递归触发本 hook）发一条「通知投递失败」告警，
// 使运维能第一时间发现「有事件通知发不出去」。
//
// best-effort：兜底告警本身失败只记日志，不再向上抛（避免故障放大）。
func buildAllFailedHook(_ context.Context, db *ent.Client, notifier *notification.Notifier, log *zap.Logger) func(context.Context, *ent.Incident, notification.Target, string, string) {
	resolver := &unroutedFallbackNotifier{db: db, notifier: notifier, log: log}
	return func(cbCtx context.Context, inc *ent.Incident, failed notification.Target, title, _ string) {
		if inc == nil {
			return
		}
		admins, err := resolver.resolveOrgAdmins(cbCtx)
		if err != nil || len(admins) == 0 {
			log.Warn("notification all-failed fallback: no org_admin to alert",
				zap.Int("incident", inc.ID), zap.String("failed_target", failed.Name), zap.Error(err))
			return
		}
		targets := make([]notification.Target, 0, len(admins))
		for _, a := range admins {
			targets = append(targets, notification.Target{UserID: a.ID, Name: a.Name, Source: "user"})
		}
		alertTitle := fmt.Sprintf("[通知投递失败] %s", inc.Number)
		alertSummary := fmt.Sprintf("事件 %s 对 %s 的全部通知通道（IM/邮件/电话/短信）均投递失败，请核查通道配置并手动跟进。原通知：%s",
			inc.Number, failed.Name, title)
		if err := notifier.NotifyUnrouted(cbCtx, targets, alertTitle, alertSummary, nil); err != nil {
			log.Warn("notification all-failed fallback alert failed",
				zap.Int("incident", inc.ID), zap.Error(err))
		}
	}
}

// emptyShiftAlerter 实现 schedule.EmptyShiftAlerter（C4）。
//
// 排班在某时刻算不出任何在班人（空班）= 无人值班的严重信号。本适配器解算该 Schedule
// 所属 team 的 team_admin 收件人，走 NotifyUnrouted（email/phone/sms/webhook，无单不走 IM 卡片）
// 告警，避免"无人值班"盲区。schedule 无 team 归属或该 team 无 team_admin 时，兜底告警 org_admin。
type emptyShiftAlerter struct {
	db       *ent.Client
	notifier *notification.Notifier
	log      *zap.Logger
}

// AlertEmptyShift 空班告警：best-effort，失败只记日志不向上抛（避免故障放大）。
func (a *emptyShiftAlerter) AlertEmptyShift(ctx context.Context, sched *ent.Schedule, at time.Time) {
	recipients := a.resolveTeamAdmins(ctx, sched)
	if len(recipients) == 0 {
		// 该 team 无 team_admin（或无 team 归属）：兜底告警 org_admin。
		fallback := &unroutedFallbackNotifier{db: a.db, notifier: a.notifier, log: a.log}
		admins, err := fallback.resolveOrgAdmins(ctx)
		if err != nil || len(admins) == 0 {
			a.log.Warn("empty shift alert: no team_admin/org_admin to notify",
				zap.Int("schedule_id", sched.ID), zap.Error(err))
			return
		}
		recipients = admins
	}
	targets := make([]notification.Target, 0, len(recipients))
	for _, u := range recipients {
		targets = append(targets, notification.Target{UserID: u.ID, Name: u.Name, Source: "user"})
	}
	title := fmt.Sprintf("[排班空班] %s", sched.Name)
	summary := fmt.Sprintf("排班「%s」在 %s 算不出任何在班人（空班/无人值班），请检查轮换参与人是否全部缺席/禁用，或补建 Override 换班。",
		sched.Name, at.Format(time.RFC3339))
	if err := a.notifier.NotifyUnrouted(ctx, targets, title, summary, nil); err != nil {
		a.log.Warn("empty shift alert failed",
			zap.Int("schedule_id", sched.ID), zap.Error(err))
	}
}

// resolveTeamAdmins 解算该 Schedule 所属 team 的在职 team_admin（去重）。
// schedule 无 team 归属时返回空（交由调用方兜底 org_admin）。
func (a *emptyShiftAlerter) resolveTeamAdmins(ctx context.Context, sched *ent.Schedule) []*ent.User {
	t, err := sched.QueryTeam().Only(ctx)
	if err != nil || t == nil {
		return nil // 无 team 归属
	}
	now := time.Now()
	bindings, err := a.db.RoleBinding.Query().
		Where(
			rolebinding.HasRoleWith(role.NameEQ("team_admin")),
			rolebinding.ScopeLevelEQ(rolebinding.ScopeLevelTeam),
			rolebinding.TeamIDEQ(strconv.Itoa(t.ID)),
			rolebinding.Or(rolebinding.ExpiresAtIsNil(), rolebinding.ExpiresAtGTE(now)),
		).
		WithUser().
		All(ctx)
	if err != nil {
		a.log.Warn("empty shift alert: query team_admins failed",
			zap.Int("team_id", t.ID), zap.Error(err))
		return nil
	}
	seen := map[int]bool{}
	var out []*ent.User
	for _, b := range bindings {
		usr := b.Edges.User
		if usr == nil || seen[usr.ID] || usr.Status != user.StatusActive {
			continue
		}
		seen[usr.ID] = true
		out = append(out, usr)
	}
	return out
}

// runbookEscalator 实现 runbook.EscalationTrigger，包装 incident.Service.Escalate。
// on_failure=escalate 时触发该 incident 的立即升级；actorID 透传自 Runbook 执行发起人
// （0 视为系统），让"谁触发的这次 Runbook 升级"可追溯（source=runbook）。
type runbookEscalator struct {
	inc *incident.Service
}

func (r runbookEscalator) Trigger(ctx context.Context, incID int, reason string, actorID int) error {
	_, err := r.inc.Escalate(ctx, incID, actorID, incident.SourceRunbook)
	return err
}

// incidentCloser 实现 postmortem.IncidentCloser，包装 incident.Service.Close。
// 复盘发布（→published）时联动把关联 incident 从 resolved 推进到 closed 终态。
// 幂等：已 closed（ErrAlreadyClosed）视为成功无操作——复盘可能被重复发布/联动，
// 不该因「已收口」把幂等场景当失败上抛，避免 best-effort 联动误记错误日志。
type incidentCloser struct {
	inc *incident.Service
}

func (c incidentCloser) Close(ctx context.Context, incID int, actorID int) error {
	_, err := c.inc.Close(ctx, incID, actorID, incident.SourceSystem)
	if errors.Is(err, incident.ErrAlreadyClosed) {
		return nil // 已 closed：幂等成功
	}
	return err
}

// triageAIAnalyzerAdapter 把 *ai.TriageAIEngine 适配成 triage.TriageAIAnalyzer（T3.2）。
// triage 侧接口只关心 error（异步 best-effort 记日志），丢弃 *ai.TriageResult 产出——
// 产出由 AIInsight 持久化，前端经 GET /incidents/:id/insights 拉取，无需异步回传。
// 适配器让 triage 无需依赖 ai 包（解耦，与 UnroutedNotifier 同款）。
type triageAIAnalyzerAdapter struct {
	e *ai.TriageAIEngine
}

func (a triageAIAnalyzerAdapter) AnalyzeIncident(ctx context.Context, incID int) error {
	_, err := a.e.AnalyzeIncident(ctx, incID)
	return err
}

// imMapperResolver 把 im.Mapper 适配成 auth.IMAccountResolver（QA 审计 C6）。
// im.Mapper.ListBindings 返回 []im.IMBindingView，这里转成 []auth.IMAccountInfo。
type imMapperResolver struct {
	m *im.Mapper
}

func (r imMapperResolver) ListBindings(ctx context.Context, userID int) ([]auth.IMAccountInfo, error) {
	views, err := r.m.ListBindings(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]auth.IMAccountInfo, 0, len(views))
	for _, v := range views {
		out = append(out, auth.IMAccountInfo{Platform: v.Platform, AccountID: v.AccountID})
	}
	return out, nil
}

// registerSensitiveRoutePerms 登记敏感写路由的权限点（QA 审计 C1 RBAC 接线）。
// 按 (method, path) 注册到 RouteGuard，命中则鉴权。路径与各 handler Register 中的定义一致。
// 未登记的写路由保持现状（渐进启用）；本轮覆盖审计点名的越权风险面：
// 角色管理 / API Key / incident 操作 / 排班 / 复盘删除 / runbook 等。
func registerSensitiveRoutePerms(g *auth.RouteGuard) {
	// 角色与角色绑定（M13.3）—— 最敏感：能自授任意角色。
	// 读端点也登记（审计 S2）：角色/绑定关系暴露组织权限结构，仅 role.view 可见。
	g.RoutePerm(http.MethodGet, "/roles", auth.PermRoleView)
	g.RoutePerm(http.MethodPost, "/roles", auth.PermRoleCreate)
	// T2.7/M2：编辑角色权限集/名称（内置角色 handler 内拒改）。
	g.RoutePerm(http.MethodPatch, "/roles/:id", auth.PermRoleUpdate)
	g.RoutePerm(http.MethodDelete, "/roles/:id", auth.PermRoleDelete)
	g.RoutePerm(http.MethodGet, "/role-bindings", auth.PermRoleView)
	g.RoutePerm(http.MethodPost, "/role-bindings", auth.PermRoleAssign)
	g.RoutePerm(http.MethodDelete, "/role-bindings/:id", auth.PermRoleAssign)
	// 用户管理（M13.1）—— 审计 S2：原仅登录态，任意用户可枚举全员并改他人（含停用）。
	// GET /users 暴露全员名录 → user.view；PATCH /users/:id 改名/时区/启停 → user.update
	//（改 status=disabled 时 handler 内再叠加 user.disable，见 auth.UserHandler.updateUser）。
	g.RoutePerm(http.MethodGet, "/users", auth.PermUserView)
	// T2.6/M1：建用户（user.create）；管理员重置他人密码（user.update，重置后吊销旧 token）。
	g.RoutePerm(http.MethodPost, "/users", auth.PermUserCreate)
	g.RoutePerm(http.MethodPatch, "/users/:id", auth.PermUserUpdate)
	g.RoutePerm(http.MethodPost, "/users/:id/reset-password", auth.PermUserUpdate)
	// API Key（M13.7）—— 审计点名：原不限 org_admin，任何人可签发。
	g.RoutePerm(http.MethodPost, "/api-keys", auth.PermAdminAPIKeyManage)
	g.RoutePerm(http.MethodDelete, "/api-keys/:id", auth.PermAdminAPIKeyManage)
	// 审计日志查看（M13.5）
	g.RoutePerm(http.MethodGet, "/audit-logs", auth.PermAdminAuditView)
	// incident 生命周期操作（M6.5 手动升级等）
	g.RoutePerm(http.MethodPost, "/incidents/:id/ack", auth.PermIncidentAck)
	g.RoutePerm(http.MethodPost, "/incidents/:id/resolve", auth.PermIncidentResolve)
	g.RoutePerm(http.MethodPost, "/incidents/:id/close", auth.PermIncidentClose)
	g.RoutePerm(http.MethodPost, "/incidents/:id/escalate", auth.PermIncidentEscalate)
	g.RoutePerm(http.MethodPost, "/incidents/:id/reopen", auth.PermIncidentReopen)
	// 排班写操作 + override（M5.3）
	g.RoutePerm(http.MethodPost, "/schedules", auth.PermScheduleCreate)
	g.RoutePerm(http.MethodPatch, "/schedules/:id", auth.PermScheduleUpdate)
	g.RoutePerm(http.MethodDelete, "/schedules/:id", auth.PermScheduleDelete)
	// 换班 Override（C5/M5.3）：创建/删除需 schedule.override（换他人再叠加 schedule.update，见 handler）。
	g.RoutePerm(http.MethodPost, "/schedules/:id/overrides", auth.PermScheduleOverride)
	g.RoutePerm(http.MethodDelete, "/schedules/:id/overrides/:oid", auth.PermScheduleOverride)
	// 升级策略
	g.RoutePerm(http.MethodPost, "/escalation-policies", auth.PermEscalationCreate)
	g.RoutePerm(http.MethodPatch, "/escalation-policies/:id", auth.PermEscalationUpdate)
	g.RoutePerm(http.MethodDelete, "/escalation-policies/:id", auth.PermEscalationDelete)
	// Runbook（M9 写操作安全护栏）
	g.RoutePerm(http.MethodPost, "/runbooks", auth.PermRunbookCreate)
	g.RoutePerm(http.MethodPatch, "/runbooks/:id", auth.PermRunbookUpdate)
	g.RoutePerm(http.MethodDelete, "/runbooks/:id", auth.PermRunbookDelete)
	g.RoutePerm(http.MethodPost, "/runbooks/:id/execute", auth.PermRunbookExecute)
	// 复盘删除 + 状态流转 + 改进项（M12）
	g.RoutePerm(http.MethodDelete, "/postmortems/:id", auth.PermPostmortemUpdate)
	g.RoutePerm(http.MethodPatch, "/postmortems/:id/transition", auth.PermPostmortemPublish)
	// AI 建议采纳/拒绝（S11 处置级，非只读 incident.view，subscriber 不得改判）
	g.RoutePerm(http.MethodPost, "/ai-insights/:id/resolve", auth.PermAIInsightResolve)
	// 服务目录写（M13.4）
	g.RoutePerm(http.MethodPost, "/services", auth.PermServiceCreate)
	g.RoutePerm(http.MethodPatch, "/services/:id", auth.PermServiceUpdate)
	g.RoutePerm(http.MethodDelete, "/services/:id", auth.PermServiceDelete)
	// 未路由 Event 重路由（M6）—— 手动指派 Service，需路由改写权限（service.route_override）。
	// 资源级 scope 在 handler 内按目标 service 反查 team 校验（团队软隔离）。
	g.RoutePerm(http.MethodPost, "/events/:id/reroute", auth.PermServiceRouteOverride)
	// 接入点写（含 token 生成，M1.5）
	g.RoutePerm(http.MethodPost, "/integrations", auth.PermIntegrationCreate)
	g.RoutePerm(http.MethodPatch, "/integrations/:id", auth.PermIntegrationUpdate)
	g.RoutePerm(http.MethodDelete, "/integrations/:id", auth.PermIntegrationDelete)
	// 团队写（M13.2）
	g.RoutePerm(http.MethodPost, "/teams", auth.PermTeamCreate)
	g.RoutePerm(http.MethodPatch, "/teams/:id", auth.PermTeamUpdate)
	g.RoutePerm(http.MethodDelete, "/teams/:id", auth.PermTeamDelete)
	// 团队成员增删（M3 / S15，T2.7）—— team.member.manage 悬空点落地。
	g.RoutePerm(http.MethodPost, "/teams/:id/members", auth.PermTeamMemberManage)
	g.RoutePerm(http.MethodDelete, "/teams/:id/members/:uid", auth.PermTeamMemberManage)
	// IM 账号绑定（M8.6 / M13.1，QA 审计 C6）
	g.RoutePerm(http.MethodPost, "/users/:id/im-accounts", auth.PermUserIMBind)
	// 通知规则 / 抑制规则 / 模板写
	g.RoutePerm(http.MethodPost, "/notification-rules", auth.PermNotificationRuleCreate)
	g.RoutePerm(http.MethodPatch, "/notification-rules/:id", auth.PermNotificationRuleUpdate)
	g.RoutePerm(http.MethodDelete, "/notification-rules/:id", auth.PermNotificationRuleDelete)
	g.RoutePerm(http.MethodPost, "/suppression-rules", auth.PermSuppressionCreate)
	g.RoutePerm(http.MethodPatch, "/suppression-rules/:id", auth.PermSuppressionUpdate)
	g.RoutePerm(http.MethodDelete, "/suppression-rules/:id", auth.PermSuppressionDelete)
	g.RoutePerm(http.MethodPost, "/notification-templates", auth.PermNotificationTemplateCreate)
	g.RoutePerm(http.MethodPatch, "/notification-templates/:id", auth.PermNotificationTemplateUpdate)
	g.RoutePerm(http.MethodDelete, "/notification-templates/:id", auth.PermNotificationTemplateDelete)
	// 分析报表（能力域 11）—— 审计 S14：统一挂 analytics.view 拦截未授权用户。
	// 团队 scope 数据隔离已实现（handler.resolveScope → engine 按可见 team 过滤）：
	// org 级角色看全组织，team 级 Leader（team_admin/responder_lead 持 analytics.view）仅见本团队。
	g.RoutePerm(http.MethodGet, "/analytics/dashboard", auth.PermAnalyticsView)
	g.RoutePerm(http.MethodGet, "/analytics/alerts", auth.PermAnalyticsView)
	g.RoutePerm(http.MethodGet, "/analytics/incidents", auth.PermAnalyticsView)
	g.RoutePerm(http.MethodGet, "/analytics/team-load", auth.PermAnalyticsView)
	g.RoutePerm(http.MethodGet, "/analytics/postmortems", auth.PermAnalyticsView)
	g.RoutePerm(http.MethodGet, "/analytics/trend", auth.PermAnalyticsView)
}

// forcePasswordGuard 强制改密守卫（QA 审计 C8 / H1.6）。
// 用户 must_change_password=true 时，仅放行改密端点，其余业务 API 返回 403
// 引导前端走改密流程，杜绝 admin/changeme 长期可用。
func forcePasswordGuard(db *ent.Client) auth.UserGuard {
	return func(c *echo.Context, uid int) (bool, int, string) {
		// echo v5 的 c.Path() 含 group 前缀（/api/v1/...），去前缀后比对放行清单
		// （与 RouteGuard.lookupPerm 同款修正：原写法比对的是相对路径，永远不匹配 →
		// 改密端点本身也会被守卫拦死，导致改密闭环无法走通）。
		path := strings.TrimPrefix(c.Path(), "/api/v1")
		if path == "/auth/change-password" || path == "/auth/me" || path == "/health" {
			return true, 0, ""
		}
		u, err := db.User.Get(c.Request().Context(), uid)
		if err != nil {
			// 查不到用户不在此处阻断（交给后续 handler 处理），避免误伤
			return true, 0, ""
		}
		if u.MustChangePassword {
			return false, http.StatusForbidden, "must_change_password"
		}
		return true, 0, ""
	}
}
