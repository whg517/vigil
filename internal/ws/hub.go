// Package ws 实现 WebSocket 实时推送（能力域 8 §状态双向同步 + 架构 §6.3）。
//
// 单实例内存 hub：客户端按 incident_id 订阅，incident 状态变更时广播给订阅者。
// 多实例部署时需换 Redis pub/sub（架构 §6.4 已预留），本期单实例起步。
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
)

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

// Hub 维护 incident_id → 订阅客户端集合，支持订阅/退订/广播。
// 并发安全（多个 handler goroutine 同时操作）。
type Hub struct {
	mu      sync.RWMutex
	clients map[int]map[*client]struct{} // incident_id → 客户端集合
}

// NewHub 创建 hub。
func NewHub() *Hub {
	return &Hub{clients: make(map[int]map[*client]struct{})}
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
func (h *Hub) Broadcast(incidentID int, msg Message) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
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

// newClient 创建客户端（send 带缓冲避免阻塞广播）。
func newClient() *client {
	return &client{send: make(chan []byte, 16)}
}
