# ADR-0037: 收敛延期功能 — 移除电话/SMS 通道、企微、Jira/禅道与 Zabbix/云监控占位

| 字段 | 内容 |
|------|------|
| **状态** | Accepted |
| **日期** | 2026-07-10 |
| **相关** | [ADR-0036](./0036-remove-war-room.md)、[ADR-0017](./0017-notification-fallback-chain.md)、[ADR-0019](./0019-imbot-pluggable-degradation.md)、[design/0002](../design/0002-trim-deferred-features.md) |

## 背景

backlog 长期挂着一批"已评估、推迟、留了占位"的项:电话/SMS 通道(webhook 占位转发,从未对接真实语音 API)、企微 bot(NoopBot 占位)、Jira/禅道 SDK(返回 not-implemented 的占位适配器)、Zabbix/云监控接入类型(枚举与模板存在,无适配器,推送落 parse_failed)、IaC Provider 与首次部署向导(纯设想)。

与作战室([ADR-0036](./0036-remove-war-room.md))同理:占位的持有成本是持续的——用户在界面上看到"电话通道/zabbix 类型"以为可用,实际是假的;每个接口实现者被迫携带占位分支;文档需反复解释"存在但没实现"。而期权价值极低:恢复任何一项的成本 ≈ 从零实现,占位代码帮不上忙。

## 决策

**全部移除,不留占位**:

- **电话/SMS 通道整体删除**(含"假电话"的 webhook 转发占位)。默认降级链从 `im→email→phone→sms` 收敛为 `[webhook]→im→email`;存量配置中残留的 `phone`/`sms` 通道名按未知通道跳过,链上其余通道照常。`User.phone` 保留为通用联系信息。
- **企微(wecom)支持删除**:NoopBot 及注册一并删,平台矩阵收敛为飞书 + 钉钉。
- **Jira/禅道占位适配器删除**:工单集成只保留真实可用的通用 webhook。
- **Zabbix/云监控接入类型删除**:Integration type 枚举收窄,存量行迁移转 `webhook`(其实际语义)。
- **IaC/Terraform、首次部署向导、AI 回训**:backlog 条目删除(AI 回训的否决裁决本体在 [ADR-0025](./0025-no-auto-retrain.md))。

## 理由

- **不欺骗用户**:界面/配置里出现的每个选项都必须真实可用。"选了电话实际发 webhook"、"选了 zabbix 实际 parse_failed"是最坏的一类行为。
- **多通道兜底的实质未受损**:兜底语义靠"链上有多个独立通道",im/email/webhook 三通道仍构成降级链;"假电话"从未提供真实的第二信道。
- **恢复成本不因删除而增加**:真实语音 API 对接、企微 bot、Jira SDK 反正都要从头写;届时按 [`docs/design/`](../design/) 流程重新设计。

## 备选方案

- **维持占位等需求出现**:否决——占位已挂数月无需求牵引,期权价值低于"假功能"的信任成本。
- **只删 backlog 不删代码**:否决——那只是把谎言从文档挪进代码。

## 影响 / 权衡

- 通知兜底从五通道名义收敛为三通道实质,依赖电话强提醒的场景须外接:webhook 出口对接自建语音网关是既有能力。
- 存量库含 zabbix/cloud/jira/zentao 类型行的,迁移统一转 webhook;含 phone/sms 通道配置的按未知跳过,无需迁移。
- 未来恢复任一能力时,本 ADR 标 `Superseded`。
