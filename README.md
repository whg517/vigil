# Vigil

> **守夜人 —— 让每一条告警都被妥善接力到终点。**

Vigil 是一个开源、IM 原生、AI 原生的告警处置平台，专注解决告警发生**之后**的"下一步"问题。

---

## 为什么做 Vigil

监控/告警系统（Prometheus、Zabbix、Datadog……）解决了"**发现问题**"，把指标越界变成一条告警。但告警产生之后到问题被**真正解决**之间，还有一大段没人管的地带：

> 告警进来 → 谁来响应？怎么通知到他？多人怎么协同？查什么？
> 按什么步骤处置？谁升级？解决后怎么复盘？

这段"告警之后"的链条，就是 Vigil 要解决的问题——完整的 **Incident Response Lifecycle（事件响应生命周期）**，而不是简单的发短信通知。

## 核心定位

**开源、IM 原生、AI 原生的告警处置平台。**

| 支柱 | 说明 |
|------|------|
| **开源自托管** | 数据不出企业网络，MIT 协议 |
| **IM 原生** | 钉钉/飞书/企微深度协同（本土空白） |
| **AI 原生** | LLM 作为横向 Copilot 贯穿分诊/诊断/复盘 |

## 当前阶段

📋 **完整需求与架构设计阶段** —— 先想清楚"应该是什么"，不切分 MVP、不设版本里程碑。需求、数据模型、架构、技术选型已成型。

## 文档导航

### 总览文档
| 文档 | 说明 |
|------|------|
| [`docs/PRD.md`](docs/PRD.md) | 产品需求文档 —— 15+2 能力域完整需求 |
| [`docs/data-model.md`](docs/data-model.md) | 核心数据模型 + RBAC 权限模型 |
| [`docs/architecture.md`](docs/architecture.md) | 系统架构设计 |
| [`docs/tech-stack.md`](docs/tech-stack.md) | 技术选型说明 |
| [`docs/development.md`](docs/development.md) | **开发流程** —— worktree 工作模式、特性分支、提交规范（禁用 chore） |
| [`docs/competitive-analysis.md`](docs/competitive-analysis.md) | 竞品分析（PagerDuty/incident.io/Rootly 等） |
| [`docs/personas.md`](docs/personas.md) | 目标用户画像 |

### 能力域详细设计（`docs/capabilities/`）
| 文档 | 覆盖能力域 |
|------|-----------|
| [`01-ingestion-normalization.md`](docs/capabilities/01-ingestion-normalization.md) | 1-2 接入与归一化 |
| [`02-triage-routing.md`](docs/capabilities/02-triage-routing.md) | 3-4 分诊降噪与路由 |
| [`03-scheduling-escalation.md`](docs/capabilities/03-scheduling-escalation.md) | 5-6 排班与升级 |
| [`04-notification.md`](docs/capabilities/04-notification.md) | 7 通知 |
| [`05-im-chatops.md`](docs/capabilities/05-im-chatops.md) | 8 IM 协同 ★ |
| [`06-runbook.md`](docs/capabilities/06-runbook.md) | 9 Runbook 处置 |
| [`07-timeline-ai.md`](docs/capabilities/07-timeline-ai.md) | 10-11 时间线与 AI |
| [`08-postmortem.md`](docs/capabilities/08-postmortem.md) | 12 复盘 |
| [`09-admin-rbac.md`](docs/capabilities/09-admin-rbac.md) | 13 管理与 RBAC |
| [`10-integrations-analytics.md`](docs/capabilities/10-integrations-analytics.md) | 14-15 集成与报表 |

## License

[MIT](LICENSE)
