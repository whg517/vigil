// trigger_match.go trigger 求值的纯函数（无 IO，便于单测）。
//
// trigger 存为 map[string]any（schema.Runbook.trigger 是 JSON），形如：
//
//	{"type": "on_severity", "condition": "severity >= warning"}
//	{"type": "on_label_match", "labels": {"service": "payment", "env": "prod"}}
//	{"type": "on_incident"}
//	{"type": "manual"}
//
// 求值只认结构化字段（type + labels + condition 的严重度阈值），不做通用表达式求值——
// 保持可预期、可测、无注入面。
package runbook

import "strings"

// 触发类型常量（与 capabilities/06-runbook.md §3 一致）。
const (
	triggerManual       = "manual"
	triggerOnIncident   = "on_incident"
	triggerOnSeverity   = "on_severity"
	triggerOnLabelMatch = "on_label_match"
)

// severityRank 严重度有序化（用于 on_severity 的「≥ 阈值」比较）。数字越大越严重。
// 与 ent Incident.severity 枚举值集合一致（critical/warning/info）。
var severityRank = map[string]int{
	"info":     1,
	"warning":  2,
	"critical": 3,
}

// triggerType 取 trigger 的类型字符串（缺省/非法视为 manual——不自动触发）。
func triggerType(trigger map[string]any) string {
	if trigger == nil {
		return triggerManual
	}
	if t, ok := trigger["type"].(string); ok {
		return t
	}
	return triggerManual
}

// matchTrigger 求值 trigger 是否命中当前 Incident（severity + 关联 Event labels）。
//
// 语义：
//   - manual / 未知类型 / nil：不命中（响应者手动执行，自动求值不触发）。
//   - on_incident：无条件命中（建单即展示）。
//   - on_severity：incidentSeverity ≥ trigger.condition 里的阈值严重度。
//     condition 形如 "severity >= warning"，宽松解析——取其中出现的已知严重度词为阈值；
//     无法解析阈值时保守命中（宁可多展示一个 Runbook，也不漏掉——展示无副作用）。
//   - on_label_match：trigger.labels 的每个键值都能在 Incident labels 中找到（子集匹配）。
//     trigger.labels 为空则视为不限（命中）。
func matchTrigger(trigger map[string]any, incidentSeverity string, labels map[string]string) bool {
	switch triggerType(trigger) {
	case triggerOnIncident:
		return true
	case triggerOnSeverity:
		return matchSeverity(trigger, incidentSeverity)
	case triggerOnLabelMatch:
		return matchLabels(trigger, labels)
	default: // manual / 未知：不自动触发
		return false
	}
}

// matchSeverity 判断 incidentSeverity 是否达到 trigger 的阈值（≥）。
func matchSeverity(trigger map[string]any, incidentSeverity string) bool {
	threshold := parseSeverityThreshold(trigger)
	if threshold == "" {
		// 无法解析阈值 → 保守命中（展示无副作用，漏展示才有风险）。
		return true
	}
	incRank, ok := severityRank[incidentSeverity]
	if !ok {
		return false
	}
	return incRank >= severityRank[threshold]
}

// parseSeverityThreshold 从 trigger 提取阈值严重度。
// 优先 trigger.severity（结构化）；否则从 trigger.condition 文本里找已知严重度词。
func parseSeverityThreshold(trigger map[string]any) string {
	if s, ok := trigger["severity"].(string); ok {
		if _, known := severityRank[s]; known {
			return s
		}
	}
	cond, _ := trigger["condition"].(string)
	cond = strings.ToLower(cond)
	// 取 condition 里出现的已知严重度词（如 "severity >= warning" → warning）。
	for sev := range severityRank {
		if strings.Contains(cond, sev) {
			return sev
		}
	}
	return ""
}

// matchLabels 判断 trigger.labels 是否为 incidentLabels 的子集（全部键值命中）。
// trigger.labels 缺省/空 → 不限，命中。
func matchLabels(trigger map[string]any, incidentLabels map[string]string) bool {
	want := extractLabels(trigger)
	if len(want) == 0 {
		return true // 未指定 label 条件 → 不限
	}
	for k, v := range want {
		if incidentLabels[k] != v {
			return false
		}
	}
	return true
}

// extractLabels 从 trigger.labels 提取期望标签（JSON 解码后可能是 map[string]any 或 map[string]string）。
func extractLabels(trigger map[string]any) map[string]string {
	raw, ok := trigger["labels"]
	if !ok {
		return nil
	}
	switch m := raw.(type) {
	case map[string]string:
		return m
	case map[string]any:
		out := make(map[string]string, len(m))
		for k, v := range m {
			if s, ok := v.(string); ok {
				out[k] = s
			}
		}
		return out
	default:
		return nil
	}
}
