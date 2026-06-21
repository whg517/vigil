// Package migrate 实现数据库版本化迁移管理。
//
// 方案：schema_migrations 表追踪已应用版本 + ent auto-migrate 保证 schema 与代码同步。
// 不依赖外部 CLI（atlas），跨环境可靠。
//
// 版本文件存于 migrations/ 目录（.sql），按文件名排序 apply。
// 每个文件应用前检查 schema_migrations 是否已记录，已应用则跳过（幂等）。
package migrate

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"sort"
	"strings"

	"github.com/kevin/vigil/ent"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Run 执行版本化迁移：
// 1. 确保 schema_migrations 表存在
// 2. 按序 apply migrations/*.sql（已应用的跳过）
// 3. ent auto-migrate 补充（保证 ent schema 与 DB 一致）
func Run(ctx context.Context, sqlDB *sql.DB, entDB *ent.Client) error {
	// 1. 确保 schema_migrations 表
	if _, err := sqlDB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version VARCHAR(255) PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`); err != nil {
		return fmt.Errorf("create schema_migrations table: %w", err)
	}

	// 2. 读取已应用版本
	applied, err := getAppliedVersions(ctx, sqlDB)
	if err != nil {
		return fmt.Errorf("get applied versions: %w", err)
	}

	// 3. 读取并排序迁移文件
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		entries = nil // 无迁移文件目录则跳过
	}
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	// 4. 按序 apply 未应用的迁移
	for _, f := range files {
		version := strings.TrimSuffix(f, ".sql")
		if applied[version] {
			continue
		}
		content, err := migrationFS.ReadFile("migrations/" + f)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", f, err)
		}
		if _, err := sqlDB.ExecContext(ctx, string(content)); err != nil {
			return fmt.Errorf("apply migration %s: %w", f, err)
		}
		if _, err := sqlDB.ExecContext(ctx,
			"INSERT INTO schema_migrations (version) VALUES ($1)", version); err != nil {
			return fmt.Errorf("record migration %s: %w", f, err)
		}
	}

	// 5. ent auto-migrate 补充（同步 ent schema 定义到 DB）
	if err := entDB.Schema.Create(ctx); err != nil {
		return fmt.Errorf("ent auto-migrate: %w", err)
	}
	return nil
}

// getAppliedVersions 查询已应用的版本集合。
func getAppliedVersions(ctx context.Context, sqlDB *sql.DB) (map[string]bool, error) {
	rows, err := sqlDB.QueryContext(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	applied := map[string]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, rows.Err()
}
