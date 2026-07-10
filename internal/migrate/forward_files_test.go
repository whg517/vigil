package migrate

import (
	"strings"
	"testing"
)

// TestForwardMigrationFiles_ExcludesDownScripts 回归测试：
// 正向迁移文件列表必须包含 up 脚本、且绝不包含任何遗留的 *.down.sql。
//
// 背景：本项目不支持 down 回滚迁移（回滚靠备份恢复 scripts/restore.sh），但历史上
// 曾短暂引入过 pre_0001_pgvector.down.sql —— 前向 Run 的 `HasSuffix(".sql")` 会把该
// down 脚本也当成正向迁移执行（DROP EXTENSION vector），导致 `vigil migrate` 失败。
// 此测试锁定「正向只收 up、防御性排除任何 *.down.sql」的规则，防止回归。
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

	// up 脚本必须在正向列表里
	for _, want := range []string{"pre_0001_pgvector.sql", "0002_baseline.sql", "0003_drop_war_room.sql", "0004_trim_deferred_types.sql"} {
		if !has(want) {
			t.Errorf("forward migration files 缺少 up 脚本 %q：%v", want, files)
		}
	}

	// 任何 *.down.sql（遗留/误放）都绝不能出现在正向列表——这是防御性排除规则。
	for _, f := range files {
		if strings.HasSuffix(f, downSuffix) {
			t.Errorf("正向迁移列表混入了 down 脚本 %q（全部：%v）", f, files)
		}
	}
}
