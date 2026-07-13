package triage

// 聚合建单临界区的串行化锁（ADR-0012 修订 2026-07-14）。
//
// aggregate 的「查活跃 Incident → 建单」是经典 check-then-act 临界区：去重 SETNX 只拦
// 相同 dedup_key，拦不住同 service+severity 的不同指纹告警。Asynq 并发消费下两条这样的
// 告警毫秒级并发到达时，会双双 miss 查询、各建一个 Incident 并各自启动升级链（双倍打扰）。
// 本文件提供事务级 PostgreSQL advisory lock，把同 (service, severity) 的临界区串行化。

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/event"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/predicate"
)

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

// aggregateLockPredicate 构造把 pg_advisory_xact_lock 挂进查询 HAVING 的谓词。
// 独立成函数以便单测对 PG / sqlite 两种方言直接渲染 SQL 做断言（无需真实 PG）。
//
// 为什么借道 HAVING 而非直接 Exec 原生 SQL：本仓库 ent 代码生成未启用 sql/execquery
// 特性，*ent.Client / *ent.Tx 均不暴露 ExecContext；装配层（wire.go，并行分支冻结中）
// 也无法注入 *sql.DB。因此把锁函数作为 HAVING 谓词挂在一条零行 COUNT 聚合查询上：
//   - 无 GROUP BY 的聚合查询恒产出一行（不受 WHERE 过滤为空影响），对该行求值 HAVING
//     时锁函数必然恰好执行一次；
//   - pg_advisory_xact_lock 是 volatile 函数，计划器不会常量折叠或剪枝；
//   - 查询经 ent 事务驱动执行，锁落在本事务的连接上，满足 xact lock 语义。
//
// 方言守卫：advisory lock 是 PostgreSQL 专属。单测用 sqlite（enttest），从 Selector
// 拿到方言后非 PG 直接不挂 HAVING——锁退化为一次零行 COUNT，聚合走原逻辑。单测串行
// 执行无并发竞态；真实并发互斥由 PG 集成测试覆盖（race_integration_test.go）。
func aggregateLockPredicate(serviceID int, severity event.Severity) predicate.Incident {
	return func(s *entsql.Selector) {
		// 零行守卫：这条查询的目的只是「在本事务连接上执行一次锁函数」，不需要真的数行。
		// WHERE id = -1 让 COUNT 走主键零行扫描，锁查询本身开销可忽略。
		s.Where(entsql.EQ(s.C(incident.FieldID), -1))
		if s.Dialect() != dialect.Postgres {
			return
		}
		s.Having(entsql.P(func(b *entsql.Builder) {
			// CASE 两臂同为 TRUE：唯一目的是强制求值 WHEN 条件里的锁函数，同时让 HAVING
			// 恒真、COUNT 行正常返回（Count 扫描不报 no rows）。锁函数返回 void，IS NULL
			// 仅作合法的布尔包装，其真假无关紧要。
			b.WriteString("CASE WHEN pg_advisory_xact_lock(")
			b.Arg(aggregateLockKey(serviceID, severity))
			b.WriteString(") IS NULL THEN TRUE ELSE TRUE END")
		}))
	}
}

// acquireAggregateLock 在既有事务内取 (service, severity) 粒度的 advisory xact lock，
// 把「查活跃单 → 建单」临界区串行化。已被占用时阻塞等待（持有方临界区仅数条语句，极短）。
//
// 为什么是 PG advisory lock 而非进程内 mutex：Vigil 支持多副本部署，且单副本内 Asynq
// worker 并发消费——竞态天然跨 goroutine、跨进程。进程内 mutex 只能挡住本进程；
// advisory lock 由 PostgreSQL 统一仲裁，天然跨进程/跨副本。选 xact 级（而非 session 级）
// 是因为锁随事务 commit/rollback 自动释放：无需显式 unlock，异常路径（panic/连接断开）
// 也随事务终止释放，无泄漏风险。
func acquireAggregateLock(ctx context.Context, tx *ent.Tx, serviceID int, severity event.Severity) error {
	if _, err := tx.Incident.Query().
		Where(aggregateLockPredicate(serviceID, severity)).
		Count(ctx); err != nil {
		return fmt.Errorf("acquire aggregate advisory lock: %w", err)
	}
	return nil
}
