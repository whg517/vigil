// testreset.go 测试专用重置端点（仅 development 环境挂载，生产禁用）。
//
// 用途：前端 Playwright e2e 每个 spec 前清空业务数据，保证用例间隔离。
// 复用后端 e2e（test/e2e/helpers_test.go）的 allTables 列表，保持单一信源。
//
// 安全：registerTestReset 仅在 !IsProduction() 时调用，生产环境该端点不存在。
// 端点路径 /api/v1/__test__/reset，挂在 public group（不走 RBAC，仅限受信网络）。
package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/kevin/vigil/internal/auth"

	"github.com/labstack/echo/v5"
)

// allTestTables 需 TRUNCATE 的业务表（与 test/e2e/helpers_test.go 的 allTables 一致）。
// schema_migrations 表保留（迁移版本记录不重置）。
var allTestTables = []string{
	"action_items",
	"ai_insights",
	"api_keys",
	"audit_logs",
	"escalation_policies",
	"escalation_policy_schedules",
	"events",
	"im_account_bindings",
	"incident_actions",
	"incident_responders",
	"incidents",
	"integrations",
	"notification_rules",
	"notification_templates",
	"postmortems",
	"raw_events",
	"role_bindings",
	"roles",
	"rotation_participants",
	"rotations",
	"runbooks",
	"schedules",
	"service_runbooks",
	"service_schedules",
	"services",
	"suppression_rules",
	"team_role_bindings",
	"team_users",
	"teams",
	"timeline_items",
	"users",
}

// registerTestReset 注册测试重置端点。仅 development 环境调用（生产不挂载）。
// 挂在 public group：reset 是 e2e 专用、受信网络调用，不走 RBAC。
func (s *Server) registerTestReset() {
	// 路径用 __test__ 前缀，明确标识为测试专用，避免与业务路由混淆。
	s.public.POST("/__test__/reset", func(c *echo.Context) error {
		stmt := "TRUNCATE " + strings.Join(allTestTables, ", ") + " RESTART IDENTITY CASCADE"
		if _, err := s.store.SQL.ExecContext(c.Request().Context(), stmt); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "reset failed: " + err.Error()})
		}
		// 同步清 Redis（dedup key / 聚合器 / asynq 残留任务）。
		if err := s.store.Redis.FlushDB(c.Request().Context()).Err(); err != nil {
			// Redis 清理失败非致命（DB 已清），仅记录。
			return c.JSON(http.StatusOK, map[string]any{"status": "ok", "warning": "redis flush: " + err.Error()})
		}
		// 重建默认管理员：reset 清空了 users 表（含 server.Wire 时 SeedDefaultAdmin 建的 admin），
		// 必须重建，否则后续所有 login(admin/changeme) 会 401。
		// 与后端 e2e（test/e2e/helpers_test.go 的 reseedAdmin）保持一致。
		if _, err := auth.SeedDefaultAdmin(context.Background(), s.store.DB); err != nil {
			return c.JSON(http.StatusOK, map[string]any{"status": "ok", "warning": "reseed admin: " + err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})
}
