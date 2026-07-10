// status_test.go 覆盖 migrate status（版本状态采集与展示）。
//
// 用 sqlite 内存库承载 schema_migrations 表（status 的版本表操作是通用 SQL），
// 保证测试无需 Postgres 依赖。
package migrate

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// newTestDB 起一个 sqlite 内存库并建好 schema_migrations 表（sqlite 兼容 DDL）。
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// ensureVersionTable 用的是 postgres DDL（TIMESTAMPTZ/NOW），sqlite 不认；
	// 这里用 sqlite 兼容 DDL 预建同名表，之后 ensureVersionTable 的 IF NOT EXISTS 会跳过。
	if _, err := db.Exec(`CREATE TABLE schema_migrations (
		version VARCHAR(255) PRIMARY KEY,
		applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	return db
}

func markApplied(t *testing.T, db *sql.DB, versions ...string) {
	t.Helper()
	for _, v := range versions {
		if _, err := db.Exec("INSERT INTO schema_migrations (version) VALUES (?)", v); err != nil {
			t.Fatalf("insert %s: %v", v, err)
		}
	}
}

// TestStatus_ListsAppliedCurrentPending 验证 status 正确区分已应用/当前/待应用。
func TestStatus_ListsAppliedCurrentPending(t *testing.T) {
	db := newTestDB(t)
	// 只应用 pre_0001_pgvector（apply 序在前），0002_baseline 待应用。
	markApplied(t, db, "pre_0001_pgvector")

	rep, err := Status(context.Background(), db)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	// 已知版本应为 3 个，按 apply 顺序：pre_0001_pgvector, 0002_baseline, 0003_drop_war_room
	if len(rep.Versions) != 3 {
		t.Fatalf("want 3 known versions, got %d: %+v", len(rep.Versions), rep.Versions)
	}
	if rep.Versions[0].Version != "pre_0001_pgvector" || rep.Versions[1].Version != "0002_baseline" {
		t.Errorf("apply order wrong: %s, %s", rep.Versions[0].Version, rep.Versions[1].Version)
	}
	// pre 已应用
	if !rep.Versions[0].Applied {
		t.Errorf("pre_0001 should be applied: %+v", rep.Versions[0])
	}
	if rep.Versions[0].AppliedAt == nil {
		t.Error("applied version should have AppliedAt timestamp")
	}
	// baseline 待应用
	if rep.Versions[1].Applied {
		t.Errorf("0002_baseline should be pending: %+v", rep.Versions[1])
	}
	// 当前版本 = 最后一个已应用 = pre_0001_pgvector
	if rep.Current != "pre_0001_pgvector" {
		t.Errorf("Current = %q, want pre_0001_pgvector", rep.Current)
	}
	// 待应用 = [0002_baseline, 0003_drop_war_room]
	if len(rep.Pending) != 2 || rep.Pending[0] != "0002_baseline" {
		t.Errorf("Pending = %v, want [0002_baseline 0003_drop_war_room]", rep.Pending)
	}
}

// TestStatus_NoneApplied 验证空库（未应用任何版本）时 Current 为空、全部待应用。
func TestStatus_NoneApplied(t *testing.T) {
	db := newTestDB(t)
	rep, err := Status(context.Background(), db)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.Current != "" {
		t.Errorf("Current should be empty, got %q", rep.Current)
	}
	if len(rep.Pending) != 3 {
		t.Errorf("all versions should be pending, got %d", len(rep.Pending))
	}
}

// TestStatus_Orphaned 验证库里有、嵌入目录已无的版本被标为孤儿记录。
func TestStatus_Orphaned(t *testing.T) {
	db := newTestDB(t)
	markApplied(t, db, "9999_ghost_migration")
	rep, err := Status(context.Background(), db)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(rep.Orphaned) != 1 || rep.Orphaned[0] != "9999_ghost_migration" {
		t.Errorf("Orphaned = %v, want [9999_ghost_migration]", rep.Orphaned)
	}
}

// TestStatus_WriteTo 验证文本输出含关键信息 + 备份恢复提示（回滚靠备份，非逆向迁移）。
func TestStatus_WriteTo(t *testing.T) {
	db := newTestDB(t)
	markApplied(t, db, "pre_0001_pgvector")
	rep, err := Status(context.Background(), db)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	var sb strings.Builder
	if err := rep.Render(&sb); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := sb.String()
	for _, want := range []string{"当前版本", "pre_0001_pgvector", "0002_baseline", "备份恢复", "ent 结构变更"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q\n---\n%s", want, out)
		}
	}
}
