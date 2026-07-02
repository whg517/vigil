// schema_consistency_test.go schema 自洽性检测（ENG-01）。
//
// CI 在 lint 后、测试前运行此用例，验证 ent schema 能无错生成到 sqlite 内存库。
// 若 schema 定义有矛盾（非法类型/循环外键/缺字段），enttest.Open 会失败，
// 从而在 CI 早期拦截（而非在生产 auto-migrate 时才报错）。
package migrate

import (
	"testing"

	"github.com/kevin/vigil/ent/enttest"

	_ "github.com/mattn/go-sqlite3"
)

// TestSchemaConsistency 验证 ent schema 可自洽生成（CI drift 检测配套）。
// enttest.Open 会运行 auto-migrate 建全部表；任何 schema 定义错误都会在此暴露。
func TestSchemaConsistency(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:consistency_check?mode=memory&cache=shared&_fk=1")
	defer func() { _ = c.Close() }()
	// 若 enttest.Open 成功（无 panic/error），说明 schema 自洽。
	// 不需要额外断言——成功构建即通过。
}
