# ADR-0017: 通知逐通道兜底降级链 + 送达三态 + 聚合

| 字段 | 内容 |
|------|------|
| **状态** | Accepted |
| **日期** | 2026-07-09 |
| **相关** | [`0007-async-tasks-asynq.md`](./0007-async-tasks-asynq.md)、`ent/schema/notification.go`、[`../architecture.md`](../architecture.md) |

## 背景

通知要把 Incident 送到人手里,但单一通道并不可靠(IM 未读、离线、平台故障)。若把多通道并联全发,人会被同一告警重复打扰;若只发一个通道,失败就是静默丢失。夜间还需降噪,又不能把真正的 critical 也压掉。通知发不出去这件事本身,也不能无痕消失。

## 决策

`msg.Channels` 是**有序降级链(非并联)**:对每个 target 按链顺序逐通道尝试,首个成功即停,失败才降级到下一通道。实现见 `notifier.go` 的 `deliverChain / resolveChannels`。

- **通道来源优先级**:① 本层 `EscalationLevel.notify_channels` > ② 命中的 `NotificationRule.channels` > ③ 全局默认链 `[webhook?] + im + email + phone + sms`。规则按"更具体者优先"评估(`RuleResolver` 按 severity / team / service 匹配,无命中回落默认链)。
- **整链失败兜底**:某 target 整条链全部失败 → 记 `failed` + `allFailedHook` 兜底告警 org_admin(走非 IM 通道),使"通知发不出去"不再静默。实现见 `rule.go` / `wire.go` 的 `buildAllFailedHook`。
- **送达三态落库(只追加)**:`Notification` ent 记录每次发送,`Status` ∈ `pending | sent | failed | suppressed`,含 Channel / Target / Level / Severity 快照;`suppressed` = 命中 quiet_hours 被静默(不再无痕丢弃,可查可补发)。查询 `GET /incidents/:id/notifications`(权限 `incident.view`)。实现见 `ent/schema/notification.go` / `recorder.go`。
- **静默时段**:`NotificationRule.quiet_hours` 支持跨午夜窗,`bypass_for:[critical]` 让 critical 穿透,值班人始终通知,跨时区按 target 用户时区计算。实现见 `quiet_hours.go`。
- **聚合**:默认 30s 窗口内对同一 target 的多条合并成一条,critical 不聚合立即单发;Redis `pending_notify:{target_id}` 队列。实现见 `aggregator.go`。
- **可插拔与重试**:Notifier 可插拔(`Channel / Send`),电话 / SMS 对接云厂商语音 API 不自建;失败走 Asynq 指数退避重试,幂等键 `notification_id`;模板用 Go `text/template`,内置 3 个默认模板 seed 幂等 upsert,渲染失败降级 `FormatTitle / FormatSummary` 兜底不丢通知。

## 理由

- 降级链避免每通道各发一遍的重复打扰,同时保留"IM 优先、电话/短信兜底"的层次。
- 三态落库让送达可观测,`suppressed` 让夜间降噪可追溯、可补发。
- 30s 聚合进一步减少打扰,critical 例外确保紧急告警不被延迟。

## 备选方案

- **多通道并联全发**:同一告警重复打扰,通知疲劳——否决。
- **单通道 + 失败即弃**:一次失败即静默丢失,违背"接得住不丢失"基线——否决。

## 影响 / 权衡

- 降级链是串行尝试,极端情况下最后一通道才成功,送达延迟略增,换取不重复打扰。
- 三态只追加,`Notification` 表随发送次数增长,需归档/清理策略。
- quiet_hours 的 `bypass_for` 与聚合的 critical 例外是刻意的"降噪不误杀"设计,配置错误会直接影响紧急送达,须谨慎默认。
