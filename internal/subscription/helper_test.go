package subscription

import (
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"

	_ "github.com/mattn/go-sqlite3"
)

// enttestOpen 打开一个内存 sqlite ent 客户端（每个测试独立 schema）。
func enttestOpen(t *testing.T) *ent.Client {
	t.Helper()
	return enttest.Open(t, "sqlite3", "file:sub_"+t.Name()+"?mode=memory&cache=shared&_fk=1")
}
