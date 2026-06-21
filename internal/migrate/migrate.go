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

// preMigratePrefix 迁移文件名前缀：pre_ 开头的文件在 ent auto-migrate 之前执行
// （如安装 pgvector 扩展），其余在之后执行。
const preMigratePrefix = "pre_"

// Run 执行版本化迁移：
// 1. 确保 schema_migrations 表存在
// 2. 执行 pre_ 前缀迁移（ent auto-migrate 前置，如安装扩展）
// 3. ent auto-migrate 创建基础表结构（保证 ent schema 与代码同步）
// 4. 执行其余增量迁移（已应用的跳过；可安全引用 ent 创建的表）
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

	// 4. 分离 pre-migrate 和 post-migrate
	var preFiles, postFiles []string
	for _, f := range files {
		version := strings.TrimSuffix(f, ".sql")
		if applied[version] {
			continue
		}
		if strings.HasPrefix(version, preMigratePrefix) {
			preFiles = append(preFiles, f)
		} else {
			postFiles = append(postFiles, f)
		}
	}

	// 5. 执行 pre-migrate（在 ent auto-migrate 之前）
	if err := applyFiles(ctx, sqlDB, preFiles, applied); err != nil {
		return err
	}

	// 6. ent auto-migrate（创建基础表结构）
	if err := entDB.Schema.Create(ctx); err != nil {
		return fmt.Errorf("ent auto-migrate: %w", err)
	}

	// 7. 执行 post-migrate（在 ent auto-migrate 之后，可安全引用表）
	if err := applyFiles(ctx, sqlDB, postFiles, applied); err != nil {
		return err
	}

	return nil
}

// applyFiles 按序执行迁移文件。
func applyFiles(ctx context.Context, sqlDB *sql.DB, files []string, applied map[string]bool) error {
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
