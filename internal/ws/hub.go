// Package ws 实现 WebSocket 实时推送（能力域 8 §状态双向同步 + 架构 §6.3）。
//
// 单实例内存 hub：客户端按 incident_id 订阅，incident 状态变更时广播给订阅者。
//
// 多副本 pub/sub（T6.4，架构 §7）：WS 广播原本是「进程内内存」——多个 API 副本时，
// 连到副本 A 的客户端收不到副本 B 处理的事件推送（副本间广播不互通），多副本下实时
// 刷新不一致。本包通过可选 Redis pub/sub 中继（pubsub.go）跨副本同步：hub 广播时本地
// 直发订阅者（快路径），同时把消息发布到 Redis channel；每个副本订阅该 channel，收到
// **其它副本**的消息后转发给本地连接。去重防回环：消息带发起副本 id，收到自己发的直接丢弃
// （本地已直发过），只处理跨副本消息。无 Redis / 单副本时中继为 nil，退化纯进程内广播
// （不破坏现有行为）。
//
// 消息类型：incident 变更（ack/resolve/escalate 等）、时间线新增。
// 前端收到后用 React Query 的 setQueryData/invalidateQueries 刷新对应缓存。
package ws

import (
	"context"
	"encoding/json"
	"sync"

	domainevent "github.com/kevin/vigil/internal/event"
)

// MessageType 推送消息类型。
type MessageType string

const (
	MsgIncidentChanged MessageType = "incident_changed" // incident 状态变更（ack/resolve/escalate）
	MsgTimelineAdded   MessageType = "timeline_added"   // 时间线新增条目
	// MsgDashboardUpdate 看板更新（值班大屏/仪表盘用）。任一 incident 生命周期事件发生时
	// 向 /ws/dashboard 订阅者广播一条轻量增量，使大屏免轮询实时刷新。
	MsgDashboardUpdate MessageType = "dashboard_update"
)

// dashboardTopic 看板订阅在 hub.clients 内的伪 incident_id 键。
//
// hub 原以 incident_id 分组订阅；看板订阅不针对具体 incident（关注全局增量），
// 故复用同一 clients map，用一个不会与真实 incident id 冲突的负数键归组所有看板订阅者。
// 这样广播/退订/跨副本转发全部复用现有 per-incident 通路，无需再造一套并行结构。
// 真实 incident id 恒为正（ent 自增主键），负键天然隔离。
const dashboardTopic = -1

// Message 推送给客户端的消息体。
type Message struct {
	Type       MessageType `json:"type"`
	IncidentID int         `json:"incident_id"`
	Action     string      `json:"action,omitempty"` // ack/resolve/escalate/add_responder
	Data       any         `json:"data,omitempty"`   // 附带数据（如 incident 快照）
}

// client 一个 WebSocket 连接，按订阅的 incident_id 分组。
type client struct {
	send chan []byte
}

// relay 跨副本中继：hub 本地广播后把消息透传到其它副本（T6.4）。
// 由 RedisPubSub 实现；nil 时退化为纯进程内广播（单副本/无 Redis）。
type relay interface {
	// Publish 把某 incident 的消息发布到跨副本 channel（best-effort，失败不阻塞本地广播）。
	Publish(incidentID int, data []byte)
}

// Hub 维护 incident_id → 订阅客户端集合，支持订阅/退订/广播。
// 并发安全（多个 handler goroutine 同时操作）。
type Hub struct {
	mu      sync.RWMutex
	clients map[int]map[*client]struct{} // incident_id → 客户端集合
	// relay 跨副本中继（T6.4）。nil = 纯进程内广播（向后兼容单副本）。
	// 设为非 nil 后，本地广播会额外把消息发布到 Redis，供其它副本转发给各自连接。
	relay relay
}

// NewHub 创建 hub。
func NewHub() *Hub {
	return &Hub{clients: make(map[int]map[*client]struct{})}
}

// SetRelay 注入跨副本中继（T6.4）。装配层在多副本 pub/sub 可用时调用；
// 传 nil 或不调用则保持纯进程内广播。非并发安全：只应在启动装配期调用一次。
func (h *Hub) SetRelay(r relay) {
	h.relay = r
}

// Subscribe 客户端订阅某 incident 的变更。返回退订函数（连接关闭时调用）。
func (h *Hub) Subscribe(incidentID int, c *client) func() {
	h.mu.Lock()
	if h.clients[incidentID] == nil {
		h.clients[incidentID] = make(map[*client]struct{})
	}
	h.clients[incidentID][c] = struct{}{}
	h.mu.Unlock()

	return func() {
		h.mu.Lock()
		if subs, ok := h.clients[incidentID]; ok {
			delete(subs, c)
			if len(subs) == 0 {
				delete(h.clients, incidentID)
			}
		}
		h.mu.Unlock()
	}
}

// Broadcast 向某 incident 的所有订阅者广播消息。
// 无订阅者时静默跳过（常见：无人看详情页时 incident 变更无需推送）。
//
// 多副本（T6.4）：先本地直发（deliverLocal），再把消息透传到 relay（Redis pub/sub）
// 供其它副本转发给各自连接。本地已直发过，故其它副本据发起副本 id 去重跳过自己。
// relay 为 nil（单副本/无 Redis）时只做本地广播，行为与旧版一致。
func (h *Hub) Broadcast(incidentID int, msg Message) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	h.deliverLocal(incidentID, data)
	// 跨副本透传（best-effort）：即便本副本此刻无订阅者，也要发布——
	// 该 incident 的订阅者可能连在其它副本上。
	if h.relay != nil {
		h.relay.Publish(incidentID, data)
	}
}

// deliverLocal 把已序列化的消息投递给本副本上订阅该 incident 的连接。
// 供两个来源调用：① 本地 Broadcast；② relay 收到其它副本的跨副本消息后转发。
// 无本地订阅者时静默跳过。
func (h *Hub) deliverLocal(incidentID int, data []byte) {
	h.mu.RLock()
	subs := h.clients[incidentID]
	// 复制一份避免持锁发送（channel send 可能阻塞）
	clients := make([]*client, 0, len(subs))
	for c := range subs {
		clients = append(clients, c)
	}
	h.mu.RUnlock()

	for _, c := range clients {
		select {
		case c.send <- data:
		default:
			// 客户端 send 缓冲满（消费慢/断开），跳过避免阻塞广播。
			// 客户端读循环会因写超时清理该连接。
		}
	}
}

// BroadcastIncident 广播 incident 变更（最常用场景）。
func (h *Hub) BroadcastIncident(incidentID int, action string, snapshot any) {
	h.Broadcast(incidentID, Message{
		Type:       MsgIncidentChanged,
		IncidentID: incidentID,
		Action:     action,
		Data:       snapshot,
	})
}

// BroadcastTimelineAdded 广播时间线新增条目（B11）。
// timeline.Recorder 写入新条目后调用（经 timeline.TimelineBroadcaster 接口），
// 使订阅该 incident 的 Web 详情页时间线实时刷新（前端收到后 invalidate 时间线查询）。
// item 为刚写入的条目（作为消息 Data 下发，前端可直接插入而无需重查）。
func (h *Hub) BroadcastTimelineAdded(incidentID int, item any) {
	h.Broadcast(incidentID, Message{
		Type:       MsgTimelineAdded,
		IncidentID: incidentID,
		Data:       item,
	})
}

// OnIncidentEvent 领域事件适配：收到 incident 变更事件时广播给订阅者。
// 实现 event.Handler，供装配时 bus.Subscribe 挂载。
// 所有 incident 动作事件（ack/resolve/escalate/reopen/add_responder）统一走此入口。
func (h *Hub) OnIncidentEvent(ctx context.Context, e domainevent.Event) error {
	if e.Incident == nil {
		return nil
	}
	h.BroadcastIncident(e.Incident.ID, string(e.Action), e.Incident)
	return nil
}

// SubscribeDashboard 订阅看板增量（值班大屏/仪表盘用）。返回退订函数。
// 复用 per-incident 订阅通路，只是键固定为 dashboardTopic（全局单组）。
func (h *Hub) SubscribeDashboard(c *client) func() {
	return h.Subscribe(dashboardTopic, c)
}

// BroadcastDashboard 向所有看板订阅者广播一条看板更新增量。
// 复用 Broadcast → deliverLocal + relay（跨副本）通路：看板订阅者可能连在任意副本，
// 故同样走 Redis pub/sub 转发，多副本下大屏都能实时刷新。
//
// 载荷保持轻量：只带触发本次更新的 incident 摘要 + 动作，前端据此增量刷新
// （或作为「有变更」信号去 invalidate 拉一次最新 KPI），不在推送里塞全量指标。
func (h *Hub) BroadcastDashboard(action string, summary any) {
	h.Broadcast(dashboardTopic, Message{
		Type:       MsgDashboardUpdate,
		IncidentID: dashboardTopic,
		Action:     action,
		Data:       summary,
	})
}

// OnDashboardEvent 领域事件适配：任一 incident 生命周期事件发生时，向看板订阅者
// 广播一条轻量看板增量（不针对具体订阅，只发「全局有变更」信号 + 触发单摘要）。
// 实现 event.Handler，装配时对所有 incident 事件 bus.Subscribe 挂载。
//
// team scope 说明：大屏定位为 org 级只读 NOC 看板（握手要求 org 级 analytics.view），
// 故此处不按 team 过滤——所有看板订阅者都是有权看全组织的角色。
func (h *Hub) OnDashboardEvent(ctx context.Context, e domainevent.Event) error {
	if e.Incident == nil {
		return nil
	}
	h.BroadcastDashboard(string(e.Action), newDashboardSummary(e))
	return nil
}

// BroadcastDashboardTick 广播一条无 incident 的看板心跳（可选定时刷新）。
// 装配层可挂一个周期 ticker（如每 30s）调用此方法，让大屏即便无事件也定期重拉 KPI
// （兜底聚合类指标随时间推移的变化：MTTA/MTTR/近 N 分钟告警量）。action="tick"。
func (h *Hub) BroadcastDashboardTick() {
	h.BroadcastDashboard("tick", nil)
}

// dashboardSummary 看板增量载荷（轻量：仅触发单的关键字段 + 动作）。
// 不下发全量 incident（大屏无需详情），只给前端「哪个单、什么状态、什么动作」，
// 前端据此增量更新活跃列表 / 触发一次 KPI 重拉。
type dashboardSummary struct {
	IncidentID int    `json:"incident_id"`
	Number     string `json:"number,omitempty"`
	Title      string `json:"title,omitempty"`
	Severity   string `json:"severity,omitempty"`
	Status     string `json:"status,omitempty"`
	Action     string `json:"action,omitempty"`
}

// newDashboardSummary 从领域事件构造看板增量摘要。
func newDashboardSummary(e domainevent.Event) dashboardSummary {
	s := dashboardSummary{Action: string(e.Action)}
	if inc := e.Incident; inc != nil {
		s.IncidentID = inc.ID
		s.Number = inc.Number
		s.Title = inc.Title
		s.Severity = string(inc.Severity)
		s.Status = string(inc.Status)
	}
	return s
}

// newClient 创建客户端（send 带缓冲避免阻塞广播）。
func newClient() *client {
	return &client{send: make(chan []byte, 16)}
}
