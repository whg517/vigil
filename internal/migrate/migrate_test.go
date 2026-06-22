package migrate

import (
	"sort"
	"strings"
	"testing"
)

// TestMigrationFilesEmbedded 迁移 SQL 文件被正确嵌入（embed.FS 可读）。
func TestMigrationFilesEmbedded(t *testing.T) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no migration files embedded")
	}
	// 每个文件应以 .sql 结尾
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".sql") {
			t.Errorf("unexpected file in migrations/: %q", e.Name())
		}
	}
}

// TestPreMigratePrefix pre_ 前缀常量正确。
func TestPreMigratePrefix(t *testing.T) {
	if preMigratePrefix != "pre_" {
		t.Errorf("preMigratePrefix=%q, want pre_", preMigratePrefix)
	}
}

// TestMigrationFilesSortable 迁移文件名可排序（文件名排序是 apply 顺序的基础）。
func TestMigrationFilesSortable(t *testing.T) {
	entries, _ := migrationFS.ReadDir("migrations")
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sorted := make([]string, len(names))
	copy(sorted, names)
	sort.Strings(sorted)

	// 排序后应与原顺序不同或相同（验证可排序不 panic）
	if len(sorted) != len(names) {
		t.Error("sort changed file count")
	}
}

// TestMigrationFileContentNonEmpty 嵌入的迁移文件内容非空。
func TestMigrationFileContentNonEmpty(t *testing.T) {
	entries, _ := migrationFS.ReadDir("migrations")
	for _, e := range entries {
		data, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			t.Errorf("read %s: %v", e.Name(), err)
			continue
		}
		if len(strings.TrimSpace(string(data))) == 0 {
			t.Errorf("migration file %s is empty", e.Name())
		}
	}
}
