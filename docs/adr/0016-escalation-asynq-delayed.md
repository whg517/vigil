# ADR-0016: 升级引擎 Asynq 延迟任务 + 状态守卫

| 字段 | 内容 |
|------|------|
| **状态** | Accepted |
| **日期** | 2026-07-09 |
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
- 升级任务绝不能丢:走高优先级队列 + 高 `MaxRetry`。
- 状态守卫作为最终一致兜底,消解取消/触发的竞态窗口,无需分布式事务。

## 备选方案

- **进程内定时器(time.After / timer)**:进程重启即全部丢失,升级链断裂——否决。
- **DB 轮询到期任务**:精度差、放大数据库压力,且仍需自己实现重试/死信——Asynq 开箱即有。

## 影响 / 权衡

- 依赖 Redis 与 Asynq 的可靠投递语义(at-least-once),因此 handler 必须幂等(见 ADR-0007)。
- ack 竞态窗口内可能出现"已删除任务仍被触发",由状态守卫吸收,不产生实际升级动作。
- 升级不继承父服务,每个 Service 都须显式绑定 EscalationPolicy——换取路由与升级的确定性。
