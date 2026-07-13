# ADR-0039: 数据生命周期:保留清理、归档取舍与分区路线

| 字段 | 内容 |
|------|------|
| **状态** | Proposed(§已实现 部分为补记既成事实;§待实现 为已决策未实现) |
| **日期** | 2026-07-14 |
| **相关** | [`0010-event-incident-separation.md`](./0010-event-incident-separation.md)、[`0017-notification-fallback-chain.md`](./0017-notification-fallback-chain.md)、[`0029-dual-audit-no-silent-truncation.md`](./0029-dual-audit-no-silent-truncation.md)、`internal/event/retention.go`、`internal/config/config.go`(Retention) |

## 背景

Vigil 的接入吞吐目标 ≥ 1000 events/min(见 architecture.md 性能目标)。按此速率,Event 表每月新增约 4300 万行;Event/Incident 分离设计(ADR-0010)决定了 Event 是海量只追加数据,没有生命周期策略就是无限吃存储 + 大表拖垮查询。

现状分两块:

1. **已实现但无决策记录**:`internal/event/retention.go` 已实现 Event/RawEvent 的保留清理巡检(实现质量良好,含证据保护与分页删除),但当初未落 ADR——属"先有代码后补决策"的债,本 ADR 补记。
2. **承诺过但从未落地**:ADR-0017 自己写明"`Notification` 表随发送次数增长,需归档/清理策略",至今未实现。除 Notification 外,TimelineItem、AuditLog、IncidentAction、WebhookDelivery 均只追加、无任何清理,属无界增长表。

## 决策

### 已实现(补记,状态等同 Accepted)

Event/RawEvent 保留清理巡检(`internal/event/retention.go`,配置 `internal/config/config.go` 的 `Retention`):

- **Event 默认保留 90 天**(`VIGIL_RETENTION_EVENT_DAYS`,约一个季度,覆盖多数复盘/审计回溯);**RawEvent 默认 30 天**(`VIGIL_RETENTION_RAW_EVENT_DAYS`,原始 payload 体积大、价值随时间衰减快)。任一配 `<=0` 即对应清理不启用(永不删,向后兼容)。
- **活跃 Incident 证据保护**(★ 安全核心):只删「无关联 Incident,或关联 Incident 已 closed」的 Event;任何未 closed 态(triggered/escalated/acked/resolved)引用的 Event 即使超期也保留——它们是处置与复盘的证据。
- **RawEvent 只清终态**:`requeued`(待回灌、尚未成功归一化)不删,删了会丢告警。
- **批量分页删除**:每批默认 500(`VIGIL_RETENTION_BATCH_SIZE`),避免一次删百万行的大事务锁表;巡检间隔默认 6h(`VIGIL_RETENTION_INTERVAL`),ticker 与 Asynq 低优先级任务(`vigil:event_cleanup`)双触发、同一 Sweep、幂等。

另一处相关既成事实:MetricsSnapshot **没有** TTL 清理——`internal/analytics/aggregator.go` 里的删除仅是同 (team, period, period_start) 窗口的幂等覆盖(防重复行),hourly/daily 快照随时间累积。其增速为「团队数 × 窗口数」量级,远低于 Event,暂不构成压力,TTL 见下节。

### 待实现(本 ADR 新决策)

**1. 各无界表的保留默认值与清理 vs 归档取舍**:

| 实体 | 建议默认保留 | 方式 | 理由 |
|------|------|------|------|
| Notification | 90 天 | 直接删 | 发送记录属运维数据,与 Event 对齐;三态统计已聚合进 MetricsSnapshot,明细过期即可删(兑现 ADR-0017 的欠账) |
| WebhookDelivery | 30 天 | 直接删 | 投递流水,排障价值随时间衰减快,与 RawEvent 对齐 |
| MetricsSnapshot(hourly) | 90 天 | 直接删 | 细粒度趋势回看窗口;daily 快照**不设默认清理**(体积极小,长期报表趋势价值高) |
| AuditLog | 365 天 | **先归档后删** | 管理操作审计(ADR-0029),合规属性强;删除前须先导出留档(复用 ADR-0029 的 CSV 全量导出,不静默截断),归档产物由部署方转存对象存储/冷备 |
| IncidentAction | 365 天 | **先归档后删**,且仅 closed Incident | 事件操作审计(ADR-0029 双轨之一),同样合规属性;额外受证据保护约束 |
| TimelineItem | 不设独立 TTL | 跟随 Incident | 时间线是 Incident 的处置证据主体,Incident 本身量少有状态、不在本 ADR 清理范围;若未来做 Incident 归档,TimelineItem 随之整体归档 |

- 所有新增保留项沿用现有约定:`<=0` 关闭、默认值可被环境变量覆盖、批量分页删除。合规环境(等保/SOX 等)应按自身留存要求上调审计类保留期——默认值只是无强合规要求场景的保守起点。
- **活跃 Incident 证据保护原则升格为通用规则**:任何挂在 Incident 下的数据(TimelineItem/IncidentAction/关联 Notification),只要 Incident 未 closed 一律不清理,沿用 retention.go 的既有做法。
- **归档先于删除仅适用于审计类**(AuditLog/IncidentAction):第一阶段"归档"就是导出成文件(复用既有导出链路),不引入对象存储等新依赖;自动化归档到外部存储是后置增强,不阻塞清理落地。

**2. Event 按月分区路线(达到目标吞吐前的预案)**:

- **动机**:DELETE 式清理在月增 4300 万行规模下,死元组回收依赖 autovacuum,清理批次将持续追着写入跑,索引膨胀与 vacuum 压力不可忽视。PostgreSQL 原生 RANGE 分区(按 `created_at` 按月)可用 `DROP PARTITION` 整月释放,秒级、无死元组、不占 vacuum。
- **触发条件**(满足其一再动手,避免过早复杂化):Event 表行数持续 > 5000 万;或保留清理跟不上写入(连续多个巡检周期删除量顶满批上限、表仍在净增长)。
- **ent 不原生支持声明式分区的应对**:分区表的建立与月度分区维护走**原生 SQL 迁移脚本**(独立于 ent auto-migrate 管理,与 ADR-0032 的迁移策略并存);ent 侧无感——分区对读写查询透明,ent 生成的 CRUD 不需要改。切换存量表需"建分区表 → 回填 → 换名"的维护窗口操作,届时在 operations.md 落操作手册。
- **证据保护在分区方案下的等价实现**:DROP 整月分区前先查该分区内是否仍有未 closed Incident 引用的 Event,有则**推迟该分区的 DROP**(而非行级摘除)——保护语义不变,粒度从行放宽到月,可接受(未 closed 的超期老 Incident 本身就该被升级/关单流程消化)。

## 理由

- 补记而非重写:retention.go 的实现(证据保护、分页、双触发幂等)经受了 e2e 验证,决策内容与实现一致,缺的只是记录;按 ADR-0001 惯例把既成事实显式化,后续演进才有锚点。
- 审计类先归档后删:审计数据的价值恰恰在"出事后回看",直接删与审计目的自相矛盾;但无限留存又违背本 ADR 初衷——"导出留档 + 库内清理"兼顾两者,且导出链路(ADR-0029)现成。
- 分区设触发条件而非立即做:当前实际负载远未到目标吞吐,分区带来的迁移复杂度(原生 SQL、维护窗口、月度分区滚动)在小数据量下纯属负担;把方案与阈值先钉死,到量再执行。

## 备选方案

- **全部靠 DELETE 清理走到底**:实现最简单(现状延伸),但目标吞吐下 vacuum 压力与索引膨胀无解,大表 DELETE 会与业务写入争 I/O。仅作为分区落地前的过渡。
- **pg_partman / TimescaleDB 等扩展管分区**:功能省心,但引入部署强依赖(前置依赖清单要多一项扩展),与"单二进制 + 最少外部依赖"(ADR-0031)冲突;原生分区 + SQL 迁移脚本已够用。
- **Notification/审计也进对象存储自动归档**:一步到位但引入 S3 等新依赖与凭据管理面;先文件导出,自动化归档后置。
- **TimelineItem 设独立 TTL**:清理逻辑简单,但会把活跃度不高却仍有复盘价值的 closed Incident 时间线掏空,处置证据不完整比省存储更伤——否决。

## 影响 / 权衡

- 本 ADR 的"待实现"部分落地前,Notification/AuditLog/IncidentAction/WebhookDelivery 继续无界增长——中小规模部署(远低于目标吞吐)短期无碍,但这是显式接受的债,不是遗漏。
- 审计类清理引入"归档产物去哪儿"的运维责任(部署方转存/保管),operations.md 需随实现补操作步骤。
- 分区切换是一次有停机窗口的存量迁移,且此后 schema 演进要兼顾分区表(原生 SQL 迁移与 ent auto-migrate 的边界需要在实现时明确)。
- 保留默认值是产品立场(保守、可覆盖),不是合规承诺;文档须避免给使用者"默认即合规"的暗示。
