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
	"io"
	"sort"
	"strings"

	"github.com/kevin/vigil/ent"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// preMigratePrefix 迁移文件名前缀：pre_ 开头的文件在 ent auto-migrate 之前执行
// （如安装 pgvector 扩展），其余在之后执行。
const preMigratePrefix = "pre_"

// downSuffix down 脚本文件名后缀。约定：版本 <version> 的逆向脚本文件名为
// <version>.down.sql，与 up 脚本（<version>.sql）同目录（migrations/）。
//   - up:   0002_baseline.sql        → version = 0002_baseline
//   - up:   pre_0001_pgvector.sql    → version = pre_0001_pgvector
//   - down: 0002_baseline.down.sql   ← 对应上面的 down（若提供）
//
// 注意：并非所有版本都有 down 脚本。baseline / ent 相关的结构无法安全逆向
// （ent auto-migrate 是声明式 diff，down 需 hand-tuned SQL 或备份恢复），
// 这类版本不提供 down 脚本，`migrate down` 遇到会显式拒绝而非静默跳过。
const downSuffix = ".down.sql"

// destructiveMarker down 脚本内的破坏性标记。脚本首部含此标记（注释行）表示
// 逆向会删除数据/结构（如 DROP EXTENSION / DROP TABLE），`migrate down` 会要求
// 交互确认或 --force。无此标记的 down 脚本视为安全（如 DROP INDEX）。
const destructiveMarker = "-- vigil:destructive"

// Run 执行版本化迁移：
// 1. 确保 schema_migrations 表存在
// 2. 执行 pre_ 前缀迁移（ent auto-migrate 前置，如安装扩展）
// 3. ent auto-migrate 创建基础表结构（保证 ent schema 与代码同步）
// 4. 执行其余增量迁移（已应用的跳过；可安全引用 ent 创建的表）
func Run(ctx context.Context, sqlDB *sql.DB, entDB *ent.Client) error {
	// 1. 确保 schema_migrations 表
	if err := ensureVersionTable(ctx, sqlDB); err != nil {
		return err
	}

	// 2. 读取已应用版本
	applied, err := getAppliedVersions(ctx, sqlDB)
	if err != nil {
		return fmt.Errorf("get applied versions: %w", err)
	}

	// 3. 读取并排序前向迁移文件（排除 .down.sql 逆向脚本）
	files := forwardMigrationFiles()

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

// forwardMigrationFiles 返回 migrations/ 下所有【前向】迁移文件名（升序）。
// 关键：排除 .down.sql 逆向脚本——它们只供 migrate down 使用，绝不能被前向 Run
// 当成独立迁移执行（否则 pre_0001_pgvector.down.sql 的 DROP EXTENSION 会在 up 时误跑，
// 导致 migrate 失败）。抽成独立函数以便回归测试锁定该排除规则。
func forwardMigrationFiles() []string {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return nil // 无迁移目录则视为空
	}
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") && !strings.HasSuffix(e.Name(), downSuffix) {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	return files
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

// ensureVersionTable 确保 schema_migrations 表存在（status/down 与 Run 共用）。
// DDL 按驱动方言选择：生产 postgres 用 TIMESTAMPTZ/NOW()；测试 sqlite 用
// TIMESTAMP/CURRENT_TIMESTAMP（sqlite 不识别 TIMESTAMPTZ/NOW()）。
func ensureVersionTable(ctx context.Context, sqlDB *sql.DB) error {
	tsType, tsDefault := "TIMESTAMPTZ", "NOW()"
	if isSQLite(sqlDB) {
		tsType, tsDefault = "TIMESTAMP", "CURRENT_TIMESTAMP"
	}
	ddl := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version VARCHAR(255) PRIMARY KEY,
		applied_at %s NOT NULL DEFAULT %s
	)`, tsType, tsDefault)
	if _, err := sqlDB.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("create schema_migrations table: %w", err)
	}
	return nil
}

// versionOf 从迁移文件名推导版本号（去掉 .sql 后缀，保留 pre_ 前缀）。
func versionOf(fileName string) string {
	return strings.TrimSuffix(fileName, ".sql")
}

// migrationVersions 返回 migrations/ 目录内所有 up 迁移文件对应的版本号，
// 按【真实 apply 顺序】排列（与 Run 一致）：pre_* 先（组内文件名升序），
// 然后非 pre_*（组内文件名升序）——因为 Run 是「pre → ent auto-migrate → post」。
// 排除 .down.sql（那是逆向脚本，非独立版本）。
//
// down 逆向时按本序倒序执行（后应用的先逆向）。
func migrationVersions() ([]string, error) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return nil, nil // 无目录 → 无版本
	}
	var preFiles, postFiles []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		if strings.HasSuffix(name, downSuffix) {
			continue // 逆向脚本不是独立版本
		}
		if strings.HasPrefix(name, preMigratePrefix) {
			preFiles = append(preFiles, name)
		} else {
			postFiles = append(postFiles, name)
		}
	}
	sort.Strings(preFiles)
	sort.Strings(postFiles)
	files := append(preFiles, postFiles...)
	versions := make([]string, len(files))
	for i, f := range files {
		versions[i] = versionOf(f)
	}
	return versions, nil
}

// downScriptFor 读取版本 version 的 down 脚本内容；不存在返回 (nil, false)。
func downScriptFor(version string) ([]byte, bool) {
	content, err := migrationFS.ReadFile("migrations/" + version + downSuffix)
	if err != nil {
		return nil, false
	}
	return content, true
}

// isDestructive 判断 down 脚本是否带破坏性标记（删数据/结构）。
func isDestructive(script []byte) bool {
	return strings.Contains(string(script), destructiveMarker)
}

// errWriter 包裹 io.Writer，累积首个写错误，避免每次 Fprint 都手动查错（errcheck）。
// 输出目标通常是 os.Stdout（写失败极罕见），用完调 Err() 统一检查即可。
type errWriter struct {
	w   io.Writer
	err error
}

// printf 写格式化文本；已出错则跳过（沿用首个错误）。
func (e *errWriter) printf(format string, args ...any) {
	if e.err != nil {
		return
	}
	_, e.err = fmt.Fprintf(e.w, format, args...)
}

// Err 返回累积的首个写错误。
func (e *errWriter) Err() error { return e.err }

// isSQLite 通过驱动类型名判定是否 sqlite（go-sqlite3 驱动类型含 "SQLite"）。
// 仅用于测试期方言适配；生产运行时始终是 postgres。
func isSQLite(sqlDB *sql.DB) bool {
	return strings.Contains(fmt.Sprintf("%T", sqlDB.Driver()), "SQLite")
}

// placeholder 返回参数占位符。生产用 postgres（$1）；测试用 sqlite（?）。
func placeholder(sqlDB *sql.DB) string {
	if isSQLite(sqlDB) {
		return "?"
	}
	return "$1"
}
