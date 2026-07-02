// Package runbook 实现能力域 9：Runbook 处置。
//
// 对应 docs/capabilities/06-runbook.md：
// · 两档执行：诊断类（readonly）内置安全执行；处置类（写）require_approval 人确认或外接
// · Executor 接口可插拔（http/ansible/jenkins/内部诊断）
// · 失败按 on_failure 处理（continue/abort/escalate）
// · 执行结果回写时间线
//
// 设计基线第 8 条：Vigil 不直接碰用户生产环境的写操作，避免"能搞垮生产的定时炸弹"。
package runbook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/kevin/vigil/ent/schema"
)

// StepResult 单步执行结果。
type StepResult struct {
	StepID   string
	Name     string
	Action   string // diagnose | execute | ...
	Success  bool
	Output   string // 执行输出
	Error    string
	Duration time.Duration
	Skipped  bool // 因 require_approval 未确认而跳过
}

// Executor 执行器接口。各执行器（http/ansible/jenkins/内部）实现。
// 对应 capabilities §4。
type Executor interface {
	// Kind 执行器标识：http | internal | ansible | jenkins
	Kind() string
	// Execute 执行某步骤的动作。
	// readonly=true 的诊断动作可安全直接执行；
	// 写动作应由调用方先确认 require_approval。
	Execute(ctx context.Context, target schema.StepTarget, params map[string]any) (output string, err error)
}

// HTTPExecutor HTTP 执行器：POST 到 target.Endpoint，返回响应体。
// 诊断类（查日志 API）与处置类（触发 webhook）通用。
//
// AllowPrivate 控制 SSRF 防护是否放行私网/loopback（SEC-03）：
// 生产默认 false（拒绝私网）；测试场景（httptest 绑定 127.0.0.1）设 true。
type HTTPExecutor struct {
	Client       *http.Client
	AllowPrivate bool // 放行私网地址（仅测试用，生产保持 false）
}

// NewHTTPExecutor 创建 HTTP 执行器（生产用，AllowPrivate=false）。
func NewHTTPExecutor() *HTTPExecutor {
	return &HTTPExecutor{Client: &http.Client{Timeout: 30 * time.Second}}
}

func (HTTPExecutor) Kind() string { return "http" }

func (h *HTTPExecutor) Execute(ctx context.Context, target schema.StepTarget, params map[string]any) (string, error) {
	if target.Endpoint == "" {
		return "", fmt.Errorf("empty endpoint")
	}
	// SEC-03：SSRF 防护——校验目标 URL（禁私网/云元数据/非 http scheme）。
	if err := (&endpointValidator{allowPrivate: h.AllowPrivate}).validate(target.Endpoint); err != nil {
		return "", err
	}
	var body []byte
	if params != nil {
		body, _ = json.Marshal(params)
	} else {
		body = []byte("{}")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.Endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	if resp.StatusCode >= 400 {
		return buf.String(), fmt.Errorf("http %d", resp.StatusCode)
	}
	return buf.String(), nil
}

// InternalExecutor 内置诊断执行器（只读安全动作，能力域 9 M9.4）。
//
// 根据 params.action 执行不同诊断：
//   - check_http：对 target.endpoint 做 HTTP GET 探活，返回状态码（验证服务可达性）
//   - info（默认）：返回 target 元信息（kind/endpoint/readonly）
//
// 全部只读，不修改外部状态。后续可扩展 query_metrics（查 Prometheus）等。
type InternalExecutor struct {
	client       *http.Client
	AllowPrivate bool // 放行私网（仅测试用，生产保持 false）
}

// NewInternalExecutor 创建内置执行器。
func NewInternalExecutor() *InternalExecutor {
	return &InternalExecutor{client: &http.Client{Timeout: 10 * time.Second}}
}

func (*InternalExecutor) Kind() string { return "internal" }

func (e *InternalExecutor) Execute(ctx context.Context, target schema.StepTarget, params map[string]any) (string, error) {
	action, _ := params["action"].(string)
	if action == "" {
		action = "info"
	}

	switch action {
	case "check_http":
		return e.checkHTTP(ctx, target.Endpoint)
	default:
		// info：返回 target 元信息（结构化，非纯模拟）
		return fmt.Sprintf(`{"action":"info","target":{"kind":%q,"endpoint":%q,"readonly":%t}}`,
			target.Kind, target.Endpoint, target.Readonly), nil
	}
}

// checkHTTP 对 endpoint 做 GET 探活，返回状态码与耗时。
func (e *InternalExecutor) checkHTTP(ctx context.Context, endpoint string) (string, error) {
	if endpoint == "" {
		return "", fmt.Errorf("check_http requires endpoint")
	}
	// SEC-03：SSRF 防护（与 HTTPExecutor 同一校验）。
	if err := (&endpointValidator{allowPrivate: e.AllowPrivate}).validate(endpoint); err != nil {
		return "", err
	}
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Sprintf(`{"action":"check_http","endpoint":%q,"status":"unreachable","error":%q}`, endpoint, err.Error()), nil
	}
	defer func() { _ = resp.Body.Close() }()
	elapsed := time.Since(start).Milliseconds()
	return fmt.Sprintf(`{"action":"check_http","endpoint":%q,"status_code":%d,"latency_ms":%d}`,
		endpoint, resp.StatusCode, elapsed), nil
}

// Registry 执行器注册表。
type Registry struct {
	executors map[string]Executor
}

// NewRegistry 创建注册表并注册内置执行器。
func NewRegistry() *Registry {
	r := &Registry{executors: make(map[string]Executor)}
	r.Register(NewHTTPExecutor())
	r.Register(NewInternalExecutor())
	return r
}

// Register 注册执行器。
func (r *Registry) Register(e Executor) {
	r.executors[e.Kind()] = e
}

// Get 按 kind 取执行器。
func (r *Registry) Get(kind string) (Executor, bool) {
	e, ok := r.executors[kind]
	return e, ok
}
