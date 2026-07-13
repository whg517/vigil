package triage

import (
	"slices"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	"github.com/kevin/vigil/ent/event"
)

// TestAggregateLockKey 验证锁键的确定性与区分度：
// 同 (service, severity) 必得同键（跨副本一致的前提）；不同 service 或 severity 键不同。
func TestAggregateLockKey(t *testing.T) {
	k1 := aggregateLockKey(42, event.SeverityCritical)
	k2 := aggregateLockKey(42, event.SeverityCritical)
	if k1 != k2 {
		t.Fatalf("同输入应得同键: %d != %d", k1, k2)
	}
	if k1 < 0 {
		t.Errorf("键应已清符号位（非负）: %d", k1)
	}
	if k3 := aggregateLockKey(43, event.SeverityCritical); k3 == k1 {
		t.Errorf("不同 service 应得不同键: 都是 %d", k1)
	}
	if k4 := aggregateLockKey(42, event.SeverityWarning); k4 == k1 {
		t.Errorf("不同 severity 应得不同键: 都是 %d", k1)
	}
}

// renderLockQuery 用指定方言渲染锁谓词挂上后的 COUNT 查询（不连库，纯 SQL 生成）。
func renderLockQuery(t *testing.T, dia string, serviceID int, sev event.Severity) (string, []any) {
	t.Helper()
	s := entsql.Dialect(dia).
		Select(entsql.Count("id")).
		From(entsql.Table("incidents"))
	aggregateLockPredicate(serviceID, sev)(s)
	return s.Query()
}

// TestAggregateLockPredicate_Postgres 验证 PG 方言下锁函数确实进入 SQL 且键参数正确——
// 这是 sqlite 单测环境能覆盖到的「锁函数被调用路径」：真实互斥语义由 PG 集成测试
// （race_integration_test.go，//go:build integration）验证。
func TestAggregateLockPredicate_Postgres(t *testing.T) {
	query, args := renderLockQuery(t, dialect.Postgres, 42, event.SeverityCritical)
	if !strings.Contains(query, "pg_advisory_xact_lock") {
		t.Fatalf("PG 方言应在 HAVING 中调用 pg_advisory_xact_lock, got: %s", query)
	}
	if !strings.Contains(query, "HAVING") {
		t.Fatalf("锁函数应挂在 HAVING 上（聚合查询恒返回一行,保证恰好求值一次), got: %s", query)
	}
	// 键作为绑定参数传入（非拼接），且与 aggregateLockKey 一致
	want := aggregateLockKey(42, event.SeverityCritical)
	if !slices.Contains(args, any(want)) {
		t.Errorf("锁键 %d 应在查询参数中, got args: %v", want, args)
	}
}

// TestAggregateLockPredicate_NonPostgres 验证方言守卫：非 PG（单测 sqlite）不注入锁函数，
// 查询退化为一次零行 COUNT（WHERE id = -1），聚合走原逻辑。
func TestAggregateLockPredicate_NonPostgres(t *testing.T) {
	query, _ := renderLockQuery(t, dialect.SQLite, 42, event.SeverityCritical)
	if strings.Contains(query, "pg_advisory_xact_lock") {
		t.Fatalf("sqlite 方言不应出现 PG 专属锁函数, got: %s", query)
	}
	if strings.Contains(query, "HAVING") {
		t.Fatalf("sqlite 方言不应注入 HAVING, got: %s", query)
	}
	if !strings.Contains(query, "WHERE") {
		t.Errorf("零行守卫（WHERE id = -1）应保留, got: %s", query)
	}
}
