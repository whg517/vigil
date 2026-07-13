// adapters_builtin.go 内置告警源适配器实现。
package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kevin/vigil/ent"
)

// PrometheusAdapter 适配 Prometheus/Alertmanager webhook。
//
// Alertmanager payload 结构（关键字段）：
//
//	{
//	  "alerts": [
//	    { "status":"firing", "labels":{...}, "annotations":{...},
//	      "startsAt":..., "fingerprint":"abc123", ... }
//	  ]
//	}
//
// 一次 webhook 可能含多条 alert，每条归一化为独立 Event（修复早期"只取首条"丢告警 bug）。
type PrometheusAdapter struct{}

func (PrometheusAdapter) Type() string { return "prometheus" }

func (PrometheusAdapter) Normalize(ctx context.Context, raw []byte, integ *ent.Integration, rawEvent *ent.RawEvent) ([]*NormalizedEvent, error) {
	var am struct {
		Alerts []struct {
			Status      string            `json:"status"`
			Labels      map[string]string `json:"labels"`
			Annotations map[string]string `json:"annotations"`
			StartsAt    string            `json:"startsAt"`
			Fingerprint string            `json:"fingerprint"`
		} `json:"alerts"`
	}
	if err := json.Unmarshal(raw, &am); err != nil {
		return nil, fmt.Errorf("parse prometheus payload: %w", err)
	}
	if len(am.Alerts) == 0 {
		return nil, fmt.Errorf("prometheus payload has no alerts")
	}

	events := make([]*NormalizedEvent, 0, len(am.Alerts))
	for _, a := range am.Alerts {
		// 严重度归一：Alertmanager 无 severity 字段，靠 label.severity 映射（severity_map 可覆盖）
		severity := mapSeverity(integ, a.Labels["severity"], mapPromSeverity)
		summary := a.Annotations["summary"]
		if summary == "" {
			summary = fmt.Sprintf("[%s] %s", a.Labels["alertname"], a.Labels["instance"])
		}
		// sourceEventId 用 fingerprint（Alertmanager 提供的去重指纹）
		srcID := a.Fingerprint
		if srcID == "" {
			srcID = a.Labels["alertname"] + ":" + a.Labels["instance"]
		}
		events = append(events, &NormalizedEvent{
			SourceEventID: srcID,
			Source:        "prometheus",
			Severity:      severity,
			Status:        a.Status, // firing | resolved
			Summary:       summary,
			Detail:        map[string]any{"raw": json.RawMessage(raw)}, // 保留原文
			Labels:        a.Labels,
			DedupKey:      dedupKey("prometheus", srcID),
		})
	}
	return events, nil
}

// GrafanaAdapter 适配 Grafana alerting webhook（能力域 1 M1.2）。
//
// Grafana unified alerting payload 结构（关键字段）：
//
//	{
//	  "alerts": [
//	    { "status":"firing", "labels":{...}, "annotations":{...},
//	      "startsAt":..., "fingerprint":"...", "severity":"critical", ... }
//	  ]
//	}
//
// 与 Prometheus 结构相似（Grafana 借鉴了 Alertmanager），但：
//   - Grafana 有原生 severity 字段（critical/warning/info），优先用之
//   - fingerprint 字段同样可用作去重
type GrafanaAdapter struct{}

func (GrafanaAdapter) Type() string { return "grafana" }

func (GrafanaAdapter) Normalize(ctx context.Context, raw []byte, integ *ent.Integration, rawEvent *ent.RawEvent) ([]*NormalizedEvent, error) {
	var ga struct {
		Alerts []struct {
			Status      string            `json:"status"`
			Labels      map[string]string `json:"labels"`
			Annotations map[string]string `json:"annotations"`
			StartsAt    string            `json:"startsAt"`
			Fingerprint string            `json:"fingerprint"`
			Severity    string            `json:"severity"` // Grafana 原生 severity
		} `json:"alerts"`
	}
	if err := json.Unmarshal(raw, &ga); err != nil {
		return nil, fmt.Errorf("parse grafana payload: %w", err)
	}
	if len(ga.Alerts) == 0 {
		return nil, fmt.Errorf("grafana payload has no alerts")
	}

	events := make([]*NormalizedEvent, 0, len(ga.Alerts))
	for _, a := range ga.Alerts {
		// 严重度归一：优先用 Grafana 原生 severity，回退 label.severity（severity_map 可覆盖）
		rawSev := a.Severity
		if rawSev == "" {
			rawSev = a.Labels["severity"]
		}
		severity := mapSeverity(integ, rawSev, func(string) string {
			s := normalizeSeverity(a.Severity)
			if s == "info" {
				s = mapPromSeverity(a.Labels["severity"])
			}
			return s
		})
		summary := a.Annotations["summary"]
		if summary == "" {
			alertname := a.Labels["alertname"]
			if alertname == "" {
				alertname = a.Labels["__alert_rule_uuid__"] // Grafana 内部规则 ID
			}
			summary = fmt.Sprintf("[%s] %s", alertname, a.Labels["instance"])
		}
		srcID := a.Fingerprint
		if srcID == "" {
			srcID = a.Labels["alertname"] + ":" + a.Labels["instance"]
		}
		events = append(events, &NormalizedEvent{
			SourceEventID: srcID,
			Source:        "grafana",
			Severity:      severity,
			Status:        a.Status,
			Summary:       summary,
			Detail:        map[string]any{"raw": json.RawMessage(raw)},
			Labels:        a.Labels,
			DedupKey:      dedupKey("grafana", srcID),
		})
	}
	return events, nil
}

// severityFromConfig 查 Integration.config 的 severity_map 覆盖表（原始值 → critical/warning/info，
// 键不区分大小写）。命中且目标值合法返回映射结果；未配置/未命中/目标值非法返回 ""，
// 调用方回落默认映射——错误配置绝不导致告警被吞或映射到未知级别。
func severityFromConfig(integ *ent.Integration, raw string) string {
	if integ == nil || integ.Config == nil {
		return ""
	}
	sm, ok := integ.Config["severity_map"].(map[string]any)
	if !ok {
		return ""
	}
	v, ok := sm[strings.ToLower(raw)]
	if !ok {
		return ""
	}
	switch s := strings.ToLower(fmt.Sprintf("%v", v)); s {
	case "critical", "warning", "info":
		return s
	}
	return ""
}

// mapSeverity 严重度归一统一入口：接入点 severity_map 配置覆盖优先，未命中回落 fallback 默认映射。
func mapSeverity(integ *ent.Integration, raw string, fallback func(string) string) string {
	if s := severityFromConfig(integ, raw); s != "" {
		return s
	}
	return fallback(raw)
}

// mapPromSeverity 把 Prometheus label.severity 归一到 critical/warning/info（默认映射）。
func mapPromSeverity(s string) string {
	switch strings.ToLower(s) {
	case "critical", "error", "page":
		return "critical"
	case "warning", "warn":
		return "warning"
	default:
		return "info"
	}
}

// GenericJSONAdapter 通用 JSON 适配器。
// 用于无专用适配器的告警源，或用户自定义字段映射的接入。
// 期望 payload 含约定字段（可缺省，缺省则用默认值）。
type GenericJSONAdapter struct{}

func (GenericJSONAdapter) Type() string { return "webhook" } // 对应 Integration.Type=webhook

func (GenericJSONAdapter) Normalize(ctx context.Context, raw []byte, integ *ent.Integration, rawEvent *ent.RawEvent) ([]*NormalizedEvent, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse generic json: %w", err)
	}

	// 约定字段名（兼容常见告警源）：
	//   source_event_id / id, severity, status, summary, labels
	str := func(k string) string {
		if v, ok := m[k].(string); ok {
			return v
		}
		return ""
	}
	srcID := firstNonEmpty(str("source_event_id"), str("id"), str("event_id"))
	if srcID == "" {
		// 无 id 时用整个 payload 摘要作 srcID（保证有去重键）
		srcID = fmt.Sprintf("generic-%d", len(raw))
	}
	severity := mapSeverity(integ, str("severity"), normalizeSeverity)
	status := strings.ToLower(str("status"))
	if status == "" {
		status = "firing" // 缺省视为 firing
	}
	summary := str("summary")
	if summary == "" {
		summary = str("message")
	}
	if summary == "" {
		summary = "告警（通用接入）"
	}

	// labels：从 payload 的 labels 子对象提取
	labels := map[string]string{}
	if l, ok := m["labels"].(map[string]any); ok {
		for k, v := range l {
			labels[k] = fmt.Sprintf("%v", v)
		}
	}

	return []*NormalizedEvent{{
		SourceEventID: srcID,
		Source:        "generic",
		Severity:      severity,
		Status:        status,
		Summary:       summary,
		Detail:        m,
		Labels:        labels,
		DedupKey:      dedupKey("generic", srcID),
	}}, nil
}

// normalizeSeverity 把任意严重度字符串归一到三级。
func normalizeSeverity(s string) string {
	switch strings.ToLower(s) {
	case "critical", "error", "high", "p1", "sev1", "urgent":
		return "critical"
	case "warning", "warn", "medium", "p2", "sev2":
		return "warning"
	default:
		return "info"
	}
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
