// notifier.go 通知分发器：实现 escalation.Notifier，把升级触发分发到各通道。
package notification

import (
	"context"
	"fmt"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/internal/escalation"
	"github.com/kevin/vigil/internal/metrics"
)

// Notifier 通知分发器，实现 escalation.Notifier 接口。
// 把升级 targets 组装成 Message，按 msg.Channels 分发到注册的通道。
// 注：notification 单向依赖 escalation（escalation 不反向依赖），无循环。
type Notifier struct {
	registry     *Registry
	defaultChans []string                      // 默认启用通道（当 msg.Channels 为空时）
	recordResult func(incID int, r SendResult) // 送达记录回调（持久化），可选
	// quietHours 静默时段评估（能力域 7 M7.8）。nil 时不静默（降级）。
	// 由 SetQuietHoursResolver 注入：按 incident/team 解析适用的静默配置。
	quietHoursResolver func(inc *ent.Incident) *QuietHours
	// aggregator 通知聚合（能力域 7 M7.9）。nil 时不聚合（立即发送，降级）。
	aggregator *Aggregator
	// templates 通知模板引擎（能力域 7 M7.5）。nil 时用 FormatTitle/Summary 兜底。
	templates *TemplateEngine
	// templateNameResolver 按 incident 解析适用模板名（从 NotificationRule.template_id）。
	// 返回空串时 notifier 按 channel 用默认模板。
	templateNameResolver func(inc *ent.Incident) string
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

// SetResultRecorder 设置送达记录回调（由 main 注入，写 Notification 记录）。
func (n *Notifier) SetResultRecorder(fn func(incID int, r SendResult)) {
	n.recordResult = fn
}

// SetQuietHoursResolver 注入静默时段解析器。
// resolver 按 incident 返回适用的静默配置（可从 NotificationRule.quiet_hours 解析）；nil 表示该事件不静默。
func (n *Notifier) SetQuietHoursResolver(resolver func(inc *ent.Incident) *QuietHours) {
	n.quietHoursResolver = resolver
}

// SetAggregator 注入通知聚合器。nil 表示不聚合（立即发送）。
func (n *Notifier) SetAggregator(a *Aggregator) {
	n.aggregator = a
}

// SetTemplateEngine 注入通知模板引擎（能力域 7 M7.5）。
// resolver 按 incident 返回适用模板名（从 NotificationRule.template_id 解析）。
func (n *Notifier) SetTemplateEngine(e *TemplateEngine, resolver func(inc *ent.Incident) string) {
	n.templates = e
	n.templateNameResolver = resolver
}

// NotifyEscalation 实现 escalation.Notifier。
// targets 来自升级引擎（已解析的人/team）；level 为升级层级。
// channels 为本层 EscalationLevel.notify_channels（B6）：非空时按其选通道分发，
// 为空时降级到全局默认通道（保持无配置时的行为不回归）。
func (n *Notifier) NotifyEscalation(ctx context.Context, inc *ent.Incident, level int, targets []escalation.NotifyTarget, channels []string) error {
	msgTargets := make([]Target, 0, len(targets))
	for _, t := range targets {
		msgTargets = append(msgTargets, Target{UserID: t.UserID, Name: t.Name, Source: t.Source})
	}

	// B6：本层配置了 notify_channels 就用它，否则回退默认通道。
	chans := channels
	if len(chans) == 0 {
		chans = n.defaultChans
	}
	msg := &Message{
		Incident: inc,
		Targets:  msgTargets,
		Level:    level,
		Title:    FormatTitle(inc),
		Summary:  FormatSummary(inc, level),
		Channels: chans,
	}
	// 模板渲染（能力域 7 M7.5）：按 incident 解析模板名，渲染标题/正文覆盖兜底文案。
	// 渲染失败由 TemplateEngine 内部降级，不丢通知。
	if n.templates != nil {
		tmplName := ""
		if n.templateNameResolver != nil {
			tmplName = n.templateNameResolver(inc)
		}
		// 取本次分发的首个启用通道决定默认模板（im/email/webhook）
		chanForDefault := ""
		if len(chans) > 0 {
			chanForDefault = chans[0]
		}
		rendered, rerr := n.templates.Render(ctx, tmplName, chanForDefault, TemplateData{
			Incident: inc,
			Targets:  msgTargets,
			Level:    level,
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
	// 解析该事件的静默配置（能力域 7 M7.8）
	var qh *QuietHours
	if n.quietHoursResolver != nil {
		qh = n.quietHoursResolver(inc)
	}

	var firstErr error
	for _, chanName := range msg.Channels {
		ch, ok := n.registry.Get(chanName)
		if !ok {
			continue // 通道未注册（如 IM 未接入），跳过
		}

		// 通知聚合（能力域 7 M7.9）：非 critical 入 per-target 队列，窗口结束合并发送。
		// critical 不聚合，立即发送。
		if n.aggregator != nil && severity != "critical" {
			suppressed := false
			for _, t := range msgTargets {
				item := AggregatedItem{
					IncidentID: inc.ID, Title: msg.Title, Summary: msg.Summary,
					Level: level, Severity: severity,
				}
				// 静默判定（值班人始终通知）：source=schedule 视为值班人，不静默
				isOncall := t.Source == "schedule"
				if qh != nil && qh.ShouldSuppress(severity, isOncall, nil) {
					suppressed = true // 静默窗口内，不立即发（也不入聚合队列，等窗口外重试）
					continue
				}
				dec, aerr := n.aggregator.Add(ctx, targetKey(t), severity, item)
				if aerr != nil && firstErr == nil {
					firstErr = fmt.Errorf("aggregate: %w", aerr)
				}
				if dec != nil && dec.SendNow {
					// critical 或降级场景：立即发
					n.sendOne(ctx, ch, msg, inc)
				}
				// 否则已入聚合队列，等 Flush
			}
			if suppressed {
				continue // 该通道本轮被静默，跳过立即发送
			}
			continue // 聚合模式：不在此处立即发送整批
		}

		// 非聚合模式（critical 或无聚合器）：直接分发
		n.sendOne(ctx, ch, msg, inc)
	}
	return firstErr
}

// NotifyUnrouted 未路由兜底通知（C3）：把一条不关联 Incident 的告警送达给指定收件人。
//
// 与 NotifyEscalation 的区别：无 Incident 上下文（未路由 Event 尚未建单），故不走
// IM 卡片通道（IMChannel.Send 强依赖 Incident）；只走 email/phone/sms/webhook 这些
// 按 target 号码/URL 送达、可容忍 nil Incident 的通道。用于 critical 告警无 Service 匹配时
// 兜底通知 org_admin，避免高危故障静默。
//
// title/summary 由调用方按 Event 组装（含事件摘要/标签），channels 为空时用默认通道。
// 不聚合、不静默（兜底通知是"必达"语义，不该被静默/聚合窗延迟）。
func (n *Notifier) NotifyUnrouted(ctx context.Context, targets []Target, title, summary string, channels []string) error {
	chans := channels
	if len(chans) == 0 {
		chans = n.defaultChans
	}
	msg := &Message{
		Incident: nil, // 未路由：无关联 Incident
		Targets:  targets,
		Level:    0,
		Title:    title,
		Summary:  summary,
		Channels: chans,
	}
	for _, chanName := range chans {
		// IM 通道强依赖 Incident（渲染卡片），未路由无单可渲染，跳过。
		if chanName == "im" {
			continue
		}
		ch, ok := n.registry.Get(chanName)
		if !ok {
			continue
		}
		results, err := ch.Send(ctx, msg)
		if err != nil {
			results = append(results, SendResult{Channel: ch.Name(), Error: err.Error()})
		}
		for _, r := range results {
			resultLabel := "success"
			if !r.Success {
				resultLabel = "failed"
			}
			metrics.NotificationsSent.WithLabelValues(r.Channel, resultLabel).Inc()
			if n.recordResult != nil {
				n.recordResult(0, r) // incID=0：未路由无单
			}
		}
	}
	return nil
}

// targetKey 聚合队列按 target 维度，user 用 user_id，team/source=team 用 source 名。
func targetKey(t Target) string {
	if t.UserID > 0 {
		return fmt.Sprintf("user:%d", t.UserID)
	}
	return "team:" + t.Name
}

// sendOne 发送到单个通道并记录送达结果 + 埋点（抽出复用）。
func (n *Notifier) sendOne(ctx context.Context, ch Channel, msg *Message, inc *ent.Incident) {
	results, err := ch.Send(ctx, msg)
	if err != nil {
		// 记录到结果但不阻塞其它通道；err 透传由调用方记录
		results = append(results, SendResult{Channel: ch.Name(), Error: err.Error()})
	}
	for _, r := range results {
		resultLabel := "success"
		if !r.Success {
			resultLabel = "failed"
		}
		metrics.NotificationsSent.WithLabelValues(r.Channel, resultLabel).Inc()
		if n.recordResult != nil {
			n.recordResult(inc.ID, r)
		}
	}
}

// FlushAggregated 刷新某 target 的聚合队列，窗口到则合并发送。
// 由定时任务（main 注册 asynq periodic）按 target 维度驱动。
// 返回实际合并发送的条目数。
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
	// 合并发送：取第一条的 incident 上下文重建 Message（简化：按 incident 查询重建较重，
	// 本期用聚合项里的 Title/Summary 拼一条汇总 Message）。
	// 注：完整实现应按 incident 逐条查 DB 重建 msg；此处用首条 + 汇总计数。
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
	sent := 0
	for _, chanName := range msg.Channels {
		ch, ok := n.registry.Get(chanName)
		if !ok {
			continue
		}
		results, err := ch.Send(ctx, msg)
		if err == nil {
			sent++
		}
		for _, r := range results {
			resultLabel := "success"
			if !r.Success {
				resultLabel = "failed"
			}
			metrics.NotificationsSent.WithLabelValues(r.Channel, resultLabel).Inc()
			if n.recordResult != nil {
				n.recordResult(first.IncidentID, r)
			}
		}
	}
	return sent, nil
}

// FlushAll 扫描所有有积压待发通知的 target，逐个 FlushAggregated 合并发送。
// 返回本次实际 flush 的 target 数 + 首个错误（不因单个 target 失败中断其余）。
//
// QA 审计 C3：原实现 FlushAggregated 从未被任何调度器调用 → 聚合通知成死信。
// 此方法供 wire.go 的周期 ticker 驱动（间隔 ≤ 聚合窗口）。
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
