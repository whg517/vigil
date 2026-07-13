# Vigil 系统架构

| 字段 | 内容 |
|------|------|
| **状态** | Living(随实现演进) |
| **更新** | 2026-07-14 |
| **决策** | 全部设计取舍见 [`adr/`](./adr/) |

> 本文档是 Vigil 的**架构全景**:产品定位、组件结构、数据流、核心引擎、横切关注点。
> 它只回答"系统长什么样、怎么运转";每个"为什么这么定"由对应 [ADR](./adr/) 承载,本文以链接引用,不重复理由。
> 实体字段以 `ent/schema/` 为准,权限点以 `internal/auth/permission.go` 为准。

---

## 一、产品定位

**Vigil(守夜人)** —— 开源、IM 原生、AI 原生的**告警处置平台**。它不做"发现问题"(监控采集),只解决"**告警之后的下一步**":告警产生到问题被真正解决之间那段无人管的链条——谁响应、怎么通知、多人怎么协同、按什么步骤处置、谁升级、怎么复盘。愿景:**让每一条告警都被妥善接力到终点。**

三大差异化支柱(市场上三者同时满足者无同类):

1. **自托管** —— 数据不出企业网络,3 个容器即可起步。
2. **本土 IM 原生** —— 钉钉/飞书是**协同工作面**而非通知通道,是最核心差异化。
3. **LLM 横向贯穿** —— 分诊/诊断/复盘全程 AI 贯穿,是底层能力而非付费墙。

范围与取舍详见 [ADR-0002 产品定位与非目标](./adr/0002-product-positioning.md)(不做监控采集 / SOC / SaaS 硬多租户 / 无人值守自动修复)。

---

## 二、架构目标

架构服务于以下核心要求:

1. **可自托管、轻量** —— 3 容器起步(vigil + postgres + redis)。→ [ADR-0031](./adr/0031-single-binary-compose-helm.md)
2. **事件驱动 + 全异步** —— 接入、升级计时、排班计算、通知重试都是异步任务。→ [ADR-0007](./adr/0007-async-tasks-asynq.md)
3. **IM-first** —— IM 操作与 Web 操作走同一套业务逻辑与鉴权。→ [ADR-0018](./adr/0018-im-same-rbac-as-web.md)
4. **可插拔** —— 告警源、通知通道、执行器、LLM、IM 平台都能扩展不动核心。→ [ADR-0009](./adr/0009-pluggable-integrations.md)
5. **自身可观测** —— 暴露 metrics/health,能被自家告警监控(吃自己狗粮)。→ [ADR-0033](./adr/0033-selfmon-and-auth.md)
6. **水平可扩展** —— 核心无状态,靠 Redis 队列协调多实例。

**非功能基线**(目标值,来源 [ADR-0002](./adr/0002-product-positioning.md);回归验证依赖 e2e 与 `/metrics`):

| 指标 | 目标 |
|------|------|
| 接入吞吐 | ≥ 1000 events/min |
| 通知送达延迟 | P95 < 5s |
| 降噪效果 | 无意义打扰次数 ↓ 50%+(Event→通知的降噪率见 analytics) |
| 部署门槛 | Docker Compose 一键起步(硬指标) |

---

## 三、总体架构(C4 - Container 视图)

```
┌──────────────────────────────────────────────────────────────────────┐
│                          外部系统                                       │
│  Prometheus      Grafana      邮件      自研监控                        │
└───────┬─────────────┬───────────┬──────────┬─────────────────────────┘
        │ webhook     │ webhook   │ SMTP     │ API                       ▼
┌──────────────────────────────────────────────────────────────────────┐
│                    Vigil 平台(单二进制,多模块)                        │
│  ┌──────────────────────── 接入层 ───────────────────────────────┐   │
│  │  HTTP API(REST)   WebSocket   Webhook Receivers   SMTP In      │   │
│  └─────────────────────────────┬──────────────────────────────────┘   │
│  ┌──────────────────── 核心服务层(业务逻辑)────────────────────┐   │
│  │  Ingestion → Triage → Routing                                    │   │
│  │  Schedule · Escalation · Incident · Runbook · AI · Postmortem   │   │
│  └─────────────────────────────┬──────────────────────────────────┘   │
│  ┌──────────────────── 异步任务层(Asynq Worker Pool)───────────┐   │
│  │  延迟队列(升级/重试) · 定时任务(排班/报表) · 事件任务 · 死信 │   │
│  └─────────────────────────────┬──────────────────────────────────┘   │
│  ┌──────────────── 集成层(可插拔)· IM 层(双向)───────────────┐   │
│  │  Adapters  Notifiers  Executors  LLM Providers  IMBot(钉/飞)   │   │
│  └──────────────────────────────────────────────────────────────────┘ │
└──────────┬──────────────────────────────────────┬────────────────────┘
           ▼                                       ▼
     ┌──────────────┐                       ┌──────────────┐
     │ PostgreSQL   │                       │    Redis     │
     │ (持久化,     │                       │(缓存/队列/锁) │
     │  含 pgvector)│                       │              │
     └──────────────┘                       └──────────────┘
```

### 分层职责

| 层 | 职责 | 关键 ADR |
|----|------|---------|
| **接入层** | 协议入口:HTTP API、WebSocket、Webhook、SMTP;只接收不采集 | [0011](./adr/0011-ingestion-decoupled-idempotent.md) |
| **核心服务层** | 归一化、分诊、路由、排班、升级、事件管理、处置、复盘 | [0012](./adr/0012-triage-three-stage-pipeline.md) · [0015](./adr/0015-schedule-realtime-no-snapshot.md) · [0016](./adr/0016-escalation-asynq-delayed.md) |
| **异步任务层** | worker 池执行延迟/定时/事件任务 | [0007](./adr/0007-async-tasks-asynq.md) |
| **集成层** | 可插拔适配器:告警源、通知、执行器、LLM | [0009](./adr/0009-pluggable-integrations.md) |
| **IM 层** | IM 双向通信(IM-first 承载) | [0018](./adr/0018-im-same-rbac-as-web.md) · [0019](./adr/0019-imbot-pluggable-degradation.md) |

---

## 四、核心数据模型:Event / Incident 分离

Vigil 数据模型的基石是 **Event 与 Incident 分离**([ADR-0010](./adr/0010-event-incident-separation.md)):

- **Event** —— 原始告警信号:海量、不可变、只追加。
- **Incident** —— 值得人介入的处理单元:少量、有上下文、有状态机与责任人。

分诊层把 N 个 Event 聚合成 1 个 Incident。Incident 状态机:

```
triggered ──ack──▶ acked ──resolve──▶ resolved ──▶ closed
    │                 ▲
    └──超时──▶ escalated ──ack──┘
```

> 硬约束:任何状态变更必须产生 TimelineItem(全程留痕,[ADR-0022](./adr/0022-aiinsight-hitl-evidence.md))。

实体全貌以 `ent/schema/` 为准(当前 32 个实体 + `vector.go` 辅助类型;计数随演进变化,以 schema 为唯一信源)。关键实体:User / Team / Role / RoleBinding(身份与权限;权限点 Permission 是 `internal/auth/permission.go` 的代码枚举,非实体)、Service / Integration / Schedule / EscalationPolicy / Runbook / NotificationRule(配置蓝图)、RawEvent / Event / Incident / TimelineItem / IncidentAction / Postmortem / AIInsight(运行时实例)。

---

## 五、核心引擎与数据流

复杂度集中在几个"引擎",它们决定产品能否立住。

### 5.1 接入流水线(Ingestion) → [ADR-0011](./adr/0011-ingestion-decoupled-idempotent.md)

```
Webhook 接收 → 校验+落 raw → 入队 → [归一化] → [去重] → [抑制] → [聚合] → [路由] → 创建/合并 Incident
     202                          每步独立 Asynq 任务,失败→死信可重放
```

接收与处理解耦:Receiver 只校验 token + 落原始 payload + 入队,秒级返回 202;归一化及后续每步是独立 Asynq 任务。以 `source_event_id` 为幂等键。绝不在接收阶段同步处理(下游慢会致告警源超时丢告警);背压/熔断时 payload 先落库,恢复后回灌。

### 5.2 分诊引擎(Triage) → [ADR-0012](./adr/0012-triage-three-stage-pipeline.md) · [ADR-0013](./adr/0013-deterministic-routing.md)

固定顺序 **去重 → 抑制 → 相关性聚合 → 路由**:

| 机制 | 实现 |
|------|------|
| 去重 | Redis `dedup:{dedup_key}` SETNX + 窗口(默认 5min,`VIGIL_TRIAGE_DEDUP_WINDOW`) |
| 抑制 | `SuppressionRule`(adhoc / maintenance),`preserve_critical` 守卫 critical 不被抑制 |
| 聚合 | 键 `service+severity`,5min 窗口内并入活跃 Incident(按窗口查询活跃单合并,普通查询实现) |
| 路由 | 以 Service 为锚点,四级确定性裁决;未命中进 `unrouted` 池(可申诉);可自动供给 Service([ADR-0014](./adr/0014-service-auto-provisioning.md)) |

### 5.3 排班引擎(Schedule) → [ADR-0015](./adr/0015-schedule-realtime-no-snapshot.md)

**实时回答"此刻谁在班"**:Schedule 是纯蓝图,不存值班人,按 `timezone + layers + Rotation + Override` 实时计算,分钟级缓存 Redis。排班变更立即生效,无快照一致性问题。支持 calendar / rotation / follow_the_sun。

### 5.4 升级引擎(Escalation)★ → [ADR-0016](./adr/0016-escalation-asynq-delayed.md)

oncall 的灵魂——"没人理找下一个"。Incident 创建即 `asynq.ProcessIn(delay)` 排升级延迟任务;ack(Web/IM)经事件总线 `DeleteTask` 取消,并用 incident 状态作守卫(已 ack/resolved 即使误触发也不动作)。幂等键 `esc:{inc}:{level}:{repeatSeq}`,高优先级队列 + 高 MaxRetry,worker 重启不丢。

### 5.5 通知引擎(Notification) → [ADR-0017](./adr/0017-notification-fallback-chain.md)

`msg.Channels` 是**有序降级链**(非并联):对每 target 逐通道尝试,首个成功即停。送达三态落库(`pending|sent|failed|suppressed`);整条链全失败→兜底告警 org_admin(走非 IM 通道)。30s 窗口聚合防轰炸,critical 立即单发;quiet_hours 支持 critical 穿透。内置通道为 im / email / webhook 三种(电话/SMS 已移除,[ADR-0037](./adr/0037-trim-deferred-features.md);配置残留的未知通道名按跳过处理,降级链继续)。

### 5.6 IM 协同层(ChatOps)★ 差异化核心 → [ADR-0018](./adr/0018-im-same-rbac-as-web.md) · [ADR-0019](./adr/0019-imbot-pluggable-degradation.md)

IM 双向通信 + 状态同步:IM 回调 → 映射 `im_accounts`→User → **走与 Web 完全相同的 RBAC 鉴权** → 调核心服务(复用 `internal/incident/service.go`)。交互卡片按权限渲染(无权按钮不显示),状态变化时原地更新卡片;平台能力参差走降级矩阵(飞书全 / 钉钉部分)。作战室能力已整体移除,协同由「工作群 + 交互卡片 + 实时刷新」承载。

### 5.7 端到端:一条告警的生命周期

```
1. Prometheus 触发 → POST /api/v1/webhook/{integration_id}
2. 接入层校验 token → 落 raw → 入 ingestion 队列 → 202
3. worker: 归一化为 Event → 去重 → 抑制 → 聚合到 Incident → 路由匹配 Service
4. Incident 进入 triggered → 入队升级延迟任务(Asynq ProcessIn)
5. 升级到期 → 排班引擎算在班人 → 通知引擎逐通道分发
6. IM 卡片送达值班人 → 点 [ack] → IM 层映射→鉴权→核心服务 ack
7. ack 取消后续升级 → Incident 进入 acked → 时间线记录
8. 处置:展示 runbook / 诊断只读执行 / 处置写操作须确认([ADR-0021](./adr/0021-runbook-two-tier.md))
9. 标记 resolved → AI 起草复盘草稿 → 人工校对 → published
10. 闭环:复盘进知识库,反哺相似事件检索(pgvector,[ADR-0024](./adr/0024-similar-incident-pgvector.md))
```

---

## 六、代码结构

单二进制内按领域模块组织(`internal/` 下 35 个模块):

```
cmd/vigil/          # 入口(+ genmigration / swagfix / verify-ai)
internal/
├── server/         # HTTP/WS 装配、路由、中间件
├── ingestion/      # 接入流水线      ├── triage/       # 分诊 + 路由引擎
├── schedule/       # 排班引擎        ├── escalation/   # 升级引擎
├── notification/   # 通知引擎        ├── incident/     # 事件管理 + 状态机 + 临时授权
├── event/          # Event 域        ├── service/      # 服务目录
├── servicesync/    # 服务主动同步(方案C)
├── runbook/        # 处置执行        ├── postmortem/   # 复盘
├── ai/             # AI Copilot(LLM Provider + 成本控制 + 诊断)
├── im/             # IM 协同层(IMBot 适配)
├── auth/           # RBAC(permission.go 权限点枚举)
├── integration/ webhook/ subscription/ ticket/ credential/ crypto/   # 集成与凭据
├── analytics/ metrics/ selfmon/       # 报表 · 指标 · 自监控
├── queue/          # Asynq 任务定义 + handler + worker
├── store/          # ent 数据访问     ├── migrate/     # 迁移
├── ws/             # WebSocket 广播   ├── timeline/    # 时间线
├── config/ logger/ errs/ httputil/ middleware/   # 基础设施
└── web/            # 前端 embed
ent/schema/         # 实体定义(当前 32 个,以 schema 为准;改后须 go generate ./ent/...)
web/                # 前端(React + Vite + shadcn/ui + Tailwind + i18n)
deploy/helm/        # Helm Chart    docs/  # 本文档 + adr/
```

---

## 七、横向关注点

### 7.1 鉴权与多团队隔离 → [ADR-0027](./adr/0027-rbac-permissions-roles.md) · [ADR-0028](./adr/0028-single-org-soft-isolation.md)

统一鉴权中间件解析 `(user, action, resource)` → 查 RBAC。Permission 是系统固定权限点枚举,Role 由使用者自由组合,RoleBinding 带 scope(org/team),org+team 权限**取并集**。资源归属即作用域(操作 Incident 取 `incident.team_id`)。团队软隔离,跨团队靠 add_responder 拉人 + 事件级临时授权([ADR-0020](./adr/0020-responder-temp-grant.md)),团队树不继承权限。

### 7.2 配置驱动 → [ADR-0009](./adr/0009-pluggable-integrations.md)

告警源、通知通道、IM 平台、LLM provider 的启停与参数全走配置 + 数据库,不写死;插件注册表启动时按配置装载。

### 7.3 可观测性(吃自己狗粮) → [ADR-0033](./adr/0033-selfmon-and-auth.md)

`/metrics`(HTTP 请求量与延迟直方图、告警接入量、事件/升级/通知计数、队列分状态 gauge `vigil_queue_tasks{queue,state}`——含死信 archived、LLM 调用与 token 成本、自监控告警计数)、`/health`、结构化日志(贯穿 `incident_id`/`event_id`)、Asynqmon 任务面板。自监控(selfmon)在队列积压/通知失败率超阈、或队列探测**连续失败**(Redis 整体故障信号)时自触发告警,走**排除 IM 的独立通道**(被监控的正是通知链路)。selfmon 与进程共生死——外部监控接入(必抓指标/告警规则/部署建议)见 [operations.md「外部监控接入」](./operations.md#8-外部监控接入谁来监控守夜人)。

### 7.4 可靠性

| 风险 | 对策 |
|------|------|
| Redis 宕机 | Asynq 状态存于 Redis,建议部署方开启 AOF/RDB 持久化(Redis HA 非 Vigil 内置,由部署方自备);接入层降级(先落 PostgreSQL 原始事件,恢复后回灌) |
| 任务重复投递(at-least-once) | 所有 handler 幂等:升级 `esc:{inc}:{level}`、通知 `notification_id`、流水线 `source_event_id` |
| 通知通道故障 | 逐通道兜底降级链;整链失败兜底告警 org_admin |
| worker 崩溃 | Asynq 任务持久化 + 至少一次投递,重启自动恢复 |
| LLM 不可用 | AI 功能降级(非核心链路),不影响告警主流程 |
| 数据无界增长 | Event/RawEvent 保留清理巡检(默认 90/30 天,分页删除、保护活跃 Incident 证据);其余表的保留策略与按月分区路线见 [ADR-0039](./adr/0039-data-lifecycle.md) |

---

## 八、部署拓扑 → [ADR-0031](./adr/0031-single-binary-compose-helm.md)

- **单机(默认)**:Docker Compose 3 容器(vigil = API+worker+前端,postgres,redis)。适用中小团队/试用/PoC。
- **集群**:Kubernetes + Helm(`deploy/helm/`)可部署。当前经验证的形态是单进程单副本(API+worker 同进程);vigil-api 与 vigil-worker 拆分独立扩缩(核心无状态,状态在 PostgreSQL/Redis,多实例 WebSocket 广播走 Redis pub/sub)为路线图方向,尚未验证。

迁移与回滚见 [ADR-0032](./adr/0032-migration-backup-restore.md)(ent auto-migrate + 备份即回滚,不做逆向迁移)。

---

## 九、决策索引

按主题速查全部 ADR:

| 主题 | ADR |
|------|-----|
| 定位 | [0002](./adr/0002-product-positioning.md) |
| 技术栈 | [0003](./adr/0003-backend-language-go.md) [0004](./adr/0004-web-framework-echo.md) [0005](./adr/0005-data-access-ent-atlas.md) [0006](./adr/0006-primary-store-postgresql.md) [0007](./adr/0007-async-tasks-asynq.md) [0008](./adr/0008-frontend-vite-shadcn.md) [0009](./adr/0009-pluggable-integrations.md) |
| 接入分诊 | [0010](./adr/0010-event-incident-separation.md) [0011](./adr/0011-ingestion-decoupled-idempotent.md) [0012](./adr/0012-triage-three-stage-pipeline.md) [0013](./adr/0013-deterministic-routing.md) [0014](./adr/0014-service-auto-provisioning.md) [0038](./adr/0038-smtp-inbound.md) |
| 排班升级 | [0015](./adr/0015-schedule-realtime-no-snapshot.md) [0016](./adr/0016-escalation-asynq-delayed.md) |
| 通知 | [0017](./adr/0017-notification-fallback-chain.md) |
| IM 协同 | [0018](./adr/0018-im-same-rbac-as-web.md) [0019](./adr/0019-imbot-pluggable-degradation.md) [0020](./adr/0020-responder-temp-grant.md) |
| Runbook/AI/复盘 | [0021](./adr/0021-runbook-two-tier.md) [0022](./adr/0022-aiinsight-hitl-evidence.md) [0023](./adr/0023-llm-provider-cost-control.md) [0024](./adr/0024-similar-incident-pgvector.md) [0025](./adr/0025-no-auto-retrain.md) [0026](./adr/0026-postmortem-ai-draft.md) |
| 治理/RBAC/集成 | [0027](./adr/0027-rbac-permissions-roles.md) [0028](./adr/0028-single-org-soft-isolation.md) [0029](./adr/0029-dual-audit-no-silent-truncation.md) [0030](./adr/0030-integrations-encrypted-openapi.md) |
| 部署运维 | [0031](./adr/0031-single-binary-compose-helm.md) [0032](./adr/0032-migration-backup-restore.md) [0033](./adr/0033-selfmon-and-auth.md) |
| UX/流程 | [0034](./adr/0034-uiux-oncall-principles.md) [0035](./adr/0035-dev-workflow-gates.md) |

完整索引见 [`adr/README.md`](./adr/README.md)。
