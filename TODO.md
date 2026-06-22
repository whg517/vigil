# Vigil 待办事项（TODO）

> 本文件记录**已确认要做、但未纳入当前迭代**的事项。已完成的项在下方标记 ✅。
> 与 `docs/` 下各文档的"开放问题"互补：开放问题偏设计未决，本文件偏实现排期。

---

## 告警源适配器（能力域 1）

- [ ] **Zabbix 适配器**（PRD M1.2）：解析 Zabbix action script payload（trigger/priority/eventid）。
- [ ] **云监控适配器**（PRD M1.2）：阿里云/腾讯云/AWS SNS 各自消息结构适配。
- [ ] **邮件接入 SMTP→Event**（PRD M1.3）：SMTP 收信地址收告警，从主题解析 severity、正文解析 detail。
  - 注意：与"邮件**通知**通道"（迭代 2.1，对外发邮件）是两件事——前者是入向告警源，后者是出向通知。
- [ ] **严重度映射表可配置**（M2.3）：`mapPromSeverity` 当前写死，支持 Integration.config 覆盖映射表。

## IM 协同（能力域 8）

- [ ] **企业微信真实适配器**（M8.8）：当前 `im.NewNoopBot("wecom")` 占位，待 PoC 后接入真实 API（卡片交互/建群/@人）。
- [ ] **作战室归档**（M8.9）：事件关闭后保留作战室聊天记录，关联复盘。需 war_room 实体落库。
- [ ] **IM 消息回写时间线**（M10.5）：`internal/im/handler.go:149` 本期不回写，留后续。仅捕获含关键词/@机器人的消息。

## 通知通道（能力域 7）

- [ ] **电话/SMS 真实云厂商对接**（M7.2）：迭代 2.2 先做 Channel 抽象层 + 占位（webhook+日志），真实对接阿里云/腾讯云语音 API 留此。
- [ ] **通知送达持久化**（M7.6）：无 Notification 实体，送达结果只打日志。加 Notification 表落库后可统计 P95、重试追溯。
- [ ] **NotificationRule 精确匹配**（M7.5）：`main.go:189/215` 当前"取首条 enabled 规则"，未按 severity/team/service 精确匹配。

## 处置执行（能力域 9）

- [x] ~~InternalExecutor 真实诊断查询（M9.4）~~ ✅ 已完成（feat-runbook-closure：check_http 真实探活）
- [ ] **执行器凭证管理**：Ansible/Jenkins token 加密存储（Q1）。
- [ ] InternalExecutor 扩展：query_metrics（查 Prometheus）/ query_logs（查 Loki）等更多只读诊断。

## AI 智能（能力域 11）

- [ ] **智能降噪 AI 模式学习**（M11.5）：迭代 2.5 起步用规则式，AI 学习模式识别噪音留此。
- [ ] **本地模型 Ollama Provider**（M11.10）：隐私场景数据不出境，当前仅接智谱 GLM。
- [ ] **AI 建议置信度阈值**（Q2）：默认 0.6 可配，低于阈值不展示。

## 非功能与运维

- [ ] **Kubernetes Helm Chart 完整化**（H1.2，deployment.md D1）：`deploy/helm/` values/secrets/ingress。迭代 4.2 起步，完整化留此。
- [ ] **备份恢复自动化脚本**（H1.5，deployment.md D2）：pg_dump + Redis RDB 快照定期备份。
- [ ] **i18n 国际化**（NFR）：前端无 i18n 框架，文案硬编码中文，结构未预留。
- [ ] **多副本 worker 队列分片**（architecture A2）：初期单实例，扩展时细化。
- [ ] **吃自己狗粮闭环**（H2.4）：队列积压/通知失败率超阈值时 Vigil 对接自身触发告警，当前未闭环。

## 测试覆盖

- [ ] `internal/server`、`internal/migrate`、`internal/queue`、`internal/store`、`internal/logger` 5 个零测试包补关键路径测（迭代 4.3 起步）。

---

## 已完成迭代（✅ 全部合入 main，26 包测试 + golangci-lint 0）

- ✅ 迭代 1：认证系统(JWT) / API Key / 审计日志 / 限流背压
- ✅ 迭代 2：邮件通道 / 电话通道抽象 / WebSocket / Grafana 适配器 / 知识沉淀
- ✅ 迭代 3：接入管理页 / 升级策略页 / 用户团队管理 + 通知配置表单
- ✅ 迭代 4：Runbook 闭环修复 / 部署制品 / 测试盲点补全
