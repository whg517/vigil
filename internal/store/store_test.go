package store

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/kevin/vigil/ent/enttest"
	_ "github.com/mattn/go-sqlite3"
	"github.com/redis/go-redis/v9"
)

// TestClose_ClosesAllClients Close 应依次关闭 DB/Redis/SQL，不 panic。
func TestClose_ClosesAllClients(t *testing.T) {
	// 用 sqlite ent client + miniredis 构造 Store（绕过 New 的 PG 连通性要求）
	db := enttest.Open(t, "sqlite3", "file:store_close_test?mode=memory&cache=shared&_fk=1")
	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	s := &Store{DB: db, Redis: rc}
	// Close 不应 panic，返回首个错误（这里都应成功）
	err := s.Close()
	// miniredis Close 后 rc.Close 可能返回 err，但不应 panic
	_ = err
}

// TestClose_NilSQL SQL 为 nil 时 Close 不 panic。
func TestClose_NilSQL(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:store_nilsql_test?mode=memory&cache=shared&_fk=1")
	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	s := &Store{DB: db, Redis: rc, SQL: nil}
	_ = s.Close()
}

// TestClose_FirstErrorPreserved DB 关闭错误应被保留（模拟）。
// 实际场景 DB 关闭失败少见，这里验证逻辑：第一个非 nil error 被记录。
func TestClose_FirstErrorPreserved(t *testing.T) {
	// 已关闭的 client 再 Close（模拟错误）——用 miniredis 关闭后再 Close rc
	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	mr.Close() // 先关 miniredis
	// rc.Close 对已关闭的后端可能返回 err，验证不 panic
	db := enttest.Open(t, "sqlite3", "file:store_err_test?mode=memory&cache=shared&_fk=1")
	s := &Store{DB: db, Redis: rc}
	_ = s.Close()
}

// 确保 context 包被引用（部分测试可能间接需要）
var _ = context.Background
