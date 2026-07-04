// export.go 报表 CSV 导出（T6.1，能力域 15 §B4 M15.6）。
//
// 对应 docs/capabilities/10-integrations-analytics.md §B4 导出：「CSV 导出」。
// 各维度报表（alerts/incidents/team-load/postmortems）提供 CSV 下载端点，
// 供二次分析 / 归档 / 对接 BI。权限与团队 scope 完全复用现有 analytics 端点
// （analytics.view + resolveScope 团队软隔离），导出只是「同数据换个呈现格式」。
package analytics

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/kevin/vigil/internal/errs"

	"github.com/labstack/echo/v5"
)

// itoaInt 整数转字符串（CSV 单元格）。
func itoaInt(v int) string { return strconv.Itoa(v) }

// ftoa 浮点转字符串，保留两位小数（CSV 单元格，避免科学计数/超长尾数）。
func ftoa(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }

// writeCSVHeader 设置 CSV 下载响应头（Content-Type + 附件文件名）。
// filename 带时间戳，避免浏览器缓存/覆盖。
func writeCSVHeader(c *echo.Context, name string) {
	filename := fmt.Sprintf("%s_%s.csv", name, time.Now().Format("20060102_150405"))
	c.Response().Header().Set(echo.HeaderContentType, "text/csv; charset=utf-8")
	c.Response().Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	c.Response().WriteHeader(http.StatusOK)
}

// exportAlerts 告警度量 CSV 导出。
//
// @Summary      Export alert metrics as CSV
// @Description  按 start/end 时间范围导出告警度量 CSV（附件下载）。权限与 team scope 同 /analytics/alerts。
// @Tags         analytics
// @Produce      text/csv
// @Param        start  query  string  false  "起始时间 RFC3339"
// @Param        end    query  string  false  "结束时间 RFC3339"
// @Success      200  {string}  string  "CSV 文件"
// @Failure      500  {object}  httputil.ErrorResponse
// @Router       /analytics/alerts/export [get]
// @Security     bearerAuth
func (h *Handler) exportAlerts(c *echo.Context) error {
	ctx := c.Request().Context()
	scope, err := h.resolveScope(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	m, err := h.engine.AlertMetrics(ctx, parseRange(c), scope)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	writeCSVHeader(c, "alerts")
	w := csv.NewWriter(c.Response())
	_ = w.Write([]string{"metric", "value"})
	_ = w.Write([]string{"total", itoaInt(m.Total)})
	_ = w.Write([]string{"notified", itoaInt(m.Notified)})
	_ = w.Write([]string{"unrouted", itoaInt(m.Unrouted)})
	_ = w.Write([]string{"noise_rate", ftoa(m.NoiseRate)})
	w.Flush()
	return w.Error()
}

// exportIncidents 事件度量 CSV 导出。
//
// @Summary      Export incident metrics as CSV
// @Description  按 start/end 时间范围导出事件度量 CSV（含 severity/status 分布）。权限与 team scope 同 /analytics/incidents。
// @Tags         analytics
// @Produce      text/csv
// @Param        start  query  string  false  "起始时间 RFC3339"
// @Param        end    query  string  false  "结束时间 RFC3339"
// @Success      200  {string}  string  "CSV 文件"
// @Failure      500  {object}  httputil.ErrorResponse
// @Router       /analytics/incidents/export [get]
// @Security     bearerAuth
func (h *Handler) exportIncidents(c *echo.Context) error {
	ctx := c.Request().Context()
	scope, err := h.resolveScope(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	m, err := h.engine.IncidentMetrics(ctx, parseRange(c), scope)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	writeCSVHeader(c, "incidents")
	w := csv.NewWriter(c.Response())
	_ = w.Write([]string{"metric", "value"})
	_ = w.Write([]string{"total", itoaInt(m.Total)})
	_ = w.Write([]string{"resolved", itoaInt(m.ResolvedCount)})
	_ = w.Write([]string{"mtta_seconds", ftoa(m.MTTARatio)})
	_ = w.Write([]string{"mttr_seconds", ftoa(m.MTTRatio)})
	// severity/status 分布：按 key 排序保证 CSV 稳定可 diff。
	for _, k := range sortedKeys(m.BySeverity) {
		_ = w.Write([]string{"severity_" + k, itoaInt(m.BySeverity[k])})
	}
	for _, k := range sortedKeys(m.ByStatus) {
		_ = w.Write([]string{"status_" + k, itoaInt(m.ByStatus[k])})
	}
	w.Flush()
	return w.Error()
}

// exportTeamLoad 团队负载 CSV 导出（每团队一行）。
//
// @Summary      Export team load as CSV
// @Description  按 start/end 时间范围导出各团队负载 CSV（每团队一行）。权限与 team scope 同 /analytics/team-load。
// @Tags         analytics
// @Produce      text/csv
// @Param        start  query  string  false  "起始时间 RFC3339"
// @Param        end    query  string  false  "结束时间 RFC3339"
// @Success      200  {string}  string  "CSV 文件"
// @Failure      500  {object}  httputil.ErrorResponse
// @Router       /analytics/team-load/export [get]
// @Security     bearerAuth
func (h *Handler) exportTeamLoad(c *echo.Context) error {
	ctx := c.Request().Context()
	scope, err := h.resolveScope(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	loads, err := h.engine.TeamLoad(ctx, parseRange(c), scope)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	writeCSVHeader(c, "team-load")
	w := csv.NewWriter(c.Response())
	_ = w.Write([]string{"team_id", "team_name", "incidents"})
	for _, l := range loads {
		_ = w.Write([]string{itoaInt(l.TeamID), l.TeamName, itoaInt(l.Incidents)})
	}
	w.Flush()
	return w.Error()
}

// exportPostmortems 复盘度量 CSV 导出。
//
// @Summary      Export postmortem metrics as CSV
// @Description  按 start/end 时间范围导出复盘度量 CSV。权限与 team scope 同 /analytics/postmortems。
// @Tags         analytics
// @Produce      text/csv
// @Param        start  query  string  false  "起始时间 RFC3339"
// @Param        end    query  string  false  "结束时间 RFC3339"
// @Success      200  {string}  string  "CSV 文件"
// @Failure      500  {object}  httputil.ErrorResponse
// @Router       /analytics/postmortems/export [get]
// @Security     bearerAuth
func (h *Handler) exportPostmortems(c *echo.Context) error {
	ctx := c.Request().Context()
	scope, err := h.resolveScope(ctx)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	m, err := h.engine.PostmortemMetrics(ctx, parseRange(c), scope)
	if err != nil {
		return errs.Internal(c, nil, err)
	}
	writeCSVHeader(c, "postmortems")
	w := csv.NewWriter(c.Response())
	_ = w.Write([]string{"metric", "value"})
	_ = w.Write([]string{"total", itoaInt(m.Total)})
	_ = w.Write([]string{"published", itoaInt(m.Published)})
	_ = w.Write([]string{"completion_rate", ftoa(m.CompletionRate)})
	w.Flush()
	return w.Error()
}

func sortedKeys(mp map[string]int) []string {
	keys := make([]string, 0, len(mp))
	for k := range mp {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
