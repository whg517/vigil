# ADR-0038: SMTP 入向接入 — 邮件告警源

| 字段 | 内容 |
|------|------|
| **状态** | Accepted |
| **日期** | 2026-07-13 |
| **相关** | [ADR-0011 接入解耦](./0011-ingestion-decoupled-idempotent.md)、[ADR-0009 可插拔](./0009-pluggable-integrations.md)、`internal/ingestion/smtp_server.go` |

## 背景

大量遗留监控系统只会发邮件告警。Integration 的 `email` 类型此前是占位(config-template 提示"设计目标"),推送无门。与「邮件**通知**通道」(出向,已实现)是两件事:本决策解决**入向**——邮件进来变成 Event。

## 决策

内置一个轻量 SMTP 接收端(基于 `github.com/emersion/go-smtp`),默认关闭:

- **启停与端口**:`VIGIL_SMTP_IN_ENABLED`(默认 false)+ `VIGIL_SMTP_IN_ADDR`(默认 `:2525`)。
- **鉴权 = 收件人即令牌**:收件地址的 local part 必须等于某个 `type=email` 且 enabled 的 Integration 的 webhook token(如 `a1b2c3@vigil.local`)。复用既有 token 体系,不引入 SMTP AUTH——部署形态是内网接收,网络边界 + token 双层已够;token 查不到直接拒收(RCPT 阶段 550)。
- **复用接入核心**:收信后走与 webhook 完全相同的 `IngestRaw` 链路(先落 RawEvent → 限流 → 背压 → 入归一化队列),邮件原文以 JSON 信封(from/to/subject/body/message_id)存 payload,不丢失保证与回灌能力与 HTTP 入口完全一致。
- **EmailAdapter 归一化**:severity 从主题解析(`[CRITICAL]`/`[WARNING]`/`[RESOLVED]` 前缀及常见关键词,`severity_map` 可覆盖);summary=主题;`source_event_id`=Message-ID(缺失则 from+subject 哈希);主题带 `[RESOLVED]`/`[OK]` 归一为 resolved。
- **防滥用**:单封邮件 payload 上限 1MB(与 webhook 同限);DATA 超限拒收。

## 理由

- 收件人即令牌把"哪个邮箱收告警"与"哪个接入点"一一对应,路由/限流/审计全部复用 per-integration 既有机制,零新概念。
- 复用 `IngestRaw` 使邮件入口自动获得「先落库不丢、限流、背压、死信回灌」全部可靠性保证,不重复实现。
- go-smtp 是 Go 生态事实标准的 SMTP 服务器库(MIT,维护活跃),自研 SMTP 协议解析不值得。

## 备选方案

- **对接外部邮箱轮询(IMAP/POP3 拉取)**:需要凭据管理与轮询调度,延迟高;否决——内置接收端更简单实时,遗留系统只需改 SMTP relay 地址。
- **SMTP AUTH 用户名密码**:多一套凭据体系;否决——token-in-recipient 已提供等价鉴权且与现有 Integration 模型一致。
- **每封邮件同步归一化**:违反 ADR-0011 接收解耦原则;否决。

## 影响 / 权衡

- SMTP 端口需网络策略保护(operations checklist 已注明:仅内网可达)。
- 邮件无结构化 labels,路由主要靠 Integration 的默认归属兜底(收件人 token 已绑定 Integration → Service)。
- TLS(STARTTLS)暂不做:内网接收场景;公网暴露属错误部署。
