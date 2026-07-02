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
	e.AllowPrivate = true // httptest 绑定 127.0.0.1，测试需放行私网
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
	e.AllowPrivate = true // 目标 127.0.0.1，测试需放行私网
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
