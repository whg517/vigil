// handler_audit.go 审计日志查询 API（能力域 13 §审计日志，PRD M13.5）。
//
// 仅查询（审计日志只追加，不提供修改/删除 API）。
// 权限点 admin.audit.view（已存在）。支持按 操作者/操作类型/对象类型/时间 筛选。
package auth

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/auditlog"
	"github.com/kevin/vigil/internal/errs"
	"github.com/kevin/vigil/internal/httputil"

	"github.com/labstack/echo/v5"
	"go.uber.org/zap"
)

// auditExportMaxRows 导出上限保护：单次最多导出的行数。
// 审计只追加、长期累积，无分页导出若不设上限可能拉出百万级数据拖垮内存/连接。
// 达上限不静默截断——记 warn 日志 + 响应头 X-Vigil-Truncated 标注（见 exportAuditLogs）。
const auditExportMaxRows = 50000

// AuditHandler 审计日志查询 handler。
type AuditHandler struct {
	db  *ent.Client
	log *zap.Logger
}

// NewAuditHandler 创建 handler。
func NewAuditHandler(db *ent.Client) *AuditHandler {
	return &AuditHandler{db: db, log: zap.NewNop()}
}

// SetLogger 注入 logger（导出达上限时记 warn，默认 Nop 不影响现有调用方）。
func (h *AuditHandler) SetLogger(log *zap.Logger) {
	if log != nil {
		h.log = log
	}
}

// Register 挂载路由到 v1（权限点 admin.audit.view 由 main 装配时挂中间件）。
func (h *AuditHandler) Register(g *echo.Group) {
	g.GET("/audit-logs", h.list)
	g.GET("/audit-logs/export", h.exportAuditLogs)
}

// buildFilteredQuery 依据请求的筛选参数构造审计查询（created_at 倒序）。
// list 与 export 共用同一套筛选语义（actor_user_id/action/resource_type/resource_id/from/to），
// 避免两处重复维护导致行为漂移。解析失败的参数一律宽松忽略（不阻塞查询），与既有 list 风格一致。
func (h *AuditHandler) buildFilteredQuery(c *echo.Context) *ent.AuditLogQuery {
	q := h.db.AuditLog.Query().Order(ent.Desc(auditlog.FieldCreatedAt))

	if v := c.QueryParam("actor_user_id"); v != "" {
		if id, err := strconv.Atoi(v); err == nil {
			q = q.Where(auditlog.ActorUserIDEQ(id))
		}
	}
	if v := c.QueryParam("action"); v != "" {
		q = q.Where(auditlog.ActionEQ(v))
	}
	if v := c.QueryParam("resource_type"); v != "" {
		q = q.Where(auditlog.ResourceTypeEQ(v))
	}
	if v := c.QueryParam("resource_id"); v != "" {
		if id, err := strconv.Atoi(v); err == nil {
			q = q.Where(auditlog.ResourceIDEQ(id))
		}
	}
	// 时间区间筛选（C21）：合规追溯常按"某时段内的敏感操作"检索。
	// 解析失败（格式非法）静默忽略该边界，不阻塞查询（与上面其它参数一致的宽松风格）。
	if t, ok := parseAuditTime(c.QueryParam("from")); ok {
		q = q.Where(auditlog.CreatedAtGTE(t))
	}
	if t, ok := parseAuditTime(c.QueryParam("to")); ok {
		q = q.Where(auditlog.CreatedAtLTE(t))
	}
	return q
}

// list 查询审计日志（支持筛选 + 分页，默认倒序）。
// 查询参数：actor_user_id / action / resource_type / resource_id / from / to / limit / offset
//
// @Summary      审计日志查询
// @Tags         audit
// @Produce      json
// @Param        actor_user_id   query    int     false  "按操作者过滤"
// @Param        action          query    string  false  "按操作类型过滤"
// @Param        resource_type   query    string  false  "按对象类型过滤"
// @Param        resource_id     query    int     false  "按对象 ID 过滤"
// @Param        from            query    string  false  "起始时间（含），RFC3339 或 unix 秒"
// @Param        to              query    string  false  "结束时间（含），RFC3339 或 unix 秒"
// @Param        limit           query    int     false  "分页大小（默认 50，上限 200）"  default(50) maximum(200)
// @Param        offset          query    int     false  "分页偏移"                       default(0)
// @Success      200             {object} httputil.Paginated[ent.AuditLog]
// @Failure      500             {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /audit-logs [get]
func (h *AuditHandler) list(c *echo.Context) error {
	ctx := c.Request().Context()
	q := h.buildFilteredQuery(c)

	limit := 50
	if v := c.QueryParam("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	offset := 0
	if v := c.QueryParam("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}

	// 先 count（在加 limit/offset 之前，count 的是全量匹配数）
	total, err := q.Clone().Count(ctx)
	if err != nil {
		total = -1 // count 失败不阻塞，前端按 -1 处理
	}

	logs, err := q.Limit(limit).Offset(offset).All(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	return c.JSON(http.StatusOK, httputil.Paginated[*ent.AuditLog]{
		Items: logs, Total: total, Limit: limit, Offset: offset,
	})
}

// exportAuditLogs 导出审计日志为 CSV（附件下载，不分页，含上限保护）。
// 筛选参数与 list 完全一致（复用 buildFilteredQuery），按 created_at 倒序。
//
// 上限保护（S 合规 vs 可用性权衡）：审计只追加长期累积，无分页导出若不设上限可能拉出
// 百万级数据拖垮内存/连接。故最多导出 auditExportMaxRows 行；达上限不静默截断——
// 记 warn 日志 + 置响应头 X-Vigil-Truncated: true，调用方可缩小时间窗后重导。
//
// 列顺序（固定）：created_at(RFC3339) / actor_user_id / actor_name / action /
// resource_type / resource_id / resource_name / result / ip / user_agent / detail(JSON 压平)。
//
// @Summary      审计日志 CSV 导出
// @Description  按 list 同一套筛选参数导出审计日志 CSV（附件下载，不分页，最多 50000 行）。达上限置响应头 X-Vigil-Truncated: true。权限同 list（admin.audit.view，org 级）。
// @Tags         audit
// @Produce      text/csv
// @Param        actor_user_id   query    int     false  "按操作者过滤"
// @Param        action          query    string  false  "按操作类型过滤"
// @Param        resource_type   query    string  false  "按对象类型过滤"
// @Param        resource_id     query    int     false  "按对象 ID 过滤"
// @Param        from            query    string  false  "起始时间（含），RFC3339 或 unix 秒"
// @Param        to              query    string  false  "结束时间（含），RFC3339 或 unix 秒"
// @Success      200             {string} string  "CSV 文件"
// @Failure      500             {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /audit-logs/export [get]
func (h *AuditHandler) exportAuditLogs(c *echo.Context) error {
	ctx := c.Request().Context()
	// 上限 +1 拉取：多取一条即可判定是否触达上限（是否被截断），无需额外 count。
	logs, err := h.buildFilteredQuery(c).Limit(auditExportMaxRows + 1).All(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	truncated := len(logs) > auditExportMaxRows
	if truncated {
		logs = logs[:auditExportMaxRows]
		// 不静默截断：日志明确 + 响应头标注，运维/合规方可感知并缩小时间窗重导。
		h.log.Warn("audit export truncated at row limit",
			zap.Int("limit", auditExportMaxRows),
			zap.String("hint", "narrow from/to window and re-export"))
		c.Response().Header().Set("X-Vigil-Truncated", "true")
	}

	filename := fmt.Sprintf("audit-logs_%s.csv", time.Now().Format("20060102_150405"))
	c.Response().Header().Set(echo.HeaderContentType, "text/csv; charset=utf-8")
	c.Response().Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	c.Response().WriteHeader(http.StatusOK)

	w := csv.NewWriter(c.Response())
	_ = w.Write([]string{
		"created_at", "actor_user_id", "actor_name", "action",
		"resource_type", "resource_id", "resource_name", "result",
		"ip", "user_agent", "detail",
	})
	for _, l := range logs {
		_ = w.Write([]string{
			l.CreatedAt.Format(time.RFC3339),
			strconv.Itoa(l.ActorUserID),
			l.ActorName,
			l.Action,
			l.ResourceType,
			strconv.Itoa(l.ResourceID),
			l.ResourceName,
			string(l.Result),
			l.IP,
			l.UserAgent,
			flattenAuditDetail(l.Detail),
		})
	}
	w.Flush()
	return w.Error()
}

// flattenAuditDetail 把 detail(JSON) 压平成单列字符串（CSV 单元格）。
// nil/空 detail → 空串；正常序列化为紧凑 JSON（csv.Writer 自动处理引号转义）。
func flattenAuditDetail(detail map[string]any) string {
	if len(detail) == 0 {
		return ""
	}
	b, err := json.Marshal(detail)
	if err != nil {
		return ""
	}
	return string(b)
}

// parseAuditTime 解析时间边界，兼容 RFC3339（2006-01-02T15:04:05Z07:00）与 unix 秒。
// 返回 ok=false 表示空或格式非法（调用方据此跳过该边界）。
func parseAuditTime(v string) (time.Time, bool) {
	if v == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, true
	}
	if sec, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.Unix(sec, 0), true
	}
	return time.Time{}, false
}
