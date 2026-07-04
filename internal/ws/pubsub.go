// pubsub.go 多副本 WebSocket 跨副本广播（T6.4，架构 §7 多副本）。
//
// 问题：hub.go 的广播是「进程内内存」——多个 API 副本时，连到副本 A 的客户端收不到
// 副本 B 处理的事件推送（各副本 clients map 互不可见），多副本下 WS 实时刷新不一致。
//
// 方案：Redis pub/sub 跨副本中继。
//   - 发布：hub 本地广播后（deliverLocal 已投递本副本连接），把消息发布到共享 Redis
//     channel（vigil:ws:broadcast），载荷含发起副本 id（replicaID）+ incident_id + 原始
//     WS 消息字节（复用 hub 已序列化的 Message，不重复编码）。
//   - 订阅：每个副本启动时订阅同一 channel，收到消息后转发给本副本上订阅该 incident 的连接。
//
// 去重防回环（自己发自己收）：Redis pub/sub 广播给**所有**订阅者（含发起副本自己）。
// 消息带 replicaID，收到时若等于本副本 id 直接丢弃——本副本的连接在 Broadcast 时已由
// deliverLocal 直发，Redis 这条只用于把消息带给**其它**副本。故「本地直发 + 仅跨副本走
// Redis 转发」，同一连接不会收到两次。
//
// 向后兼容：hub.relay 为 nil（未装配 pub/sub / 单副本 / 无 Redis）时退化纯进程内广播，
// 现有单副本行为不变。
package ws

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// broadcastChannel 跨副本广播的 Redis pub/sub channel 名。
// 全局单 channel（所有 incident 共用）：订阅者按载荷里的 incident_id 转发，避免为每个
// incident 建 channel（incident 数量大且动态，per-incident channel 订阅难管理）。
const broadcastChannel = "vigil:ws:broadcast"

// crossReplicaMessage 跨副本消息载荷（发布到 Redis channel 的 JSON）。
type crossReplicaMessage struct {
	// ReplicaID 发起广播的副本 id，用于收端去重（等于自己则丢弃，防回环重复推送）。
	ReplicaID string `json:"replica_id"`
	// IncidentID 目标 incident，收端据此转发给本副本订阅该 incident 的连接。
	IncidentID int `json:"incident_id"`
	// Payload 原始 WS 消息字节（hub 已序列化的 Message），收端原样投递给连接，不重复编解码。
	Payload json.RawMessage `json:"payload"`
}

// RedisPubSub 用 Redis pub/sub 做 WS 跨副本广播中继（实现 hub 的 relay 接口）。
//
// 生命周期：NewRedisPubSub 构造后调 Start 启动订阅 goroutine，返回的 stop 函数在优雅关闭时调用。
type RedisPubSub struct {
	rc        *redis.Client
	hub       *Hub
	replicaID string // 本副本唯一 id（进程启动时随机生成，用于去重）
	log       *zap.Logger
}

// NewRedisPubSub 创建跨副本中继。
//
//	rc  项目共享 Redis 客户端（复用，不新建连接池）
//	hub 本副本 WS hub（收到跨副本消息后经 hub.deliverLocal 转发本地连接）
//	log 日志器（订阅错误/重连记日志；可为 nil）
//
// replicaID 每进程随机生成——保证多副本各不相同，同一副本收到自己发的消息可去重丢弃。
func NewRedisPubSub(rc *redis.Client, hub *Hub, log *zap.Logger) *RedisPubSub {
	if log == nil {
		log = zap.NewNop()
	}
	return &RedisPubSub{
		rc:        rc,
		hub:       hub,
		replicaID: newReplicaID(),
		log:       log,
	}
}

// newReplicaID 生成本副本唯一 id（16 字节随机 → hex）。
// 多副本各不相同即可（用于去重），无需全局协调；rand 失败退回固定串（极罕见，
// 此时去重退化为「同串副本不去重」——仍不影响正确性，只是理论上可能重复推送一次）。
func newReplicaID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "replica-fallback"
	}
	return hex.EncodeToString(b)
}

// Publish 实现 relay 接口：把某 incident 的 WS 消息发布到跨副本 channel。
// best-effort：序列化/发布失败只记日志，不阻塞本地广播（本地连接已由 deliverLocal 收到）。
func (p *RedisPubSub) Publish(incidentID int, data []byte) {
	if p.rc == nil {
		return
	}
	env := crossReplicaMessage{
		ReplicaID:  p.replicaID,
		IncidentID: incidentID,
		Payload:    json.RawMessage(data),
	}
	raw, err := json.Marshal(env)
	if err != nil {
		p.log.Warn("ws pubsub marshal failed", zap.Error(err))
		return
	}
	// 用 background context：Publish 从 hub.Broadcast 调用栈进入，但发布本身与请求生命周期无关，
	// 不应因请求 ctx 取消而丢广播。发布是即时操作（Redis PUBLISH 非阻塞投递）。
	if err := p.rc.Publish(context.Background(), broadcastChannel, raw).Err(); err != nil {
		p.log.Warn("ws pubsub publish failed", zap.Int("incident_id", incidentID), zap.Error(err))
	}
}

// Start 启动订阅 goroutine，返回 stop 函数（优雅关闭时调用，会退订并关闭订阅连接）。
//
// 订阅同一 broadcastChannel，收到跨副本消息后：
//   - replicaID == 自己 → 丢弃（本地已直发，去重防回环）。
//   - 否则 → hub.deliverLocal 转发给本副本订阅该 incident 的连接。
//
// ctx 取消或 stop 调用时订阅 goroutine 退出。订阅在 goroutine 内建立并持续读，
// go-redis 的 PubSub.Channel() 内部处理断线重连。
func (p *RedisPubSub) Start(ctx context.Context) (stop func()) {
	if p.rc == nil {
		return func() {}
	}
	subCtx, cancel := context.WithCancel(ctx)
	sub := p.rc.Subscribe(subCtx, broadcastChannel)
	ch := sub.Channel()

	go func() {
		for {
			select {
			case <-subCtx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return // channel 关闭（订阅结束）
				}
				p.handle(msg.Payload)
			}
		}
	}()

	return func() {
		cancel()
		_ = sub.Close()
	}
}

// handle 处理一条跨副本消息：去重后转发本地连接。
func (p *RedisPubSub) handle(raw string) {
	var env crossReplicaMessage
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		p.log.Warn("ws pubsub unmarshal failed", zap.Error(err))
		return
	}
	// 去重防回环：本副本发起的广播本地已直发过，Redis 这条丢弃避免重复推送。
	if env.ReplicaID == p.replicaID {
		return
	}
	// 跨副本消息：转发给本副本订阅该 incident 的连接（无订阅者时静默跳过）。
	p.hub.deliverLocal(env.IncidentID, env.Payload)
}
