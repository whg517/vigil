// Package metrics 定义 Vigil 的 Prometheus 指标（能力域 H2 可观测性）。
//
// 对应 architecture.md §6.3：Vigil 自身暴露 metrics 供自家/外部监控接入（吃自己狗粮）。
// · HTTP 请求指标（method/path/status/duration）—— 由中间件自动采集
// · 业务指标（告警接入量/事件数/升级次数/通知成功率/队列深度）—— 各域埋点
// · Go runtime 指标（prometheus 默认收集）
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// statusRecorder 包装 http.ResponseWriter 以捕获最终状态码。
// Echo v5 的 Context.Response() 返回标准 http.ResponseWriter（不再是 *echo.Response），
// 丢失了 .Status 字段；这里通过拦截 WriteHeader 在中间件层记录。
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

// WriteHeader 记录状态码后委托给底层 ResponseWriter。
func (r *statusRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.status = code
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(code)
}

// Write 兜底：handler 未显式调用 WriteHeader 时，Go 的 net/http 会在首次 Write 前
// 隐式写入 200；此处同步捕获以避免 status==0 的标签。
func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.status = http.StatusOK
		r.wroteHeader = true
	}
	return r.ResponseWriter.Write(b)
}

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

	// LLM 成本控制（能力域 11，capabilities/07 §B5 Q1）
	// LLMCacheHits 缓存命中（避免真实调用，省 token）
	LLMCacheHits = promauto.NewCounter(prometheus.CounterOpts{
		Name: "vigil_llm_cache_hits_total",
		Help: "LLM completion cache hits (saved a real call).",
	})
	// LLMRateLimited 被限流/配额拒绝
	LLMRateLimited = promauto.NewCounter(prometheus.CounterOpts{
		Name: "vigil_llm_rate_limited_total",
		Help: "LLM calls rejected by rate limit or quota.",
	})
	// LLMTokensTotal 累计 token 消耗（按 provider）
	LLMTokensTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "vigil_llm_tokens_total",
		Help: "Total LLM tokens consumed by provider.",
	}, []string{"provider"})
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
		return func(c *echo.Context) error {
			start := time.Now()

			// 包裹 ResponseWriter 以捕获状态码（v5 Response() 不再暴露 .Status 字段）。
			rec := &statusRecorder{ResponseWriter: c.Response(), status: http.StatusOK}
			c.SetResponse(rec)

			err := next(c)
			elapsed := time.Since(start).Seconds()

			method := c.Request().Method
			path := normalizePath(c.Path())
			status := strconv.Itoa(statusLabelToInt(statusLabel(rec.status)))

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
