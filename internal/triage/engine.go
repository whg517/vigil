// Package triage 实现能力域 3-4：分诊降噪与路由。
//
// 设计见 ADR-0012（三层分诊管线）与 ADR-0013（确定性路由）：
// · 去重 —— Redis dedup 窗口内重复 Event 丢弃
// · 路由 —— Event labels 匹配 Service，未命中入 unrouted 池
// · 相关性聚合 —— 同 service+severity 在时间窗口内聚合成一个 Incident
//
// 本包接收 ingestion 产出的 Event，产出 Incident（人介入的对象）。
package triage

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"regexp"
	"sort"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/event"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/service"
	"github.com/kevin/vigil/ent/team"
	"github.com/kevin/vigil/ent/timelineitem"
	domainevent "github.com/kevin/vigil/internal/event"
	"github.com/kevin/vigil/internal/metrics"
	"github.com/kevin/vigil/internal/timeline"

	"github.com/redis/go-redis/v9"
)

// Engine 分诊引擎：去重 + 抑制 + 路由 + 聚合。
type Engine struct {
	db    *ent.Client
	redis *redis.Client

	// dedupWindow 去重窗口（同 dedup_key 在窗口内重复直接丢弃）
	dedupWindow time.Duration
	// aggregateWindow 聚合窗口（同 service+severity 在窗口内并入同一 Incident）
	aggregateWindow time.Duration

	// suppression 抑制规则评估器（能力域 3 M3.2）。为 nil 时跳过抑制评估（降级）。
	suppression *SuppressionEngine

	// bus 领域事件总线。创建 Incident 后发布 IncidentCreated 事件，
	// 由 escalation 等订阅方启动升级链（替代原先的 OnIncidentCreated 回调，解耦）。
	// 为 nil 时跳过发布（降级/测试）。
	bus *domainevent.Bus

	// recorder 时间线记录器。B3：自动恢复（handleResolved）解决 Incident 时写 status_changed
	// 时间线（每次状态变更必产 TimelineItem 铁律）。为 nil 时跳过（降级/测试）。
	recorder *timeline.Recorder

	// unroutedNotifier 未路由兜底通知器（C3）。路由未命中且 severity=critical 时调用，
	// 兜底通知 org_admin，避免 critical 告警因无 Service 匹配而静默无人知。
	// 普通严重度不兜底（unrouted 只标记待人工分诊，不打扰管理员）。
	// 为 nil 时跳过（降级/测试）——保持"无配置不回归"。
	unroutedNotifier UnroutedNotifier

	// aiAnalyzer 分诊 AI 分析器（T3.2）。新建 Incident 后异步触发，
	// 让 LLM 产出带 evidence 的 severity_adjustment / dedup_suggestion 建议（human-in-the-loop）。
	// 为 nil 时跳过（未配置 LLM / 测试）——AI 是横向增益，缺失不影响分诊主流程。
	// 触发为异步 best-effort：不阻塞建单，LLM 不可用时分析器内部自行降级不产出。
	aiAnalyzer TriageAIAnalyzer

	// autoProvision 自动供给配置（方案C §3.5）。零值（enabled=false）时关闭，
	// 路由未命中照旧走 unrouted——「无配置不回归」。由 SetAutoProvision 注入。
	autoProvision autoProvisionConfig
}

// autoProvisionConfig 未路由告警自动创建 Service 的配置（方案C §3.5）。
type autoProvisionConfig struct {
	enabled      bool           // 总开关
	serviceLabel string         // 服务键 label 名（取值作 slug/name）
	teamLabel    string         // 团队键 label 名（取值匹配 Team.slug）
	defaultTeam  string         // 兜底团队 slug（团队键解析不到时用）
	slugPattern  *regexp.Regexp // 服务键值白名单（nil=放行任意非空值）
}

// TriageAIAnalyzer 分诊 AI 分析器接口（T3.2）。由装配层用 ai.TriageAIEngine 适配实现。
// 定义在 triage 侧、由 wire 注入实现，避免 triage 反向依赖 ai 包（与 UnroutedNotifier 同款解耦）。
// 异步触发只关心「是否出错」（best-effort 记日志），不消费产出——产出由手动端点直接取。
type TriageAIAnalyzer interface {
	// AnalyzeIncident 对新建 Incident 跑分诊 AI（severity/dedup 建议）。返回 error 供记日志。
	AnalyzeIncident(ctx context.Context, incID int) error
}

// SetAIAnalyzer 注入分诊 AI 分析器（装配时调用）。为 nil 时建单后不触发 AI 分析。
func (e *Engine) SetAIAnalyzer(a TriageAIAnalyzer) {
	e.aiAnalyzer = a
}

// UnroutedNotifier 未路由兜底通知接口（C3）。由能力域 7/装配层实现：
// 解算 org_admin 收件人并发送兜底通知。定义在 triage 侧、由 wire 注入实现，
// 避免 triage 反向依赖 notification/auth（与 escalation.Notifier 同款解耦模式）。
type UnroutedNotifier interface {
	// NotifyUnroutedCritical 对未路由的 critical Event 兜底通知 org_admin。
	NotifyUnroutedCritical(ctx context.Context, evt *ent.Event) error
}

// SetUnroutedNotifier 注入未路由兜底通知器（装配时调用）。为 nil 时不兜底。
func (e *Engine) SetUnroutedNotifier(n UnroutedNotifier) {
	e.unroutedNotifier = n
}

// SetAutoProvision 注入自动供给配置（方案C §3.5，装配时调用）。
// enabled=false（默认）时不启用，路由未命中照旧走 unrouted。slugPattern 由装配层编译校验
// （非法正则应在启动时暴露，而非静默放行）；nil 表示放行任意非空服务键值。
func (e *Engine) SetAutoProvision(enabled bool, serviceLabel, teamLabel, defaultTeam string, slugPattern *regexp.Regexp) {
	e.autoProvision = autoProvisionConfig{
		enabled:      enabled,
		serviceLabel: serviceLabel,
		teamLabel:    teamLabel,
		defaultTeam:  defaultTeam,
		slugPattern:  slugPattern,
	}
}

// 默认去重/聚合窗口（C9：可经 SetWindows 覆盖，未配置时用此默认）。
const (
	defaultDedupWindow     = 5 * time.Minute
	defaultAggregateWindow = 5 * time.Minute
)

// NewEngine 创建分诊引擎。窗口用默认值（5min），可经 SetWindows 覆盖（C9）。
func NewEngine(db *ent.Client, rc *redis.Client) *Engine {
	return &Engine{
		db:              db,
		redis:           rc,
		dedupWindow:     defaultDedupWindow,
		aggregateWindow: defaultAggregateWindow,
		suppression:     NewSuppressionEngine(db),
	}
}

// SetWindows 覆盖去重/聚合窗口（C9：从配置注入，替代硬编码 5min）。
// 传入 <=0 的窗口保留当前值（默认），便于只改其一。
func (e *Engine) SetWindows(dedup, aggregate time.Duration) {
	if dedup > 0 {
		e.dedupWindow = dedup
	}
	if aggregate > 0 {
		e.aggregateWindow = aggregate
	}
}

// SetBus 注入领域事件总线（装配时调用）。为 nil 时跳过事件发布。
func (e *Engine) SetBus(b *domainevent.Bus) {
	e.bus = b
}

// SetRecorder 注入时间线记录器（装配时调用）。B3：自动恢复写 status_changed 时间线用。
func (e *Engine) SetRecorder(r *timeline.Recorder) {
	e.recorder = r
}

// SetSuppressionEngine 注入抑制引擎（测试可替换 now）。
func (e *Engine) SetSuppressionEngine(s *SuppressionEngine) {
	e.suppression = s
}

// Result 分诊结果，描述一个 Event 被如何处置。
type Result struct {
	Action       ResultAction // skipped_dedup / suppressed / routed / unrouted / aggregated / incident_created / resolved
	IncidentID   int          // 关联/创建的 Incident（如有）
	IncidentNum  string       // 人类可读编号（如 INC-0042）
	ServiceID    int          // 路由命中的 Service（0 = 未命中）
	ServiceName  string
	IsNoise      bool
	DedupSkipped bool
	// Suppressed 命中抑制规则（action=suppress 时 true，Event 标记噪音不入 Incident）
	Suppressed bool
	// SeverityReduced 命中 reduce_severity 规则并已降级
	SeverityReduced bool
	// SuppressionRule 命中的规则名（未命中为空）
	SuppressionRule string
}

// ResultAction 分诊动作类型。
type ResultAction string

const (
	ActionIncidentCreated ResultAction = "incident_created" // 创建了新 Incident
	ActionAggregated      ResultAction = "aggregated"       // 并入既有 Incident
	ActionUnrouted        ResultAction = "unrouted"         // 路由未命中，入 unrouted 池
	ActionDedupSkipped    ResultAction = "dedup_skipped"    // 去重丢弃
	ActionResolved        ResultAction = "resolved"         // resolved 事件触发 Incident 解决
	ActionSuppressed      ResultAction = "suppressed"       // 命中抑制规则，标记噪音（§2.3）
	ActionSeverityReduced ResultAction = "severity_reduced" // 命中降级规则，降低严重度
)

// Process 处理一个 Event，执行 去重 → 路由 → 聚合 全流程。
// evtID 是 Event 的数据库 ID。
func (e *Engine) Process(ctx context.Context, evtID int) (*Result, error) {
	// 1. 取 Event
	evt, err := e.db.Event.Get(ctx, evtID)
	if err != nil {
		return nil, fmt.Errorf("get event %d: %w", evtID, err)
	}

	// 2. 去重（firing 才去重；resolved 用于触发解决流程，不去重）
	if evt.Status == event.StatusFiring {
		skipped, err := e.checkDedup(ctx, evt.DedupKey)
		if err != nil {
			return nil, fmt.Errorf("dedup: %w", err)
		}
		if skipped {
			// 标记为噪音（重复）并返回
			_ = e.db.Event.UpdateOneID(evtID).SetIsNoise(true).Exec(ctx)
			return &Result{Action: ActionDedupSkipped, DedupSkipped: true}, nil
		}
	}

	// 3. 抑制规则评估（能力域 3 M3.2，§2.1 三层处理：去重→抑制→聚合）。
	// 命中 suppress → 标记噪音、不入 Incident，仅留痕（可申诉）；
	// 命中 reduce_severity → 降低严重度后继续后续流程（路由/聚合用新严重度）。
	severityReduced := false
	suppressionRule := ""
	if e.suppression != nil {
		out, err := e.suppression.Evaluate(ctx, evt)
		if err != nil {
			return nil, fmt.Errorf("suppression: %w", err)
		}
		if out.Matched {
			originalSeverity := evt.Severity
			evt, err = e.suppression.Apply(ctx, evt, out)
			if err != nil {
				return nil, fmt.Errorf("apply suppression: %w", err)
			}
			suppressionRule = out.RuleName
			if out.Action == SuppressActionSuppress {
				// suppress：标记噪音、不入 Incident（仅留痕，可申诉，§2.5）
				return &Result{
					Action:          ActionSuppressed,
					IsNoise:         true,
					Suppressed:      true,
					SuppressionRule: out.RuleName,
				}, nil
			}
			// reduce_severity：已降级（severity 已被 Apply 改写），继续路由/聚合
			severityReduced = evt.Severity != originalSeverity
		}
	}

	// 4. 路由：匹配 Service
	svc, err := e.route(ctx, evt)
	if err != nil {
		return nil, fmt.Errorf("route: %w", err)
	}
	if svc == nil {
		// §3.5 自动供给（方案C）：路由未命中时尝试即时创建 source=auto 的 Service。
		// 开关关/不满足条件（无服务键 label、slug 不过白名单、团队或默认策略解析不到）时返回 nil，
		// 回落下方既有 unrouted 逻辑（含 critical 兜底通知），无回归。
		svc, err = e.tryAutoProvision(ctx, evt)
		if err != nil {
			return nil, fmt.Errorf("auto-provision: %w", err)
		}
	}
	if svc == nil {
		// 未命中：标记 unrouted（Event.service_id 留空），等待人工分诊。
		// C3：critical 级未路由要兜底通知 org_admin——否则 critical 告警因无 Service 匹配而
		// 完全静默（既不建单、不升级、不通知），是"漏真故障"的高危盲区。
		// 普通严重度不兜底（仅入 unrouted 池待人工分诊，不打扰管理员）。best-effort，失败不阻塞。
		if e.unroutedNotifier != nil && evt.Severity == event.SeverityCritical {
			if nerr := e.unroutedNotifier.NotifyUnroutedCritical(ctx, evt); nerr != nil {
				slog.Warn("triage: unrouted critical fallback notify failed", "event_id", evt.ID, "error", nerr)
			}
		}
		return &Result{Action: ActionUnrouted, SeverityReduced: severityReduced, SuppressionRule: suppressionRule}, nil
	}

	// 把 Service 绑定到 Event
	if err := e.db.Event.UpdateOneID(evtID).SetServiceID(svc.ID).Exec(ctx); err != nil {
		return nil, fmt.Errorf("bind service: %w", err)
	}

	// 5. resolved 事件：触发既有 Incident 解决流程
	if evt.Status == event.StatusResolved {
		return e.handleResolved(ctx, evt, svc)
	}

	// 6. 聚合：找既有活跃 Incident 或创建新的
	res, err := e.aggregate(ctx, evt, svc)
	if err != nil {
		return nil, err
	}
	// 标注降级信息（便于上层埋点/时间线）
	if res != nil && severityReduced {
		res.SeverityReduced = true
		res.SuppressionRule = suppressionRule
	}
	return res, nil
}

// ErrRerouteAlreadyRouted 目标 Event 已归属某 Service（非 unrouted），拒绝重路由。
var ErrRerouteAlreadyRouted = fmt.Errorf("event already routed to a service")

// Reroute 人工把一个未路由（unrouted）Event 指派到指定 Service（M6）。
//
// 语义：仅作用于尚无 Service 归属的 Event（unrouted 池）——已路由的 Event 不允许改派
// （避免破坏既有 Incident 归属，改派既有单应走 incident.reassign）。指派后立即按目标
// Service 走聚合（并入既有活跃单或建新单），与自动路由的后半段完全一致，使被误判为
// 未路由的告警能真正进入处置流程。firing/resolved 分别走建单/解决路径。
//
// 返回分诊 Result（Action 为 aggregated/incident_created/resolved/unrouted）。
func (e *Engine) Reroute(ctx context.Context, evtID, serviceID int) (*Result, error) {
	evt, err := e.db.Event.Get(ctx, evtID)
	if err != nil {
		return nil, fmt.Errorf("get event %d: %w", evtID, err)
	}
	// 已有 Service 归属的 Event 不允许重路由（幂等保护，避免改派破坏既有单归属）。
	if _, serr := evt.QueryService().Only(ctx); serr == nil {
		return nil, ErrRerouteAlreadyRouted
	} else if !ent.IsNotFound(serr) {
		return nil, fmt.Errorf("check event service: %w", serr)
	}

	svc, err := e.db.Service.Get(ctx, serviceID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("service %d: %w", serviceID, err)
		}
		return nil, fmt.Errorf("get service %d: %w", serviceID, err)
	}

	// 绑定 Service 到 Event（清除 unrouted 状态）。
	if err := e.db.Event.UpdateOneID(evtID).SetServiceID(svc.ID).Exec(ctx); err != nil {
		return nil, fmt.Errorf("bind service: %w", err)
	}

	// resolved 事件走解决流程；firing 走聚合建单——与自动路由后半段一致。
	if evt.Status == event.StatusResolved {
		return e.handleResolved(ctx, evt, svc)
	}
	return e.aggregate(ctx, evt, svc)
}

// checkDedup 检查去重：窗口内已见过该 dedup_key 则返回 true（跳过，标噪音）。
//
// 降级契约（B23，明确 Redis 不可用时的确定行为）：
//
//	┌────────────────────┬──────────────────────┬──────────────────────────────────────┐
//	│ Redis 状态          │ checkDedup 行为        │ 后果与兜底                             │
//	├────────────────────┼──────────────────────┼──────────────────────────────────────┤
//	│ 未注入 (e.redis==nil)│ 放行(false,nil)+记降级 │ 去重整体失效：窗口内重复告警不再丢弃。   │
//	│                     │ metric               │ 但同 service+severity 会在聚合窗口内并  │
//	│                     │                      │ 入同一 Incident（aggregate 兜底），不   │
//	│                     │                      │ 会爆量建单，仅 Event 层可能留重复信号。  │
//	├────────────────────┼──────────────────────┼──────────────────────────────────────┤
//	│ 运行时故障(SetNX err)│ 返回 error（不放行）   │ Process 上抛 → 分诊 Asynq 任务失败重试。 │
//	│                     │                      │ 「重试放行」比「降级放行」更保守：Redis  │
//	│                     │                      │ 抖动时不静默失去去重，等恢复后正常去重。  │
//	└────────────────────┴──────────────────────┴──────────────────────────────────────┘
//
// 为什么 nil 与 err 两种降级方向相反：nil 是「明确没有 Redis」的稳态（一直不去重，靠聚合兜底，
// 无重试意义）；err 是「本应有 Redis 但暂时不可用」的瞬态（重试能恢复去重，故走重试而非放行）。
//
// TTL 语义：SETNX 固定 dedupWindow 不续期——同 key 在窗口内首次落 key，窗口过后自然过期，
// 下一次同 key 告警视为新一轮（去重是「窗口内抑制」而非「永久抑制」，符合告警抖动语义）。
func (e *Engine) checkDedup(ctx context.Context, dedupKey string) (bool, error) {
	if e.redis == nil {
		// 无 Redis：去重失效降级放行。记 metric 使该盲区可观测（聚合窗口兜底防爆量，见契约表）。
		metrics.DedupDegraded.WithLabelValues("redis_nil").Inc()
		return false, nil
	}
	key := "vigil:dedup:" + dedupKey
	// SETNX：首次设置成功（返回 1=未重复），已存在返回 0（重复）
	ok, err := e.redis.SetNX(ctx, key, 1, e.dedupWindow).Result()
	if err != nil {
		// 运行时故障不降级放行：上抛让分诊任务失败重试（Asynq），避免 Redis 抖动时静默失去去重。
		return false, err
	}
	return !ok, nil // ok=false 表示已存在 → 重复 → 跳过
}

// route 按 Event labels 匹配 Service。命中返回 Service，未命中返回 nil。
//
// 匹配优先级（capabilities §3.1，C2 路由增强，确定性裁决）：
//  1. Event.labels["service"] 等值匹配 Service.slug —— 最常见、最明确的直达路径，优先返回。
//  2. Service.labels 多标签子集匹配 —— Event.labels ⊇ Service.labels（Service 每个标签
//     都能在 Event.labels 中找到、且值匹配，支持 glob path.Match，如 env=prod-*）。
//     多命中时按「匹配标签数」降序（更具体的 Service 优先），标签数相同再按 ID 升序，
//     保证同输入总得同一结果（确定性），避免"随机命中一条"。
//  3. B14：以上均未命中时，回退 Event 所属 Integration 的默认 service（接入点直达归属），
//     跳过 label 匹配——接入点预设归属服务时无需再配路由标签。
func (e *Engine) route(ctx context.Context, evt *ent.Event) (*ent.Service, error) {
	// —— 1. slug 直达（向后兼容旧行为）——
	if svcName, ok := evt.Labels["service"]; ok && svcName != "" {
		svc, err := e.db.Service.Query().
			Where(service.SlugEQ(svcName), service.StatusEQ(service.StatusActive)).
			Only(ctx)
		if err == nil {
			return svc, nil
		}
		if !ent.IsNotFound(err) {
			return nil, err
		}
		// slug 未命中不直接返回 nil：继续尝试 label 子集匹配（同一 Event 可能靠其它标签命中）。
	}

	// —— 2. Service.labels 子集匹配（多标签 + glob + 具体度优先）——
	if svc, err := e.matchByLabels(ctx, evt); err != nil {
		return nil, err
	} else if svc != nil {
		return svc, nil
	}

	// —— 3. B14：回退 Integration 默认 service（接入点直达归属）——
	return e.routeByIntegration(ctx, evt)
}

// matchByLabels 用 Service.labels 对 Event.labels 做子集匹配。
// 命中条件：Service 的每个标签 k=pattern，Event.labels[k] 存在且 path.Match(pattern, value) 成立。
// 空 labels 的 Service 不参与（否则会匹配所有 Event，语义上等于"兜底"，交由 Integration 回退处理）。
// 多命中按匹配标签数降序、ID 升序取第一（更具体优先，确定性）。
func (e *Engine) matchByLabels(ctx context.Context, evt *ent.Event) (*ent.Service, error) {
	if len(evt.Labels) == 0 {
		return nil, nil
	}
	services, err := e.db.Service.Query().
		Where(service.StatusEQ(service.StatusActive)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		svc     *ent.Service
		matched int // 匹配的标签数（越多越具体）
	}
	var hits []candidate
	for _, svc := range services {
		if len(svc.Labels) == 0 {
			continue // 无路由标签的 Service 不参与子集匹配（避免"空规则匹配一切"）
		}
		if labelsSubsetMatch(svc.Labels, evt.Labels) {
			hits = append(hits, candidate{svc: svc, matched: len(svc.Labels)})
		}
	}
	if len(hits) == 0 {
		return nil, nil
	}
	// 更具体优先：匹配标签数降序；相同则 ID 升序（确定性裁决）。
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].matched != hits[j].matched {
			return hits[i].matched > hits[j].matched
		}
		return hits[i].svc.ID < hits[j].svc.ID
	})
	return hits[0].svc, nil
}

// routeByIntegration 回退到 Event 所属 Integration 预设的默认 service（B14）。
// Integration 未绑定 service（或未关联 Integration）时返回 nil（仍走 unrouted）。
// disabled 的默认 service 不回退——避免把告警派给已停用的服务。
func (e *Engine) routeByIntegration(ctx context.Context, evt *ent.Event) (*ent.Service, error) {
	svc, err := evt.QueryIntegration().QueryService().
		Where(service.StatusEQ(service.StatusActive)).
		Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil // 无 Integration / 无默认 service / 默认 service 已停用
	}
	if err != nil {
		return nil, err
	}
	return svc, nil
}

// tryAutoProvision 未路由告警自动创建 Service（方案C §3.5）。
//
// 仅在开关开启且全部条件满足时创建，任一不满足返回 (nil, nil) 回落 unrouted——绝不制造静默盲区：
//  1. 携带服务键 label 且值非空、通过 slug 白名单（防脏值刷服务）。
//  2. 能解析归属团队（团队键 label → Team.slug，否则兜底团队）。
//  3. 该团队已配 default_escalation_policy——无默认策略则不创建（否则新服务无策略、不升级，
//     等于把"未路由"换成"已路由但静默"，更危险；此时回落 unrouted，critical 仍兜底通知）。
//
// 幂等：Service.slug 唯一约束兜底——并发/已存在同 slug 时查回既有（仅当其为 active 才复用，
// 尊重人工 disable，不复活被停用的服务）。
func (e *Engine) tryAutoProvision(ctx context.Context, evt *ent.Event) (*ent.Service, error) {
	if !e.autoProvision.enabled {
		return nil, nil
	}
	// 1. 服务键
	slug := evt.Labels[e.autoProvision.serviceLabel]
	if slug == "" {
		return nil, nil
	}
	// 2. slug 白名单（非空正则时须匹配）
	if e.autoProvision.slugPattern != nil && !e.autoProvision.slugPattern.MatchString(slug) {
		slog.Warn("triage: auto-provision skipped, slug rejected by pattern",
			"slug", slug, "event_id", evt.ID)
		return nil, nil
	}
	// 3. 归属团队
	tm, err := e.resolveAutoProvisionTeam(ctx, evt)
	if err != nil {
		return nil, err
	}
	if tm == nil {
		return nil, nil // 团队解析不到 → 回落 unrouted
	}
	// 4. 团队默认升级策略（无则不供给）
	policy, err := tm.QueryDefaultEscalationPolicy().Only(ctx)
	if ent.IsNotFound(err) {
		slog.Warn("triage: auto-provision skipped, team has no default escalation policy",
			"team", tm.Slug, "slug", slug, "event_id", evt.ID)
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query team default escalation policy: %w", err)
	}
	// 5. 创建（source=auto，继承团队默认策略）
	svc, err := e.db.Service.Create().
		SetName(slug).
		SetSlug(slug).
		SetLabels(map[string]string{e.autoProvision.serviceLabel: slug}).
		SetSource(service.SourceAuto).
		SetProvisionedAt(time.Now()).
		SetStatus(service.StatusActive).
		SetAutoCreateIncident(true).
		SetTeamID(tm.ID).
		SetEscalationPolicyID(policy.ID).
		Save(ctx)
	if err != nil {
		// slug 唯一冲突：并发已建 / 已存在同名服务——查回既有，仅复用 active 的。
		if ent.IsConstraintError(err) {
			existing, gerr := e.db.Service.Query().Where(service.SlugEQ(slug)).Only(ctx)
			if gerr != nil {
				return nil, fmt.Errorf("auto-provision slug conflict, refetch service %q: %w", slug, gerr)
			}
			if existing.Status != service.StatusActive {
				// 已存在但被停用：尊重人工 disable，不复活；回落 unrouted。
				return nil, nil
			}
			return existing, nil
		}
		return nil, fmt.Errorf("auto-provision service %q: %w", slug, err)
	}
	metrics.ServicesAutoProvisioned.WithLabelValues(tm.Slug).Inc()
	slog.Info("triage: auto-provisioned service",
		"slug", slug, "team", tm.Slug, "policy_id", policy.ID, "event_id", evt.ID)
	return svc, nil
}

// resolveAutoProvisionTeam 解析自动供给的归属团队（方案C §3.5）：
// 优先团队键 label 匹配 Team.slug，缺失/无匹配时回退配置的兜底团队；都解析不到返回 (nil, nil)。
func (e *Engine) resolveAutoProvisionTeam(ctx context.Context, evt *ent.Event) (*ent.Team, error) {
	// 团队键 label
	if e.autoProvision.teamLabel != "" {
		if slug := evt.Labels[e.autoProvision.teamLabel]; slug != "" {
			tm, err := e.db.Team.Query().Where(team.SlugEQ(slug)).Only(ctx)
			if err == nil {
				return tm, nil
			}
			if !ent.IsNotFound(err) {
				return nil, fmt.Errorf("query team by label %q: %w", slug, err)
			}
			// label 有值但无匹配团队 → 继续尝试兜底团队
		}
	}
	// 兜底团队
	if e.autoProvision.defaultTeam != "" {
		tm, err := e.db.Team.Query().Where(team.SlugEQ(e.autoProvision.defaultTeam)).Only(ctx)
		if err == nil {
			return tm, nil
		}
		if !ent.IsNotFound(err) {
			return nil, fmt.Errorf("query default team %q: %w", e.autoProvision.defaultTeam, err)
		}
	}
	return nil, nil
}

// labelsSubsetMatch 判断 pattern 集是否是 target 的子集（值支持 glob）。
// pattern 的每个 k=p，都须在 target 中存在 k 且 path.Match(p, target[k]) 成立。
// path.Match 的模式非法时退化为等值比较（保守，绝不因非法模式误命中）。
func labelsSubsetMatch(pattern, target map[string]string) bool {
	for k, p := range pattern {
		v, ok := target[k]
		if !ok {
			return false
		}
		if !globMatch(p, v) {
			return false
		}
	}
	return true
}

// globMatch 用 path.Match 做 glob 匹配（支持 * ? [chars]）。
// 无通配符时等价于等值比较；模式非法时退化为等值（不误命中）。
func globMatch(pattern, value string) bool {
	ok, err := path.Match(pattern, value)
	if err != nil {
		return pattern == value // 模式非法 → 保守等值
	}
	return ok
}

// aggregate 相关性聚合：同 service+severity 在时间窗口内的活跃 Incident 并入；否则创建新 Incident。
func (e *Engine) aggregate(ctx context.Context, evt *ent.Event, svc *ent.Service) (*Result, error) {
	// 找同 service+severity、在聚合窗口内、状态为 triggered/escalated/acked 的活跃 Incident
	since := time.Now().Add(-e.aggregateWindow)
	existing, err := e.db.Incident.Query().
		Where(
			incident.HasServiceWith(service.IDEQ(svc.ID)),
			incident.SeverityEQ(incident.Severity(evt.Severity)),
			incident.StatusIn(incident.StatusTriggered, incident.StatusEscalated, incident.StatusAcked),
			incident.CreatedAtGTE(since),
		).
		Order(ent.Desc(incident.FieldCreatedAt)).
		First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, fmt.Errorf("query active incident: %w", err)
	}

	res := &Result{ServiceID: svc.ID, ServiceName: svc.Name}

	if existing != nil {
		// 并入既有 Incident：把 Event 关联到该 Incident
		if err := e.db.Event.UpdateOneID(evt.ID).SetIncidentID(existing.ID).Exec(ctx); err != nil {
			return nil, fmt.Errorf("attach event to incident: %w", err)
		}
		// 写 event_attached 时间线（B5：聚合并入已有 Incident 的告警要留痕，原先零写入）。
		// 让复盘能看到「这个单聚合了哪些后续告警」，而非只见首告警。系统触发，source=system。
		if e.recorder != nil {
			_ = e.recorder.Record(ctx, existing.ID, timelineitem.TypeEventAttached,
				fmt.Sprintf("新告警并入本事件：%s", evt.Summary),
				timeline.Actor{Kind: "system"}, timelineitem.SourceSystem,
				map[string]any{"event_id": evt.ID, "dedup_key": evt.DedupKey, "severity": string(evt.Severity), "source_event_id": evt.SourceEventID})
		}
		res.Action = ActionAggregated
		res.IncidentID = existing.ID
		res.IncidentNum = existing.Number
		return res, nil
	}

	// 创建新 Incident（仅当 Service.auto_create_incident=true 或 severity=critical）
	if !svc.AutoCreateIncident && evt.Severity != event.SeverityCritical {
		// 不自动创建，Event 暂不进 Incident（等待人工提升）
		return &Result{Action: ActionUnrouted, ServiceID: svc.ID, ServiceName: svc.Name}, nil
	}

	inc, err := e.createIncident(ctx, evt, svc)
	if err != nil {
		return nil, err
	}
	// 创建后：绑定 Service 的升级策略到 Incident，并发布 IncidentCreated 事件。
	// 原由 OnIncidentCreated 回调（main 注入 escEngine）完成；现改为事件解耦——
	// triage 只负责「绑定策略 + 发事件」，升级链启动由 escalation 订阅事件完成。
	e.bindPolicyAndPublish(ctx, inc, svc)
	// T3.2：新建 Incident 后异步触发分诊 AI（severity/dedup 建议），不阻塞分诊主流程。
	e.triggerAIAnalysis(ctx, inc.ID)
	res.Action = ActionIncidentCreated
	res.IncidentID = inc.ID
	res.IncidentNum = inc.Number
	return res, nil
}

// bindPolicyAndPublish 把 Service 的 EscalationPolicy 绑定到 Incident，
// 然后发布 IncidentCreated 事件供 escalation 订阅启动升级链。
//
// 绑定策略是 Incident 的数据归属（其 escalation_policy_id），发生在 triage 创建后，
// 这样 escalation 订阅方 OnCreated 重新查 incident 时能拿到已绑定的 policy。
// 无策略 / 绑定失败时仍发事件（escalation 侧 OnCreated 会再次判断无策略则跳过）。
func (e *Engine) bindPolicyAndPublish(ctx context.Context, inc *ent.Incident, svc *ent.Service) {
	if policy, err := svc.QueryEscalationPolicy().Only(ctx); err == nil && policy != nil {
		_ = e.db.Incident.UpdateOneID(inc.ID).SetEscalationPolicyID(policy.ID).Exec(ctx)
	}
	if e.bus != nil {
		e.bus.Publish(ctx, domainevent.Event{
			Type:     domainevent.IncidentCreated,
			Incident: inc,
			// Action=created：下游同步订阅方（ws/webhook/im）据此区分语义。
			// 尤其出站 webhook 用它拼 event 名（incident.created），否则名为 "incident."（空 action，C24）。
			Action:          domainevent.Action("created"),
			ActorID:         0,
			SystemTriggered: true, // 系统建单（triage 自动），非人工请求
		})
	}
}

// triggerAIAnalysis 异步触发分诊 AI 分析（T3.2）。
//
// 建单后在独立 goroutine 里跑 severity/dedup 建议——LLM 调用可能耗时数秒，绝不能阻塞分诊主流程
// （分诊在 Asynq worker 里同步执行，阻塞会拖慢整条 ingestion→triage 流水线）。best-effort：
// analyzer 为 nil（未配 LLM）时直接跳过；内部失败仅记日志，不影响已建单结果。
// 用 context.WithoutCancel 解绑父 ctx 生命周期——worker 处理完当前任务后父 ctx 可能被取消，
// 但 AI 分析应能独立跑完（另配超时防 goroutine 泄漏）。
func (e *Engine) triggerAIAnalysis(ctx context.Context, incID int) {
	if e.aiAnalyzer == nil {
		return
	}
	// 解绑父 ctx 取消信号，但另设超时上限，避免 LLM 卡死导致 goroutine 泄漏。
	bg := context.WithoutCancel(ctx)
	go func() {
		aiCtx, cancel := context.WithTimeout(bg, aiAnalysisTimeout)
		defer cancel()
		if err := e.aiAnalyzer.AnalyzeIncident(aiCtx, incID); err != nil {
			slog.Warn("triage: ai analysis failed", "incident_id", incID, "error", err)
		}
	}()
}

// aiAnalysisTimeout 分诊 AI 异步分析的超时上限（防 LLM 卡死泄漏 goroutine）。
const aiAnalysisTimeout = 90 * time.Second

// createIncident 创建新 Incident，并把 Event 关联进去。
// 编号生成并发安全：Redis INCR 原子分配；无 Redis 时 Count+1 并在 number 唯一冲突时重试。
func (e *Engine) createIncident(ctx context.Context, evt *ent.Event, svc *ent.Service) (*ent.Incident, error) {
	// 查 Service 归属的 Team（team 是 edge，非字段）
	team, err := svc.QueryTeam().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("query service team: %w", err)
	}
	priority := severityToPriority(evt.Severity)

	// 编号分配 + 唯一冲突重试。
	// Redis 在线时 INCR 强一致，几乎不冲突；无 Redis 时 Count+1 可能冲突，
	// 靠 incident.number Unique 约束兜底，捕获 ConstraintError 换号重试。
	const maxRetries = 5
	var inc *ent.Incident
	for attempt := 0; attempt < maxRetries; attempt++ {
		num, err := e.nextIncidentNumber(ctx)
		if err != nil {
			return nil, fmt.Errorf("alloc incident number: %w", err)
		}
		inc, err = e.db.Incident.Create().
			SetNumber(num).
			SetTitle(evt.Summary).
			SetSeverity(incident.Severity(evt.Severity)).
			SetStatus(incident.StatusTriggered).
			SetPriority(incident.Priority(priority)).
			SetSummary(evt.Summary).
			SetTriggerType(incident.TriggerTypeAuto).
			SetTriggerSourceEventID(evt.SourceEventID).
			SetService(svc).
			SetTeamID(team.ID).
			Save(ctx)
		if err == nil {
			break
		}
		// number 唯一冲突（并发分配到同号）→ 换号重试
		if ent.IsConstraintError(err) && attempt < maxRetries-1 {
			continue
		}
		return nil, fmt.Errorf("create incident: %w", err)
	}
	// 埋点：事件创建数（按 severity）
	metrics.IncidentsCreated.WithLabelValues(string(inc.Severity)).Inc()
	// 关联 Event 到 Incident
	if err := e.db.Event.UpdateOneID(evt.ID).SetIncidentID(inc.ID).Exec(ctx); err != nil {
		return nil, fmt.Errorf("attach event: %w", err)
	}
	// 写 incident_created 时间线（B4：建单是「全程留痕」的起点，原先零写入）。
	// 系统自动建单（triage），source=system。失败不阻塞建单主流程（best-effort）。
	if e.recorder != nil {
		_ = e.recorder.Record(ctx, inc.ID, timelineitem.TypeIncidentCreated,
			fmt.Sprintf("系统创建事件 %s：%s", inc.Number, inc.Title),
			timeline.Actor{Kind: "system"}, timelineitem.SourceSystem,
			map[string]any{"number": inc.Number, "severity": string(inc.Severity), "service": svc.Name, "source_event_id": evt.SourceEventID})
	}
	return inc, nil
}

// handleResolved 处理 resolved 事件：找到同一告警对应的活跃 Incident，自动推进解决。
//
// B3 修复（对照 docs/audit journey-code-audit B3）：
//  1. 收敛匹配到 dedup 维度——精确定位「就是这条告警」对应的 Incident，
//     而非旧实现按 service 维度取最新活跃单（同 service 多告警时会误解无关单）。
//     dedup_key 是同一告警 firing/resolved 的共同指纹（Event.dedup_key），
//     故用「该 Incident 挂有相同 dedup_key 的 firing Event」定位，无匹配则不解任何单。
//  2. 已 acked 的单不自动解决——ack 表示已有人接手处置，此时监控侧的 resolved 信号
//     不应替人拍板关单（可能问题未真正闭环、责任人尚未确认）。故自动恢复只作用于
//     triggered/escalated（尚无人介入）的单；acked/resolved/closed 交由人工 resolve/reopen。
//     这与「IM/Web 手动 resolve 才是人拍板」的语义一致，避免系统把单从责任人手里抽走。
//  3. 写 status_changed 时间线 + 发 IncidentResolved 领域事件——补齐「每次状态变更必产
//     TimelineItem」铁律，并让 WS 推送 / IM 卡片刷新 / 出站 webhook 感知自动恢复。
func (e *Engine) handleResolved(ctx context.Context, evt *ent.Event, svc *ent.Service) (*Result, error) {
	// 按 dedup 维度定位：找挂有相同 dedup_key 的 firing Event、且仍为 triggered/escalated
	// 的活跃 Incident（排除 acked——已有人接手，不自动关单）。
	inc, err := e.db.Incident.Query().
		Where(
			incident.HasEventsWith(
				event.DedupKeyEQ(evt.DedupKey),
				event.StatusEQ(event.StatusFiring),
			),
			incident.StatusIn(incident.StatusTriggered, incident.StatusEscalated),
		).
		Order(ent.Desc(incident.FieldCreatedAt)).
		First(ctx)
	if ent.IsNotFound(err) {
		// 无同 dedup 的未介入活跃单（可能已 acked/已解决/无匹配）——不误解其它单，仅留痕返回。
		return &Result{Action: ActionUnrouted, ServiceID: svc.ID, ServiceName: svc.Name}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query incident for resolve: %w", err)
	}
	// 推进到 resolved（生产可配为"仅提示"，这里自动解决未介入的单）。
	// 用 Save 拿回更新后快照，供时间线/领域事件携带最新状态。
	resolved, err := e.db.Incident.UpdateOneID(inc.ID).
		SetStatus(incident.StatusResolved).
		SetResolvedAt(time.Now()).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve incident: %w", err)
	}
	// 写 status_changed 时间线（系统触发的自动恢复，source=system）。失败不阻塞主流程。
	if e.recorder != nil {
		_ = e.recorder.Record(ctx, resolved.ID, timelineitem.TypeStatusChanged,
			"系统自动恢复：收到告警 resolved 信号",
			timeline.Actor{Kind: "system"}, timelineitem.SourceSystem,
			map[string]any{"status": "resolved", "auto": true, "dedup_key": evt.DedupKey})
	}
	// 发 IncidentResolved 领域事件，驱动 WS/IM 卡片/出站 webhook 同步。
	// SystemTriggered=true 标记系统触发（区别于人工 resolve），下游同步订阅方一致消费。
	if e.bus != nil {
		e.bus.Publish(ctx, domainevent.Event{
			Type:            domainevent.IncidentResolved,
			Incident:        resolved,
			Action:          domainevent.Action("resolve"),
			ActorID:         0,
			SystemTriggered: true,
		})
	}
	return &Result{Action: ActionResolved, IncidentID: resolved.ID, IncidentNum: resolved.Number, ServiceID: svc.ID, ServiceName: svc.Name}, nil
}

// nextIncidentNumber 生成人类可读编号 INC-XXXXXX。
// 优先用 Redis INCR 原子分配（全局单调计数器，并发安全）；
// 无 Redis 时降级为 Incident 总数+1（可能并发撞号，靠 createIncident 的重试兜底）。
func (e *Engine) nextIncidentNumber(ctx context.Context) (string, error) {
	if e.redis != nil {
		// Redis INCR 原子自增，key 首次访问时自动初始化为 1
		seq, err := e.redis.Incr(ctx, incidentNumberKey).Result()
		if err == nil {
			return fmt.Sprintf("INC-%04d", seq), nil
		}
		// Redis 出错不阻断，降级到 DB 计数（仍由 createIncident 重试兜底）
	}
	count, err := e.db.Incident.Query().Count(ctx)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("INC-%04d", count+1), nil
}

// incidentNumberKey Redis 上 incident 编号计数器的 key。
const incidentNumberKey = "vigil:incident:number_seq"

// severityToPriority 把 severity 映射到 priority。
func severityToPriority(s event.Severity) incident.Priority {
	switch s {
	case event.SeverityCritical:
		return incident.PriorityP1
	case event.SeverityWarning:
		return incident.PriorityP2
	default:
		return incident.PriorityP3
	}
}
