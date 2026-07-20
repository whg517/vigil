# ADR-0016： 升级引擎 Asynq 延迟任务 + 状态守卫

| 字段 | 内容 |
|------|------|
| **状态** | Accepted |
| **日期** | 2026-07-09（2026-07-14 增补对账恢复与幂等细化） |
| **相关** | [`0007-async-tasks-asynq.md`](./0007-async-tasks-asynq.md)、[`0015-schedule-realtime-no-snapshot.md`](./0015-schedule-realtime-no-snapshot.md)、[`../architecture.md`](../architecture.md) |

## 背景

升级(escalation)是 oncall 的核心兜底:某级值班人在超时内未 ack,就必须自动升到下一级,直到有人响应。这要求"延迟到某时刻触发"的能力,且必须扛住 worker 重启不丢任务;同时 ack 与延迟触发之间存在竞态——ack 已到但延迟任务恰好触发,不能把已处理的 Incident 又升级一次。

## 决策

用 **Asynq 延迟任务**驱动升级,配合状态守卫兜底竞态。

- **排程**:Incident 创建即 `asynq.ProcessIn(delay_minutes[0])` 排 level[0] 的延迟任务。
- **取消**:ack(来自 Web 或 IM)经事件总线触发 `asynq.DeleteTask`,删除所有待触发的升级任务。
- **状态守卫**:handler 执行前检查 `incident.status`,已 ack / resolved 则即使被误触发也不做动作(防"取消与触发竞态",最终一致)。
- **幂等键**:`esc:{inc}:{level}:{repeatSeq}`。
- **repeat_times 语义**:`repeat_times` 是 EscalationPolicy 级字段(非各 level 独立),对每层生效——某层未 ack 则再重复 `repeat_times` 次(每层共 `repeat_times + 1` 次),用尽才推进下一层;重复间隔 = 该层自身的 `delay_minutes`;语义由 `TestRepeatTimesSemantics` 锁定。
- **目标解析**:`target.type` 可为 schedule(调排班算在班人)/ user / team(全员,通常末级兜底);多 target 取并集去重;升级策略**不继承**父服务,每个 Service 显式绑定;末级升级到全团队 + 多通道,保证最终有人响应。

## 理由

- 延迟任务持久化在 Redis,worker 重启不丢待触发的升级。
- 升级任务绝不能丢:走高优先级队列(critical) + 显式 `MaxRetry=25`(在 enqueue 时以常量显式设置,而非依赖 asynq 库默认值——语义自包含,不随库默认漂移)。
- 状态守卫作为最终一致兜底,消解取消/触发的竞态窗口,无需分布式事务。

## Redis 数据丢失与对账恢复

Worker 重启不丢任务的前提是 **Redis 数据仍在**。Redis 丢数据(未开持久化的重启、误 `FLUSHALL`、
主从切换丢窗口)后,活跃 Incident 的升级计时器凭空消失——不再升级、不再通知、且无任何报错,
是最危险的**静默失效**。为此引入**升级对账巡检(reconciliation sweeper)**,周期性核对
「DB 里的应然」与「Redis 里的实然」:

- **扫描条件(应然)**:`status ∈ {triggered, escalated}` 且绑定了非空 levels 升级策略、
  且 `current_level < len(levels)` 的 Incident,按状态守卫语义**必然应存在**一个待触发升级任务
  (下一层首发或当前层重复)。`current_level ≥ len(levels)` 表示链已推进到末级处理完,无"应然"任务。
- **核对(实然)**:用 asynq Inspector 遍历 critical 队列 pending / scheduled / retry / active
  四态,按任务 payload 解出 incident_id 集合;任一态存在该 Incident 的升级任务即视为链健在。
- **修复**:缺失则从 `current_level` 层重排(`repeatSeq=0`,延迟 = 该层 `delay_minutes`)。

**RPO 语义(接受的恢复损耗)**:
- 重复序号(repeatSeq)只活在任务 payload 里,随 Redis 一起丢。恢复时不接续旧序号,而是直接
  从 `current_level`(下一层)首发重排——**宁可升得更快(跳过当前层剩余重复、扩大通知面),
  不可断链**;升级链的失效模式里"没人被叫"远比"多叫了人"严重。
- 末级的剩余重复通知不恢复(`current_level` 已达 `len(levels)`,每一层目标都至少通知过一次)。
- 计时从巡检重排时刻重新起算:最大恢复延迟 ≈ 巡检间隔(默认 2m,`VIGIL_ESCALATION_SWEEP_INTERVAL`
  可配)+ 该层 delay。

**幂等保证(多副本/竞态安全)**:
- 重排复用既有幂等 TaskID(`esc:{inc}:{level}:{repeatSeq}`),且 enqueue 对
  `asynq.ErrTaskIDConflict` **按成功处理**(同 key 任务已在队即目标达成)——多副本并发巡检、
  巡检与正常链推进赛跑,最坏只是撞 TaskID 被吸收,不产生重复任务。
- 巡检与 ack 赛跑:巡检读到活跃态后 Incident 恰被 ack,重排的任务到点会被状态守卫吸收,不误升级。

**HandleTask 重投幂等**:asynq 是 at-least-once,worker 在「已通知、已入队下一层、尚未 ack」
窗口崩溃会重投同一任务,状态守卫不拦(incident 仍 escalated)。为防重复通知轰炸/重复计数,
通知 + `escalated_count` 自增用 **Redis 一次性标记**(键含本任务 TaskID,TTL 24h)包住:
重投时标记命中则跳过通知/计数/时间线/事件发布,但仍执行状态推进与下一步排程(链的连续性
是硬约束)。标记**先通知后落**——宁可极小崩溃窗口重复通知一次,不可静默丢通知(at-least-once
优先);Redis 不可用时降级为原 at-least-once 行为(可能重复通知,不丢通知)。

## 备选方案

- **进程内定时器(time.After / timer)**:进程重启即全部丢失,升级链断裂——否决。
- **DB 轮询到期任务**:精度差、放大数据库压力,且仍需自己实现重试/死信——Asynq 开箱即有。

## 影响 / 权衡

- 依赖 Redis 与 Asynq 的可靠投递语义(at-least-once),因此 handler 必须幂等(见 ADR-0007);
  幂等由「状态守卫 + 一次性通知标记 + 幂等 TaskID」三层共同达成(见上节)。
- ack 竞态窗口内可能出现"已删除任务仍被触发",由状态守卫吸收,不产生实际升级动作。
- 升级不继承父服务,每个 Service 都须显式绑定 EscalationPolicy——换取路由与升级的确定性。
- Redis 丢数据的恢复不是无损的(见上节 RPO 语义):接受"升得更快/计时重启",换取"链绝不静默断掉"。
- 对账巡检本身依赖 Redis 可用;Redis 整体不可用时巡检空转(记 warn),恢复后下一轮自动补齐。
