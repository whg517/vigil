package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestAlertsReceived 埋点后计数递增 + label 隔离。
func TestAlertsReceived(t *testing.T) {
	before := testutil.ToFloat64(AlertsReceived.WithLabelValues("prometheus", "critical"))
	AlertsReceived.WithLabelValues("prometheus", "critical").Inc()
	after := testutil.ToFloat64(AlertsReceived.WithLabelValues("prometheus", "critical"))
	if after-before != 1 {
		t.Errorf("AlertsReceived 应 +1: before=%v after=%v", before, after)
	}
	// 不同 label 不应影响
	other := testutil.ToFloat64(AlertsReceived.WithLabelValues("zabbix", "warning"))
	AlertsReceived.WithLabelValues("prometheus", "critical").Inc()
	if testutil.ToFloat64(AlertsReceived.WithLabelValues("zabbix", "warning")) != other {
		t.Error("不同 label 不应互相影响")
	}
}

// TestEscalationsTriggered 升级计数。
func TestEscalationsTriggered(t *testing.T) {
	EscalationsTriggered.Inc()
	EscalationsTriggered.Inc()
	if c := testutil.ToFloat64(EscalationsTriggered); c < 2 {
		t.Errorf("EscalationsTriggered 应 >=2: got %v", c)
	}
}

// TestNotificationsSent 通知按 channel/result 计数。
func TestNotificationsSent(t *testing.T) {
	NotificationsSent.WithLabelValues("im", "success").Inc()
	NotificationsSent.WithLabelValues("im", "failed").Inc()
	s := testutil.ToFloat64(NotificationsSent.WithLabelValues("im", "success"))
	f := testutil.ToFloat64(NotificationsSent.WithLabelValues("im", "failed"))
	if s < 1 || f < 1 {
		t.Errorf("im success/failed 计数异常: s=%v f=%v", s, f)
	}
}

// TestNormalizePath 路径规范化（数字 ID 替换 :id，避免高基数）。
func TestNormalizePath(t *testing.T) {
	cases := map[string]string{
		"/api/v1/incidents":     "/api/v1/incidents",
		"/api/v1/incidents/42":  "/api/v1/incidents/:id",
		"/incidents/1/timeline": "/incidents/:id/timeline",
		"/":                     "/",
	}
	for in, want := range cases {
		if got := normalizePath(in); got != want {
			t.Errorf("normalizePath(%q): got %q, want %q", in, got, want)
		}
	}
}

// TestStatusLabel 状态码归类（避免高基数）。
func TestStatusLabel(t *testing.T) {
	cases := map[int]string{
		200: "2xx", 201: "2xx",
		301: "3xx",
		400: "4xx", 404: "4xx",
		500: "5xx", 503: "5xx",
	}
	for code, want := range cases {
		if got := statusLabel(code); got != want {
			t.Errorf("statusLabel(%d): got %q, want %q", code, got, want)
		}
	}
}

// TestMetricsRegistered 验证关键指标已注册到默认 registry。
// /metrics 端点输出这些指标。
func TestMetricsRegistered(t *testing.T) {
	// 触发所有指标埋点确保有数据（Gather 只返回有数据的 metric）
	AlertsReceived.WithLabelValues("test", "info").Inc()
	IncidentsCreated.WithLabelValues("warning").Inc()
	EscalationsTriggered.Inc()
	NotificationsSent.WithLabelValues("test", "success").Inc()
	TimelineItemsRecorded.WithLabelValues("test").Inc()
	LLMCalls.WithLabelValues("test", "success").Inc()
	IncidentDuration.Observe(120)
	httpRequests.WithLabelValues("GET", "/test", "200").Inc()
	httpDuration.WithLabelValues("GET", "/test").Observe(0.1)

	// 从默认 gatherer 收集，确认指标名存在
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	names := map[string]bool{}
	for _, mf := range mfs {
		names[mf.GetName()] = true
	}
	wantMetrics := []string{
		"vigil_alerts_received_total",
		"vigil_incidents_created_total",
		"vigil_escalations_triggered_total",
		"vigil_notifications_sent_total",
		"vigil_http_requests_total",
		"vigil_http_request_duration_seconds",
	}
	for _, name := range wantMetrics {
		if !names[name] {
			t.Errorf("指标 %q 未注册到 registry", name)
		}
	}
}

// 确保引用 strings（避免未使用 import，后续扩展用）。
var _ = strings.Contains
