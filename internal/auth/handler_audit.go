// handler_audit.go 审计日志查询 API（能力域 13 §审计日志，PRD M13.5）。
//
// 仅查询（审计日志只追加，不提供修改/删除 API）。
// 权限点 admin.audit.view（已存在）。支持按 操作者/操作类型/对象类型/时间 筛选。
package auth

import (
	"net/http"
	"strconv"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/auditlog"
	"github.com/kevin/vigil/internal/httputil"

	"github.com/labstack/echo/v5"
)

// AuditHandler 审计日志查询 handler。
type AuditHandler struct {
	db *ent.Client
}

// NewAuditHandler 创建 handler。
func NewAuditHandler(db *ent.Client) *AuditHandler {
	return &AuditHandler{db: db}
}

// Register 挂载路由到 v1（权限点 admin.audit.view 由 main 装配时挂中间件）。
func (h *AuditHandler) Register(g *echo.Group) {
	g.GET("/audit-logs", h.list)
}

// list 查询审计日志（支持筛选 + 分页，默认倒序）。
// 查询参数：actor_user_id / action / resource_type / resource_id / limit / offset
//
// @Summary      审计日志查询
// @Tags         audit
// @Produce      json
// @Param        actor_user_id   query    int     false  "按操作者过滤"
// @Param        action          query    string  false  "按操作类型过滤"
// @Param        resource_type   query    string  false  "按对象类型过滤"
// @Param        resource_id     query    int     false  "按对象 ID 过滤"
// @Param        limit           query    int     false  "分页大小（默认 50，上限 200）"  default(50) maximum(200)
// @Param        offset          query    int     false  "分页偏移"                       default(0)
// @Success      200             {object} httputil.Paginated[ent.AuditLog]
// @Failure      500             {object} httputil.ErrorResponse
// @Security     bearerAuth
// @Router       /audit-logs [get]
func (h *AuditHandler) list(c *echo.Context) error {
	ctx := c.Request().Context()
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
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
	}
	return c.JSON(http.StatusOK, httputil.Paginated[*ent.AuditLog]{
		Items: logs, Total: total, Limit: limit, Offset: offset,
	})
}
