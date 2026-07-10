# 0002. 收敛延期功能:移除电话/SMS 通道、企微占位、工单 SDK 占位、Zabbix/云监控类型

- 状态:已实现
- 关联:[ADR-0037](../adr/0037-trim-deferred-features.md)、原 [`backlog.md`](../backlog.md) §1.1/§1.2/§1.3 与三条待规划项(随本设计移除)

## 目标与非目标

**目标**:把 backlog 里长期挂起、无真实需求牵引的延期项连同其占位实现一次性移除,收敛产品能力面到"真实走通的路径"。

**非目标**:不动 IM 主路径(飞书/钉钉卡片、回调、命令)、通用 webhook 工单、SMTP 入向计划(保留待规划)、InternalExecutor。

## 移除清单

### 1. 电话/SMS 通知通道(占位转发,永不对接真实语音 API)
- `internal/notification/channels_phone.go` + 测试:PhoneChannel/SMSChannel 删除。
- `internal/server/wire.go`:注册、`resolvePhones`、默认链 `im→email→phone→sms` 缩为 `[webhook]→im→email`、selfmon 可送达判定的 phone/sms 分支、NotifyUnrouted 兜底通道列表同步。
- `internal/config/config.go` + `.env.example`:语音 webhook URL 配置项删除。
- 前端:escalation-policies / settings/notification-tab / settings/subscription-tab 的通道选项去掉 phone/sms;locales 同步。
- **存量数据兼容**:EscalationLevel.notify_channels / NotificationRule.channels / Subscription.channels 是 JSON 字符串数组,可能残留 "phone"/"sms"——通道注册表查不到即跳过(既有降级语义),不做数据迁移。
- `NotificationTemplate.channel` 枚举收窄为 `im|email|webhook`;存量 phone/sms 通道模板随迁移删除(无消费方,内置 seed 只建三通道)。
- `User.phone` 字段保留(通用联系信息,不再被通道消费)。

### 2. 企微(wecom)占位
- `internal/server/wire.go`:`reg.Register(im.NewNoopBot("wecom"))` 与占位日志删除。
- `internal/im/noop.go`:NoopBot 随唯一使用方消失一并删除(通用 `Registry` 抽出为 `registry.go`);`handler.go` 的 NoopBot 跳过守卫删除。
- `IMAccountBinding.platform` 枚举收窄为 `feishu|dingtalk`;存量 wecom 绑定随迁移删除(重绑即可)。
- 平台枚举注释/swagger 注解收敛为 `feishu | dingtalk`(回调入口对未知平台本就查无 bot 而拒绝)。

### 3. Jira/禅道 SDK 占位
- `internal/ticket/adapter.go`:`notImplementedAdapter`/`NewJiraAdapter`/`NewZentaoAdapter`/`ErrAdapterNotImplemented` 删除;wire.go 注册删除。
- TicketIntegration type 枚举若含 jira/zentao:枚举收敛 + 迁移把存量行转 webhook(占位适配器本就从未建单成功)。

### 4. Zabbix / 云监控接入类型
- `ent/schema/service.go` Integration type 枚举去掉 `zabbix`/`cloud` → `go generate` + genmigration 重生成 baseline。
- 迁移 `000X`:存量 `type IN ('zabbix','cloud')` 行转 `webhook`(无适配器,推送本就落 parse_failed;通用 webhook 是其实际语义)。
- `internal/integration/config_template.go`:模板与排序删除对应项;handler/swagger 注解同步。
- 前端 integration 类型选项(如有)同步。

### 5. 纯 backlog 项(无代码)
- IaC/Terraform、首次部署向导:仅删 backlog 条目。
- AI 无监督自学习/回训:删 backlog 条目;裁决本体在 [ADR-0025](../adr/0025-no-auto-retrain.md) 不动。

### 6. 文档同步
- backlog:上述条目全部移除。
- ADR-0017(默认链)、ADR-0019(平台矩阵)、ADR-0009(通道/告警源清单)、ADR-0002(措辞)、ADR-0030/0031(云语音可选组件)、architecture.md、operations.md、.env.example。

## 边界与失败处理

- 存量配置含 phone/sms/wecom:通道/平台注册表查不到 → 按未知项跳过并走既有降级(不 panic、不静默丢整条通知——链上其他通道继续)。
- 枚举收窄的存量行:迁移先转 webhook 再收枚举,幂等(`WHERE type IN (...)`)。

## 测试要点

- 通知链路测试改用 `im→email` 链断言;删除 phone 通道相关用例。
- 迁移测试计数随新增文件更新。
- e2e 全绿(通知/升级核心链路)。
