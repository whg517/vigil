# Changelog

本文件记录 Vigil 所有面向用户的显著变更。

格式遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本号遵循[语义化版本](https://semver.org/lang/zh-CN/)。

> **0.x 版本策略**：项目处于 0.x 阶段，**minor 升级（0.N → 0.N+1）可能包含破坏性变更**（API / 配置 / 数据库 schema），升级前请阅读对应版本条目与 [`docs/operations.md`](docs/operations.md) 的升级章节；patch 版本仅含向后兼容的修复。每个版本随 git tag（`vX.Y.Z`）发布。

## [Unreleased]

## [0.1.0] - 2026-07-14

首个版本化发布，从零到一落地告警处置全链路。以下按能力域粗粒度汇总（架构全景见 [`docs/architecture.md`](docs/architecture.md)，设计取舍见 [`docs/adr/`](docs/adr/) 的 38 份 ADR）。

### Added

- **告警接入与归一化**：通用 Webhook、Prometheus/Grafana 适配器（多 alert 拆分）、开放 API 投递、SMTP 邮件告警源；接入干跑调试、token 轮换、raw event 重放回灌、限流/背压、严重度映射表可配置。
- **分诊降噪**：去重→聚合→路由三段流水线；抑制规则与维护窗口独立操作流；路由多标签/glob/优先级；未路由告警懒供给 Service（含主动同步与过期清理）。
- **排班**：daily/weekly/custom 轮值、Override 换班、follow_the_sun 跨时区接力、空班检测告警。
- **升级引擎**：多层升级链（Asynq 延时驱动）、按层通道、手动跳层/取消、critical 兜底、reopen 重启升级链、Redis 丢数据对账恢复与重投幂等。
- **通知**：通道抽象（邮件 SMTP / Webhook / IM）、规则评估、逐通道降级链、送达三态记录、通知模板、静默时段、team/service 粒度订阅。
- **IM 协同（ChatOps）**：飞书、钉钉真实适配器；告警卡片、卡片内处置动作（与 Web 同一 RBAC 链路）、斜杠命令、mention 解析、卡片降级、IM 账号绑定/解绑。
- **Runbook 两档执行**：只读诊断内置执行（InternalExecutor，含 query_metrics/query_logs）；处置写操作强制审批闸门 + 执行锁；HTTPExecutor 带 SSRF 防护；执行器凭据 AES-256-GCM 加密托管。
- **AI Copilot**：分诊建议（严重度/合并）、根因诊断、相似事件检索（pgvector）、复盘自动起草——全部带 evidence + human-in-the-loop；Provider 可选（智谱 GLM / Ollama 本地）、成本控制与置信度阈值；噪声建议可沉淀为抑制规则。
- **复盘**：状态机（草稿→评审→发布）、逐段编辑、AI 草拟字段标记、改进项 ActionItem 与外部工单双向联动、published 复盘入库反哺相似检索。
- **时间线与审计**：incident 全生命周期统一时间线；敏感操作双审计留痕，审计日志 CSV 导出。
- **认证与 RBAC**：JWT 登录态、API Key 程序化接入、登录限流与失败锁定、改密吊销旧令牌；权限点系统枚举 + 角色自由组合、资源级 ScopeResolver 团队软隔离。
- **数据报表**:团队 scope 报表、CSV 导出、定时聚合快照、值班大屏 + PWA 可安装壳。
- **自监控**：队列积压/通知失败率超阈自触发告警（独立通道防循环）。
- **Web 前端**：19 个页面（事件/值班/升级/Runbook/复盘/报表/设置等）、全站中英双语 i18n、WebSocket 实时同步（多副本 Redis pub/sub）、集成接入向导。
- **部署与运维**：单二进制（前端 embed）、Docker Compose、Helm Chart（安全上下文/PDB/非 root）、版本化数据库迁移 + 备份恢复脚本、Prometheus 指标。
- **工程基建**：CI 三道门禁（lint→test→build）+ commitlint + 覆盖率门禁 + govulncheck + dependabot；Ginkgo 后端 e2e 与 Playwright 全栈 e2e；可共享 git hooks。

### Security

- 默认开启业务 API 鉴权，`production` 环境强制关闭身份回退头与测试端点；登录限流、SSRF 防护、凭据加密托管等安全加固随本版本一并交付（自托管基线见 [`SECURITY.md`](SECURITY.md)）。
