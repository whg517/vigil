package triage

import (
	"context"
	"strings"
	"testing"

	"entgo.io/ent/dialect"

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

// TestAggregateLockSQL 钉住锁语句形状：必须是「事务级」advisory lock（xact 变体，
// 随 commit/rollback 自动释放）且键走绑定参数。防止误改成 session 级 pg_advisory_lock
// （需显式 unlock，异常路径泄漏）或把键拼进 SQL。
func TestAggregateLockSQL(t *testing.T) {
	if !strings.Contains(aggregateLockSQL, "pg_advisory_xact_lock") {
		t.Fatalf("锁必须是事务级 advisory lock, got: %s", aggregateLockSQL)
	}
	if !strings.Contains(aggregateLockSQL, "$1") {
		t.Fatalf("锁键应作为绑定参数传入（非拼接）, got: %s", aggregateLockSQL)
	}
}

// TestDialectSniffer 验证方言嗅探：sqlite（enttest）应嗅出 sqlite3 方言，且结果被缓存
// ——第二次调用不再触达数据库（传 nil client 仍成功即为证明）。
func TestDialectSniffer(t *testing.T) {
	c := newTestClient(t)
	var d dialectSniffer
	dia, err := d.sniff(context.Background(), c)
	if err != nil {
		t.Fatalf("sniff: %v", err)
	}
	if dia != dialect.SQLite {
		t.Fatalf("enttest 环境应嗅出 sqlite 方言, got %q", dia)
	}
	// 缓存命中路径不应触达 client：nil client 若被使用会 panic，测试即失败。
	dia2, err := d.sniff(context.Background(), nil)
	if err != nil {
		t.Fatalf("cached sniff: %v", err)
	}
	if dia2 != dia {
		t.Errorf("缓存结果应与首次一致: %q != %q", dia2, dia)
	}
}

// TestAcquireAggregateLock_NonPostgresSkips 验证方言守卫：非 PG（单测 sqlite）直接跳过
// 加锁。若守卫被移除，sqlite 会因不存在 pg_advisory_xact_lock 函数而报错，本测试即失败。
// 真实互斥语义由 PG 集成测试验证（race_integration_test.go，//go:build integration）。
func TestAcquireAggregateLock_NonPostgresSkips(t *testing.T) {
	c := newTestClient(t)
	eng := NewEngine(c, nil)
	tx, err := c.Tx(context.Background())
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := eng.acquireAggregateLock(context.Background(), tx, 42, event.SeverityCritical); err != nil {
		t.Fatalf("sqlite 方言下应跳过加锁且不报错, got: %v", err)
	}
}
