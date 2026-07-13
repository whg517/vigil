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

- **通道来源优先级**:① 本层 `EscalationLevel.notify_channels` > ② 命中的 `NotificationRule.channels` > ③ 全局默认链 `[webhook?] + im + email`。规则按"更具体者优先"评估(`RuleResolver` 按 severity / team / service 匹配,无命中回落默认链)。
- **整链失败兜底**:某 target 整条链全部失败 → 记 `failed` + `allFailedHook` 兜底告警 org_admin(走非 IM 通道),使"通知发不出去"不再静默。异步投递下 hook 只在**最后一次重试失败**时触发(每轮重试都触发会轰炸 org_admin)。实现见 `rule.go` / `wire.go` 的 `buildAllFailedHook`。
- **送达三态落库**:`Notification` ent 记录每次发送,`Status` ∈ `pending | sent | failed | suppressed`,含 Channel / Target / Level / Severity 快照;`suppressed` = 命中 quiet_hours 被静默(不再无痕丢弃,可查可补发)。同步路径只追加不修改;异步投递的 tracking 行以 `pending` 落库、由任务回写终态(`sent`/`failed`),终态一旦落定不再变更(worker 以此做幂等守卫)。查询 `GET /incidents/:id/notifications`(权限 `incident.view`)。实现见 `ent/schema/notification.go` / `recorder.go`。
- **静默时段**:`NotificationRule.quiet_hours` 支持跨午夜窗,`bypass_for:[critical]` 让 critical 穿透,值班人始终通知,跨时区按 target 用户时区计算。实现见 `quiet_hours.go`。
- **聚合**:默认 30s 窗口内对同一 target 的多条合并成一条,critical 不聚合立即单发;Redis `pending_notify:{target_id}` 队列。聚合窗口到期 flush 合并出的通知同样走 Asynq 任务投递。实现见 `aggregator.go`。
- **可插拔与重试(2026-07-14 落地)**:Notifier 可插拔(`Channel / Send`);单条通知(单 target)的投递封装为独立 Asynq 任务——`Notification` 行先落 `pending`(行 ID 即任务幂等键,`TaskID=notif:{notification_id}`),worker 执行降级链,瞬时失败 return error 交给 asynq 指数退避重试(显式 `MaxRetry=5`),重试耗尽落 `failed` + 兜底告警并进 archived 死信(可查可重放);入队失败(Redis 不可用)回退同步直投,绝不丢通知。队列沿用 critical/default 约定(critical 告警的通知走 critical 队列)。模板用 Go `text/template`,内置 3 个默认模板 seed 幂等 upsert,渲染失败降级 `FormatTitle / FormatSummary` 兜底不丢通知。实现见 `delivery_task.go` / `notifier.go`。

## 理由

- 降级链避免每通道各发一遍的重复打扰,同时保留"IM 优先、email/webhook 兜底"的层次。
- 三态落库让送达可观测,`suppressed` 让夜间降噪可追溯、可补发。
- 30s 聚合进一步减少打扰,critical 例外确保紧急告警不被延迟。

## 备选方案

- **多通道并联全发**:同一告警重复打扰,通知疲劳——否决。
- **单通道 + 失败即弃**:一次失败即静默丢失,违背"接得住不丢失"基线——否决。

## 影响 / 权衡

- 降级链是串行尝试,极端情况下最后一通道才成功,送达延迟略增,换取不重复打扰。
- 三态基本只追加,`Notification` 表随发送次数增长,需归档/清理策略(异步 tracking 行例外:pending 会被回写终态,不新增行数)。
- quiet_hours 的 `bypass_for` 与聚合的 critical 例外是刻意的"降噪不误杀"设计,配置错误会直接影响紧急送达,须谨慎默认。
- 2026-07-10:电话/SMS 通道已整体移除,默认链收敛为 `[webhook?] + im + email`;电话强提醒场景经 webhook 出口外接。

## 修订记录

- **2026-07-14:通知重试与死信真正落地(此前为宣称)。** 原文写"失败走 Asynq 指数退避重试,幂等键 notification_id",但实现一直是同步一次性尽力交付:升级引擎调 `NotifyEscalation` 的结果被忽略,`deliverChain` 整链失败只记 `failed` + `allFailedHook`,无任何重投——IM/邮件/webhook 同时抖动(出口网络闪断)时该通知永久丢失。本次把宣称变成现实,关键取舍:
  - **任务粒度 = 单 target 单条通知**:降级链"首个成功即停"的语义天然以 target 为单位,重试也应只补投失败的那个人,不牵连已送达者。
  - **幂等键 = Notification 行 ID**(`TaskID=notif:{id}`,行先落 `pending` 再入队):行是唯一真实来源,worker 开头按行状态守卫(非 `pending` 即已处理,at-least-once 重投跳过),防重复送达也防 hook 重复轰炸。
  - **`MaxRetry=5`(非升级任务的 25)**:asynq 默认退避下约覆盖 15-20 分钟,足以熬过通道抖动;再晚的"迟到升级通知"大概率已被升级链 repeat/下一 level 的独立任务取代,继续坚持只会产生过时打扰。链的连续性由 escalation 保证,单条通知不必无限重试。
  - **`allFailedHook`/`failed` 落库只在最后一次重试失败时执行**,重试中行保持 `pending` 并更新 reason(在途可观测);"无任何可用通道"属配置性失败,跳过重试直接归档(`asynq.SkipRetry`)。
  - **异步路径不逐通道追加送达记录**(tracking 行统一承载结果),避免"重试次数 × 通道数"的记录膨胀;同步路径(未装配队列/单测)行为与历史完全一致。
  - **`NotifyUnrouted`(自监控告警、全败兜底告警)刻意保持同步直投**:被监控/被兜底的可能正是队列本身,兜底通知不能依赖队列;入队失败(Redis 不可用)时普通通知同样回退同步直投——降级语义是"可能少重试,绝不丢通知"。
  - 升级引擎的重投幂等标记(ADR-0016)语义保持:入队通知任务本身就是被标记守卫的副作用,升级任务重投不会重复入队通知。
