// testreset.go 测试专用重置端点（默认不注册，须 VIGIL_TEST_ENDPOINTS_ENABLED=true 显式开启）。
//
// 用途：前端 Playwright e2e 每个 spec 前清空业务数据，保证用例间隔离。
// 表清单程序化派生自 ent/migrate.Tables（与 test/e2e/helpers_test.go 的 allTables 同方案），
// 新增实体经 go generate 自动纳入，无需人工同步。
//
// 安全（SEC-02 修订）：该端点无鉴权 TRUNCATE 全库，绝不能隐式跟随默认环境开启——
// 注册由 TestEndpoints.EffectiveEnabled 门控（默认 false；生产无条件强制 false）。
// 端点路径 /api/v1/__test__/reset，挂在 public group（不走 RBAC，仅限受信网络）。
package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	entmigrate "github.com/kevin/vigil/ent/migrate"
	"github.com/kevin/vigil/internal/auth"

	"github.com/hibiken/asynq"
	"github.com/labstack/echo/v5"
)

// allTestTables 需 TRUNCATE 的业务表，程序化派生自 ent/migrate.Tables。
//
// 为什么派生而非手工清单：手工列表会随 schema 演进静默漂移——历史上曾漏掉 9 张表
// （credentials/metrics_snapshots/notifications/overrides/subscriptions 等），漏掉的表
// 在 Playwright spec 之间残留数据，是跨用例 flaky 的隐性来源。ent/migrate.Tables
// 由 `go generate ./ent/...` 从 schema 生成（含多对多关联表），新增实体后自动纳入；
// TRUNCATE 带 CASCADE，外键依赖顺序也无需关心。
//
// schema_migrations（迁移版本记录表）不属于 ent schema，天然不在 Tables 中；
// 这里仍显式排除，自文档化「迁移记录不清、reset 不重跑迁移」的意图。
var allTestTables = func() []string {
	names := make([]string, 0, len(entmigrate.Tables))
	for _, t := range entmigrate.Tables {
		if t.Name == "schema_migrations" {
			continue
		}
		names = append(names, t.Name)
	}
	return names
}()

// registerTestResetIfEnabled 按门控注册测试重置端点，返回是否已注册（供装配层打 WARN）。
// 门控语义（SEC-02 修订）：VIGIL_TEST_ENDPOINTS_ENABLED 默认 false；
// 生产环境（APP_ENV=production）即使误配 true 也强制不注册（双保险）。
func (s *Server) registerTestResetIfEnabled() bool {
	if !s.cfg.TestEndpoints.EffectiveEnabled(s.cfg.App.IsProduction()) {
		return false
	}
	s.registerTestReset()
	return true
}

// registerTestReset 注册测试重置端点。仅经 registerTestResetIfEnabled 门控后调用。
// 挂在 public group：reset 是 e2e 专用、受信网络调用，不走 RBAC。
func (s *Server) registerTestReset() {
	// 路径用 __test__ 前缀，明确标识为测试专用，避免与业务路由混淆。
	s.public.POST("/__test__/reset", func(c *echo.Context) error {
		ctx := c.Request().Context()

		insp := asynq.NewInspector(asynq.RedisClientOpt{
			Addr: s.cfg.Redis.Addr, Password: s.cfg.Redis.Password, DB: s.cfg.Redis.DB,
		})
		defer func() { _ = insp.Close() }()

		// 1. 暂停队列：阻止 worker 取新任务，让 in-flight 任务尽快完成。
		for _, q := range []string{"critical", "default"} {
			_ = insp.PauseQueue(q)
		}
		// 短暂等待 in-flight 任务处理完（它们会落库，但随后被 TRUNCATE 清掉）。
		waitForActiveDrain(ctx, insp, 2*time.Second)

		// 2. 清空所有状态的残留任务。
		for _, q := range []string{"critical", "default"} {
			_, _ = insp.DeleteAllPendingTasks(q)
			_, _ = insp.DeleteAllScheduledTasks(q)
			_, _ = insp.DeleteAllRetryTasks(q)
			_, _ = insp.DeleteAllArchivedTasks(q)
			_, _ = insp.DeleteAllCompletedTasks(q)
		}

		// 3. TRUNCATE 所有业务表（在 in-flight 落库之后，保证清空）。
		// #nosec G202 -- 表名来自 ent 生成的 migrate.Tables（编译期固定、非用户输入），
		// 且本端点为 e2e 专用（__test__ 前缀，须 VIGIL_TEST_ENDPOINTS_ENABLED 显式开启，
		// 生产强制不注册）；TRUNCATE 表名无法参数化。
		stmt := "TRUNCATE " + strings.Join(allTestTables, ", ") + " RESTART IDENTITY CASCADE"
		if _, err := s.store.SQL.ExecContext(ctx, stmt); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "reset failed: " + err.Error()})
		}

		// 4. 清 Redis（dedup key / 聚合器 / asynq 状态）。
		if err := s.store.Redis.FlushDB(ctx).Err(); err != nil {
			return c.JSON(http.StatusOK, map[string]any{"status": "ok", "warning": "redis flush: " + err.Error()})
		}

		// 5. 恢复队列（PauseQueue 状态存在 Redis，FlushDB 已清，但显式 Unpause 保险）。
		for _, q := range []string{"critical", "default"} {
			_ = insp.UnpauseQueue(q)
		}

		// 6. 重建角色 + 默认管理员 + admin 绑定。
		// FIX-G：reset 清表含 roles + role_bindings（users 也清），原仅调 SeedDefaultAdmin
		// 会因 org_admin 角色不存在导致绑定失败 → reset 后系统无角色/admin 无权限。
		// 故按 wire.go 启动顺序补回：roles → admin → 确保绑定（admin 已存在也补）。
		if err := auth.SeedBuiltinRoles(ctx, s.store.DB); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "re-seed roles: " + err.Error()})
		}
		if _, err := auth.SeedDefaultAdmin(ctx, s.store.DB); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "re-seed admin: " + err.Error()})
		}
		// SeedDefaultAdmin 对已存在 admin 不补绑定（created=false 时跳过 bindOrgAdmin），
		// 显式补回 org_admin 绑定，保证 reset 后 admin 仍有全部权限。
		if err := auth.EnsureAdminOrgAdminBinding(ctx, s.store.DB); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "ensure admin binding: " + err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})
}

// waitForActiveDrain 轮询直到所有队列无 active 任务（in-flight 完成）或超时。
// 用途：reset 前确保 worker 处理完正在执行的任务，避免它们在 TRUNCATE 后落库。
func waitForActiveDrain(ctx context.Context, insp *asynq.Inspector, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		drained := true
		for _, q := range []string{"critical", "default"} {
			active, err := insp.ListActiveTasks(q)
			if err != nil || len(active) > 0 {
				drained = false
				break
			}
		}
		if drained {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
}
