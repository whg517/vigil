// Package ticket 实现能力域 14 出向工单集成（T4.3）。
//
// 对应 docs/capabilities/10-integrations-analytics.md §A2「工单系统」与
// docs/capabilities/08-postmortem.md §5「改进项跟踪」：
// 复盘发布时把 ActionItem 推到外部工单系统建改进任务，回写 tracker_url。
//
// 设计取舍（scope 已核实）：
//   - 只做**通用 webhook 工单**（POST 可配 URL，payload 含 ActionItem）；
//     Jira/禅道 等具体 SDK 明确不做，需要时经 webhook 网关对接。
//   - 建单**best-effort**：工单系统不可达/失败不阻断复盘发布，仅记日志（复用出站 webhook 的
//     降级契约）。回写 tracker_url 成功才算建单闭环，失败留待人工/重试。
//   - 凭据经 ent Sensitive 存储、不明文回显（见 ent/schema/ticket_integration.go）。
package ticket

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// ErrTicketBlocked endpoint 未通过 SSRF/URL 校验（建单目标被拦）。
var ErrTicketBlocked = errors.New("ticket endpoint blocked")

// ErrCallbackUnknownStatus 工单侧回调携带的状态无法归一到 ActionItem 三态（N1.3）。
// handler 据此返 400（明确告知回调方状态无效），而非静默忽略（避免掩盖对接口径错误）。
var ErrCallbackUnknownStatus = errors.New("ticket callback status not recognized")

// TicketRequest 建单请求（适配器无关的中立结构）。
// 各适配器把它翻译成目标系统的建单 payload。
type TicketRequest struct {
	// Title 工单标题（取 ActionItem.description 首行/摘要）。
	Title string `json:"title"`
	// Description 工单正文（含 ActionItem 上下文：负责人、截止日期、来源复盘/事件）。
	Description string `json:"description"`
	// OwnerID 负责人标识（工单系统可据字段映射转 assignee）。
	OwnerID string `json:"owner_id,omitempty"`
	// DueDate 截止日期（RFC3339，可空）。
	DueDate string `json:"due_date,omitempty"`
	// ActionItemID / PostmortemID 溯源用（payload 带上，接收端可反查）。
	ActionItemID int `json:"action_item_id"`
	PostmortemID int `json:"postmortem_id,omitempty"`
	IncidentID   int `json:"incident_id,omitempty"`
	// Project 目标项目 key（来自集成 config，webhook 适配器透传）。
	Project string `json:"project,omitempty"`
}

// TicketResult 建单结果。
type TicketResult struct {
	// TrackerURL 外部工单 URL（回写 ActionItem.tracker_url）。空串表示未拿到 URL（视为建单未闭环）。
	TrackerURL string
	// ExternalID 外部工单 id（可选，供日志/审计）。
	ExternalID string
}

// Adapter 工单系统适配器接口。
//
// 实现方把中立的 TicketRequest 翻译成目标系统的建单调用，返回工单 URL。
// 预留 Jira/禅道适配器实现此接口即可接入建单链路，无需改 Engine。
type Adapter interface {
	// Type 适配器对应的工单类型（webhook/jira/zentao）。
	Type() string
	// CreateTicket 在外部系统建单。cfg 为集成配置（endpoint/credential/config）。
	// 失败返回 error（调用方 best-effort 忽略，不阻断复盘发布）。
	CreateTicket(ctx context.Context, cfg AdapterConfig, req TicketRequest) (*TicketResult, error)
}

// AdapterConfig 传给适配器的集成配置（从 ent.TicketIntegration 摘取，凭据明文仅在内存传递）。
type AdapterConfig struct {
	Endpoint   string         // 建单目标 URL
	Credential string         // 凭据明文（从 Sensitive 字段读出，仅进程内传递，不落日志）
	Config     map[string]any // 目标项目/字段映射等
}

// project 从 config 取目标项目 key（webhook 适配器透传给接收端）。
func (a AdapterConfig) project() string {
	if a.Config == nil {
		return ""
	}
	if v, ok := a.Config["project"].(string); ok {
		return v
	}
	return ""
}

// WebhookAdapter 通用 webhook 工单适配器：POST 可配 URL，body 为 TicketRequest JSON。
//
// 接收端（用户自建的工单代理/自研工单）据 payload 建单，并在响应体回传工单 URL：
//
//	{ "tracker_url": "https://tickets.example.com/T-123", "external_id": "T-123" }
//
// 凭据（若配）以 Authorization: Bearer <credential> 头带上（不进 body/日志）。
// 带 SSRF 防护（连接时校验真实 IP，防 rebinding/内网元数据），与 runbook 出站同款思路。
type WebhookAdapter struct {
	client *http.Client
}

// NewWebhookAdapter 创建通用 webhook 工单适配器。
// allowPrivate=true 放行私网（测试/同集群），生产必须 false。
func NewWebhookAdapter(allowPrivate bool) *WebhookAdapter {
	return &WebhookAdapter{client: newTicketHTTPClient(allowPrivate)}
}

// Type 返回 "webhook"。
func (a *WebhookAdapter) Type() string { return "webhook" }

// webhookResp 接收端回传的工单信息（回写 tracker_url 用）。
type webhookResp struct {
	TrackerURL string `json:"tracker_url"`
	ExternalID string `json:"external_id"`
}

// CreateTicket POST TicketRequest 到 endpoint，解析响应拿工单 URL。
func (a *WebhookAdapter) CreateTicket(ctx context.Context, cfg AdapterConfig, req TicketRequest) (*TicketResult, error) {
	if err := validateTicketEndpoint(cfg.Endpoint); err != nil {
		return nil, err
	}
	if p := cfg.project(); p != "" && req.Project == "" {
		req.Project = p
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal ticket request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build ticket request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "Vigil-Ticket/1.0")
	if cfg.Credential != "" {
		// 凭据只进 Authorization 头，不进 body/日志。
		httpReq.Header.Set("Authorization", "Bearer "+cfg.Credential)
	}
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("post ticket: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ticket endpoint returned status %d", resp.StatusCode)
	}
	// 解析响应拿 tracker_url（best-effort：响应无 URL 也不算硬错，但返回空 URL 表示未闭环）。
	var wr webhookResp
	_ = json.NewDecoder(resp.Body).Decode(&wr)
	return &TicketResult{TrackerURL: strings.TrimSpace(wr.TrackerURL), ExternalID: wr.ExternalID}, nil
}

// validateTicketEndpoint 静态校验建单 URL（scheme/host），IP 校验交给 dialer（防 rebinding）。
func validateTicketEndpoint(endpoint string) error {
	if endpoint == "" {
		return fmt.Errorf("%w: empty endpoint", ErrTicketBlocked)
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("%w: parse endpoint: %w", ErrTicketBlocked, err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("%w: scheme %q not allowed", ErrTicketBlocked, scheme)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("%w: empty host", ErrTicketBlocked)
	}
	return nil
}

// newTicketHTTPClient 构造带 SSRF 防护的 http.Client（连接时校验真实 IP，防 rebinding）。
// 与 runbook/ssrf.go 同思路：Control 回调在 TCP 连接建立前校验实际 IP，无 TOCTOU 间隙。
func newTicketHTTPClient(allowPrivate bool) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if !allowPrivate {
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				host = address
			}
			if ip := net.ParseIP(host); ip != nil && isBlockedIP(ip) {
				return fmt.Errorf("%w: dial to private/reserved address %s", ErrTicketBlocked, ip)
			}
			return nil
		}
	}
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{DialContext: dialer.DialContext},
	}
}

// isBlockedIP 判断 IP 是否属于禁止访问的私网/保留地址段（loopback/私网/链路本地含云元数据/未指定）。
func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
}
