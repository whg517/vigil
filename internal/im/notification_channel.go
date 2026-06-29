// notification_channel.go 把 IM 适配成 notification.Channel。
//
// 这是集成缺口的补全：让升级触发通知时，告警通过 IM 卡片送达值班人。
// 对应 capabilities/04-notification.md §4（IM 通道）+ capabilities/05（IM 卡片）。
//
// 依赖方向：im → notification（单向，im 实现 notification.Channel 接口）。
package im

import (
	"context"
	"fmt"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/internal/notification"
)

// 确保 IMChannel 实现 notification.Channel 接口。
var _ notification.Channel = (*IMChannel)(nil)

// IMChannel 把 IM Registry 适配成 notification.Channel。
// 升级通知触发时，把 Incident 渲染成卡片，通过可用 IM bot 发送。
type IMChannel struct {
	registry   *Registry                                                     // IM bot 注册表
	cardStore  *CardStore                                                    // 记录已发卡片（供后续状态更新）
	getChannel func(inc *ent.Incident, targets []notification.Target) string // 解析目标 IM channel（群ID/私聊）
	renderer   *Renderer                                                     // 按接收者权限渲染按钮（QA 审计 C5）
}

// NewIMChannel 创建 IM 通知通道。
// getChannel 返回应发送到的 IM channel 标识（值班群 ID 或目标用户私聊标识）；
// 为 nil 时跳过发送（无目标 channel）。
func NewIMChannel(reg *Registry, cards *CardStore, getChannel func(inc *ent.Incident, targets []notification.Target) string) *IMChannel {
	return &IMChannel{registry: reg, cardStore: cards, getChannel: getChannel}
}

// SetRenderer 注入卡片渲染器（QA 审计 C5）。
// 渲染器为 nil 时，卡片渲染全部默认按钮（宽松渲染，回调侧 resolveAndCheck 兜底鉴权）。
// 渲染器非 nil 且能解析接收者 user_id 时，按其权限裁剪按钮（权限感知卡片 M8.7）。
func (c *IMChannel) SetRenderer(r *Renderer) { c.renderer = r }

// Name 实现 notification.Channel。
func (c *IMChannel) Name() string { return "im" }

// Send 实现 notification.Channel：把通知渲染成 IM 卡片并发送。
func (c *IMChannel) Send(ctx context.Context, msg *notification.Message) ([]notification.SendResult, error) {
	if msg.Incident == nil {
		return nil, fmt.Errorf("im channel requires incident in message")
	}
	// 解析目标 channel
	var channel string
	if c.getChannel != nil {
		channel = c.getChannel(msg.Incident, msg.Targets)
	}
	if channel == "" {
		// 无目标 channel（如未配置值班群），跳过——不是错误
		return nil, nil
	}

	// 渲染卡片（assignee 取首个 target 名字，用于卡片展示）
	assignee := ""
	if len(msg.Targets) > 0 {
		assignee = msg.Targets[0].Name
	}
	card := BuildCard(msg.Incident, assignee)

	// QA 审计 C5：通知主路径必须渲染操作按钮，否则值班人收到的告警卡只能看不能点，
	// IM 差异化核心（不切系统完成 ack/升级/解决）失效。
	// 策略：宽松渲染 + 回调硬鉴权。能解析接收者 user_id 且有渲染器时按权限裁剪（M8.7）；
	// 否则渲染全部默认按钮（回调 resolveAndCheck 是权威鉴权，卡片侧宽松安全）。
	buttons := DefaultButtons()
	if c.renderer != nil && len(msg.Targets) > 0 {
		// 取首个有效 user_id 作为接收者代表渲染按钮
		for _, t := range msg.Targets {
			if t.UserID > 0 {
				if err := c.renderer.WithPermittedButtons(card, t.UserID, nil, DefaultButtons()); err == nil {
					buttons = card.Buttons // WithPermittedButtons 已写入 card.Buttons
				}
				break
			}
		}
	}
	card.Buttons = buttons

	var results []notification.SendResult
	// 通过所有可用 IM bot 发送（多平台冗余送达）
	for _, bot := range c.registry.Available() {
		cardID, err := bot.SendCard(ctx, channel, card)
		r := notification.SendResult{
			Channel: "im",
			Target:  fmt.Sprintf("%s:%s", bot.Platform(), channel),
		}
		if err != nil {
			r.Error = err.Error()
			results = append(results, r)
			continue
		}
		r.Success = true
		// 记录已发卡片，供后续状态变更时 UpdateCard（§8 双向同步）
		if c.cardStore != nil {
			c.cardStore.Put(msg.Incident.ID, bot.Platform(), cardID)
		}
		results = append(results, r)
	}
	return results, nil
}
