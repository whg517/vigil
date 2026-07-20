# ADR-0011： 接入解耦 —— 先落 RawEvent + source_event_id 幂等

| 字段 | 内容 |
|------|------|
| **状态** | Accepted |
| **日期** | 2026-07-09 |
| **相关** | [`0007-async-tasks-asynq.md`](./0007-async-tasks-asynq.md)、[`0012-triage-three-stage-pipeline.md`](./0012-triage-three-stage-pipeline.md)、[`0009-pluggable-integrations.md`](./0009-pluggable-integrations.md) |

## 背景

漏一条告警可能导致故障无人响应。若在接收阶段同步做归一化/分诊,下游一慢就会拖住接收,告警源超时后可能直接丢弃告警。接入层必须"接得住不丢失"。

## 决策

- **Receiver 极简**:只做「校验 token + 落原始 payload + 入队」,秒级返回 **202**。
- **归一化异步化**:归一化是独立 Asynq worker 任务(选 Adapter → `Normalize` → 写 PG)。
- **新增 `raw_event` 表**(data-model 未定义,本设计新增),状态机 `received | normalized | parse_failed | requeued`。
- **幂等键 `source_event_id`**:重复推送不产生新 Event,仅更新状态。
- **去重键 `DedupKey = sha1(source + fingerprint)`** 在归一化阶段生成。
- **背压兜底与错误分级**:
  - 单接入点限流超 `rate_limit` → **429**;
  - 全局队列积压 → **503**,但 payload 仍落库(限流/背压/熔断时先落库标 pending,恢复后回灌,绝不内存丢弃);
  - 持续无效 payload → 熔断封禁;
  - 鉴权失败 **401** 不落库但记审计(防探测);
  - 格式错误落库标 `parse_failed` 不阻塞;
  - 归一化失败 Asynq 重试 N 次 → 死信,Asynqmon 可重放。
- **Adapter 接口** `Type() / Normalize()`;严重度统一归 `critical/warning/info`(映射表 Integration 可配);原始 payload 存 `Event.Detail` 不丢。
- **接入方式**:通用 Webhook `POST /api/v1/webhook/{integration_id}`、专用适配器、邮件 SMTP、开放 API `POST /api/v1/events`。

## 理由

- 绝不在接收阶段同步归一化/分诊:下游慢会致告警源超时丢告警。
- 漏一条告警可能致故障无人响应,故任何阶段失败都能从原始 payload 重放。
- 错误分级让不同失败各得其所:可重放的落库、恶意的封禁、探测的只记审计不落库。

## 备选方案

- 接收即同步归一化并直接写 Event:实现简单但接收吞吐受下游速度制约,告警源超时即丢,否决。

## 影响 / 权衡

- 新增 `raw_event` 表与一层异步任务,存储与流程更复杂,换来"先落库不丢失 + 可重放"。
- 至少一次投递要求归一化 handler 幂等(见 ADR-0007),以 `source_event_id` 保证重复推送不产生重复 Event。
