package ingestion

import (
	"context"
	"testing"

	"github.com/kevin/vigil/ent"
)

// TestPrometheusAdapter 验证 Prometheus/Alertmanager 适配器归一化（单 alert）。
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

	evts, err := a.Normalize(context.Background(), payload, nil, nil)
	if err != nil {
		t.Fatalf("Normalize failed: %v", err)
	}
	if len(evts) != 1 {
		t.Fatalf("expected 1 event, got %d", len(evts))
	}
	evt := evts[0]

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

// TestPrometheusAdapter_MultipleAlerts 验证多 alert 拆分（修复"只取首条"丢告警 bug）。
func TestPrometheusAdapter_MultipleAlerts(t *testing.T) {
	a := PrometheusAdapter{}
	payload := []byte(`{
		"alerts": [
			{"status":"firing","labels":{"alertname":"A","severity":"critical"},"fingerprint":"fp1"},
			{"status":"firing","labels":{"alertname":"B","severity":"warning"},"fingerprint":"fp2"},
			{"status":"resolved","labels":{"alertname":"C","severity":"info"},"fingerprint":"fp3"}
		]
	}`)
	evts, err := a.Normalize(context.Background(), payload, nil, nil)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(evts) != 3 {
		t.Fatalf("expected 3 events (multi-alert split), got %d", len(evts))
	}
	// 验证三条各自独立
	if evts[0].SourceEventID != "fp1" || evts[1].SourceEventID != "fp2" || evts[2].SourceEventID != "fp3" {
		t.Errorf("source_event_ids: %s %s %s", evts[0].SourceEventID, evts[1].SourceEventID, evts[2].SourceEventID)
	}
	if evts[2].Status != "resolved" {
		t.Errorf("3rd alert status: %q, want resolved", evts[2].Status)
	}
}

// TestGrafanaAdapter 验证 Grafana 适配器归一化（用原生 severity）。
func TestGrafanaAdapter(t *testing.T) {
	a := GrafanaAdapter{}
	if a.Type() != "grafana" {
		t.Fatalf("Type: got %q, want grafana", a.Type())
	}
	payload := []byte(`{
		"alerts": [{
			"status": "firing",
			"labels": {"alertname": "DBDown", "instance": "db1:5432"},
			"annotations": {"summary": "数据库连接失败"},
			"severity": "warning",
			"fingerprint": "grafana-fp-1"
		}]
	}`)
	evts, err := a.Normalize(context.Background(), payload, nil, nil)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(evts) != 1 {
		t.Fatalf("expected 1 event, got %d", len(evts))
	}
	evt := evts[0]
	if evt.Source != "grafana" {
		t.Errorf("source: %q, want grafana", evt.Source)
	}
	if evt.Severity != "warning" {
		t.Errorf("severity: %q, want warning (from Grafana native)", evt.Severity)
	}
	if evt.SourceEventID != "grafana-fp-1" {
		t.Errorf("source_event_id: %q", evt.SourceEventID)
	}
	if evt.Summary != "数据库连接失败" {
		t.Errorf("summary: %q", evt.Summary)
	}
	if evt.DedupKey != "grafana:grafana-fp-1" {
		t.Errorf("dedup_key: %q", evt.DedupKey)
	}
}

// TestGrafanaAdapter_MultipleAlerts Grafana 多 alert 拆分。
func TestGrafanaAdapter_MultipleAlerts(t *testing.T) {
	a := GrafanaAdapter{}
	payload := []byte(`{
		"alerts": [
			{"status":"firing","labels":{"alertname":"X"},"severity":"critical","fingerprint":"g1"},
			{"status":"firing","labels":{"alertname":"Y"},"severity":"info","fingerprint":"g2"}
		]
	}`)
	evts, _ := a.Normalize(context.Background(), payload, nil, nil)
	if len(evts) != 2 {
		t.Fatalf("expected 2 grafana events, got %d", len(evts))
	}
}

// TestGrafanaAdapter_FallbackSeverity Grafana 无原生 severity 时回退 label.severity。
func TestGrafanaAdapter_FallbackSeverity(t *testing.T) {
	a := GrafanaAdapter{}
	payload := []byte(`{
		"alerts": [{
			"status":"firing",
			"labels":{"alertname":"Z","severity":"critical"},
			"fingerprint":"g3"
		}]
	}`)
	evts, _ := a.Normalize(context.Background(), payload, nil, nil)
	if len(evts) != 1 {
		t.Fatalf("expected 1, got %d", len(evts))
	}
	if evts[0].Severity != "critical" {
		t.Errorf("fallback severity: %q, want critical (from label)", evts[0].Severity)
	}
}

func TestPrometheusAdapter_SeverityMapping(t *testing.T) {
	cases := map[string]string{
		"critical": "critical", "error": "critical", "page": "critical",
		"warning": "warning", "warn": "warning",
		"info": "info", "": "info", "unknown": "info",
	}
	for in, want := range cases {
		if got := mapPromSeverity(in); got != want {
			t.Errorf("mapPromSeverity(%q): got %q, want %q", in, got, want)
		}
	}
}

func TestPrometheusAdapter_NoAlerts(t *testing.T) {
	a := PrometheusAdapter{}
	_, err := a.Normalize(context.Background(), []byte(`{"alerts":[]}`), nil, nil)
	if err == nil {
		t.Error("expected error for empty alerts, got nil")
	}
}

func TestPrometheusAdapter_InvalidJSON(t *testing.T) {
	a := PrometheusAdapter{}
	_, err := a.Normalize(context.Background(), []byte(`{not json`), nil, nil)
	if err == nil {
		t.Error("expected error for invalid json, got nil")
	}
}

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
	evts, err := a.Normalize(context.Background(), payload, nil, nil)
	if err != nil {
		t.Fatalf("Normalize failed: %v", err)
	}
	evt := evts[0]
	if evt.SourceEventID != "evt-001" {
		t.Errorf("source_event_id: got %q", evt.SourceEventID)
	}
	if evt.Severity != "critical" {
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

func TestGenericJSONAdapter_Defaults(t *testing.T) {
	a := GenericJSONAdapter{}
	evts, err := a.Normalize(context.Background(), []byte(`{"id":"x1"}`), nil, nil)
	if err != nil {
		t.Fatalf("Normalize failed: %v", err)
	}
	evt := evts[0]
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

func TestAdapterRegistry(t *testing.T) {
	r := NewAdapterRegistry()
	if _, ok := r.Get("prometheus"); !ok {
		t.Error("prometheus adapter not registered")
	}
	if _, ok := r.Get("grafana"); !ok {
		t.Error("grafana adapter not registered")
	}
	if _, ok := r.Get("webhook"); !ok {
		t.Error("webhook (generic) adapter not registered")
	}
	if _, ok := r.Get("nonexistent"); ok {
		t.Error("nonexistent adapter should not exist")
	}
}

func TestNormalizeSeverity(t *testing.T) {
	cases := map[string]string{
		"P1": "critical", "SEV2": "warning", "medium": "warning",
		"low": "info", "": "info",
	}
	for in, want := range cases {
		if got := normalizeSeverity(in); got != want {
			t.Errorf("normalizeSeverity(%q): got %q, want %q", in, got, want)
		}
	}
}

// TestSeverityMapOverride 接入点 severity_map 覆盖默认映射;非法目标值/未命中回落默认。
func TestSeverityMapOverride(t *testing.T) {
	integ := &ent.Integration{Config: map[string]any{
		"severity_map": map[string]any{
			"disaster": "critical", // 自定义原始值 → critical
			"warn":     "info",     // 覆盖默认(warn 默认是 warning)
			"weird":    "fatal",    // 非法目标值,应回落默认
		},
	}}

	// Prometheus:label.severity=disaster 默认会落 info,覆盖后应为 critical
	prom := []byte(`{"alerts":[{"status":"firing","labels":{"alertname":"A","severity":"disaster"},"fingerprint":"f1"}]}`)
	evts, err := PrometheusAdapter{}.Normalize(context.Background(), prom, integ, nil)
	if err != nil {
		t.Fatalf("prom normalize: %v", err)
	}
	if evts[0].Severity != "critical" {
		t.Errorf("prom disaster: got %q, want critical(覆盖生效)", evts[0].Severity)
	}

	// 覆盖优先于默认:warn 默认 warning,被映射到 info
	prom2 := []byte(`{"alerts":[{"status":"firing","labels":{"alertname":"A","severity":"warn"},"fingerprint":"f2"}]}`)
	evts, _ = PrometheusAdapter{}.Normalize(context.Background(), prom2, integ, nil)
	if evts[0].Severity != "info" {
		t.Errorf("prom warn override: got %q, want info", evts[0].Severity)
	}

	// 非法目标值 fatal:回落默认映射(weird→info)
	prom3 := []byte(`{"alerts":[{"status":"firing","labels":{"alertname":"A","severity":"weird"},"fingerprint":"f3"}]}`)
	evts, _ = PrometheusAdapter{}.Normalize(context.Background(), prom3, integ, nil)
	if evts[0].Severity != "info" {
		t.Errorf("prom illegal target: got %q, want info(回落默认)", evts[0].Severity)
	}

	// Generic:同一覆盖表对通用适配器生效
	gen := []byte(`{"source_event_id":"g1","severity":"DISASTER","summary":"x"}`)
	evts, err = GenericJSONAdapter{}.Normalize(context.Background(), gen, integ, nil)
	if err != nil {
		t.Fatalf("generic normalize: %v", err)
	}
	if evts[0].Severity != "critical" {
		t.Errorf("generic disaster(大小写不敏感): got %q, want critical", evts[0].Severity)
	}

	// Grafana:原生 severity 命中覆盖表
	gra := []byte(`{"alerts":[{"status":"firing","severity":"disaster","labels":{"alertname":"G"},"fingerprint":"gf1"}]}`)
	evts, err = GrafanaAdapter{}.Normalize(context.Background(), gra, integ, nil)
	if err != nil {
		t.Fatalf("grafana normalize: %v", err)
	}
	if evts[0].Severity != "critical" {
		t.Errorf("grafana disaster: got %q, want critical", evts[0].Severity)
	}

	// 未配置 severity_map(integ=nil):默认映射不受影响
	evts, _ = PrometheusAdapter{}.Normalize(context.Background(), prom, nil, nil)
	if evts[0].Severity != "info" {
		t.Errorf("nil integ: got %q, want info(默认)", evts[0].Severity)
	}
}
