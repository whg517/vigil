package runbook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kevin/vigil/ent/schema"
)

func TestInternalExecutor_Info(t *testing.T) {
	e := NewInternalExecutor()
	out, err := e.Execute(context.Background(), schema.StepTarget{Kind: "internal", Endpoint: "db1:5432", Readonly: true}, nil)
	if err != nil {
		t.Fatalf("Execute info: %v", err)
	}
	if !strings.Contains(out, "db1:5432") {
		t.Errorf("info output missing endpoint: %s", out)
	}
	if !strings.Contains(out, "info") {
		t.Errorf("info output missing action: %s", out)
	}
}

func TestInternalExecutor_CheckHTTP_Reachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	e := NewInternalExecutor()
	e.SetAllowPrivate(true) // httptest 绑定 127.0.0.1，测试需放行私网
	out, err := e.Execute(context.Background(),
		schema.StepTarget{Kind: "internal", Endpoint: srv.URL},
		map[string]any{"action": "check_http"})
	if err != nil {
		t.Fatalf("check_http: %v", err)
	}
	if !strings.Contains(out, `"status_code":200`) {
		t.Errorf("check_http output missing 200: %s", out)
	}
	if !strings.Contains(out, "latency_ms") {
		t.Errorf("check_http output missing latency: %s", out)
	}
}

func TestInternalExecutor_CheckHTTP_Unreachable(t *testing.T) {
	e := NewInternalExecutor()
	e.SetAllowPrivate(true) // 目标 127.0.0.1，测试需放行私网
	// 坏地址（不 panic，返回 unreachable 状态）
	out, err := e.Execute(context.Background(),
		schema.StepTarget{Kind: "internal", Endpoint: "http://127.0.0.1:1/nonexistent"},
		map[string]any{"action": "check_http"})
	if err != nil {
		t.Fatalf("check_http unreachable should not error: %v", err)
	}
	if !strings.Contains(out, "unreachable") {
		t.Errorf("expected unreachable status: %s", out)
	}
}

func TestInternalExecutor_CheckHTTP_NoEndpoint(t *testing.T) {
	e := NewInternalExecutor()
	_, err := e.Execute(context.Background(),
		schema.StepTarget{Kind: "internal", Endpoint: ""},
		map[string]any{"action": "check_http"})
	if err == nil {
		t.Error("check_http without endpoint should error")
	}
}

func TestInternalExecutor_Kind(t *testing.T) {
	if got := (NewInternalExecutor()).Kind(); got != "internal" {
		t.Errorf("Kind=%q, want internal", got)
	}
}

// TestHTTPExecutor_StructuredOutput FIX-E：Execute 返回结构化输出含 status_code，
// 即使 body 空（如探活端点）也能看到状态码。
func TestHTTPExecutor_StructuredOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK) // 无 body
	}))
	defer srv.Close()

	h := NewHTTPExecutor()
	h.SetAllowPrivate(true) // httptest 绑定 127.0.0.1
	out, err := h.Execute(context.Background(), schema.StepTarget{Kind: "http", Endpoint: srv.URL, Readonly: true}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "status_code") {
		t.Errorf("FIX-E: output should contain status_code, got %q", out)
	}
	if !strings.Contains(out, "200") {
		t.Errorf("output should contain 200, got %q", out)
	}
}

// TestHTTPExecutor_ErrorStatusKeepsOutput 强化 FIX-E：HTTP 状态码≥400 时，Execute
// 应同时返回 error 与结构化 output（含 status_code/body），供上层透传到 StepResult.Output，
// 让前端在失败时仍能看到状态码/响应体，而非只有一句 error。
func TestHTTPExecutor_ErrorStatusKeepsOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"msg":"boom"}`))
	}))
	defer srv.Close()

	h := NewHTTPExecutor()
	h.SetAllowPrivate(true) // httptest 绑定 127.0.0.1
	out, err := h.Execute(context.Background(), schema.StepTarget{Kind: "http", Endpoint: srv.URL, Readonly: true}, nil)
	if err == nil {
		t.Fatal("HTTP 500 应返回 error")
	}
	if out == "" {
		t.Fatal("FIX-E: 失败时 output 不应为空（含状态码/响应体的结构化诊断）")
	}
	if !strings.Contains(out, `"status_code":500`) {
		t.Errorf("FIX-E: output 应含 status_code:500，got %q", out)
	}
	if !strings.Contains(out, "boom") {
		t.Errorf("output 应含响应体 body，got %q", out)
	}
}

// TestInternalExecutor_QueryMetrics Prometheus 即时查询:样本摘要 + 截断 + 参数校验。
func TestInternalExecutor_QueryMetrics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if q := r.URL.Query().Get("query"); q != `up{job="api"}` {
			t.Errorf("query param = %q", q)
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"job":"api","instance":"a:9090"},"value":[1720000000,"1"]},
			{"metric":{"job":"api","instance":"b:9090"},"value":[1720000000,"0"]}]}}`))
	}))
	defer srv.Close()

	e := NewInternalExecutor()
	e.SetAllowPrivate(true)
	out, err := e.Execute(context.Background(),
		schema.StepTarget{Kind: "internal", Endpoint: srv.URL},
		map[string]any{"action": "query_metrics", "query": `up{job="api"}`})
	if err != nil {
		t.Fatalf("query_metrics: %v", err)
	}
	for _, want := range []string{`"status":"success"`, `"total":2`, `"a:9090"`, `"value":"1"`} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %s: %s", want, out)
		}
	}

	// 缺 query 参数应拒绝
	if _, err := e.Execute(context.Background(),
		schema.StepTarget{Endpoint: srv.URL}, map[string]any{"action": "query_metrics"}); err == nil {
		t.Error("missing query should error")
	}
}

// TestInternalExecutor_QueryLogs Loki 区间查询:行摘要 + limit + 上游错误处理。
func TestInternalExecutor_QueryLogs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/loki/api/v1/query_range" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if l := r.URL.Query().Get("limit"); l != "5" {
			t.Errorf("limit = %q, want 5", l)
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"result":[
			{"stream":{"app":"api"},"values":[["1720000000000000000","error: timeout"],["1720000001000000000","error: refused"]]}]}}`))
	}))
	defer srv.Close()

	e := NewInternalExecutor()
	e.SetAllowPrivate(true)
	out, err := e.Execute(context.Background(),
		schema.StepTarget{Kind: "internal", Endpoint: srv.URL},
		map[string]any{"action": "query_logs", "query": `{app="api"} |= "error"`, "limit": float64(5)})
	if err != nil {
		t.Fatalf("query_logs: %v", err)
	}
	for _, want := range []string{`"total":2`, "error: timeout", `"window":"15m"`} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %s: %s", want, out)
		}
	}
}

// TestInternalExecutor_QueryMetrics_UpstreamError 上游 5xx:返回 unreachable 结构化结果,不 error(诊断降级不炸步骤)。
func TestInternalExecutor_QueryMetrics_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	e := NewInternalExecutor()
	e.SetAllowPrivate(true)
	out, err := e.Execute(context.Background(),
		schema.StepTarget{Endpoint: srv.URL},
		map[string]any{"action": "query_metrics", "query": "up"})
	if err != nil {
		t.Fatalf("should not hard-fail: %v", err)
	}
	if !strings.Contains(out, "unreachable") {
		t.Errorf("expect unreachable marker: %s", out)
	}
}

// TestInternalExecutor_QueryMetrics_SSRFBlocked 生产模式(不放行私网)拒绝私网 Prometheus 地址。
func TestInternalExecutor_QueryMetrics_SSRFBlocked(t *testing.T) {
	e := NewInternalExecutor() // AllowPrivate=false
	out, err := e.Execute(context.Background(),
		schema.StepTarget{Endpoint: "http://127.0.0.1:9090"},
		map[string]any{"action": "query_metrics", "query": "up"})
	// SSRF 拦截路径:readonlyGet 返回 err → 输出 unreachable 结构化结果(与网络不可达同一降级面)
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if !strings.Contains(out, "unreachable") {
		t.Errorf("private endpoint should be blocked, got: %s", out)
	}
}
