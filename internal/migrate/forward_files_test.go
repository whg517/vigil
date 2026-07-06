package migrate

import (
	"strings"
	"testing"
)

// TestForwardMigrationFiles_ExcludesDownScripts 回归测试：
// 前向迁移文件列表必须包含 up 脚本、且绝不包含 .down.sql 逆向脚本。
// 背景：migrate down 特性新增 pre_0001_pgvector.down.sql 后，前向 Run 的
// `HasSuffix(".sql")` 曾把该 down 脚本也当成前向迁移执行（DROP EXTENSION vector），
// 导致 `vigil migrate` 失败。此测试锁定「前向只收 up、排除 down」的规则。
func TestForwardMigrationFiles_ExcludesDownScripts(t *testing.T) {
	files := forwardMigrationFiles()

	has := func(name string) bool {
		for _, f := range files {
			if f == name {
				return true
			}
		}
		return false
	}

	// up 脚本必须在前向列表里
	for _, want := range []string{"pre_0001_pgvector.sql", "0002_baseline.sql"} {
		if !has(want) {
			t.Errorf("forward migration files 缺少 up 脚本 %q：%v", want, files)
		}
	}

	// 任何 .down.sql 逆向脚本都绝不能出现在前向列表
	for _, f := range files {
		if strings.HasSuffix(f, downSuffix) {
			t.Errorf("前向迁移列表混入了 down 脚本 %q（全部：%v）", f, files)
		}
	}
	if has("pre_0001_pgvector.down.sql") {
		t.Errorf("down 脚本泄漏进前向迁移列表：%v", files)
	}
}
