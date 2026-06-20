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
type HTTPExecutor struct {
	Client *http.Client
}

// NewHTTPExecutor 创建 HTTP 执行器。
func NewHTTPExecutor() *HTTPExecutor {
	return &HTTPExecutor{Client: &http.Client{Timeout: 30 * time.Second}}
}

func (HTTPExecutor) Kind() string { return "http" }

func (h *HTTPExecutor) Execute(ctx context.Context, target schema.StepTarget, params map[string]any) (string, error) {
	if target.Endpoint == "" {
		return "", fmt.Errorf("empty endpoint")
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
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	if resp.StatusCode >= 400 {
		return buf.String(), fmt.Errorf("http %d", resp.StatusCode)
	}
	return buf.String(), nil
}

// InternalExecutor 内置诊断执行器（只读安全动作）。
// 当前为占位：返回模拟的诊断结果。后续可对接指标/日志/拓扑查询。
type InternalExecutor struct{}

func (InternalExecutor) Kind() string { return "internal" }

func (InternalExecutor) Execute(ctx context.Context, target schema.StepTarget, params map[string]any) (string, error) {
	// TODO: 接入只读查询（查指标/日志/拓扑）
	// 当前返回模拟诊断结果，保证链路通畅
	return fmt.Sprintf(`{"status":"ok","diagnose":"internal check for %s"}`, target.Endpoint), nil
}

// Registry 执行器注册表。
type Registry struct {
	executors map[string]Executor
}

// NewRegistry 创建注册表并注册内置执行器。
func NewRegistry() *Registry {
	r := &Registry{executors: make(map[string]Executor)}
	r.Register(NewHTTPExecutor())
	r.Register(InternalExecutor{})
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
