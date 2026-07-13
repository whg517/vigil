// config_template.go 集成配置模板/指引辅助端点（T6.2/M14.6 集成向导后端辅助）。
//
// 背景：M14.6「集成目录：内置常见集成的配置向导」是前端大件（分步向导 UI）。本端点为向导
// 提供**后端辅助数据**：给定接入点类型，返回该类型的配置字段说明、示例值、上游（Prometheus/
// Grafana/Zabbix 等）如何指向 Vigil 的接线指引。完整分步向导 UI 是设计目标（前端待做），
// 后端先把「配置模板/建议」这块结构化数据备齐，让向导不必在前端硬编码各源的配置知识。
//
// 纯只读、无副作用、不碰 DB——因此不挂资源级鉴权（任何登录用户可查配置指引，与查看文档等价）。
package integration

import (
	"net/http"

	"github.com/kevin/vigil/internal/httputil"

	"github.com/labstack/echo/v5"
)

// configField 单个配置字段说明（供向导渲染表单/校验）。
type configField struct {
	Key      string `json:"key"`               // config map 里的键
	Label    string `json:"label"`             // 展示名
	Required bool   `json:"required"`          // 是否必填
	Example  string `json:"example,omitempty"` // 示例值
	Help     string `json:"help,omitempty"`    // 填写说明
}

// configTemplate 某接入点类型的配置模板 + 接线指引。
type configTemplate struct {
	Type        string        `json:"type"`         // 接入点类型
	DisplayName string        `json:"display_name"` // 中文展示名
	Description string        `json:"description"`  // 该类型用途简述
	Fields      []configField `json:"fields"`       // config 字段清单
	// SetupHint 上游如何指向 Vigil 的接线指引（webhook URL 模板、告警转发配置片段等）。
	SetupHint string `json:"setup_hint"`
}

// configTemplates 内置各类型的配置模板。
// key 对应 ent Integration.type 枚举（webhook|email|prometheus|grafana|api）。
//
// 说明性数据，随支持的接入源演进补充；这里覆盖当前 schema 枚举全集，避免向导拿到未知类型。
var configTemplates = map[string]configTemplate{
	"prometheus": {
		Type:        "prometheus",
		DisplayName: "Prometheus Alertmanager",
		Description: "接收 Alertmanager 的 webhook 告警推送，按 labels 路由到服务。",
		Fields: []configField{
			{Key: "rate_limit", Label: "每分钟限流", Required: false, Example: "600", Help: "单接入点每分钟最大请求数，0=不限流。"},
			{Key: "severity_map", Label: "严重度映射覆盖", Required: false, Example: `{"disaster":"critical","average":"warning"}`, Help: "原始严重度 → critical/warning/info 的覆盖表（JSON 对象，键不区分大小写）；未命中回落内置默认映射。"},
		},
		SetupHint: "在 Alertmanager 的 receivers 中配置 webhook_config：url 指向 " +
			"`https://<vigil-host>/api/v1/webhook/<token>`（token 见创建接入点时返回）。" +
			"Vigil 按告警 labels（env/service/tier 等）匹配 Service.labels 完成路由。",
	},
	"grafana": {
		Type:        "grafana",
		DisplayName: "Grafana Alerting",
		Description: "接收 Grafana 统一告警（Unified Alerting）的 contact point webhook。",
		Fields: []configField{
			{Key: "rate_limit", Label: "每分钟限流", Required: false, Example: "600", Help: "单接入点每分钟最大请求数，0=不限流。"},
			{Key: "severity_map", Label: "严重度映射覆盖", Required: false, Example: `{"disaster":"critical","average":"warning"}`, Help: "原始严重度 → critical/warning/info 的覆盖表（JSON 对象，键不区分大小写）；未命中回落内置默认映射。"},
		},
		SetupHint: "在 Grafana → Alerting → Contact points 新建 Webhook 类型，URL 指向 " +
			"`https://<vigil-host>/api/v1/webhook/<token>`，方法 POST。",
	},
	"webhook": {
		Type:        "webhook",
		DisplayName: "通用 Webhook / JSON",
		Description: "任意系统按通用 JSON 格式 POST 告警，适配器做通用归一化。",
		Fields: []configField{
			{Key: "rate_limit", Label: "每分钟限流", Required: false, Example: "600", Help: "单接入点每分钟最大请求数，0=不限流。"},
			{Key: "severity_map", Label: "严重度映射覆盖", Required: false, Example: `{"disaster":"critical","average":"warning"}`, Help: "原始严重度 → critical/warning/info 的覆盖表（JSON 对象，键不区分大小写）；未命中回落内置默认映射。"},
		},
		SetupHint: "POST JSON 到 `https://<vigil-host>/api/v1/webhook/<token>`；" +
			"或用开放 API `POST /api/v1/events`（带 X-Vigil-Key + integration_id）。" +
			"payload 顶层含 source_event_id/severity/summary/labels 即可被通用适配器识别。",
	},
	"api": {
		Type:        "api",
		DisplayName: "开放 API 投递",
		Description: "外部系统凭 API Key 程序化投递 Event（POST /api/v1/events）。",
		Fields: []configField{
			{Key: "rate_limit", Label: "每分钟限流", Required: false, Example: "600", Help: "单接入点每分钟最大请求数，0=不限流。"},
		},
		SetupHint: "`POST /api/v1/events`，头带 `X-Vigil-Key: <api-key>`，query 或 body 带 " +
			"`integration_id=<本接入点 id>`；body 为通用 JSON 告警。返回 202 + raw_event_id。",
	},
	"email": {
		Type:        "email",
		DisplayName: "邮件接入",
		Description: "通过邮件接收告警（设计目标：邮件解析适配器待实现）。",
		Fields:      []configField{},
		SetupHint:   "邮件接入适配器为设计目标，当前请优先使用 webhook/prometheus 等类型。",
	},
}

// configTemplate 端点：返回给定类型的配置模板/接线指引（M14.6 向导后端辅助）。
//
// GET /integrations/config-template?type=prometheus
// 不带 type 或 type=all 返回全部类型的模板（供向导首屏列出可选源）。
//
// @Summary      集成配置模板/接线指引
// @Tags         integration
// @Produce      json
// @Param        type  query    string  false  "接入点类型（webhook|email|prometheus|grafana|api），空/all=全部"
// @Success      200  {object} configTemplate
// @Failure      404  {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /integrations/config-template [get]
func (h *Handler) configTemplate(c *echo.Context) error {
	t := c.QueryParam("type")
	if t == "" || t == "all" {
		// 返回全部模板（稳定顺序：按 schema 枚举顺序，便于向导展示与 diff）。
		order := []string{"prometheus", "grafana", "webhook", "api", "email"}
		out := make([]configTemplate, 0, len(order))
		for _, k := range order {
			if tpl, ok := configTemplates[k]; ok {
				out = append(out, tpl)
			}
		}
		return c.JSON(http.StatusOK, map[string]any{"templates": out})
	}
	tpl, ok := configTemplates[t]
	if !ok {
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "unknown integration type: " + t})
	}
	return c.JSON(http.StatusOK, tpl)
}
