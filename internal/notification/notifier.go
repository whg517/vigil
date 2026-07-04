// notifier.go 通知分发器：实现 escalation.Notifier，把升级触发分发到各通道。
//
// 分发核心：逐通道兜底降级链（B7/C12），而非无脑并联。
//
// 降级链设计（capabilities/04 §4「通道优先级与降级」）：
//   - msg.Channels 是一条有序降级链（主通道在前，兜底在后），来源优先级见 resolveChannels。
//   - 对每个 target，按链顺序逐通道尝试：首个成功即「送达」并停止该 target 的链（不再往下发），
//     失败则降级到下一通道。这与原实现「每通道各发一份」的并联语义相反——并联会重复打扰、
//     且无「主通道失败才兜底」的层次（IM 优先、电话/SMS 仅在前面失败时才强打扰）。
//   - 整条链全失败 → 记 failed + 触发兜底告警（allFailedHook，通常通知 org_admin）。
//   - 静默时段命中（非 critical、非值班人）→ 记 suppressed，不发也不丢（B22 可查可补发）。
//
// 注：notification 单向依赖 escalation（escalation 不反向依赖），无循环。
package notification

import (
	"context"
	"fmt"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/internal/escalation"
	"github.com/kevin/vigil/internal/metrics"
)

// Notifier 通知分发器，实现 escalation.Notifier 接口。
// 把升级 targets 组装成 Message，按降级链分发到注册的通道。
type Notifier struct {
	registry     *Registry
	defaultChans []string // 默认降级链（当规则/层级都未指定通道时兜底）
	// recordResult 送达记录回调（持久化 metrics 层，保留兼容），可选。
	recordResult func(incID int, r SendResult)
	// delivery 送达三态落库（Notification 实体，B22/M13），可选（nil 时不落库）。
	delivery DeliveryRecorder
	// quietHours 静默时段评估（能力域 7 M7.8）。nil 时不静默（降级）。
	quietHoursResolver func(inc *ent.Incident) *QuietHours
	// ruleResolver 按 incident 解析适用的 NotificationRule（B7/C12：channels/template/quiet_hours）。
	ruleResolver *RuleResolver
	// aggregator 通知聚合（能力域 7 M7.9）。nil 时不聚合（立即发送，降级）。
	aggregator *Aggregator
	// templates 通知模板引擎（能力域 7 M7.5）。nil 时用 FormatTitle/Summary 兜底。
	templates *TemplateEngine
	// templateNameResolver 兜底模板名解析（当规则未指定模板时）。
	templateNameResolver func(inc *ent.Incident) string
	// allFailedHook 整条降级链对某 target 全失败时的兜底告警回调（B22），可选。
	// 由装配方注入（如通知 org_admin）。参数为失败上下文，实现方决定如何兜底。
	allFailedHook func(ctx context.Context, inc *ent.Incident, t Target, title, summary string)
}

// NewNotifier 创建通知分发器。
func NewNotifier(reg *Registry, defaultChannels []string) *Notifier {
	if len(defaultChannels) == 0 {
		defaultChannels = []string{"webhook"}
	}
	return &Notifier{registry: reg, defaultChans: defaultChannels}
}

// Registry 返回底层通道注册表（供装配方晚注册通道，如 IMChannel）。
func (n *Notifier) Registry() *Registry { return n.registry }

// SetResultRecorder 设置送达记录回调（结构化日志/metrics 层）。
func (n *Notifier) SetResultRecorder(fn func(incID int, r SendResult)) {
	n.recordResult = fn
}

// SetDeliveryRecorder 注入送达三态落库器（Notification 实体，B22/M13）。
func (n *Notifier) SetDeliveryRecorder(d DeliveryRecorder) { n.delivery = d }

// SetRuleResolver 注入通知规则解析器（B7/C12：condition→channels/template/quiet_hours）。
func (n *Notifier) SetRuleResolver(r *RuleResolver) { n.ruleResolver = r }

// SetAllFailedHook 注入整条链全失败时的兜底告警回调（B22：全通道失败告警 org_admin）。
func (n *Notifier) SetAllFailedHook(fn func(ctx context.Context, inc *ent.Incident, t Target, title, summary string)) {
	n.allFailedHook = fn
}

// SetQuietHoursResolver 注入静默时段解析器。
// resolver 按 incident 返回适用的静默配置；nil 表示该事件不静默。
// 注：若已注入 RuleResolver，则规则命中的 quiet_hours 优先；本 resolver 作兜底。
func (n *Notifier) SetQuietHoursResolver(resolver func(inc *ent.Incident) *QuietHours) {
	n.quietHoursResolver = resolver
}

// SetAggregator 注入通知聚合器。nil 表示不聚合（立即发送）。
func (n *Notifier) SetAggregator(a *Aggregator) {
	n.aggregator = a
}

// SetTemplateEngine 注入通知模板引擎（能力域 7 M7.5）。
// resolver 按 incident 返回兜底模板名（当规则未指定模板时用）。
func (n *Notifier) SetTemplateEngine(e *TemplateEngine, resolver func(inc *ent.Incident) string) {
	n.templates = e
	n.templateNameResolver = resolver
}

// resolveChannels 决定本次分发的降级链（有序），优先级从高到低：
//  1. levelChannels：本层 EscalationLevel.notify_channels（T2.1，最贴近升级意图）；
//  2. rule.Channels：命中的 NotificationRule.channels（B7/C12，管理员配置的通道链）；
//  3. defaultChans：全局默认链（向后兼容，无任何配置也能发）。
func (n *Notifier) resolveChannels(levelChannels []string, rule *MatchedRule) []string {
	if len(levelChannels) > 0 {
		return levelChannels
	}
	if rule != nil && len(rule.Channels) > 0 {
		return rule.Channels
	}
	return n.defaultChans
}

// resolveQuietHours 决定适用的静默配置：规则命中的优先，否则用兜底 resolver。
func (n *Notifier) resolveQuietHours(inc *ent.Incident, rule *MatchedRule) *QuietHours {
	if rule != nil && rule.QuietHours != nil {
		return rule.QuietHours
	}
	if n.quietHoursResolver != nil {
		return n.quietHoursResolver(inc)
	}
	return nil
}

// resolveTemplateName 决定模板名：规则命中的优先，否则用兜底 resolver。
func (n *Notifier) resolveTemplateName(inc *ent.Incident, rule *MatchedRule) string {
	if rule != nil && rule.TemplateName != "" {
		return rule.TemplateName
	}
	if n.templateNameResolver != nil {
		return n.templateNameResolver(inc)
	}
	return ""
}

// NotifyEscalation 实现 escalation.Notifier。
// targets 来自升级引擎（已解析的人/team）；level 为升级层级；
// channels 为本层 EscalationLevel.notify_channels（T2.1）。
func (n *Notifier) NotifyEscalation(ctx context.Context, inc *ent.Incident, level int, targets []escalation.NotifyTarget, channels []string) error {
	msgTargets := make([]Target, 0, len(targets))
	for _, t := range targets {
		msgTargets = append(msgTargets, Target{UserID: t.UserID, Name: t.Name, Source: t.Source})
	}

	// B7/C12：按 incident 解析适用规则（condition 匹配），取其 channels/template/quiet_hours。
	var rule *MatchedRule
	if n.ruleResolver != nil {
		rule = n.ruleResolver.Resolve(ctx, inc)
	}
	chans := n.resolveChannels(channels, rule)

	msg := &Message{
		Incident: inc,
		Targets:  msgTargets,
		Level:    level,
		Title:    FormatTitle(inc),
		Summary:  FormatSummary(inc, level),
		Channels: chans,
	}
	// 模板渲染（能力域 7 M7.5）：规则模板优先，渲染失败内部降级，不丢通知。
	if n.templates != nil {
		tmplName := n.resolveTemplateName(inc, rule)
		chanForDefault := ""
		if len(chans) > 0 {
			chanForDefault = chans[0]
		}
		rendered, rerr := n.templates.Render(ctx, tmplName, chanForDefault, TemplateData{
			Incident: inc, Targets: msgTargets, Level: level,
		})
		if rerr == nil && rendered != nil {
			if rendered.Title != "" {
				msg.Title = rendered.Title
			}
			if rendered.Body != "" {
				msg.Summary = rendered.Body
			}
		}
	}

	severity := string(inc.Severity)
	qh := n.resolveQuietHours(inc, rule)

	// 逐 target 处理：静默判定 → 聚合判定 → 降级链发送。
	var firstErr error
	for _, t := range msgTargets {
		isOncall := t.Source == "schedule" // 值班人始终通知，不静默

		// 静默时段（M7.8 / B22）：命中则记 suppressed，不发也不丢（可补发）。
		if qh != nil && qh.ShouldSuppress(severity, isOncall, nil) {
			n.recordDelivery(ctx, DeliveryRecord{
				IncidentID: incID(inc), UserID: t.UserID, Channel: firstChan(chans),
				Target: targetKey(t), Status: StatusSuppressed,
				Reason: "quiet_hours", Level: level, Severity: severity,
			})
			continue
		}

		// 通知聚合（M7.9）：非 critical 入 per-target 队列，窗口结束合并发送（走 Flush 的降级链）。
		if n.aggregator != nil && severity != "critical" {
			item := AggregatedItem{
				IncidentID: inc.ID, Title: msg.Title, Summary: msg.Summary,
				Level: level, Severity: severity,
			}
			dec, aerr := n.aggregator.Add(ctx, targetKey(t), severity, item)
			if aerr != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("aggregate: %w", aerr)
				}
				// 聚合失败：降级为立即走降级链，避免丢通知
				n.deliverChain(ctx, inc, msg, t, chans, level, severity)
				continue
			}
			if dec != nil && dec.SendNow {
				n.deliverChain(ctx, inc, msg, t, chans, level, severity)
			}
			// 否则已入聚合队列，等 Flush（FlushAggregated 走同一降级链）
			continue
		}

		// 非聚合（critical 或无聚合器）：立即走降级链。
		n.deliverChain(ctx, inc, msg, t, chans, level, severity)
	}
	return firstErr
}

// deliverChain 对单个 target 执行逐通道兜底降级链（B7/C12）。
//
// 按 chans 顺序逐通道尝试：首个成功即停止（送达），失败降级到下一通道。
// 整条链全失败 → 记 failed + 触发兜底告警（allFailedHook）。
//
// IM 通道对无关联 Incident 的消息（msg.Incident==nil，如 unrouted 兜底）不可渲染卡片，
// 跳过（不计入失败，链继续尝试其它通道）。
func (n *Notifier) deliverChain(ctx context.Context, inc *ent.Incident, msg *Message, t Target, chans []string, level int, severity string) {
	// 单 target 消息：降级链对「这一个人」逐通道尝试，故 msg 复制一份只含该 target。
	single := *msg
	single.Targets = []Target{t}

	delivered := false
	var lastErr string
	for _, chanName := range chans {
		ch, ok := n.registry.Get(chanName)
		if !ok {
			continue // 通道未注册（如 IM 未接入），跳过（不算失败，链继续）
		}
		// IM 无单不可渲染卡片：跳过而不计失败。
		if chanName == "im" && single.Incident == nil {
			continue
		}
		ok, errStr := n.sendOne(ctx, ch, &single, inc, t, level, severity)
		if ok {
			delivered = true
			break // 降级链核心：首个成功即停止，不再往下打扰
		}
		lastErr = errStr
		// 失败：继续降级到下一通道
	}

	if !delivered {
		// 整条链全失败（或无任何可用通道）：记 failed + 兜底告警（B22）。
		reason := lastErr
		if reason == "" {
			reason = "no available channel in fallback chain"
		}
		n.recordDelivery(ctx, DeliveryRecord{
			IncidentID: incID(inc), UserID: t.UserID, Channel: firstChan(chans),
			Target: targetKey(t), Status: StatusFailed,
			Reason: reason, Level: level, Severity: severity,
		})
		// 兜底告警仅在有 incident 上下文时触发（inc!=nil）。
		// 未路由兜底/兜底告警本身（inc==nil）全失败时不再递归触发 hook——否则
		// 「兜底告警发不出去 → 又发一条兜底告警 → 又发不出去」会无限递归。
		if inc != nil && n.allFailedHook != nil {
			n.allFailedHook(ctx, inc, t, msg.Title, msg.Summary)
		}
	}
}

// NotifyUnrouted 未路由兜底通知（C3）：把一条不关联 Incident 的告警送达给指定收件人。
//
// 与 NotifyEscalation 的区别：无 Incident 上下文（未路由 Event 尚未建单），故不走
// IM 卡片通道；只走 email/phone/sms/webhook。不聚合、不静默（兜底通知「必达」语义）。
// 但仍走降级链：逐通道尝试，首个成功即停。
func (n *Notifier) NotifyUnrouted(ctx context.Context, targets []Target, title, summary string, channels []string) error {
	chans := channels
	if len(chans) == 0 {
		chans = n.defaultChans
	}
	msg := &Message{
		Incident: nil, Targets: targets, Level: 0,
		Title: title, Summary: summary, Channels: chans,
	}
	for _, t := range targets {
		n.deliverChain(ctx, nil, msg, t, chans, 0, "")
	}
	return nil
}

// targetKey 聚合队列/送达记录按 target 维度，user 用 user_id，team/source=team 用 source 名。
func targetKey(t Target) string {
	if t.UserID > 0 {
		return fmt.Sprintf("user:%d", t.UserID)
	}
	return "team:" + t.Name
}

// firstChan 取降级链首通道名（记录 channel 字段用），空链返回 ""。
func firstChan(chans []string) string {
	if len(chans) > 0 {
		return chans[0]
	}
	return ""
}

// incID 取 incident id（nil 安全，未路由兜底返 0）。
func incID(inc *ent.Incident) int {
	if inc == nil {
		return 0
	}
	return inc.ID
}

// recordDelivery 落一条送达记录（三态，B22/M13）。delivery 未注入时降级为无操作。
func (n *Notifier) recordDelivery(ctx context.Context, rec DeliveryRecord) {
	if n.delivery == nil {
		return
	}
	_ = n.delivery.Record(ctx, rec)
}

// sendOne 发送到单个通道，记录 metrics + 送达记录。返回是否有任一成功送达 + 最后错误串。
//
// 一个通道 Send 可能返回多条结果（如 webhook 多 URL、email 多收件人）：
// 只要有一条成功即视为该通道对该 target 送达成功（链停止）。
func (n *Notifier) sendOne(ctx context.Context, ch Channel, msg *Message, inc *ent.Incident, t Target, level int, severity string) (bool, string) {
	results, err := ch.Send(ctx, msg)
	if err != nil {
		results = append(results, SendResult{Channel: ch.Name(), Error: err.Error()})
	}
	anySuccess := false
	lastErr := ""
	for _, r := range results {
		resultLabel := "success"
		if r.Success {
			anySuccess = true
		} else {
			resultLabel = "failed"
			lastErr = r.Error
		}
		metrics.NotificationsSent.WithLabelValues(r.Channel, resultLabel).Inc()
		if n.recordResult != nil {
			n.recordResult(incID(inc), r)
		}
		// 送达记录（B22/M13）：每条结果落一条 Notification（sent/failed）。
		status := StatusSent
		reason := ""
		if !r.Success {
			status = StatusFailed
			reason = r.Error
		}
		n.recordDelivery(ctx, DeliveryRecord{
			IncidentID: incID(inc), UserID: t.UserID, Channel: r.Channel,
			Target: pickTarget(r.Target, t), Status: status,
			Reason: reason, Level: level, Severity: severity,
		})
	}
	// 通道 Send 返回空结果（如未配置降级）：视为该通道不可用，链继续（不算成功不算硬失败）。
	if len(results) == 0 {
		return false, ""
	}
	return anySuccess, lastErr
}

// pickTarget 优先用通道返回的具体目标（email/url/phone），否则用 target key。
func pickTarget(chanTarget string, t Target) string {
	if chanTarget != "" {
		return chanTarget
	}
	return targetKey(t)
}

// FlushAggregated 刷新某 target 的聚合队列，窗口到则合并发送（走降级链）。
// 由定时任务（wire 注册 ticker）按 target 维度驱动。返回实际合并发送的条目数。
func (n *Notifier) FlushAggregated(ctx context.Context, targetID string) (int, error) {
	if n.aggregator == nil {
		return 0, nil
	}
	items, err := n.aggregator.Flush(ctx, targetID)
	if err != nil {
		return 0, err
	}
	if len(items) == 0 {
		return 0, nil
	}
	// 合并成一条汇总 Message（按首条 + 计数）。
	first := items[0]
	summary := first.Summary
	if len(items) > 1 {
		summary = fmt.Sprintf("%s（含 %d 条聚合通知）", first.Summary, len(items))
	}
	msg := &Message{
		Title:    fmt.Sprintf("[聚合] %s", first.Title),
		Summary:  summary,
		Channels: n.defaultChans,
		Level:    first.Level,
	}
	// 聚合 flush 无原始 target 上下文（队列 key 即 targetID），用占位 Target 走降级链。
	// incident 上下文按首条 IncidentID 尽力关联送达记录。
	t := Target{Name: targetID}
	n.deliverChain(ctx, nil, msg, t, msg.Channels, first.Level, first.Severity)
	return len(items), nil
}

// FlushAll 扫描所有有积压待发通知的 target，逐个 FlushAggregated 合并发送。
// 返回本次实际 flush 的 target 数 + 首个错误（不因单个 target 失败中断其余）。
func (n *Notifier) FlushAll(ctx context.Context) (int, error) {
	if n.aggregator == nil {
		return 0, nil
	}
	targets, err := n.aggregator.PendingTargets(ctx)
	if err != nil {
		return 0, err
	}
	flushed := 0
	var firstErr error
	for _, t := range targets {
		n2, err := n.FlushAggregated(ctx, t)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if n2 > 0 {
			flushed++
		}
	}
	return flushed, firstErr
}
