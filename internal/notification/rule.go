// rule.go 通知规则评估（B7 / C12）。
//
// 让 NotificationRule.condition（severity/team/service 匹配）与 channels 真正参与
// 评估分发——原实现只取「首条 enabled 规则」的 quiet_hours/template_id，condition/channels
// 完全不参与，导致所有事件走同一套通道、同一静默配置，规则形同虚设。
//
// 评估流程（按 incident 解析适用规则）：
//  1. 遍历所有 enabled 规则，逐条用 condition 匹配 incident（severity/team/service）；
//  2. 命中多条时按「条件更具体者优先」打分排序（匹配的条件维度越多分越高），取最高分；
//  3. 无命中：返回 nil，notifier 退回全局默认通道/无静默（向后兼容，无配置也能发）。
//
// 返回的 MatchedRule 一次性给出该事件应走的 channels（降级链顺序）、模板名、静默配置，
// 三者出自同一条规则，消除「静默取一条、模板取另一条、通道取默认」的割裂。
package notification

import (
	"context"
	"strconv"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/notificationrule"
)

// MatchedRule 某 incident 命中的通知规则解析结果。
type MatchedRule struct {
	RuleID       int         // 命中的规则 ID（0=无命中，用默认）
	RuleName     string      // 规则名（便于日志/审计）
	Channels     []string    // 启用通道（即降级链顺序，B7/C12）
	TemplateName string      // 通知模板名（对应 NotificationRule.template_id）
	QuietHours   *QuietHours // 静默配置（解析自 rule.quiet_hours）
}

// RuleResolver 按 incident 解析适用的通知规则。
// 依赖 ent.Client 查 NotificationRule 及其关联 team/service，做 condition 匹配。
type RuleResolver struct {
	db *ent.Client
}

// NewRuleResolver 创建规则解析器。db 为 nil 时 Resolve 恒返回 nil（降级到默认）。
func NewRuleResolver(db *ent.Client) *RuleResolver {
	return &RuleResolver{db: db}
}

// Resolve 按 incident 解析适用规则；无命中返回 nil（调用方退回默认通道/无静默）。
//
// condition 支持的键（与 capabilities/04 §5 对齐）：
//   - severity: 字符串，等值匹配 incident.severity
//   - team_id / team: 数字/字符串，等值匹配 incident 归属 team
//   - service_id / service: 数字/字符串，等值匹配 incident 归属 service
//
// 匹配语义：condition 中出现的键全部满足才算命中（AND）；condition 为空 = 匹配所有事件（兜底规则）。
func (r *RuleResolver) Resolve(ctx context.Context, inc *ent.Incident) *MatchedRule {
	if r.db == nil || inc == nil {
		return nil
	}
	rules, err := r.db.NotificationRule.Query().
		Where(notificationrule.EnabledEQ(true)).
		All(ctx)
	if err != nil || len(rules) == 0 {
		return nil
	}
	// 解析该 incident 的 team/service（用于 condition 匹配），查失败按无归属处理。
	teamID, serviceID := r.resolveIncidentScope(ctx, inc.ID)

	var best *ent.NotificationRule
	bestScore := -1
	for _, rule := range rules {
		score, ok := matchCondition(rule.Condition, string(inc.Severity), teamID, serviceID)
		if !ok {
			continue
		}
		// 分数相同时保留先命中者（稳定：规则创建顺序即 ID 升序）；更具体者（分高）覆盖。
		if score > bestScore {
			bestScore = score
			best = rule
		}
	}
	if best == nil {
		return nil
	}
	m := &MatchedRule{
		RuleID:       best.ID,
		RuleName:     best.Name,
		Channels:     append([]string(nil), best.Channels...),
		TemplateName: best.TemplateID,
		QuietHours:   ParseQuietHoursPublic(best.QuietHours),
	}
	return m
}

// resolveIncidentScope 查 incident 归属的 team_id / service_id（0=无归属）。
func (r *RuleResolver) resolveIncidentScope(ctx context.Context, incID int) (teamID, serviceID int) {
	inc, err := r.db.Incident.Query().
		Where(incident.IDEQ(incID)).
		WithTeam().
		WithService().
		Only(ctx)
	if err != nil {
		return 0, 0
	}
	if inc.Edges.Team != nil {
		teamID = inc.Edges.Team.ID
	}
	if inc.Edges.Service != nil {
		serviceID = inc.Edges.Service.ID
	}
	return teamID, serviceID
}

// matchCondition 判断 condition 是否匹配给定的 severity/team/service，返回匹配分与是否命中。
// 分数 = 满足的条件维度数（越具体分越高，用于多规则命中时优选）。
// condition 为空视为「匹配所有」，分数 0（最兜底）。
func matchCondition(cond map[string]any, severity string, teamID, serviceID int) (score int, ok bool) {
	if len(cond) == 0 {
		return 0, true // 无条件 = 兜底规则，匹配所有
	}
	if v, present := cond["severity"]; present {
		if condStr(v) != severity {
			return 0, false
		}
		score++
	}
	if id, present := condIntKey(cond, "team_id", "team"); present {
		if id != teamID {
			return 0, false
		}
		score++
	}
	if id, present := condIntKey(cond, "service_id", "service"); present {
		if id != serviceID {
			return 0, false
		}
		score++
	}
	return score, true
}

// condStr 把 condition 值转字符串（JSON 反序列化后可能是 string）。
func condStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// condIntKey 从 condition 按候选键取整数值（team_id/team、service_id/service）。
// JSON number 反序列化为 float64；也容忍字符串数字。present=false 表示该维度未在 condition 出现。
func condIntKey(cond map[string]any, keys ...string) (id int, present bool) {
	for _, k := range keys {
		v, ok := cond[k]
		if !ok {
			continue
		}
		switch t := v.(type) {
		case float64:
			return int(t), true
		case int:
			return t, true
		case string:
			n, err := strconv.Atoi(t)
			if err == nil {
				return n, true
			}
			// 字符串非数字：视为出现但不匹配任何 id（返回 0，present=true）
			return 0, true
		}
	}
	return 0, false
}
