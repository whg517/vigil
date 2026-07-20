# ADR-0007： 异步任务选 Asynq + 幂等约定

| 字段 | 内容 |
|------|------|
| **状态** | Accepted |
| **日期** | 2026-07-09 |
| **相关** | [ADR-0003](0003-backend-language-go.md)、[ADR-0006](0006-primary-store-postgresql.md)、[ADR-0011](0011-ingestion-decoupled-idempotent.md)、[ADR-0016](0016-escalation-asynq-delayed.md)、[ADR-0017](0017-notification-fallback-chain.md)、[`../architecture.md`](../architecture.md) |

## 背景

Vigil 是事件驱动系统,存在五类异步任务:事件流水线、延迟任务(升级引擎核心)、定时任务、通知重试、长耗时 AI。它们有共同的硬要求:at-least-once 可靠投递、幂等、延迟精度、崩溃恢复、可观测、优先级。需要一个在 Go 生态内、依赖组件少的异步任务方案。

## 决策

异步任务采用 **Asynq(Go + Redis)+ Asynqmon 监控**;由 **Redis 一个组件**同时承担缓存、轻量队列、分布式锁,避免引入专用消息中间件。

- Asynq 开箱覆盖:延迟(`ProcessIn`)、定时(`PeriodicTask`)、重试、死信、优先级 + Asynqmon 面板 + Redis 持久化。
- **at-least-once ⇒ handler 必须幂等**,统一约定幂等键:
  - 升级任务:`esc:{inc}:{level}:{repeatSeq}`(或 `incident_id + level`)。
  - 通知任务:`notif:{notification_id}`(详见 ADR-0017 修订记录)。
  - 事件流水线:以 `source_event_id` 去重。

## 理由

- Asynq 原生覆盖延迟/定时/重试/死信/优先级五项需求,无需自研调度逻辑。
- Redis 一个组件承载缓存 + 队列 + 锁,契合「不拆组件」原则(见 [ADR-0006](0006-primary-store-postgresql.md)),减少自托管部署的进程数。
- Asynqmon 面板提供可观测性,死信可重放满足崩溃恢复。
- at-least-once 语义下重复投递不可避免,因此在 handler 层强制幂等,幂等键随任务类型固定约定,避免重复副作用。

## 备选方案

- **自研 Redis ZSET 队列**:需自行实现延迟精度、重试、死信、优先级、监控,重复造轮子且可靠性无保障,否决。

## 影响 / 权衡

- at-least-once 把幂等责任推给每个 handler,新增任务类型时必须显式设计幂等键,属开发约束。
- 延迟任务持久化在 Redis,worker 重启不丢;但状态依赖 Redis 可用性,Redis 成为关键组件。
- 升级、通知等关键任务通过高优先级队列 + 高 MaxRetry 保证不丢(见 [ADR-0016](0016-escalation-asynq-delayed.md)、[ADR-0017](0017-notification-fallback-chain.md))。

出处:tech-stack §二/§3.4。
