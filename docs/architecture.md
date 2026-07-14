# Vigil 系统架构

| 字段 | 内容 |
|------|------|
| **状态** | Living（随实现演进） |
| **回答** | 系统长什么样、怎么运转 |
| **不回答** | 要做什么 → [`requirements.md`](./requirements.md)；为什么这么定 → [`adr/`](./adr/)；怎么部署运维 → [`operations.md`](./operations.md)；怎么扩展 → [`extending.md`](./extending.md)；怎么开发提交 → [`AGENTS.md`](../AGENTS.md) |

> **单一信源声明**：实体与字段以 `ent/schema/` 为准，权限点以 `internal/auth/permission.go` 为准；本文不复制清单，只讲结构与机制。功能/非功能需求编号（FR-xx / NFR-xx）指向 [`requirements.md`](./requirements.md)，典型场景见 [`user-stories/`](./user-stories/)。

---

## 0. 读者导航

| 你是谁 | 建议路径 |
|--------|---------|
| **评估选型的架构师** | §1 目标 → §2 总体结构 → §4 核心引擎 → §7 横切关注点 → §8 部署；配合 [`requirements.md`](./requirements.md)（能力范围与非目标）与 [`adr/README.md`](./adr/README.md)（决策理由） |
| **接入部署的运维** | §2.3 运行形态 → §7.3 可观测 → §7.4 可靠性 → §8 部署；随后转 [`operations.md`](./operations.md)（可执行步骤） |
| **上手的开发者** | §2 总体结构 → §3 数据模型 → §4 所在能力域的引擎小节 → §9 代码地图；随后转 [`AGENTS.md`](../AGENTS.md)（命令与工作流）与 [`extending.md`](./extending.md)（扩展点） |

章节总览：1 架构约束与目标 · 2 总体结构 · 3 核心数据模型 · 4 核心引擎 · 5 一条告警的一生 · 6 集成面 · 7 横切关注点 · 8 部署形态与演进 · 9 代码地图 · 10 决策追溯。

---

## 1. 架构约束与目标

**Vigil（守夜人）** —— 开源、IM 原生、AI 原生的告警处置平台，只解决"告警之后的下一步"（谁响应、怎么通知、多人怎么协同、按什么步骤处置、谁升级、怎么复盘），不做监控采集。定位、三大差异化支柱（自托管 / 本土 IM 原生 / LLM 横向贯穿）与非目标的现行口径见 [`requirements.md`](./requirements.md)，历史裁决见 [ADR-0002](./adr/0002-product-positioning.md)。

架构服务于六条目标：

| # | 目标 | 决策 |
|---|------|------|
| 1 | **可自托管、轻量** —— 3 容器起步（vigil + postgres + redis） | [ADR-0031](./adr/0031-single-binary-compose-helm.md) |
| 2 | **事件驱动 + 全异步** —— 接入、升级计时、通知重试都是异步任务 | [ADR-0007](./adr/0007-async-tasks-asynq.md) |
| 3 | **IM-first** —— IM 操作与 Web 走同一套业务逻辑与鉴权 | [ADR-0018](./adr/0018-im-same-rbac-as-web.md) |
| 4 | **可插拔** —— 告警源、通知通道、执行器、LLM、IM 平台扩展不动核心 | [ADR-0009](./adr/0009-pluggable-integrations.md) |
| 5 | **自身可观测** —— 暴露 metrics/health，能被自家告警监控（吃自己狗粮） | [ADR-0033](./adr/0033-selfmon-and-auth.md) |
| 6 | **水平可扩展** —— 核心无状态，靠 Redis 队列协调多实例 | [ADR-0031](./adr/0031-single-binary-compose-helm.md) |

**非功能基线**（目标值；完整定义见 [`requirements.md`](./requirements.md) NFR-PERF/NFR-DEP，压测方法与实测数据的单一信源是 [operations.md §10 容量规划](./operations.md#10-容量规划压测方法与基线实测)）：

| 指标 | 目标 | 现状 |
|------|------|------|
| 接入吞吐 | ≥ 1000 events/min | 已实测达标，余量约 2 倍（细节见 operations §10） |
| 通知送达延迟 | P95 < 5s | 内部链路约 0.7s；通道段未实测（待 IM 沙箱） |
| 降噪效果 | 无意义打扰 ↓ 50%+ | 运行期指标，由 analytics 持续度量，非压测可断言 |
| 部署门槛 | Compose 一键起步（硬指标） | 已落地（operations §2） |

技术栈：Go 1.25 + Echo + ent/Atlas + Asynq，React + Vite + shadcn/ui，选型论证见 [ADR-0003](./adr/0003-backend-language-go.md) · [0004](./adr/0004-web-framework-echo.md) · [0005](./adr/0005-data-access-ent-atlas.md) · [0006](./adr/0006-primary-store-postgresql.md) · [0007](./adr/0007-async-tasks-asynq.md) · [0008](./adr/0008-frontend-vite-shadcn.md)。

---

## 2. 总体结构

### 2.1 容器视图

```
 Prometheus / Grafana / 自研监控         邮件系统          程序化调用方          钉钉 / 飞书
        │ webhook                        │ SMTP            │ REST API           ▲ 卡片   │ 回调
        ▼                                ▼                 ▼                    │        ▼
┌─ Vigil 单二进制（API + Worker + 前端 embed）───────────────────────────────────────────────
│
│  接入层         HTTP API（REST） · WebSocket · Webhook Receivers · SMTP In
│                      │  只校验、落库、入队 —— 不做同步处理，也不做监控采集
│  核心服务层     Ingestion → Triage（去重 → 抑制 → 路由 → 聚合成 Incident）
│                 Incident · Schedule · Escalation · Notification · Runbook · AI · Postmortem
│                      │
│  异步任务层     Asynq Worker 池：延迟任务（升级） · 重试 · 定时巡检 · 死信
│                      │
│  集成层 / IM 层  Adapters · Notification Channels · Executors · LLM Providers · IMBot
└───────────────────────────────────────────────────────────────────────────────────────────
        │ 持久化                                        │ 缓存 / 队列 / 分布式锁
        ▼                                               ▼
   PostgreSQL（含 pgvector）                          Redis
```

### 2.2 分层职责与决策映射

| 层 | 职责 | 关键 ADR |
|----|------|---------|
| **接入层** | 协议入口：HTTP API、WebSocket、Webhook、SMTP；只接收不采集 | [0011](./adr/0011-ingestion-decoupled-idempotent.md) · [0038](./adr/0038-smtp-inbound.md) |
| **核心服务层** | 归一化、分诊、路由、排班、升级、事件管理、处置、复盘 | [0012](./adr/0012-triage-three-stage-pipeline.md) · [0015](./adr/0015-schedule-realtime-no-snapshot.md) · [0016](./adr/0016-escalation-asynq-delayed.md) |
| **异步任务层** | worker 池执行延迟/定时/事件任务，at-least-once + 幂等 | [0007](./adr/0007-async-tasks-asynq.md) |
| **集成层** | 可插拔适配器：告警源、通知通道、执行器、LLM | [0009](./adr/0009-pluggable-integrations.md) |
| **IM 层** | IM 双向通信（IM-first 的承载） | [0018](./adr/0018-im-same-rbac-as-web.md) · [0019](./adr/0019-imbot-pluggable-degradation.md) |

存储职责固定：**PostgreSQL 是唯一持久化底座**（关系 + JSONB + pg_trgm/pgvector，一个 PG 内解决，[ADR-0006](./adr/0006-primary-store-postgresql.md)）；缓存、队列、分布式锁等易失负载归 **Redis**（[ADR-0007](./adr/0007-async-tasks-asynq.md)）。

### 2.3 进程与运行形态

- **单二进制多角色**：同一个 Go 二进制承载 REST API、Asynq worker 与前端静态资源（编译期 embed），见 [ADR-0031](./adr/0031-single-binary-compose-helm.md)。
- **队列**：Asynq 三队列按权重调度 `critical(6) / default(3) / low(1)`。核心异步任务：`vigil:normalize`（归一化）、`vigil:triage`（分诊）、`vigil:escalation`（延迟升级，critical 队列）、`vigil:notification:deliver`（通知投递）、`vigil:event_cleanup`（保留期清理，low）、`vigil:metrics_aggregate`（报表快照，low）。另有进程内周期任务（升级对账 sweeper、通知聚合 flusher、保留期巡检、selfmon 巡检、servicesync 同步/清理等）。
- **实时推送**：WebSocket `/ws/incidents/:id`（单事件详情）与 `/ws/dashboard`（大盘），握手鉴权。
- **幂等是全局约定**：Asynq at-least-once 语义下所有 handler 必须幂等，统一幂等键：升级 `esc:{inc}:{level}:{repeatSeq}`、通知 `notif:{id}`、事件流水线 `source_event_id`；新增任务类型必须显式设计幂等键（[ADR-0007](./adr/0007-async-tasks-asynq.md)）。

---

## 3. 核心数据模型

### 3.1 Event / Incident 分离

数据模型的基石（[ADR-0010](./adr/0010-event-incident-separation.md)）：

- **Event** —— 原始告警信号：海量、不可变、只追加；原始 payload 存 `Event.Detail` 不丢。
- **Incident** —— 值得人介入的处理单元：少量、有上下文、有状态机与责任人。

分诊层把 N 个 Event 聚合成 1 个 Incident，人只面对 Incident。

### 3.2 Incident 状态机

```
triggered ──ack──▶ acked ──resolve──▶ resolved ──close──▶ closed
    │                ▲
    └──超时未 ack──▶ escalated ──ack──┘
```

- `resolve` 可从任意活跃态（triggered / escalated / acked）直达 resolved。
- `resolved → closed` 是 closed 的唯一入边，由人工关闭或复盘发布联动触发；**critical 事件须先过复盘闸门**（复盘 published/archived 或显式跳过）才能 close（[ADR-0026](./adr/0026-postmortem-ai-draft.md)）。
- resolved / closed 可 `reopen` 回 triggered（误解决或问题复现）。

### 3.3 实体分组

实体全貌以 `ent/schema/` 为准（计数随演进变化，不在文档写死）。按角色分三组理解：

| 组 | 代表实体 |
|----|---------|
| **身份与权限** | User / Team / Role / RoleBinding / APIKey / IMAccountBinding（权限点 Permission 是 `internal/auth/permission.go` 的代码枚举，**非实体**） |
| **配置蓝图** | Service / Integration / Schedule / EscalationPolicy / Runbook / NotificationRule / SuppressionRule / Credential |
| **运行时实例** | RawEvent / Event / Incident / TimelineItem / IncidentAction / Notification / Postmortem / AIInsight |

### 3.4 硬约束：全程留痕

**任何 Incident 状态变更必须产生一条 TimelineItem**；时间线只追加、不原地修改（修正只能新增条目），保证处置过程可溯源（[ADR-0010](./adr/0010-event-incident-separation.md) · [ADR-0022](./adr/0022-aiinsight-hitl-evidence.md)）。结构化操作另落 IncidentAction（含 `via` 渠道字段：web/im/api/automation），与时间线双轨，见 §7.2 审计。

---

## 4. 核心引擎

每个引擎小节按统一结构展开：**职责 → 机制 → 关键设计 → 决策**。

### 4.1 接入流水线（Ingestion）

**职责**：接得住、不丢失——把外部告警安全落地并异步归一化（需求：[FR-ING](./requirements.md)）。

**机制**：

```
Webhook/SMTP/API 接收 → 校验 + 落 RawEvent → 入队 → [归一化] → 交分诊管线（§4.2）
        202 秒级返回                            每步独立 Asynq 任务，失败 → 死信可重放
```

Receiver 极简：只校验 token + 落原始 payload + 入队，秒级返回 202；归一化及后续每步是独立 Asynq 任务。四种接入方式：通用 Webhook `POST /api/v1/webhook/{token}`、专用适配器（prometheus / grafana / webhook / email 四内置）、邮件 SMTP 入向（[ADR-0038](./adr/0038-smtp-inbound.md)，默认关闭）、开放 API `POST /api/v1/events`。

**关键设计**：

- **绝不在接收阶段同步处理**——下游慢会致告警源超时丢告警；背压/熔断时 payload 仍先落库，恢复后回灌。
- 幂等键 `source_event_id`；去重键 `DedupKey = sha1(source + fingerprint)` 在归一化阶段生成。
- 错误分级不丢数据：限流 429、队列积压 503（payload 仍落库）、鉴权失败 401 不落库但记审计（防探测）、格式错误落库标 `parse_failed`、归一化失败重试 → 死信可重放。严重度统一归 `critical / warning / info`。

**决策**：[ADR-0011](./adr/0011-ingestion-decoupled-idempotent.md) · [ADR-0038](./adr/0038-smtp-inbound.md)

### 4.2 分诊与路由（Triage & Routing）

**职责**：降噪不误杀、路由不漏单——把 Event 流收敛成少量该有人管的 Incident 并找到归属（需求：[FR-TRI / FR-RTE](./requirements.md)）。

**机制**：固定顺序 **去重 → 抑制 → 路由 → 相关性聚合**（`internal/triage/engine.go`；先路由到 Service 才能按 `service + severity` 聚合）：

| 阶段 | 实现 |
|------|------|
| 去重 | Redis `dedup:{dedup_key}` SETNX + 过期窗口（默认 5min，`VIGIL_TRIAGE_DEDUP_WINDOW`） |
| 抑制 | SuppressionRule（adhoc / maintenance 共用同一匹配逻辑，kind 只是分类标签） |
| 路由 | 以 Service 为锚点，四级确定性裁决：① slug 直达 → ② 多标签子集匹配（值支持 glob）→ ③ 多命中按匹配标签数降序、Service ID 升序 → ④ Integration 默认归属兜底 |
| 聚合 | 键 `service + severity`，5min 窗口内并入活跃 Incident（triggered/escalated/acked）；普通条件查询实现，「查活跃单 → 建单」临界区以 PostgreSQL advisory lock 串行化 |

**关键设计**：

- **`preserve_critical` 默认守卫**：critical 不被抑制——降噪不误杀是刻意设计。
- resolved Event 不丢弃：关联同 DedupKey 的 firing Incident 触发自动解决（可配"仅提示等人确认"）。
- 路由失败**进 `unrouted` 池而非静默丢弃**（查看需 `event.view_unrouted` 权限），可人工 reroute（`POST /events/:id/reroute`，权限 `service.route_override`）；unrouted 的 critical 有兜底通知。
- **Service 自动供给**（[ADR-0014](./adr/0014-service-auto-provisioning.md)）：默认关闭；开启后在进 unrouted 前懒创建 `source=auto` 的轻量 Service，但**无默认升级策略则不创建**（"已路由但静默"比 unrouted 更危险），绝不触碰 `source=manual` 的人工配置。配套主动同步（servicesync，file/http 源）与 Pruner（N 天无 Event 自动停用）。

**决策**：[ADR-0012](./adr/0012-triage-three-stage-pipeline.md) · [ADR-0013](./adr/0013-deterministic-routing.md) · [ADR-0014](./adr/0014-service-auto-provisioning.md)

### 4.3 排班引擎（Schedule）

**职责**：实时回答"此刻谁在班"（需求：[FR-ONC](./requirements.md)）。

**机制**：Schedule 是**纯蓝图**，不物化"当前值班人"；每次按 `timezone + layers（priority 升序） + Rotation + Override` 实时计算。Rotation 公式：`班次序号 = floor((T - start_date) / 周期)`，`当前值班 = participants[序号 mod 人数]`。支持 calendar / rotation / follow_the_sun，Override 最高优先级。

**关键设计**：

- **缓存但不物化生效**：分钟级 Redis 缓存只用于日历展示，生效判断永远实时算——排班变更立即生效，升级自动找到新人，无快照漂移。
- 越权守卫：**顶替人为操作者本人**时仅需 `schedule.override`，**顶替人为他人**时须叠加 `schedule.update`（判定维度是顶替人是谁，防值班人越权指派他人替班）。
- 空班检测告警 team_admin，不让"没人在班"静默。

**决策**：[ADR-0015](./adr/0015-schedule-realtime-no-snapshot.md)

### 4.4 升级引擎（Escalation）

**职责**：oncall 的灵魂——"没人理就找下一个"，绝不断链（需求：[FR-ESC](./requirements.md)）。

**机制**：Incident 创建即 `asynq.ProcessIn(delay)` 排延迟升级任务（critical 队列，`MaxRetry=25`）；ack（Web/IM）经事件总线触发 `DeleteTask` 取消。`repeat_times` 是 **EscalationPolicy 级字段**：对每层生效，每层共 `repeat_times + 1` 次、间隔为该层自身 `delay_minutes`，用尽才推进下一层；末级升级到全团队 + 多通道。升级策略不继承父服务，每个 Service 显式绑定。

**关键设计**：

- **状态守卫消解竞态**：handler 执行前检查 `incident.status`，已 ack/resolved 即使误触发也不动作（最终一致，无需分布式事务）。
- 幂等三层：状态守卫 + Redis 一次性通知标记（TTL 24h，先通知后落标记）+ 幂等 TaskID（`esc:{inc}:{level}:{repeatSeq}`，冲突按成功处理）。
- **对账巡检（sweeper）**：周期性（默认 2min，`VIGIL_ESCALATION_SWEEP_INTERVAL`）核对 DB"应然"与 Redis"实然"，Redis 丢数据后自动从 `current_level` 重排，防最危险的静默失效。恢复不是无损的——宁可升得更快（跳过剩余重复、扩大通知面），不可断链："没人被叫"远比"多叫了人"严重。

**决策**：[ADR-0016](./adr/0016-escalation-asynq-delayed.md)

### 4.5 通知引擎（Notification）

**职责**：把消息送到人手上，送不到就换路，全程可查账（需求：[FR-NTF](./requirements.md)）。

**机制**：

- **有序降级链（非并联）**：`msg.Channels` 逐通道尝试，首个成功即停。通道来源优先级：EscalationLevel.notify_channels > NotificationRule.channels > 全局默认链 `webhook(若配置) → im → email`。内置通道仅 im / email / webhook（电话/SMS 已移除，[ADR-0037](./adr/0037-trim-deferred-features.md)；未知通道名跳过、降级链继续）。
- **投递 Asynq 化**：Notification 行先落 `pending`（行 ID 即幂等键 `notif:{id}`，任务粒度 = 单 target 单条通知），瞬时失败指数退避重试（`MaxRetry=5`，约 15–20 分钟窗口）。
- **送达四态落库**：`pending | sent | failed | suppressed`（suppressed = 免打扰静默，落库可查、不丢数据；补发端点为**规划中**，尚未实现）。

**关键设计**：

- **整链失败不静默**：重试耗尽落 `failed` + 兜底告警 org_admin（走非 IM 通道，异步下只在最后一次重试失败触发）+ 进 archived 死信。
- **入队失败回退同步直投**（Redis 不可用时）——"可能少重试，绝不丢通知"；selfmon 与兜底告警（`NotifyUnrouted`）刻意保持同步直投，兜底通知不能依赖被兜底的队列。
- 30s 窗口聚合防轰炸（flush 合并的通知同走任务投递），**critical 不聚合立即单发**；quiet_hours 按规则配置的 IANA 时区计算（`NotificationRule.quiet_hours.timezone`，与接收人个人时区无关）、支持跨午夜、`bypass_for:[critical]` 穿透；**值班人（排班解算目标）不受 quiet_hours 静默**——保证升级链不因免打扰断链。
- 模板可配置（Go template，NotificationTemplate 实体，支持预览）。

**决策**：[ADR-0017](./adr/0017-notification-fallback-chain.md) · [ADR-0037](./adr/0037-trim-deferred-features.md)

### 4.6 IM 协同层（ChatOps）★ 差异化核心

**职责**：让 IM 成为处置工作面而非通知通道（需求：[FR-IM](./requirements.md)）。

**机制**：IM 回调 → `im_accounts` 映射到 User → **走与 Web 完全相同的 RBAC 鉴权** → 复用同一核心服务（`internal/incident/service.go`，鉴权只有这一处真相）。`IMBot` 接口（SendCard / UpdateCard / ParseCallback 等）封装平台差异，业务层不感知具体平台；平台矩阵为**飞书 + 钉钉**（企微已移除，[ADR-0037](./adr/0037-trim-deferred-features.md)）。

**关键设计**：

- **IM 非权限后门**：未绑定 IM 账号的用户在 IM 的操作被拒；无权操作拒绝并记审计；跨团队协作不靠 IM 放行，走事件级临时授权（§7.1）。
- **宽松渲染 + 回调硬鉴权**：群卡片全群共享一张，按钮按**代表接收者**（首个可解析 user_id 的通知目标，通常为当班值班人）权限裁剪一次，不做逐观看者裁剪；权限硬校验在回调侧（权威判定），无权点击被拒并记审计（口径见 [FR-IM-4](./requirements.md)）；状态双向同步：IM → Web 走 WebSocket，Web → IM 由领域事件驱动卡片刷新；卡片状态经 Redis 持久化（7 天 TTL）。
- **平台能力降级矩阵**：钉钉无法原地刷新卡片，以"重发带状态徽章的新消息"模拟（平台限制下的取舍）；能力缺失不静默丢告警，降级走通知兜底链；值班群未配置记 metric + Warn 不静默。
- 作战室能力已整体移除（[ADR-0036](./adr/0036-remove-war-room.md)），协同由「工作群 + 交互卡片 + 实时刷新」承载。

**决策**：[ADR-0018](./adr/0018-im-same-rbac-as-web.md) · [ADR-0019](./adr/0019-imbot-pluggable-degradation.md) · [ADR-0036](./adr/0036-remove-war-room.md)

### 4.7 AI Copilot 与知识闭环

**职责**：AI 横向贯穿分诊/诊断/复盘，只降负担、不替人拍板（需求：[FR-AI / FR-PMR](./requirements.md)）。

**机制**：

- **统一承载**：所有 AI 产出经 `AIInsight` 实体（stage: triage / diagnose / postmortem / copilot；confidence 0.0~1.0；`Evidence[]`；状态机 `suggested → accepted / rejected / applied`）。
- **Provider 抽象**：`LLMProvider` 接口（Complete / Embed），`VIGIL_LLM_PROVIDER` 选 glm（默认）或 ollama（本地，数据不出境）。成本三闸按序：缓存（Redis，sha256(prompt)，1h TTL）→ 限流（滑动窗）→ 配额（token 计数）；无 Redis 时三闸降级透传。置信度阈值默认 0.6，低于不产出。
- **相似事件检索**：主路径 pgvector 余弦距离（`Incident.embedding` 为空时懒计算回写）；pgvector 或 Embed 不可用时降级 LIKE 文本匹配。维度陷阱：embedding 列 `vector(1536)` 对齐 GLM embedding-3，Ollama 默认 embed 模型是 768 维，切 Provider 须改列维度或接受 LIKE 降级。
- **复盘闭环**：状态机 `draft → in_review → published → archived`；critical 强制复盘（resolved 后自动建 draft，见 §3.2 复盘闸门）。草稿三路合成（时间线自动填充 + AI 起草 summary/impact/root_cause + Incident 元数据），每字段标注 AI 来源、逐字段 accept/edit/reject（`generated_by`: ai/human/mixed）；改进项 ActionItem 经通用 webhook 工单出口外接（§6）；published 复盘进知识库反哺相似检索。

**关键设计**（HITL 硬约束）：

- **AI 建议须人 accept 才生效；无 evidence 不展示；accept/reject 记审计**（[ADR-0022](./adr/0022-aiinsight-hitl-evidence.md)）。
- **不做自动回训**（[ADR-0025](./adr/0025-no-auto-retrain.md) 明确否决）：任何模型/规则不得在无人确认时改变自身行为。保留闭环：AI 产出降噪建议 → 人 accept → 沉淀为显式 SuppressionRule；`/analytics/ai-feedback` 提供采纳率供人工调优。
- **AI 是增强非核心链路**：LLM 失败则 AI 功能降级，不影响告警主流程（[ADR-0023](./adr/0023-llm-provider-cost-control.md)）。

**决策**：[ADR-0022](./adr/0022-aiinsight-hitl-evidence.md) · [ADR-0023](./adr/0023-llm-provider-cost-control.md) · [ADR-0024](./adr/0024-similar-incident-pgvector.md) · [ADR-0025](./adr/0025-no-auto-retrain.md) · [ADR-0026](./adr/0026-postmortem-ai-draft.md)

### 4.8 Runbook 执行引擎

**职责**：把处置经验变成可执行资产，同时守住"写操作不碰生产"的红线（需求：[FR-RBK](./requirements.md)）。

**机制**：Runbook 分两档——`document`（纯 Markdown 参考）与 `executable`（结构化步骤）。executable 每步骤带 `readonly` 标志：只读诊断（查日志/指标/拓扑）可内置自动执行；写处置（重启/扩容/回滚）生成指令交人确认或对接外部平台。`Executor` 接口抽象执行后端（HTTP / 内置诊断等），执行时解密注入托管凭据（§7.2）；失败按 `on_failure`（continue / abort / escalate）处理，所有执行落 IncidentAction。

**关键设计**：

- **默认行为是"展示给人参考、不自动执行"**；处置写步骤默认 `require_approval:true`，**须人在 Web 确认——IM 不承载审批**（IM 内只读步骤照常执行，写步骤一律被 HITL 闸门阻断待审，`internal/im/` 中 approved 恒为 false）；`require_approval:false` 仅限高度可信场景 + admin 显式配置。
- auto-run 仅在显式配置且步骤全 readonly 时允许——这是全项目 HITL 基线的一部分。

**决策**：[ADR-0021](./adr/0021-runbook-two-tier.md)

---

## 5. 一条告警的一生

端到端主链路（典型场景见 [`user-stories/developer.md`](./user-stories/developer.md) 值班工程师 US-DEV 系列；运营治理视角另见 [`user-stories/ops-lead.md`](./user-stories/ops-lead.md) US-OPS 系列）：

```
 1. Prometheus 触发 → POST /api/v1/webhook/{token}
 2. 接入层校验 token → 落 RawEvent → 入队 → 202
 3. worker：归一化为 Event → 去重 → 抑制 → 路由匹配 Service → 聚合到 Incident
 4. Incident 进入 triggered → 排升级延迟任务（asynq.ProcessIn）
 5. 升级到期 → 排班引擎实时算在班人 → 通知引擎逐通道降级分发
 6. IM 卡片送达值班人 → 点 [ack] → IM 层映射账号 → RBAC 鉴权 → 核心服务 ack
 7. ack 取消后续升级 → Incident 进入 acked → TimelineItem 留痕
 8. 处置：展示 Runbook / 诊断只读执行 / 处置写操作须人确认
 9. resolve → AI 起草复盘草稿 → 逐字段人工校对 → published →（critical 过复盘闸门）close
10. 闭环：published 复盘进知识库，反哺相似事件检索（pgvector）
```

---

## 6. 集成面

四方向集成模型（[ADR-0030](./adr/0030-integrations-encrypted-openapi.md)）：

| 方向 | 能力 | 要点 |
|------|------|------|
| **入向（告警源）** | 通用 Webhook / 专用 Adapter（prometheus·grafana·webhook·email）/ SMTP 入向 / 开放 API | Integration 实体承载接入点（token 鉴权，可轮换）；SMTP 入向默认关闭、"收件人即令牌"、单封上限 1MB、端口仅应内网可达（[ADR-0038](./adr/0038-smtp-inbound.md)） |
| **双向（IM）** | 飞书 / 钉钉卡片下发 + 回调操作 | 见 §4.6 |
| **出向（webhook）** | WebhookSubscription 订阅 Incident 生命周期事件推送 URL | WebhookDelivery 送达记录、可重放；电话强提醒等场景经此出口外接自建网关 |
| **出向（工单）** | 复盘 ActionItem 推外部工单（通用 webhook），回调回写 tracker_url | 不做具体厂商 SDK（[ADR-0026](./adr/0026-postmortem-ai-draft.md)） |

**开放 API 契约**是 code-first 单一权威源：spec 由 swaggo 从 handler 注解生成（OpenAPI 3.1）、编译期 embed，挂 `/openapi.yaml` 与 `/docs`（Swagger UI）；前端类型从同一 spec 派生（`types.gen.ts`），CI 校验无漂移。程序化调用走 API Key（见 §7.1）。变更流程：改注解 → `go generate ./cmd/vigil/...` → `pnpm --dir web gen:types` → 提交生成产物。

用户侧还有**定向订阅**（Subscription 实体，按 team/service 订阅 incident 变更通知）。集成管理需求见 [FR-INT](./requirements.md)。

---

## 7. 横切关注点

### 7.1 认证与授权

**认证**（[ADR-0033](./adr/0033-selfmon-and-auth.md)）：业务 API 唯一声明方案是 HTTP Bearer JWT（`POST /auth/login`，可撤销）；程序化调用走 Scoped API Key（`X-Vigil-Key`，库中只存 SHA256 哈希，org_admin 管理）；webhook token 与 IM 回调签名各有独立校验，不走 RBAC。`X-Vigil-User-ID` 头回退与 `__test__` 测试端点由独立显式开关控制（`VIGIL_AUTH_HEADER_FALLBACK` / `VIGIL_TEST_ENDPOINTS_ENABLED`，**默认关闭**，production 无条件强制关闭，开启时启动打印 SECURITY WARN）。

**授权**（[ADR-0027](./adr/0027-rbac-permissions-roles.md) · [ADR-0028](./adr/0028-single-org-soft-isolation.md)）：统一中间件解析 `(user, action, resource)` → 查 RBAC 三元组 `User ──(RoleBinding, scope)──▶ Role ──▶ Permission`。Permission 是系统固定枚举（`internal/auth/permission.go`），Role 由使用者自由组合（内置角色不可删、不可改，定制须参照其权限集新建自定义角色），RoleBinding 带 scope（org/team），org + team 权限**取并集**（撤权须清理所有相关绑定）。资源归属即作用域（操作 Incident 取 `incident.team_id`）；团队软隔离、**团队树不继承权限**（不是多租户 SaaS）。

**跨团队协作 = 拉人 + 事件级临时授权**（[ADR-0020](./adr/0020-responder-temp-grant.md)）：`add_responder` 时仅当被拉人无权限才发放内置 responder 角色绑定（team scope，带 `expires_at` 默认 24h + `source_incident_id`）；Incident 收口自动撤销 + 过期兜底双重回收，发放/撤销均落审计。

### 7.2 安全

- **凭据加密**（[ADR-0030](./adr/0030-integrations-encrypted-openapi.md)）：Runbook 执行器等凭据经 AES-256-GCM 加密落 Credential 实体，明文永不回显；执行时解密注入。密钥轮换的现实约束见 [operations.md §6](./operations.md#6-密钥与凭证轮换)。
- **双轨审计**（[ADR-0029](./adr/0029-dual-audit-no-silent-truncation.md)）：管理审计（AuditLog：角色变更/集成 token/配置改动，查看需 `admin.audit.view`）+ 操作审计（IncidentAction，`via` 字段可统计 IM 操作占比——IM-first 可被数据验证）。CSV 导出单次上限 50000 行，达上限**绝不静默截断**：记 warn 日志 + 响应头 `X-Vigil-Truncated: true`，调用方须检查该头、按时间窗分段拉取。
- 登录防护（auth 模块内置）；错误响应统一模型：4xx 稳定 message、5xx 隐藏内部细节。安全需求见 [NFR-SEC](./requirements.md)。

### 7.3 可观测性与自监控

**吃自己狗粮**（[ADR-0033](./adr/0033-selfmon-and-auth.md)）：

- `/metrics`：HTTP 请求量与延迟直方图、告警接入量、事件/升级/通知计数、队列分状态 gauge `vigil_queue_tasks{queue,state}`（含 archived 死信）、LLM 调用与 token 成本、自监控计数。
- `/health`、结构化日志（贯穿 `incident_id`/`event_id`）、Asynqmon 任务面板。
- 报表（analytics）：`/analytics/*` 基于 MetricsSnapshot 预计算快照（每小时聚合任务），需求见 [FR-RPT](./requirements.md)。
- **selfmon**（默认关闭）：巡检队列积压、通知失败率、队列探测**连续失败**（Redis 整体故障信号），超阈自触发告警，走**排除 IM 的独立通道**（被监控的正是通知链路）；通知失败率统计排除自告警 unrouted 防自激循环；独立通道未配置时启动即 log warn，绝不假装闭环。selfmon 与进程共生死——**必须配外部监控兜底**，接入方法见 [operations.md §9](./operations.md#9-外部监控接入谁来监控守夜人)。

### 7.4 可靠性与失败模式

| 风险 | 对策 |
|------|------|
| Redis 宕机 | Asynq 状态在 Redis，部署方应开启 AOF/RDB 持久化（Redis HA 非内置）；接入层降级先落 PostgreSQL，恢复后回灌；升级对账 sweeper 自动重排丢失任务（§4.4） |
| 任务重复投递（at-least-once） | 全 handler 幂等：`esc:{...}` / `notif:{id}` + 行状态守卫 / `source_event_id` 三类键（§2.3） |
| 通知通道故障 | 逐通道降级链 + 指数退避重试（MaxRetry=5）+ 耗尽落 failed + 兜底告警 + archived 死信可重放（§4.5） |
| worker 崩溃 | Asynq 任务持久化 + 至少一次投递，重启自动恢复 |
| LLM 不可用 | AI 功能降级（非核心链路），不影响告警主流程 |
| 数据无界增长 | 保留期清理，见 §7.5 |

可靠性需求见 [NFR-RELY](./requirements.md)。

### 7.5 数据生命周期

[ADR-0039](./adr/0039-data-lifecycle.md)（Proposed，须区分已实现与规划）：

- **已实现**：Event / RawEvent 保留清理巡检（默认 90 / 30 天，`<=0` 关闭；批量分页删除默认 500/批，巡检 6h）。**活跃 Incident 证据保护**：未 closed Incident 引用的 Event 超期也保留；RawEvent 只清终态。
- **规划中（未实现）**：Notification / WebhookDelivery / MetricsSnapshot hourly 定期清理、AuditLog / IncidentAction 先归档后删、Event 按月分区（有触发条件的预案，非现状）。落地前 Notification 与审计类数据无界增长是显式接受的债。
- 保留默认值是产品立场而非合规承诺，合规环境应自行上调审计类保留期。

### 7.6 配置驱动与可插拔

- **12-Factor 配置**：运行时配置全走 `VIGIL_` 前缀环境变量（internal/config）。告警源、通知通道、IM 平台、LLM provider 的启停与参数走配置 + 数据库，注册表启动时按配置装载。
- **扩展模型如实表述**：5 类扩展点（告警源 Adapter / 通知 Channel / 执行器 Executor / LLM Provider / IM Bot）均为 **Go 接口 + 编译期注册**，不是运行时插件系统——新增实现需改代码、重新编译，部分扩展点还有 schema 枚举/配置模板/前端选项/i18n 配套触点。逐扩展点代码触点清单见 [`extending.md`](./extending.md)；第三方扩展以 fork + PR 进主仓库落地（[ADR-0009](./adr/0009-pluggable-integrations.md)）。
- **i18n**：前端全站 i18next 中英双语，中文优先；UI 遵循 oncall 四原则（半夜能用 / 一屏决策 / 降噪优先 / 状态可见，[ADR-0034](./adr/0034-uiux-oncall-principles.md)）。需求见 [NFR-I18N](./requirements.md)。

---

## 8. 部署形态与演进

### 8.1 当前已验证形态

- **单机（默认）**：Docker Compose 3 容器（vigil = API + worker + 前端，postgres 需 pgvector 扩展，redis）。适用中小团队/试用/PoC，步骤见 [operations.md §2](./operations.md)。
- **Kubernetes（Helm）**：`deploy/helm/` 可部署，迁移由 pre-install/pre-upgrade hook Job 自动执行（fail-fast：hook 失败则新版本不上线）。当前经验证的形态是**单进程单副本**（API + worker 同进程）。
- **迁移与回滚**（[ADR-0032](./adr/0032-migration-backup-restore.md)）：版本化 SQL + ent auto-migrate 双轨，`schema_migrations` 幂等追踪；**不做逆向迁移**，回滚 = 备份恢复——升级前备份是回滚的唯一前提，回滚粒度是整库恢复到备份点。

### 8.2 路线图（规划中，未端到端验证）

- vigil-api 与 vigil-worker 拆分独立扩缩（核心无状态，状态在 PostgreSQL/Redis）。
- 多副本部署：WebSocket 广播走 Redis pub/sub（代码已实现，未经多副本端到端验证）。
- Redis HA 由部署方自备，非 Vigil 内置。

部署需求与兼容策略见 [NFR-DEP / NFR-COMP](./requirements.md)。

---

## 9. 代码地图

模块 ↔ 架构位置映射（目录细节与开发命令见 [`AGENTS.md`](../AGENTS.md)；`cmd/vigil` 另含 genmigration / swagfix / verify-ai 子命令）：

| 架构位置 | `internal/` 模块 |
|----------|------------------|
| 接入流水线（§4.1） | ingestion |
| 分诊与路由（§4.2） | triage · service（服务目录）· servicesync |
| 排班（§4.3） | schedule |
| 升级（§4.4） | escalation |
| 通知（§4.5） | notification |
| IM 协同（§4.6） | im（子包 dingtalk / feishu） |
| AI 与复盘（§4.7） | ai · postmortem |
| Runbook（§4.8） | runbook · credential · crypto |
| 事件域 | incident（状态机 + 临时授权）· event（领域事件总线 + 清理）· timeline · subscription |
| 集成面（§6） | integration · webhook · ticket |
| 认证与安全（§7.1/7.2） | auth（permission.go 权限点枚举） |
| 可观测（§7.3） | metrics · selfmon · analytics |
| 装配与基础设施 | server（wire.go 全模块装配）· queue · store · migrate · ws · config · logger · errs · httputil · middleware · web（前端 embed） |

---

## 10. 决策追溯

全部 ADR 的唯一索引是 [`adr/README.md`](./adr/README.md)（按主题分组，不在本文复制）。ADR 治理规则见 [ADR-0001](./adr/0001-record-architecture-decisions.md)：一决策一文件、编号单调递增永不复用、改变已有决策以 Superseded 关系新增；已移除能力留墓碑（[ADR-0036](./adr/0036-remove-war-room.md) 作战室、[ADR-0037](./adr/0037-trim-deferred-features.md) 电话/SMS/企微/工单 SDK/Zabbix 占位）。开发工作流与质量门禁见 [ADR-0035](./adr/0035-dev-workflow-gates.md)。
