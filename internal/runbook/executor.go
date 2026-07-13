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
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kevin/vigil/ent/schema"
)

// StepResult 单步执行结果。
// json tag 用 snake_case，供前端逐步渲染成败/输出；不加 tag 会序列化成 PascalCase 让前端读不到。
type StepResult struct {
	StepID   string        `json:"step_id"`
	Name     string        `json:"name"`
	Action   string        `json:"action"` // diagnose | execute | ...
	Success  bool          `json:"success"`
	Output   string        `json:"output"` // 执行输出
	Error    string        `json:"error"`
	Duration time.Duration `json:"duration"`
	Skipped  bool          `json:"skipped"` // 因 require_approval 未确认而跳过
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
	AllowPrivate bool               // 放行私网地址（仅测试用，生产保持 false）
	creds        CredentialResolver // 凭据解析器（T6.3，可选注入；nil 则不注入凭据）
}

// NewHTTPExecutor 创建 HTTP 执行器（生产用，AllowPrivate=false，SSRF 防护生效）。
func NewHTTPExecutor() *HTTPExecutor {
	return &HTTPExecutor{Client: newHTTPClient(false)}
}

// SetAllowPrivate 切换私网放行（测试用：httptest 绑定 127.0.0.1 需放行）。
// 生产代码不要调用；保留默认 false。
func (h *HTTPExecutor) SetAllowPrivate(allow bool) {
	h.AllowPrivate = allow
	h.Client = newHTTPClient(allow)
}

func (HTTPExecutor) Kind() string { return "http" }

// SetCredentialResolver 注入凭据解析器（T6.3）。装配层与注册表统一调用。
func (h *HTTPExecutor) SetCredentialResolver(r CredentialResolver) { h.creds = r }

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
	// T6.3：若 step 引用了托管凭据，解密后注入头（明文只在此处短暂持有，不落日志/时间线）。
	if err := injectCredential(ctx, h.creds, target.CredentialRef, req); err != nil {
		return "", err
	}
	resp, err := h.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	// FIX-E：返回结构化输出（status_code + body），而非裸 body。
	// 修复前对探活端点（如 /status/200 无 body）返回空串，用户看不到任何结果。
	// 现在即使 body 空，也能看到状态码，便于判断处置结果。
	result := fmt.Sprintf(`{"status_code":%d,"body":%s}`, resp.StatusCode, jsonQuote(buf.String()))
	if resp.StatusCode >= 400 {
		return result, fmt.Errorf("http %d", resp.StatusCode)
	}
	return result, nil
}

// jsonQuote 把字符串转为 JSON 字符串字面量（含转义），供结构化输出用。
func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// injectCredential 若 credRef>0 且配了 resolver，解密凭据并注入 HTTP 头（T6.3）。
// resolver 为 nil（未装配）或 credRef<=0（无引用）时无操作。
// ★ 明文只在 req.Header.Set 这一瞬间写入请求头，不返回、不打印、不落任何持久化。
func injectCredential(ctx context.Context, r CredentialResolver, credRef int, req *http.Request) error {
	if r == nil || credRef <= 0 {
		return nil
	}
	hdr, err := r.ResolveHeader(ctx, credRef)
	if err != nil {
		return err // 已脱敏（不含明文），调用方按执行失败处理
	}
	if hdr != nil {
		req.Header.Set(hdr.name, hdr.value)
	}
	return nil
}

// InternalExecutor 内置诊断执行器（只读安全动作，能力域 9 M9.4）。
//
// 根据 params.action 执行不同诊断：
//   - check_http：对 target.endpoint 做 HTTP GET 探活，返回状态码（验证服务可达性）
//   - query_metrics：对 target.endpoint（Prometheus base URL）执行即时查询
//     （params.query 为 PromQL），返回样本摘要——排障时"看一眼指标"不离开事件现场
//   - query_logs：对 target.endpoint（Loki base URL）执行区间查询
//     （params.query 为 LogQL，params.limit 可选），返回日志行摘要
//   - info（默认）：返回 target 元信息（kind/endpoint/readonly）
//
// 全部只读 GET，不修改外部状态；与 check_http 共用 SSRF 防护与托管凭据注入。
type InternalExecutor struct {
	client       *http.Client
	AllowPrivate bool               // 放行私网（仅测试用，生产保持 false）
	creds        CredentialResolver // 凭据解析器（T6.3，可选注入）
}

// NewInternalExecutor 创建内置执行器（生产用，AllowPrivate=false，SSRF 防护生效）。
func NewInternalExecutor() *InternalExecutor {
	c := newHTTPClient(false)
	c.Timeout = 10 * time.Second // 内置探活用更短超时
	return &InternalExecutor{client: c}
}

// SetAllowPrivate 切换私网放行（测试用）。
func (e *InternalExecutor) SetAllowPrivate(allow bool) {
	e.AllowPrivate = allow
	c := newHTTPClient(allow)
	c.Timeout = 10 * time.Second
	e.client = c
}

func (*InternalExecutor) Kind() string { return "internal" }

// SetCredentialResolver 注入凭据解析器（T6.3，供 check_http 诊断带鉴权访问外部只读 API）。
func (e *InternalExecutor) SetCredentialResolver(r CredentialResolver) { e.creds = r }

func (e *InternalExecutor) Execute(ctx context.Context, target schema.StepTarget, params map[string]any) (string, error) {
	action, _ := params["action"].(string)
	if action == "" {
		action = "info"
	}

	switch action {
	case "check_http":
		return e.checkHTTP(ctx, target)
	case "query_metrics":
		return e.queryMetrics(ctx, target, params)
	case "query_logs":
		return e.queryLogs(ctx, target, params)
	default:
		// info：返回 target 元信息（结构化，非纯模拟）
		return fmt.Sprintf(`{"action":"info","target":{"kind":%q,"endpoint":%q,"readonly":%t}}`,
			target.Kind, target.Endpoint, target.Readonly), nil
	}
}

// checkHTTP 对 endpoint 做 GET 探活，返回状态码与耗时。
func (e *InternalExecutor) checkHTTP(ctx context.Context, target schema.StepTarget) (string, error) {
	endpoint := target.Endpoint
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
	// T6.3：诊断探活也可带托管凭据（访问需鉴权的只读 API），明文只在此处注入。
	if err := injectCredential(ctx, e.creds, target.CredentialRef, req); err != nil {
		return "", err
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

// queryMetrics 对 Prometheus 执行即时查询（GET /api/v1/query），返回样本摘要。
// endpoint = Prometheus base URL;params.query = PromQL(必填)。
// 输出截断到前 maxDiagSamples 条样本——诊断结果进时间线/IM 卡片,要的是"一眼看关键值"而非全量数据。
func (e *InternalExecutor) queryMetrics(ctx context.Context, target schema.StepTarget, params map[string]any) (string, error) {
	promql, _ := params["query"].(string)
	if target.Endpoint == "" || promql == "" {
		return "", fmt.Errorf("query_metrics requires endpoint and params.query")
	}
	u := strings.TrimRight(target.Endpoint, "/") + "/api/v1/query?query=" + url.QueryEscape(promql)
	body, err := e.readonlyGet(ctx, u, target)
	if err != nil {
		return fmt.Sprintf(`{"action":"query_metrics","status":"unreachable","error":%q}`, err.Error()), nil
	}
	// 解析 Prometheus 标准响应,提取样本摘要(容错:解析失败原样截断返回)。
	var pr struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Value  []any             `json:"value"` // [ts, "value"]
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &pr); err != nil {
		return fmt.Sprintf(`{"action":"query_metrics","status":"ok","raw":%q}`, truncateDiag(string(body))), nil
	}
	samples := make([]map[string]any, 0, maxDiagSamples)
	for i, r := range pr.Data.Result {
		if i >= maxDiagSamples {
			break
		}
		val := any(nil)
		if len(r.Value) == 2 {
			val = r.Value[1]
		}
		samples = append(samples, map[string]any{"metric": r.Metric, "value": val})
	}
	out, _ := json.Marshal(map[string]any{
		"action": "query_metrics", "status": pr.Status, "result_type": pr.Data.ResultType,
		"total": len(pr.Data.Result), "samples": samples,
	})
	return string(out), nil
}

// queryLogs 对 Loki 执行区间查询（GET /loki/api/v1/query_range），返回日志行摘要。
// endpoint = Loki base URL;params.query = LogQL(必填);params.limit 可选(默认 20,上限 100);
// 查询窗口固定最近 15 分钟——排障场景关注"现在正在发生什么"。
func (e *InternalExecutor) queryLogs(ctx context.Context, target schema.StepTarget, params map[string]any) (string, error) {
	logql, _ := params["query"].(string)
	if target.Endpoint == "" || logql == "" {
		return "", fmt.Errorf("query_logs requires endpoint and params.query")
	}
	limit := 20
	if l, ok := params["limit"].(float64); ok && l > 0 && l <= 100 {
		limit = int(l)
	}
	now := time.Now()
	q := url.Values{}
	q.Set("query", logql)
	q.Set("limit", fmt.Sprintf("%d", limit))
	q.Set("start", fmt.Sprintf("%d", now.Add(-15*time.Minute).UnixNano()))
	q.Set("end", fmt.Sprintf("%d", now.UnixNano()))
	u := strings.TrimRight(target.Endpoint, "/") + "/loki/api/v1/query_range?" + q.Encode()
	body, err := e.readonlyGet(ctx, u, target)
	if err != nil {
		return fmt.Sprintf(`{"action":"query_logs","status":"unreachable","error":%q}`, err.Error()), nil
	}
	var lr struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Stream map[string]string `json:"stream"`
				Values [][2]string       `json:"values"` // [ts, line]
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &lr); err != nil {
		return fmt.Sprintf(`{"action":"query_logs","status":"ok","raw":%q}`, truncateDiag(string(body))), nil
	}
	lines := make([]string, 0, limit)
	total := 0
	for _, r := range lr.Data.Result {
		total += len(r.Values)
		for _, v := range r.Values {
			if len(lines) < limit {
				lines = append(lines, truncateDiag(v[1]))
			}
		}
	}
	out, _ := json.Marshal(map[string]any{
		"action": "query_logs", "status": lr.Status, "total": total, "window": "15m", "lines": lines,
	})
	return string(out), nil
}

// readonlyGet 只读诊断共用的 GET:SSRF 校验 → 凭据注入 → 请求 → 限量读响应。
func (e *InternalExecutor) readonlyGet(ctx context.Context, u string, target schema.StepTarget) ([]byte, error) {
	if err := (&endpointValidator{allowPrivate: e.AllowPrivate}).validate(u); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if err := injectCredential(ctx, e.creds, target.CredentialRef, req); err != nil {
		return nil, err
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("upstream returned %d", resp.StatusCode)
	}
	// 限量读:诊断响应不该无界(防大响应拖垮 worker/塞爆时间线)。
	return io.ReadAll(io.LimitReader(resp.Body, maxDiagResponseBytes))
}

const (
	maxDiagSamples       = 10        // query_metrics 样本摘要上限
	maxDiagResponseBytes = 256 << 10 // 诊断响应读取上限(256KB)
	maxDiagLineBytes     = 500       // 单行/原样输出截断长度
)

// truncateDiag 截断诊断输出的单条内容。
func truncateDiag(s string) string {
	if len(s) <= maxDiagLineBytes {
		return s
	}
	return s[:maxDiagLineBytes] + "…(truncated)"
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

// credentialAware 执行器可接收凭据解析器的能力接口（T6.3）。
type credentialAware interface {
	SetCredentialResolver(r CredentialResolver)
}

// SetCredentialResolver 把凭据解析器注入所有支持的已注册执行器（T6.3）。
// 装配层构造 crypto.Cipher + resolver 后调用一次即可；不支持凭据的执行器自动跳过。
func (r *Registry) SetCredentialResolver(res CredentialResolver) {
	for _, e := range r.executors {
		if ca, ok := e.(credentialAware); ok {
			ca.SetCredentialResolver(res)
		}
	}
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
