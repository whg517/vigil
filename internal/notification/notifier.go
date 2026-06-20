// notifier.go 通知分发器：实现 escalation.Notifier，把升级触发分发到各通道。
package notification

import (
	"context"
	"fmt"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/internal/escalation"
)

// Notifier 通知分发器，实现 escalation.Notifier 接口。
// 把升级 targets 组装成 Message，按 msg.Channels 分发到注册的通道。
// 注：notification 单向依赖 escalation（escalation 不反向依赖），无循环。
type Notifier struct {
	registry     *Registry
	defaultChans []string                   // 默认启用通道（当 msg.Channels 为空时）
	recordResult func(incID int, r SendResult) // 送达记录回调（持久化），可选
}

// NewNotifier 创建通知分发器。
func NewNotifier(reg *Registry, defaultChannels []string) *Notifier {
	if len(defaultChannels) == 0 {
		defaultChannels = []string{"webhook"}
	}
	return &Notifier{registry: reg, defaultChans: defaultChannels}
}

// SetResultRecorder 设置送达记录回调（由 main 注入，写 Notification 记录）。
func (n *Notifier) SetResultRecorder(fn func(incID int, r SendResult)) {
	n.recordResult = fn
}

// NotifyEscalation 实现 escalation.Notifier。
// targets 来自升级引擎（已解析的人/team）；level 为升级层级。
func (n *Notifier) NotifyEscalation(ctx context.Context, inc *ent.Incident, level int, targets []escalation.NotifyTarget) error {
	msgTargets := make([]Target, 0, len(targets))
	for _, t := range targets {
		msgTargets = append(msgTargets, Target{UserID: t.UserID, Name: t.Name, Source: t.Source})
	}

	msg := &Message{
		Incident: inc,
		Targets:  msgTargets,
		Level:    level,
		Title:    FormatTitle(inc),
		Summary:  FormatSummary(inc, level),
		Channels: n.defaultChans, // 升级场景用默认通道；完整实现按 NotificationRule 解析
	}

	// 分发到启用的通道
	var firstErr error
	for _, chanName := range msg.Channels {
		ch, ok := n.registry.Get(chanName)
		if !ok {
			continue // 通道未注册（如 IM 未接入），跳过
		}
		results, err := ch.Send(ctx, msg)
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("channel %s: %w", chanName, err)
		}
		// 记录送达结果
		if n.recordResult != nil {
			for _, r := range results {
				n.recordResult(inc.ID, r)
			}
		}
	}
	return firstErr
}
