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

🚀 **核心功能实现阶段** —— P1 四项（钉钉适配器 / 抑制+静默+聚合 / 通知模板 / pgvector+LLM 成本控制）已落地，P2（服务目录/排班/OpenAPI/前端全页面/部署文档）已完成。

### 快速上手

```bash
cp .env.example .env            # 按需改凭证、配 IM/LLM
docker compose up -d            # postgres(pgvector) + redis + vigil
docker compose exec vigil vigil migrate   # 建表 + 启用 pgvector
open http://localhost:8080      # Web UI
open http://localhost:8080/docs # Swagger API 文档
```

> 部署细节见 [`docs/deployment.md`](docs/deployment.md)。pgvector 是硬前置（Postgres 需装扩展，推荐 `pgvector/pgvector:pg16`）。

> ⚠️ **安全警告（自托管必读）**
>
> **JWT 登录态已就绪**（`POST /api/v1/auth/login`）。生产部署步骤：
> 1. 设置 `VIGIL_AUTH_JWT_SECRET`（必填，强随机字符串，用于 JWT 签名）；
> 2. 设置 `VIGIL_AUTH_ENABLED=true`（开启业务 API 强制身份解析）；
> 3. 首次启动自动创建默认管理员 **admin / changeme**，**请立即改密**。
>
> 鉴权双轨：
> - **JWT（推荐）**：前端登录后带 `Authorization: Bearer <token>`，`AUTH_ENABLED=true` 时优先校验；
> - **`X-Vigil-User-ID` 头（降级兼容）**：`AUTH_ENABLED=false` 时的遗留链路，**可被伪造**，仅用于内网/试用。生产环境务必保持 `AUTH_ENABLED=true`。
>
> **`AUTH_ENABLED=false`（默认）时切勿将 API 暴露到公网或不受信网络**。仍应通过反向代理 + 网络策略（防火墙/VPN/内网）限制访问。
>
> API Key（程序化接入）与审计日志为后续特性（见 [`docs/gap-analysis.md`](docs/gap-analysis.md)）。

## 文档导航

### 总览文档
| 文档 | 说明 |
|------|------|
| [`docs/PRD.md`](docs/PRD.md) | 产品需求文档 —— 15+2 能力域完整需求 |
| [`docs/data-model.md`](docs/data-model.md) | 核心数据模型 + RBAC 权限模型 |
| [`docs/architecture.md`](docs/architecture.md) | 系统架构设计 |
| [`docs/deployment.md`](docs/deployment.md) | **部署指南** —— Docker Compose 一键起、pgvector 前置、生产 checklist |
| [`docs/ui-ux.md`](docs/ui-ux.md) | UI/UX 设计 —— 设计系统、关键页面、IM 卡片、移动端 |
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
