package metrics

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
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
	other := testutil.ToFloat64(AlertsReceived.WithLabelValues("grafana", "warning"))
	AlertsReceived.WithLabelValues("prometheus", "critical").Inc()
	if testutil.ToFloat64(AlertsReceived.WithLabelValues("grafana", "warning")) != other {
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

// TestEchoMiddlewareCapturesStatus 验证 v5 迁移的核心行为：
// statusRecorder 拦截 WriteHeader/隐式 200，把状态码正确写入 vigil_http_requests_total。
// 覆盖显式状态码（2xx/4xx/5xx）与隐式 200（handler 只 Write 不 WriteHeader）两条路径。
func TestEchoMiddlewareCapturesStatus(t *testing.T) {
	e := echo.New()
	e.Use(EchoMiddleware())

	// 显式状态码路由。
	e.GET("/ok", func(c *echo.Context) error { return c.String(http.StatusOK, "ok") })
	e.GET("/notfound", func(c *echo.Context) error { return c.String(http.StatusNotFound, "nope") })
	e.GET("/boom", func(c *echo.Context) error { return c.String(http.StatusInternalServerError, "boom") })
	// 隐式 200：只写 body，不调 WriteHeader（触发 statusRecorder.Write 的兜底）。
	e.GET("/implicit", func(c *echo.Context) error {
		_, _ = c.Response().Write([]byte("implicit"))
		return nil
	})

	// 记录基线，避免其它测试累积计数干扰断言。
	// 注意：中间件把状态码经 statusLabel→statusLabelToInt 归一为桶基准值（200/400/500）。
	base := map[string]float64{
		"2xx":         testutil.ToFloat64(httpRequests.WithLabelValues("GET", "/ok", "200")),
		"4xx":         testutil.ToFloat64(httpRequests.WithLabelValues("GET", "/notfound", "400")),
		"5xx":         testutil.ToFloat64(httpRequests.WithLabelValues("GET", "/boom", "500")),
		"implicit2xx": testutil.ToFloat64(httpRequests.WithLabelValues("GET", "/implicit", "200")),
	}

	serve := func(target string) {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
	}
	serve("/ok")
	serve("/notfound")
	serve("/boom")
	serve("/implicit")

	cases := map[string]string{
		"2xx":         "GET /ok 显式 200 应归到 2xx 桶(标签 200)",
		"4xx":         "GET /notfound 显式 404 应归到 4xx 桶(标签 400)",
		"5xx":         "GET /boom 显式 500 应归到 5xx 桶(标签 500)",
		"implicit2xx": "GET /implicit 隐式 200 应归到 2xx 桶(标签 200，statusRecorder.Write 兜底)",
	}
	for key, msg := range cases {
		path, label := pathAndLabel(key)
		got := testutil.ToFloat64(httpRequests.WithLabelValues("GET", path, label))
		if got-base[key] != 1 {
			t.Errorf("%s: vigil_http_requests_total{path=%q,status=%q} 应 +1, before=%v after=%v",
				msg, path, label, base[key], got)
		}
	}
}

// pathAndLabel 是 TestEchoMiddlewareCapturesStatus 的 label→(path,statusLabel) 映射，
// 拆出来仅为避免 map value 是 struct 带来的可读性下降。
func pathAndLabel(key string) (path, label string) {
	switch key {
	case "2xx":
		return "/ok", "200"
	case "4xx":
		return "/notfound", "400"
	case "5xx":
		return "/boom", "500"
	case "implicit2xx":
		return "/implicit", "200"
	}
	return "", ""
}
