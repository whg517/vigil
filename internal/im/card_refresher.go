// card_refresher.go IM 卡片刷新的事件订阅入口。
//
// 替代原先 bootstrap 里 incService.SetOnIncidentChanged 闭包中的 IM 卡片刷新段：
// incident 变更时，遍历已发卡片刷新内容（Web→IM 双向同步）。
//
// 设计：incident.Service 不再持有 IM 相关引用，改为发布领域事件；
// 本 CardRefresher 订阅事件，调用各 IM bot 的 UpdateCard。
package im

import (
	"context"
	"fmt"

	domainevent "github.com/kevin/vigil/internal/event"
)

// CardRefresher 订阅 incident 变更事件，刷新已发 IM 卡片。
//
// 持有 IM bot 注册表（找可用 bot）与卡片 ID 存储（找已发卡片）。
// 零值不可用，用 NewCardRefresher 构造。
type CardRefresher struct {
	registry *Registry
	cards    CardStore
}

// NewCardRefresher 创建卡片刷新器。
func NewCardRefresher(reg *Registry, cards CardStore) *CardRefresher {
	return &CardRefresher{registry: reg, cards: cards}
}

// OnIncidentEvent 领域事件适配：incident 变更时刷新所有已发卡片。
// 实现 event.Handler，供装配时 bus.Subscribe 挂载。
// 无已发卡片（首次创建时卡片尚未下发）时静默跳过。
func (r *CardRefresher) OnIncidentEvent(ctx context.Context, e domainevent.Event) error {
	if r.registry == nil || r.cards == nil || e.Incident == nil {
		return nil
	}
	inc := e.Incident
	card := BuildCard(inc, "")
	// B16：Web→IM 同步的状态徽章，钉钉降级重发时作为群内可见的状态变更提示。
	card.StatusBadge = fmt.Sprintf("⚠️ %s %s", inc.Number, statusLabelCN(inc.Status))
	for _, bot := range r.registry.Available() {
		if cardID, ok := r.cards.Get(ctx, inc.ID, bot.Platform()); ok {
			_ = bot.UpdateCard(ctx, cardID, card)
		}
	}
	return nil
}
