// Package event 提供进程内领域事件总线。
//
// 设计动机：核心域（incident/triage）与下游订阅方（escalation/ws/webhook/im）
// 之间用事件解耦，避免构建期依赖环。例如 incident.Service 不再持有 escalation.Engine
// 指针，改为发布 IncidentAcked 事件，由 escalation 订阅后自行取消待触发任务。
//
// 派发语义：同步派发——订阅者在发布方的调用栈内执行。这与现有 webhook/ws 的
// 「自己起 goroutine 异步处理」契约一致（webhook.Dispatcher.OnIncidentChanged 已是
// 独立 goroutine + 独立 context），订阅方按需自行异步化。
//
// 错误语义：订阅方 panic 或返回错误只记日志，不影响发布方主流程，也不影响
// 其他订阅方（best-effort 扇出）。这与「通知送达失败不阻塞升级链」等现有降级策略一致。
package event

import (
	"context"
	"log/slog"
	"sync"

	"github.com/kevin/vigil/ent"
)

// Type 事件类型（领域语义，按需扩展）。
type Type string

const (
	// IncidentCreated 新 Incident 创建（triage 发布；escalation 订阅启动升级链）。
	IncidentCreated Type = "incident.created"
	// IncidentAcked Incident 被确认（incident.Service 发布；escalation 订阅取消后续升级）。
	IncidentAcked Type = "incident.acked"
	// IncidentEscalated Incident 被手动升级（incident.Service 发布；escalation 订阅触发目标 level）。
	IncidentEscalated Type = "incident.escalated"
	// IncidentResolved Incident 被解决。
	IncidentResolved Type = "incident.resolved"
	// IncidentClosed Incident 被关闭（终态）。
	// resolved → closed 是状态机唯一进入终态的边（人工 close 或复盘发布联动触发）。
	// 补它是为了让「已解决」的单据能真正归档收口，而非停在 resolved 无处可去。
	IncidentClosed Type = "incident.closed"
	// IncidentReopened Incident 被重新打开。
	IncidentReopened Type = "incident.reopened"
	// IncidentResponderAdded 拉入响应者。
	IncidentResponderAdded Type = "incident.responder_added"
)

// Action 触发事件的动作标签（与 incident.Action 同值集合，但独立定义避免反向依赖 incident 包）。
// 订阅方按需用它区分语义（如 webhook 推送的 event 字段、ws 推送的 action 字段）。
type Action string

// Event 领域事件载荷。
//
// 字段取舍：只携带「订阅方普遍需要」的信息（Incident + Action + Actor）。
// 需要更多上下文（如升级 policy levels）的订阅方从 DB 自行查询，
// 避免事件载荷随订阅方需求膨胀。
type Event struct {
	Type     Type
	Incident *ent.Incident
	Action   Action
	ActorID  int // 执行动作的用户 ID；0=系统/匿名
	// Via 动作来源渠道（web/im/api/automation/system），供 IncidentAction 审计留痕（B4/B5/C30）。
	// 由发布方（incident.Service）从 Source 派生传入；空串表示未指定（订阅方按需兜底为 automation/system）。
	// 与 incident.Source 解耦（避免 event 包反向依赖 incident 包），仅承载字符串值。
	Via string
	// Level 升级目标层级（仅 IncidentEscalated 携带，escalation 订阅方用它触发目标 level）。
	// 其它事件为 0。语义：与 escalation.Engine.TriggerLevelNow 的 levelIdx 一致。
	Level int
	// SystemTriggered 标记事件是否由后台引擎自身触发（而非人工/服务请求）。
	//
	// 用于打破升级事件的反馈环：escalation 订阅 IncidentEscalated（OnManualEscalate）以响应
	// 手动升级请求触发目标 level；而 escalation 自动升级（计时器到点）处理完 level 后也发布
	// IncidentEscalated 供 WS/卡片/webhook 同步——若该事件又喂回 OnManualEscalate 会重复触发同一
	// level 形成死循环。故自动升级事件置 SystemTriggered=true，escalation 订阅方据此跳过再触发，
	// 仅下游同步订阅方（ws/webhook/im）消费。
	SystemTriggered bool
}

// Handler 订阅者处理函数。返回的 error 仅用于日志，不影响其他订阅方。
type Handler func(ctx context.Context, e Event) error

// Bus 进程内同步事件总线。
//
// 线程安全：Subscribe 与 Publish 可并发调用（内部 mutex 保护订阅表）。
// 零值不可用，必须用 New 构造。
type Bus struct {
	mu   sync.RWMutex
	subs map[Type][]Handler
}

// New 创建事件总线。
func New() *Bus {
	return &Bus{subs: make(map[Type][]Handler)}
}

// Subscribe 订阅某类事件。同一 Type 可多次订阅（多订阅者扇出）。
func (b *Bus) Subscribe(t Type, h Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subs[t] = append(b.subs[t], h)
}

// Publish 同步派发事件给所有订阅者。
//
// 语义：
//   - 订阅者按订阅顺序依次调用（在发布方调用栈内）。
//   - 任一订阅者 panic/error 不中断后续订阅者，也不回传给发布方（best-effort）。
//   - 无订阅者时为 no-op。
//
// 订阅者若需异步处理（如 webhook 推送），应自行起 goroutine + 独立 context，
// 不要把发布方的 ctx 直接带过去（请求结束 ctx 取消会导致处理中断）。
func (b *Bus) Publish(ctx context.Context, e Event) {
	b.mu.RLock()
	handlers := b.subs[e.Type]
	// 拷贝一份，避免持锁期间订阅者又触发 Subscribe（虽然 RLock 下不会，但防御性拷贝更稳）
	hs := make([]Handler, len(handlers))
	copy(hs, handlers)
	b.mu.RUnlock()

	for _, h := range hs {
		// 单个订阅者失败不传染：记日志后继续下一个。
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("event bus handler panic",
						"event_type", e.Type, "panic", r, "incident_id", incidentID(e))
				}
			}()
			if err := h(ctx, e); err != nil {
				slog.Error("event bus handler error",
					"event_type", e.Type, "error", err, "incident_id", incidentID(e))
			}
		}()
	}
}

// incidentID 安全取 incident id（可能为 nil，如未来事件不带 incident）。
func incidentID(e Event) int {
	if e.Incident != nil {
		return e.Incident.ID
	}
	return 0
}
