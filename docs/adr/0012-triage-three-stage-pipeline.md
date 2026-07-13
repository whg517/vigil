# ADR-0012: 三层分诊管线 —— 去重 / 抑制 / 聚合

| 字段 | 内容 |
|------|------|
| **状态** | Accepted |
| **日期** | 2026-07-09 |
| **相关** | [`0010-event-incident-separation.md`](./0010-event-incident-separation.md)、[`0013-deterministic-routing.md`](./0013-deterministic-routing.md)、`internal/triage/engine.go` |

## 背景

告警疲劳是 oncall 头号敌人:重复告警、维护窗口内的预期告警、同一故障的关联告警若逐条创建 Incident 会淹没值班人。降噪是 oncall 的核心价值。

## 决策

固定顺序 **去重 → 抑制 → 相关性聚合 → Incident**,实现于 `internal/triage/engine.go`。

- **去重**:Redis `SET vigil:dedup:{dedup_key} event_id EX <窗口>`(SETNX + 过期窗口)。默认 5min,可配 `VIGIL_TRIAGE_DEDUP_WINDOW`。resolved Event 不丢弃,而是关联同 `DedupKey` 的 firing 以触发解决。
- **抑制**(`internal/triage/suppression.go`、`ent/schema/suppression_rule.go`):`SuppressionRule.kind` 枚举 `adhoc`(日常降噪)/ `maintenance`(计划维护窗口,含 `time_window` RFC3339 + `expires_at` 自动软失效)。两类走同一 `matchRule`(label 全等 + time_window + severity_filter),`kind` 只是分类标签,供前端维护窗口专属入口。动作 `suppress`(标 `IsNoise=true` 仅留痕)或 `reduce_severity`。**守卫 `preserve_critical` 默认生效**:critical 不被抑制,避免维护期误杀真故障。
- **聚合**:聚合键默认 `service + severity`(+ 可选 label),默认 5min 窗口。窗口内同键并入活跃 Incident(状态 ∈ {`triggered`, `escalated`, `acked`}),否则按 `Service.auto_create_incident` 创建。实现是普通条件查询(`created_at ≥ now-窗口` 取最新活跃单)而非 SQL 窗口函数;「查活跃单 → 建单」临界区在事务内以 PostgreSQL advisory lock 串行化(见修订记录 2026-07-14)。

## 理由

- 降噪是 oncall 核心价值,直接减少告警疲劳。
- 抑制两类语义不同但匹配逻辑统一,避免代码分叉。
- `expires_at` 软失效让维护窗口用完即失效,无需额外定时清理任务。

## 备选方案

- **单一去重**:不足以覆盖维护窗口与关联聚合,否决。
- **ML 聚类做相关性**:初期过重,后置。
- **维护窗口单独建实体**:会重复一套匹配逻辑,改用 `kind` 分类标签复用 `matchRule`,否决。

## 影响 / 权衡

- 固定管线顺序简单可预测,但相关性仅做规则聚合,复杂关联场景暂靠人工提升,ML 聚类留待后续。
- `preserve_critical` 默认守卫牺牲了"维护期彻底静默"的可能,换取"绝不误杀真故障"的安全底线。

## 修订记录

### 2026-07-14: 聚合建单临界区加 PostgreSQL advisory lock

聚合的「查活跃 Incident → 建单」原实现是无锁 check-then-act,也无唯一约束护栏:去重 SETNX
只拦相同 `dedup_key`,拦不住同 service+severity 的不同指纹告警。Asynq 并发消费(默认 10)下,
两条这样的告警毫秒级并发到达会双双 miss 查询、各建一个 Incident 并各自启动升级链(双倍打扰);
告警风暴恰是并发最高、最需要聚合降噪的时刻,竞态在最坏时机出现。

修复(`internal/triage/engine.go` + `internal/triage/lock.go`):

- 临界区整体放进事务,事务内先取 `(service_id, severity)` 粒度的 `pg_advisory_xact_lock`
  (键为 Go 侧 FNV-64a 确定性哈希,跨副本同输入必得同键),**锁内重查再建**;
  锁随 commit/rollback 自动释放,异常路径无泄漏。
- 选 advisory lock 而非进程内 mutex:竞态跨 Asynq worker 也跨多副本,进程内锁只能挡本进程;
  advisory lock 由 PostgreSQL 统一仲裁,天然跨进程/跨副本。
- 方言守卫:advisory lock 是 PG 专属,单测 sqlite(enttest)按方言跳过加锁走原逻辑;
  真实并发互斥由 PG 集成测试覆盖(`internal/triage/race_integration_test.go`,
  `//go:build integration`)。
- 因 ent 代码生成未启用 `sql/execquery`(client/tx 无原生 Exec 通道),锁函数以 HAVING 谓词
  挂在事务内一条零行 COUNT 聚合查询上执行(无 GROUP BY 的聚合恒返回一行,volatile 函数
  不被计划器折叠,保证恰好执行一次),实现细节与论证见 `internal/triage/lock.go`。
- `incident.number` 撞号换号重试从"事务内循环"改为"整个事务重来"(PG 语句报错后事务已
  aborted,无法原地重试),重试语义不变。
- 同时如实化本 ADR 措辞:聚合实现为普通条件查询而非"PostgreSQL 窗口函数",活跃状态集
  补上 `escalated`(与代码一致)。
