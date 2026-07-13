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
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/internal/escalation"
	"github.com/kevin/vigil/internal/metrics"

	"github.com/hibiken/asynq"
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
	// taskEnqueuer + deliveryStore 投递 Asynq 化（ADR-0017 修订）：两者都注入后，
	// deliverChain 不再同步直投，而是先落 pending 行再入队独立投递任务（瞬时失败
	// 指数退避重试）。任一为 nil（单测/无队列降级）时回退同步直投。
	taskEnqueuer  TaskEnqueuer
	deliveryStore DeliveryStore
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

// SetAsyncDelivery 注入异步投递依赖（ADR-0017 修订：通知重试 Asynq 化）。
// enq 通常为 *asynq.Client；store 为可更新 pending 行的送达记录存取器。
// 任一为 nil 时保持同步直投（向后兼容/降级路径）。
func (n *Notifier) SetAsyncDelivery(enq TaskEnqueuer, store DeliveryStore) {
	n.taskEnqueuer = enq
	n.deliveryStore = store
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

// deliverChain 单 target 投递入口（B7/C12 + ADR-0017 修订）。
//
// 已装配异步投递（SetAsyncDelivery）时：先落 pending 行，再把投递封装为独立 Asynq
// 任务（TaskID=notif:{行 ID} 幂等），降级链由 worker 执行，瞬时失败走 asynq 指数退避
// 重试；未装配或入队失败（Redis 不可用）时回退同步直投——降级语义是「可能少重试，
// 绝不丢通知」。
func (n *Notifier) deliverChain(ctx context.Context, inc *ent.Incident, msg *Message, t Target, chans []string, level int, severity string) {
	if n.enqueueDelivery(ctx, inc, msg, t, chans, level, severity) {
		return
	}
	n.deliverChainSync(ctx, inc, msg, t, chans, level, severity)
}

// enqueueDelivery 把单 target 投递封装为 Asynq 任务。返回 true 表示已受理
// （已入队，或入队失败但已同步兜底完成）；false 表示异步未装配，调用方走同步直投。
func (n *Notifier) enqueueDelivery(ctx context.Context, inc *ent.Incident, msg *Message, t Target, chans []string, level int, severity string) bool {
	if n.taskEnqueuer == nil || n.deliveryStore == nil {
		return false
	}
	// ① Notification 行先落库（pending）：行 ID 即任务幂等键，重投/重复入队都被挡住。
	id, err := n.deliveryStore.CreatePending(ctx, DeliveryRecord{
		IncidentID: incID(inc), UserID: t.UserID, Channel: firstChan(chans),
		Target: targetKey(t), Status: StatusPending,
		Reason: "queued for delivery", Level: level, Severity: severity,
	})
	if err != nil {
		return false // 落 pending 行失败（DB 抖动）：回退同步直投，保证不丢
	}
	payload, err := json.Marshal(deliveryTask{
		NotificationID: id, IncidentID: incID(inc), Target: t,
		Title: msg.Title, Summary: msg.Summary, ActionURL: msg.ActionURL,
		Channels: chans, Level: level, Severity: severity,
	})
	if err != nil {
		// 纯数据结构 marshal 实际不可失败；防御：同步兜底并回写本行，不留孤儿 pending。
		n.deliverTracked(ctx, id, inc, msg, t, chans, level, severity, true)
		return true
	}
	// ② 队列沿用 critical/default/low 约定：critical 告警的通知不排队等低优任务。
	queueName := "default"
	if severity == "critical" {
		queueName = "critical"
	}
	_, err = n.taskEnqueuer.EnqueueContext(ctx, asynq.NewTask(TaskDeliver, payload),
		asynq.Queue(queueName),
		asynq.TaskID(deliveryTaskID(id)),
		asynq.MaxRetry(deliverMaxRetry),
	)
	if err != nil {
		// TaskID 冲突 = 同一行的任务已在队（幂等命中），目标已达成。
		if errors.Is(err, asynq.ErrTaskIDConflict) {
			return true
		}
		// ③ 入队失败（Redis 不可用等）：同步直投并把结果回写本行（final=true：
		// 队列已不可用，无从重试，保持旧的一次性尽力语义）。
		n.deliverTracked(ctx, id, inc, msg, t, chans, level, severity, true)
		return true
	}
	return true
}

// deliverChainSync 对单个 target 同步执行逐通道兜底降级链（原 deliverChain 语义原样保留）。
//
// 按 chans 顺序逐通道尝试：首个成功即停止（送达），失败降级到下一通道。
// 整条链全失败 → 记 failed + 触发兜底告警（allFailedHook）。
func (n *Notifier) deliverChainSync(ctx context.Context, inc *ent.Incident, msg *Message, t Target, chans []string, level int, severity string) {
	delivered, _, _, lastErr := n.walkChain(ctx, inc, msg, t, chans, level, severity, true)

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

// walkChain 逐通道兜底降级链核心（deliverChainSync 与异步 worker 共用）。
//
// record=true：每条通道结果照旧追加送达记录（同步路径，行为与历史一致）；
// record=false：不追加每次尝试的记录（异步路径由 tracking pending 行统一承载结果，
// 避免「重试次数 × 通道数」的记录膨胀轰炸送达账本）。metrics 与结构化日志两种模式都记。
//
// IM 通道对无关联 Incident 的消息（msg.Incident==nil，如 unrouted 兜底）不可渲染卡片，
// 跳过（不计入失败，链继续尝试其它通道）。
// 返回：是否送达、成功的那条结果、是否有任一通道真正尝试过（返回过结果）、最后错误。
func (n *Notifier) walkChain(ctx context.Context, inc *ent.Incident, msg *Message, t Target, chans []string, level int, severity string, record bool) (bool, SendResult, bool, string) {
	// 单 target 消息：降级链对「这一个人」逐通道尝试，故 msg 复制一份只含该 target。
	single := *msg
	single.Targets = []Target{t}

	attempted := false
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
		ok, via, tried, errStr := n.sendOne(ctx, ch, &single, inc, t, level, severity, record)
		if tried {
			attempted = true
		}
		if ok {
			return true, via, true, "" // 降级链核心：首个成功即停止，不再往下打扰
		}
		lastErr = errStr
		// 失败：继续降级到下一通道
	}
	return false, SendResult{}, attempted, lastErr
}

// deliverTracked 执行降级链并把结果回写 tracking 行 id（异步 worker / 入队失败兜底共用）。
//
// final 语义：是否「最后一次尝试」（重试耗尽，或队列不可用的同步兜底）。
//   - 送达成功 → 行置 sent（记实际送达通道/目标）；
//   - 失败且非 final → 行保持 pending、更新 reason（在途可观测），返回 retryable=true
//     交给 asynq 指数退避重试；
//   - 失败且 final → 行置 failed + 触发 allFailedHook（只在最后一次触发，
//     避免每轮重试都轰炸 org_admin）；
//   - 整链无一通道可用（未注册/未配置，attempted=false）→ 配置性失败，重试无益：
//     立即按 final 处理（保持旧「无可用通道即失败」语义，不做无意义的 5 轮重试）。
//
// 返回：是否送达、是否值得重试、失败原因。
func (n *Notifier) deliverTracked(ctx context.Context, id int, inc *ent.Incident, msg *Message, t Target, chans []string, level int, severity string, final bool) (bool, bool, string) {
	delivered, via, attempted, lastErr := n.walkChain(ctx, inc, msg, t, chans, level, severity, false)
	if delivered {
		// 回写失败（DB 抖动）时任务仍按成功结束——宁可留一条 stale pending 行
		//（可观测异常），不可为回写而重试任务导致重复送达。
		_ = n.deliveryStore.UpdateStatus(ctx, id, StatusSent, via.Channel, pickTarget(via.Target, t), "")
		return true, false, ""
	}
	reason := lastErr
	if reason == "" {
		reason = "no available channel in fallback chain"
	}
	if !final && attempted {
		// 瞬时失败：行保持 pending、reason 记录最后错误，等 asynq 退避重试。
		_ = n.deliveryStore.UpdateStatus(ctx, id, StatusPending, "", "", reason)
		return false, true, reason
	}
	// 终局失败（重试耗尽 / 无可用通道 / 同步兜底失败）：落 failed + 兜底告警（B22）。
	_ = n.deliveryStore.UpdateStatus(ctx, id, StatusFailed, "", "", reason)
	// 兜底告警仅在有 incident 上下文时触发；hook 内部走 NotifyUnrouted（同步、不递归）。
	if inc != nil && n.allFailedHook != nil {
		n.allFailedHook(ctx, inc, t, msg.Title, msg.Summary)
	}
	return false, false, reason
}

// NotifyUnrouted 未路由兜底通知（C3）：把一条不关联 Incident 的告警送达给指定收件人。
//
// 与 NotifyEscalation 的区别：无 Incident 上下文（未路由 Event 尚未建单），故不走
// IM 卡片通道；只走 email/phone/sms/webhook。不聚合、不静默（兜底通知「必达」语义）。
// 但仍走降级链：逐通道尝试，首个成功即停。
//
// ★ 刻意走同步直投（不走 Asynq 化路径）：本方法承载自监控告警与「全通道失败」兜底告警
// ——被监控/被兜底的可能正是队列本身（Redis 故障、worker 停摆），兜底通知若也依赖队列，
// 等于「链路坏了 → 告警也走坏链路 → 告警也丢」。独立通道语义见 wire.go selfMonAlertNotifier。
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
		n.deliverChainSync(ctx, nil, msg, t, chans, 0, "")
	}
	return nil
}

// NotifyTargeted 定向订阅通知（T4.4）：把某 Incident 的生命周期变更告知订阅了它的干系人。
//
// 与 NotifyEscalation 的区别：
//   - 收件人来源不是升级链 target（处置责任人），而是订阅关系解算出的干系人（只读订阅者）。
//   - 订阅者一律视为「非值班人」（isOncall=false）——他们不是排班解算出的值班人，
//     故 quiet_hours 对非 critical 夜间通知生效（抑制打扰，与 E.6 语义一致）。
//   - 有 Incident 上下文（走 IM 卡片可渲染），走完整降级链 + 送达记录。
//
// 复用 T2.2 全套分发：规则评估（channels/template/quiet_hours）、降级链、送达三态落库。
// channels 为订阅者的通道偏好（空则回落规则/默认链）。不聚合（订阅告知即时性优先，条数有限）。
func (n *Notifier) NotifyTargeted(ctx context.Context, inc *ent.Incident, targets []Target, channels []string) error {
	if inc == nil || len(targets) == 0 {
		return nil
	}
	// 规则评估（B7/C12）：与升级通知同一套规则解析，取 channels/template/quiet_hours。
	var rule *MatchedRule
	if n.ruleResolver != nil {
		rule = n.ruleResolver.Resolve(ctx, inc)
	}
	// 通道优先级：订阅者显式偏好 > 规则 channels > 默认链。
	chans := n.resolveChannels(channels, rule)

	msg := &Message{
		Incident: inc,
		Targets:  targets,
		Level:    0, // 订阅告知无升级层级概念
		Title:    FormatTitle(inc),
		Summary:  FormatSummary(inc, 0),
		Channels: chans,
	}
	// 模板渲染（M7.5）：与升级通知同款，渲染失败内部降级不丢通知。
	if n.templates != nil {
		tmplName := n.resolveTemplateName(inc, rule)
		chanForDefault := firstChan(chans)
		rendered, rerr := n.templates.Render(ctx, tmplName, chanForDefault, TemplateData{
			Incident: inc, Targets: targets, Level: 0,
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

	for _, t := range targets {
		// 订阅者一律非值班人：quiet_hours 命中则记 suppressed，不发也不丢（B22，可查/可补发）。
		if qh != nil && qh.ShouldSuppress(severity, false, nil) {
			n.recordDelivery(ctx, DeliveryRecord{
				IncidentID: incID(inc), UserID: t.UserID, Channel: firstChan(chans),
				Target: targetKey(t), Status: StatusSuppressed,
				Reason: "quiet_hours", Level: 0, Severity: severity,
			})
			continue
		}
		// 立即走降级链（订阅告知不聚合）。
		n.deliverChain(ctx, inc, msg, t, chans, 0, severity)
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

// sendOne 发送到单个通道，记录 metrics +（record=true 时）送达记录。
// 返回：是否有任一成功送达、成功的那条结果、该通道是否真正尝试过（返回过结果）、最后错误串。
//
// 一个通道 Send 可能返回多条结果（如 webhook 多 URL、email 多收件人）：
// 只要有一条成功即视为该通道对该 target 送达成功（链停止）。
func (n *Notifier) sendOne(ctx context.Context, ch Channel, msg *Message, inc *ent.Incident, t Target, level int, severity string, record bool) (bool, SendResult, bool, string) {
	results, err := ch.Send(ctx, msg)
	if err != nil {
		results = append(results, SendResult{Channel: ch.Name(), Error: err.Error()})
	}
	anySuccess := false
	var via SendResult
	lastErr := ""
	for _, r := range results {
		resultLabel := "success"
		if r.Success {
			if !anySuccess {
				via = r
			}
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
		// 异步路径（record=false）不逐次追加——结果由 tracking pending 行统一承载。
		if record {
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
	}
	// 通道 Send 返回空结果（如未配置降级）：视为该通道不可用，链继续（不算成功不算硬失败）。
	if len(results) == 0 {
		return false, SendResult{}, false, ""
	}
	return anySuccess, via, true, lastErr
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
