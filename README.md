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

🚀 **核心功能实现阶段** —— 六大引擎（接入/分诊/排班/升级/通知/IM）与管理面已落地，服务自动供给/治理（方案C）、AI Copilot、复盘持续演进。剩余待办见 [`docs/backlog.md`](docs/backlog.md)。

### 快速上手

```bash
cp .env.example .env            # 按需改凭证、配 IM/LLM
docker compose up -d            # postgres(pgvector) + redis + vigil
docker compose exec vigil vigil migrate   # 建表 + 启用 pgvector
open http://localhost:8080      # Web UI
open http://localhost:8080/docs # Swagger API 文档
```

> 部署/升级/备份/排障步骤见 [`docs/operations.md`](docs/operations.md)（形态决策见 [ADR-0031](docs/adr/0031-single-binary-compose-helm.md)）。pgvector 是硬前置（Postgres 需装扩展，推荐 `pgvector/pgvector:pg16`）。

> ⚠️ **安全警告（自托管必读）**
>
> **JWT 登录态已就绪**（`POST /api/v1/auth/login`）。生产部署步骤：
> 1. 设置 `VIGIL_AUTH_JWT_SECRET`（必填，强随机字符串，用于 JWT 签名）；
> 2. 保持 `VIGIL_AUTH_ENABLED=true`（**默认已开启**，强制业务 API 身份解析，勿在生产关闭）；
> 3. 首次启动自动创建默认管理员 **admin / changeme**，**请立即改密**。
>
> 鉴权双轨：
> - **JWT（推荐）**：前端登录后带 `Authorization: Bearer <token>`，`AUTH_ENABLED=true` 时优先校验；
> - **`X-Vigil-User-ID` 头（降级兼容）**：仅 `AUTH_ENABLED=false` 时生效的遗留链路，**可被伪造**，仅限内网/试用显式开启。
>
> **若显式设置 `AUTH_ENABLED=false`，切勿将 API 暴露到公网或不受信网络**。仍应通过反向代理 + 网络策略（防火墙/VPN/内网）限制访问。
>
> **API Key（程序化接入）已就绪**：设置页创建后，请求带 `X-Vigil-Key: <token>` 头即可鉴权，明文仅创建时展示一次。审计日志同样已就绪（设置页可查看敏感操作留痕）。

## 文档导航

文档主干为两部分:**架构全景** + **架构决策记录(ADR)**,辅以四份活文档。

| 入口 | 说明 |
|------|------|
| [`docs/architecture.md`](docs/architecture.md) | **系统架构全景** —— 产品定位、组件结构、核心引擎、数据流、横切关注点 |
| [`docs/adr/`](docs/adr/) | **架构决策记录** —— 一决策一文件,回答"为什么这么定"([索引](docs/adr/README.md)) |
| [`docs/operations.md`](docs/operations.md) | 运维手册 —— 部署 / 升级 / 备份回滚 / 故障排查 |
| [`docs/backlog.md`](docs/backlog.md) | 待办单一信源 —— 暂不做(含重启前置)/ 待规划 |
| [`docs/design/`](docs/design/) | 功能设计 —— docs-driven 的落笔处(含模板) |
| [`docs/known-issues.md`](docs/known-issues.md) | 已知未修缺陷与限制 |

实体字段以 `ent/schema/` 为准,权限点以 [`internal/auth/permission.go`](internal/auth/permission.go) 为准,开发流程与命令见 [`AGENTS.md`](AGENTS.md)。

常用决策速查:

| 想了解 | 去哪里 |
|--------|--------|
| 产品定位与非目标 | [ADR-0002](docs/adr/0002-product-positioning.md) |
| 技术选型(Go/Echo/ent/Asynq/Postgres) | [ADR-0003](docs/adr/0003-backend-language-go.md)～[0009](docs/adr/0009-pluggable-integrations.md) |
| 接入/分诊/路由 | [ADR-0010](docs/adr/0010-event-incident-separation.md)～[0014](docs/adr/0014-service-auto-provisioning.md) |
| 排班/升级/通知 | [ADR-0015](docs/adr/0015-schedule-realtime-no-snapshot.md)～[0017](docs/adr/0017-notification-fallback-chain.md) |
| IM 协同 ★ | [ADR-0018](docs/adr/0018-im-same-rbac-as-web.md)～[0020](docs/adr/0020-responder-temp-grant.md) |
| Runbook / AI / 复盘 | [ADR-0021](docs/adr/0021-runbook-two-tier.md)～[0026](docs/adr/0026-postmortem-ai-draft.md) |
| RBAC / 软隔离 / 集成 | [ADR-0027](docs/adr/0027-rbac-permissions-roles.md)～[0030](docs/adr/0030-integrations-encrypted-openapi.md) |
| 部署 / 迁移 / 自监控 | [ADR-0031](docs/adr/0031-single-binary-compose-helm.md)～[0033](docs/adr/0033-selfmon-and-auth.md) |
| UI/UX / 开发流程 | [ADR-0034](docs/adr/0034-uiux-oncall-principles.md)、[ADR-0035](docs/adr/0035-dev-workflow-gates.md) |

## License

[MIT](LICENSE)
