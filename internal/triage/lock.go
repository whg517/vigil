package triage

// 聚合建单临界区的串行化锁（ADR-0012 修订 2026-07-14）。
//
// aggregate 的「查活跃 Incident → 建单」是经典 check-then-act 临界区：去重 SETNX 只拦
// 相同 dedup_key，拦不住同 service+severity 的不同指纹告警。Asynq 并发消费下两条这样的
// 告警毫秒级并发到达时，会双双 miss 查询、各建一个 Incident 并各自启动升级链（双倍打扰）。
// 本文件提供事务级 PostgreSQL advisory lock，把同 (service, severity) 的临界区串行化。
//
// 锁经 ent sql/execquery 特性生成的 Tx.ExecContext 以原生 SQL 在事务连接上执行
// （初版因未启用该特性，曾以 HAVING 谓词借道零行 COUNT 聚合查询执行，现已直白化）。

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"
	"sync"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/event"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/predicate"
)

// aggregateLockSQL 取事务级 advisory lock 的语句。已被占用时阻塞等待
// （持有方临界区仅数条语句，极短），锁随事务 commit/rollback 自动释放。
const aggregateLockSQL = "SELECT pg_advisory_xact_lock($1)"

// aggregateLockKey 计算 (service, severity) 聚合临界区的 advisory lock 键（bigint）。
//
// 在 Go 侧用 FNV-64a 哈希出确定性键，而非评审建议的 PG 内置 hashtext(service_id||':'||severity)：
// 跨副本一致性只要求「同输入必得同键」，FNV-64a 对任意 Go 进程/平台稳定，且键构造可以在
// 无 PG 的单测里直接断言确定性与区分度（hashtext 只能在 PG 里算）。
// 带 "vigil:triage:aggregate" 前缀避免与未来其它 advisory lock 用途撞键空间；
// 截断到 63 位（清符号位）：仅损失 1 位随机性，换取 uint64→int64 转换无溢出歧义。
// 哈希碰撞（≈2^-63）无正确性风险——碰撞只会让两个无关键互相串行，不会漏锁。
func aggregateLockKey(serviceID int, severity event.Severity) int64 {
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "vigil:triage:aggregate:%d:%s", serviceID, severity)
	return int64(h.Sum64() & math.MaxInt64)
}

// dialectSniffer 惰性嗅探并缓存 ent client 底层驱动的方言。
//
// 为什么要嗅探：advisory lock 是 PostgreSQL 专属，单测用 sqlite（enttest），必须按方言
// 守卫；而 ent 生成的 Client/Tx 不导出 driver 方言（sql/execquery 只生成 Exec/Query 通道）。
// 借一条零行 COUNT 查询（WHERE id = -1 走主键零行扫描）在 SQL 构建期从 Selector 读取方言，
// 每个 Engine 生命周期只嗅探一次，此后锁路径零额外查询。
type dialectSniffer struct {
	mu  sync.Mutex
	dia string
}

// sniff 返回缓存方言；未缓存时执行一次嗅探查询。
// 不用 sync.Once：查询在 SQL 构建前就失败（连接不可用等）时方言未捕获，Once 会把
// 「未知」永久缓存导致锁路径长期报错；这里失败不缓存，下次调用重试。
func (d *dialectSniffer) sniff(ctx context.Context, c *ent.Client) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dia != "" {
		return d.dia, nil
	}
	var dia string
	_, err := c.Incident.Query().
		Where(predicate.Incident(func(s *entsql.Selector) {
			dia = s.Dialect()
			s.Where(entsql.EQ(s.C(incident.FieldID), -1))
		})).
		Count(ctx)
	// 方言在构建期捕获：只要构建完成，即便执行失败（瞬时抖动）方言也已可信。
	if dia == "" {
		return "", fmt.Errorf("sniff dialect for advisory lock: %w", err)
	}
	d.dia = dia
	return dia, nil
}

// acquireAggregateLock 在既有事务内取 (service, severity) 粒度的 advisory xact lock，
// 把「查活跃单 → 建单」临界区串行化。
//
// 为什么是 PG advisory lock 而非进程内 mutex：Vigil 支持多副本部署，且单副本内 Asynq
// worker 并发消费——竞态天然跨 goroutine、跨进程。进程内 mutex 只能挡住本进程；
// advisory lock 由 PostgreSQL 统一仲裁，天然跨进程/跨副本。选 xact 级（而非 session 级）
// 是因为锁随事务 commit/rollback 自动释放：无需显式 unlock，异常路径（panic/连接断开）
// 也随事务终止释放，无泄漏风险。
//
// 方言守卫：非 PG（单测 sqlite）没有 advisory lock，直接跳过——单测串行执行无并发竞态；
// 真实并发互斥由 PG 集成测试覆盖（race_integration_test.go，//go:build integration）。
func (e *Engine) acquireAggregateLock(ctx context.Context, tx *ent.Tx, serviceID int, severity event.Severity) error {
	dia, err := e.lockDialect.sniff(ctx, e.db)
	if err != nil {
		return fmt.Errorf("acquire aggregate advisory lock: %w", err)
	}
	if dia != dialect.Postgres {
		return nil
	}
	// Tx.ExecContext 在本事务的连接上执行，满足 xact lock 语义。
	if _, err := tx.ExecContext(ctx, aggregateLockSQL, aggregateLockKey(serviceID, severity)); err != nil {
		return fmt.Errorf("acquire aggregate advisory lock: %w", err)
	}
	return nil
}
