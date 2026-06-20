package ingestion

import (
	"context"
	"testing"
)

// TestPrometheusAdapter 验证 Prometheus/Alertmanager 适配器归一化。
func TestPrometheusAdapter(t *testing.T) {
	a := PrometheusAdapter{}
	if a.Type() != "prometheus" {
		t.Fatalf("Type: got %q, want prometheus", a.Type())
	}

	payload := []byte(`{
		"alerts": [{
			"status": "firing",
			"labels": {
				"alertname": "HighErrorRate",
				"severity": "critical",
				"service": "payment",
				"env": "prod"
			},
			"annotations": {"summary": "支付服务 5xx 错误率 > 5%"},
			"startsAt": "2026-06-20T14:02:00Z",
			"fingerprint": "abc123def"
		}]
	}`)

	evt, err := a.Normalize(context.Background(), payload, nil, nil)
	if err != nil {
		t.Fatalf("Normalize failed: %v", err)
	}

	cases := []struct{ name, got, want string }{
		{"source", evt.Source, "prometheus"},
		{"source_event_id", evt.SourceEventID, "abc123def"},
		{"severity", evt.Severity, "critical"},
		{"status", evt.Status, "firing"},
		{"summary", evt.Summary, "支付服务 5xx 错误率 > 5%"},
		{"label.service", evt.Labels["service"], "payment"},
		{"label.env", evt.Labels["env"], "prod"},
		{"dedup_key", evt.DedupKey, "prometheus:abc123def"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
	}
}

// TestPrometheusAdapter_SeverityMapping 验证严重度归一映射。
func TestPrometheusAdapter_SeverityMapping(t *testing.T) {
	cases := map[string]string{
		"critical": "critical",
		"error":    "critical",
		"page":     "critical",
		"warning":  "warning",
		"warn":     "warning",
		"info":     "info",
		"":         "info",
		"unknown":  "info",
	}
	for in, want := range cases {
		if got := mapPromSeverity(in); got != want {
			t.Errorf("mapPromSeverity(%q): got %q, want %q", in, got, want)
		}
	}
}

// TestPrometheusAdapter_NoAlerts 验证空 alerts 报错。
func TestPrometheusAdapter_NoAlerts(t *testing.T) {
	a := PrometheusAdapter{}
	_, err := a.Normalize(context.Background(), []byte(`{"alerts":[]}`), nil, nil)
	if err == nil {
		t.Error("expected error for empty alerts, got nil")
	}
}

// TestPrometheusAdapter_InvalidJSON 验证非法 JSON 报错。
func TestPrometheusAdapter_InvalidJSON(t *testing.T) {
	a := PrometheusAdapter{}
	_, err := a.Normalize(context.Background(), []byte(`{not json`), nil, nil)
	if err == nil {
		t.Error("expected error for invalid json, got nil")
	}
}

// TestGenericJSONAdapter 验证通用 JSON 适配器。
func TestGenericJSONAdapter(t *testing.T) {
	a := GenericJSONAdapter{}
	if a.Type() != "webhook" {
		t.Fatalf("Type: got %q, want webhook", a.Type())
	}

	payload := []byte(`{
		"source_event_id": "evt-001",
		"severity": "high",
		"status": "firing",
		"summary": "自定义告警",
		"labels": {"team": "sre", "tier": "1"}
	}`)

	evt, err := a.Normalize(context.Background(), payload, nil, nil)
	if err != nil {
		t.Fatalf("Normalize failed: %v", err)
	}

	if evt.SourceEventID != "evt-001" {
		t.Errorf("source_event_id: got %q", evt.SourceEventID)
	}
	if evt.Severity != "critical" { // high → critical
		t.Errorf("severity: got %q, want critical", evt.Severity)
	}
	if evt.Summary != "自定义告警" {
		t.Errorf("summary: got %q", evt.Summary)
	}
	if evt.Labels["team"] != "sre" {
		t.Errorf("label.team: got %q", evt.Labels["team"])
	}
	if evt.DedupKey != "generic:evt-001" {
		t.Errorf("dedup_key: got %q", evt.DedupKey)
	}
}

// TestGenericJSONAdapter_Defaults 验证缺省字段填默认值。
func TestGenericJSONAdapter_Defaults(t *testing.T) {
	a := GenericJSONAdapter{}
	// 无 severity/status/summary，应填默认
	evt, err := a.Normalize(context.Background(), []byte(`{"id":"x1"}`), nil, nil)
	if err != nil {
		t.Fatalf("Normalize failed: %v", err)
	}
	if evt.Severity != "info" {
		t.Errorf("default severity: got %q, want info", evt.Severity)
	}
	if evt.Status != "firing" {
		t.Errorf("default status: got %q, want firing", evt.Status)
	}
	if evt.Summary != "告警（通用接入）" {
		t.Errorf("default summary: got %q", evt.Summary)
	}
}

// TestAdapterRegistry 验证注册表查找。
func TestAdapterRegistry(t *testing.T) {
	r := NewAdapterRegistry()

	if _, ok := r.Get("prometheus"); !ok {
		t.Error("prometheus adapter not registered")
	}
	if _, ok := r.Get("webhook"); !ok {
		t.Error("webhook (generic) adapter not registered")
	}
	if _, ok := r.Get("nonexistent"); ok {
		t.Error("nonexistent adapter should not exist")
	}
}

// TestNormalizeSeverity 验证通用严重度归一。
func TestNormalizeSeverity(t *testing.T) {
	cases := map[string]string{
		"P1":     "critical",
		"SEV2":   "warning",
		"medium": "warning",
		"low":    "info",
		"":       "info",
	}
	for in, want := range cases {
		if got := normalizeSeverity(in); got != want {
			t.Errorf("normalizeSeverity(%q): got %q, want %q", in, got, want)
		}
	}
}
