// Package metrics 定义 Vigil 的 Prometheus 指标（能力域 H2 可观测性）。
//
// 对应 architecture.md §6.3：Vigil 自身暴露 metrics 供自家/外部监控接入（吃自己狗粮）。
// · HTTP 请求指标（method/path/status/duration）—— 由中间件自动采集
// · 业务指标（告警接入量/事件数/升级次数/通知成功率/队列深度）—— 各域埋点
// · Go runtime 指标（prometheus 默认收集）
package metrics

import (
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// 业务指标（各域埋点用）
var (
	// AlertsReceived 告警接入量（按 source/severity 维度）
	AlertsReceived = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "vigil_alerts_received_total",
		Help: "Total alerts received by source and severity.",
	}, []string{"source", "severity"})

	// IncidentsCreated 创建的事件数（按 severity）
	IncidentsCreated = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "vigil_incidents_created_total",
		Help: "Total incidents created by severity.",
	}, []string{"severity"})

	// EscalationsTriggered 升级触发次数
	EscalationsTriggered = promauto.NewCounter(prometheus.CounterOpts{
		Name: "vigil_escalations_triggered_total",
		Help: "Total escalation triggers.",
	})

	// NotificationsSent 通知发送（按 channel/success）
	NotificationsSent = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "vigil_notifications_sent_total",
		Help: "Total notifications sent by channel and result.",
	}, []string{"channel", "result"})

	// IncidentDuration 事件解决时长分布（秒）
	IncidentDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "vigil_incident_resolve_duration_seconds",
		Help:    "Time from incident creation to resolution.",
		Buckets: prometheus.ExponentialBuckets(60, 2, 10), // 1min ~ 8h
	})

	// TimelineItemsRecorded 时间线条目数（按 type）
	TimelineItemsRecorded = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "vigil_timeline_items_recorded_total",
		Help: "Total timeline items recorded by type.",
	}, []string{"type"})

	// LLMCalls LLM 调用（按 stage/result，监控 AI 成本与成功率）
	LLMCalls = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "vigil_llm_calls_total",
		Help: "Total LLM calls by stage and result.",
	}, []string{"stage", "result"})
)

// HTTP 指标
var (
	httpRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "vigil_http_requests_total",
		Help: "Total HTTP requests by method, path and status.",
	}, []string{"method", "path", "status"})

	httpDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "vigil_http_request_duration_seconds",
		Help:    "HTTP request duration.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path"})
)

// statusLabel 把状态码归为 2xx/4xx/5xx 类（避免高基数）。
func statusLabel(code int) string {
	switch {
	case code < 300:
		return "2xx"
	case code < 400:
		return "3xx"
	case code < 500:
		return "4xx"
	default:
		return "5xx"
	}
}

// normalizePath 规范化路径（把数字 ID 替换为 :id，避免高基数）。
func normalizePath(path string) string {
	parts := splitPath(path)
	for i, p := range parts {
		if isNumeric(p) {
			parts[i] = ":id"
		}
	}
	return joinPath(parts)
}

func splitPath(p string) []string {
	var out []string
	cur := ""
	for _, r := range p {
		if r == '/' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
		} else {
			cur += string(r)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func joinPath(parts []string) string {
	if len(parts) == 0 {
		return "/"
	}
	out := ""
	for _, p := range parts {
		out += "/" + p
	}
	return out
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// EchoMiddleware 返回采集 HTTP 指标的 Echo 中间件。
// 自动记录请求计数（method/path/status）+ 延迟直方图。
func EchoMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			err := next(c)
			elapsed := time.Since(start).Seconds()

			method := c.Request().Method
			path := normalizePath(c.Path())
			status := strconv.Itoa(statusLabelToInt(statusLabel(c.Response().Status)))

			httpRequests.WithLabelValues(method, path, status).Inc()
			httpDuration.WithLabelValues(method, path).Observe(elapsed)
			return err
		}
	}
}

// statusLabelToInt 把状态类转回数字（用于 label，统一用数字）。
func statusLabelToInt(s string) int {
	switch s {
	case "2xx":
		return 200
	case "3xx":
		return 300
	case "4xx":
		return 400
	case "5xx":
		return 500
	}
	return 0
}
