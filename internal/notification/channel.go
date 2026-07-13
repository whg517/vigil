// Package notification 实现能力域 7：通知。
//
// 设计见 ADR-0017（逐通道兜底降级链 + 送达三态 + 聚合）：
// · Channel 接口（可插拔）—— IM/邮件/Webhook 各自实现（电话/SMS 已移除，ADR-0037）
// · Notifier 适配 escalation.Notifier，桥接升级触发与通道送达
// · 通知幂等（notification_id）、送达记录
//
// 已实现通道：Webhook / 邮件 / IM（钉钉/飞书）。
package notification

import (
	"context"

	"github.com/kevin/vigil/ent"
)

// Message 待发送的通知消息。
type Message struct {
	Incident  *ent.Incident // 事件上下文
	Targets   []Target      // 通知目标（已解析的人/team）
	Level     int           // 升级层级（0=首轮）
	Title     string        // 通知标题
	Summary   string        // 通知正文摘要
	ActionURL string        // ack/查看链接
	Channels  []string      // 启用的通道：im|phone|sms|email|webhook
}

// Target 通知目标（与 escalation.NotifyTarget 对齐，解耦两包）。
type Target struct {
	UserID int
	Name   string
	Source string // schedule | user | team
}

// SendResult 单次发送结果。
type SendResult struct {
	Channel string // webhook|email|im|...
	Target  string // 目标标识（user id/email/url）
	Success bool
	Error   string
}

// Channel 通知通道接口。各通道（Webhook/邮件/IM）实现此接口。
// 通道可插拔，统一接口（ADR-0017；新增通道的全部触点见 docs/extending.md）。
type Channel interface {
	// Name 通道标识：webhook | email | im。phone/sms 为预留名、未实现（占位已随 ADR-0037
	// 移除）：配置中残留的未知通道名 registry 查不到即跳过，降级链继续走下一通道。
	Name() string
	// Send 发送通知。返回送达结果。
	Send(ctx context.Context, msg *Message) ([]SendResult, error)
}

// Registry 通道注册表。按名字查找启用的通道。
type Registry struct {
	channels map[string]Channel
}

// NewRegistry 创建注册表。
func NewRegistry() *Registry {
	return &Registry{channels: make(map[string]Channel)}
}

// Register 注册通道。
func (r *Registry) Register(c Channel) {
	r.channels[c.Name()] = c
}

// Get 按名字取通道。
func (r *Registry) Get(name string) (Channel, bool) {
	c, ok := r.channels[name]
	return c, ok
}

// All 返回全部已注册通道。
func (r *Registry) All() []Channel {
	out := make([]Channel, 0, len(r.channels))
	for _, c := range r.channels {
		out = append(out, c)
	}
	return out
}
