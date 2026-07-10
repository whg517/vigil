# 架构决策记录(ADR)

本目录以 **一决策一文件** 的方式记录 Vigil 的架构决策:每份回答"选了什么 / 为什么 / 否决了什么"。全景视图见 [`../architecture.md`](../architecture.md);格式约定见 [ADR-0001](0001-record-architecture-decisions.md)。

> 编号单调递增、永不复用。改变一个已有决策时,新增一份 `Superseded` 关系的 ADR,不原地改写。

## 索引

### 元与定位
| # | 标题 | 状态 |
|---|------|------|
| [0001](0001-record-architecture-decisions.md) | 采用 ADR 记录架构决策 | Accepted |
| [0002](0002-product-positioning.md) | 产品定位与非目标 | Accepted |
| [0037](0037-trim-deferred-features.md) | 收敛延期功能:移除电话/SMS、企微、Jira/禅道与 Zabbix/云监控占位 | Accepted |

### 技术选型
| # | 标题 | 状态 |
|---|------|------|
| [0003](0003-backend-language-go.md) | 后端语言选 Go | Accepted |
| [0004](0004-web-framework-echo.md) | Web 框架选 Echo | Accepted |
| [0005](0005-data-access-ent-atlas.md) | 数据访问选 ent + Atlas | Accepted |
| [0006](0006-primary-store-postgresql.md) | 主存储选 PostgreSQL(含 pgvector 前置) | Accepted |
| [0007](0007-async-tasks-asynq.md) | 异步任务选 Asynq + 幂等约定 | Accepted |
| [0008](0008-frontend-vite-shadcn.md) | 前端栈 React+Vite+shadcn/ui+Tailwind | Accepted |
| [0009](0009-pluggable-integrations.md) | 可插拔集成:5 类扩展点 | Accepted |

### 接入与分诊
| # | 标题 | 状态 |
|---|------|------|
| [0010](0010-event-incident-separation.md) | Event 与 Incident 分离 | Accepted |
| [0011](0011-ingestion-decoupled-idempotent.md) | 接入解耦 + 先落 raw + 幂等 | Accepted |
| [0012](0012-triage-three-stage-pipeline.md) | 三层分诊管线:去重/抑制/聚合 | Accepted |
| [0013](0013-deterministic-routing.md) | 确定性路由裁决 + 未路由池可申诉 | Accepted |
| [0014](0014-service-auto-provisioning.md) | Service 自动供给(方案C) | Accepted |

### 排班与升级
| # | 标题 | 状态 |
|---|------|------|
| [0015](0015-schedule-realtime-no-snapshot.md) | 排班蓝图 + 实时计算(不存快照) | Accepted |
| [0016](0016-escalation-asynq-delayed.md) | 升级引擎 Asynq 延迟任务 + 状态守卫 | Accepted |

### 通知
| # | 标题 | 状态 |
|---|------|------|
| [0017](0017-notification-fallback-chain.md) | 通知逐通道兜底降级链 + 送达三态 + 聚合 | Accepted |

### IM 协同
| # | 标题 | 状态 |
|---|------|------|
| [0018](0018-im-same-rbac-as-web.md) | IM 走与 Web 相同 RBAC 链路 | Accepted |
| [0019](0019-imbot-pluggable-degradation.md) | IMBot 可插拔 + 平台能力降级矩阵 | Accepted |
| [0020](0020-responder-temp-grant.md) | 拉人即事件级临时授权 | Accepted |
| [0036](0036-remove-war-room.md) | 移除作战室能力 | Accepted |

### Runbook / AI / 复盘
| # | 标题 | 状态 |
|---|------|------|
| [0021](0021-runbook-two-tier.md) | Runbook 诊断只读 / 处置写两档 | Accepted |
| [0022](0022-aiinsight-hitl-evidence.md) | AIInsight 横向 + HITL + 强制 evidence | Accepted |
| [0023](0023-llm-provider-cost-control.md) | LLMProvider 抽象 + 成本三闸 + 可降级 | Accepted |
| [0024](0024-similar-incident-pgvector.md) | 相似事件检索 pgvector 主路径 + LIKE 降级 | Accepted |
| [0025](0025-no-auto-retrain.md) | 智能降噪不做自动回训(明确否决) | Accepted |
| [0026](0026-postmortem-ai-draft.md) | 复盘 AI 起草 + 逐字段人工校对 | Accepted |

### 治理 / RBAC / 集成
| # | 标题 | 状态 |
|---|------|------|
| [0027](0027-rbac-permissions-roles.md) | RBAC 权限点枚举 + 自配置角色 | Accepted |
| [0028](0028-single-org-soft-isolation.md) | 单组织多团队软隔离 + 团队树不继承 | Accepted |
| [0029](0029-dual-audit-no-silent-truncation.md) | 双轨审计 + CSV 导出不静默截断 | Accepted |
| [0030](0030-integrations-encrypted-openapi.md) | 凭据加密 + 四方向集成 + code-first OpenAPI | Accepted |

### 部署 / 运维
| # | 标题 | 状态 |
|---|------|------|
| [0031](0031-single-binary-compose-helm.md) | 单二进制 embed + Compose/Helm | Accepted |
| [0032](0032-migration-backup-restore.md) | 迁移与回滚:备份恢复(不做逆向迁移) | Accepted |
| [0033](0033-selfmon-and-auth.md) | 自监控三红线 + 鉴权 Bearer JWT | Accepted |

### 前端 UX / 工程流程
| # | 标题 | 状态 |
|---|------|------|
| [0034](0034-uiux-oncall-principles.md) | UI/UX 设计原则(oncall 场景) | Accepted |
| [0035](0035-dev-workflow-gates.md) | 开发工作流:worktree 闭环 + 提交规范 + 门禁 | Accepted |
