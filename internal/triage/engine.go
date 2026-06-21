// Package triage 实现能力域 3-4：分诊降噪与路由。
//
// 对应 docs/capabilities/02-triage-routing.md：
// · 去重 —— Redis dedup 窗口内重复 Event 丢弃
// · 路由 —— Event labels 匹配 Service，未命中入 unrouted 池
// · 相关性聚合 —— 同 service+severity 在时间窗口内聚合成一个 Incident
//
// 本包接收 ingestion 产出的 Event，产出 Incident（人介入的对象）。
package triage

import (
	"context"
	"fmt"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/event"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/service"
	"github.com/kevin/vigil/internal/metrics"

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

	// OnIncidentCreated Incident 创建后的回调（由 main 注入，用于启动升级/通知）。
	// 为 nil 时不触发。避免 triage 反向依赖 escalation。
	OnIncidentCreated func(ctx context.Context, inc *ent.Incident, svc *ent.Service)
}

// NewEngine 创建分诊引擎。window 参数为 0 时用默认值。
func NewEngine(db *ent.Client, rc *redis.Client) *Engine {
	return &Engine{
		db:              db,
		redis:           rc,
		dedupWindow:     5 * time.Minute,
		aggregateWindow: 5 * time.Minute,
		suppression:     NewSuppressionEngine(db),
	}
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
	ActionIncidentCreated  ResultAction = "incident_created" // 创建了新 Incident
	ActionAggregated       ResultAction = "aggregated"       // 并入既有 Incident
	ActionUnrouted         ResultAction = "unrouted"         // 路由未命中，入 unrouted 池
	ActionDedupSkipped     ResultAction = "dedup_skipped"    // 去重丢弃
	ActionResolved         ResultAction = "resolved"         // resolved 事件触发 Incident 解决
	ActionSuppressed       ResultAction = "suppressed"       // 命中抑制规则，标记噪音（§2.3）
	ActionSeverityReduced  ResultAction = "severity_reduced" // 命中降级规则，降低严重度
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
		// 未命中：标记 unrouted（Event.service_id 留空），等待人工分诊
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

// checkDedup 检查去重。窗口内已见过该 dedup_key 则返回 true（跳过）。
func (e *Engine) checkDedup(ctx context.Context, dedupKey string) (bool, error) {
	if e.redis == nil {
		return false, nil // 无 Redis 则不去重（测试友好）
	}
	key := "vigil:dedup:" + dedupKey
	// SETNX：首次设置成功（返回 1=未重复），已存在返回 0（重复）
	ok, err := e.redis.SetNX(ctx, key, 1, e.dedupWindow).Result()
	if err != nil {
		return false, err
	}
	return !ok, nil // ok=false 表示已存在 → 重复 → 跳过
}

// route 按 Event labels 匹配 Service。命中返回 Service，未命中返回 nil。
// 当前实现：精确匹配 Service.labels 中的 service/env 键（capabilities §3.1）。
func (e *Engine) route(ctx context.Context, evt *ent.Event) (*ent.Service, error) {
	// 优先用 Event.labels["service"] 匹配 Service.slug（最常见路径）
	svcName, ok := evt.Labels["service"]
	if !ok || svcName == "" {
		return nil, nil
	}
	svc, err := e.db.Service.Query().
		Where(service.SlugEQ(svcName), service.StatusEQ(service.StatusActive)).
		Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil // 未命中
	}
	if err != nil {
		return nil, err
	}
	return svc, nil
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
	// 触发 Incident 创建回调（启动升级链/通知等，由 main 注入）
	if e.OnIncidentCreated != nil {
		e.OnIncidentCreated(ctx, inc, svc)
	}
	res.Action = ActionIncidentCreated
	res.IncidentID = inc.ID
	res.IncidentNum = inc.Number
	return res, nil
}

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
	return inc, nil
}

// handleResolved 处理 resolved 事件：找到同 dedup 的活跃 Incident，按配置推进解决。
// 当前实现：把 Incident 状态置为 resolved（简化；完整实现见 capabilities §2.7）。
func (e *Engine) handleResolved(ctx context.Context, evt *ent.Event, svc *ent.Service) (*Result, error) {
	// 找同 service 的活跃 Incident（简化：用 service 维度，完整用 dedup 维度）
	inc, err := e.db.Incident.Query().
		Where(
			incident.HasServiceWith(service.IDEQ(svc.ID)),
			incident.StatusIn(incident.StatusTriggered, incident.StatusEscalated, incident.StatusAcked),
		).
		Order(ent.Desc(incident.FieldCreatedAt)).
		First(ctx)
	if ent.IsNotFound(err) {
		return &Result{Action: ActionUnrouted, ServiceID: svc.ID, ServiceName: svc.Name}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query incident for resolve: %w", err)
	}
	// 推进到 resolved（生产可配为"仅提示"，这里简化为直接 resolve）
	if err := e.db.Incident.UpdateOneID(inc.ID).
		SetStatus(incident.StatusResolved).
		SetResolvedAt(time.Now()).
		Exec(ctx); err != nil {
		return nil, fmt.Errorf("resolve incident: %w", err)
	}
	return &Result{Action: ActionResolved, IncidentID: inc.ID, IncidentNum: inc.Number, ServiceID: svc.ID, ServiceName: svc.Name}, nil
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
