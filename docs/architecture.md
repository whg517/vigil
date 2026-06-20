# Vigil 系统架构设计

| 字段 | 内容 |
|------|------|
| **文档版本** | v0.1 |
| **创建日期** | 2026-06-20 |
| **状态** | Draft |
| **关联文档** | [`PRD.md`](./PRD.md)、[`data-model.md`](./data-model.md)、[`tech-stack.md`](./tech-stack.md) |

> 本文档定义 Vigil 的组件结构、职责边界、数据流与关键引擎设计。
> 技术选型依据 [`tech-stack.md`](./tech-stack.md)；实体定义依据 [`data-model.md`](./data-model.md)。

---

## 一、架构目标

架构服务于以下核心要求（源自 PRD）：

1. **可自托管、轻量**：3 个容器即可起步（vigil + postgres + redis）。
2. **事件驱动 + 异步**：告警接入、升级计时、排班计算、通知重试都是异步任务。
3. **IM-first**：IM 是主交互面，IM 操作与 Web 操作走同一套业务逻辑与鉴权。
4. **可插拔**：告警源、通知通道、执行器、LLM、IM 平台都能扩展不动核心。
5. **自身可观测**：暴露 metrics/health，能被自家告警监控（吃自己狗粮）。
6. **水平可扩展**：核心无状态，靠 Redis 队列协调多实例。

---

## 二、总体架构（C4 - Container 视图）

```
┌──────────────────────────────────────────────────────────────────────┐
│                          外部系统                                    │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────┐  ┌──────────┐  │
│  │Prometheus│  │ Zabbix   │  │ Grafana  │  │ 邮件 │  │ 自研监控  │  │
│  └────┬─────┘  └────┬─────┘  └────┬─────┘  └──┬───┘  └────┬─────┘  │
│       │             │             │            │           │        │
└───────┼─────────────┼─────────────┼────────────┼───────────┼────────┘
        │  webhook    │  webhook    │ webhook    │ SMTP      │ API
        ▼             ▼             ▼            ▼           ▼
┌──────────────────────────────────────────────────────────────────────┐
│                       Vigil 平台（单二进制，多模块）                   │
│                                                                       │
│  ┌──────────────────────────── 接入层 ────────────────────────────┐  │
│  │  HTTP API（REST）   WebSocket   Webhook Receivers   SMTP In     │  │
│  └────────────┬───────────────────────┬────────────────────────────┘  │
│               │                       │                               │
│               ▼                       ▼                               │
│  ┌──────────────────────── 核心服务层（业务逻辑）─────────────────┐  │
│  │                                                              │  │
│  │  Ingestion → Normalization → Triage → Routing                │  │
│  │      │                                       │               │  │
│  │      ▼                                       ▼               │  │
│  │  ┌──────────┐  ┌────────────┐  ┌──────────┐  ┌───────────┐  │  │
│  │  │ Schedule │  │ Escalation │  │ Incident │  │ Postmortem│  │  │
│  │  │ Engine   │  │ Engine     │  │ Manager  │  │ Generator │  │  │
│  │  └──────────┘  └────────────┘  └──────────┘  └───────────┘  │  │
│  │                                                              │  │
│  └────────────┬───────────────────────────────────┬─────────────┘  │
│               │                                   │                │
│               ▼                                   ▼                │
│  ┌──────────────────────── 异步任务层 ──────────────────────────┐  │
│  │   Worker Pool（消费 Redis 队列）                              │  │
│  │   · 延迟队列（升级计时/重试）   · 定时任务（排班/报表）         │  │
│  │   · 事件任务（接入流水线）       · 死信处理                    │  │
│  └────────────┬───────────────────────────────────┬─────────────┘  │
│               │                                   │                │
│  ┌────────────▼───────── 集成层（可插拔）──────────▼─────────────┐  │
│  │  Adapters       Notifiers      Executors      LLM Providers   │  │
│  │  (告警源)       (通知通道)      (Runbook执行)  (AI)            │  │
│  └────────────┬───────────────────────────────────┬─────────────┘  │
│               │                                   │                │
│  ┌────────────▼───────── IM 层（双向）────────────▼─────────────┐  │
│  │  IM Bot Adapters（钉钉/飞书/企微）：收消息/发卡片/建群/@人    │  │
│  └──────────────────────────────────────────────────────────────┘  │
│                                                                       │
└──────────┬───────────────────────────────────────┬───────────────────┘
           │                                       │
           ▼                                       ▼
   ┌──────────────┐                       ┌──────────────┐
   │ PostgreSQL   │                       │    Redis     │
   │ （持久化）   │                       │（缓存/队列/锁）│
   └──────────────┘                       └──────────────┘
           ▲                                       ▲
           │              外部依赖（可选）          │
           │   ┌────────────────────────────────┐ │
           └───┤ 云语音 / Ollama / Ansible ...  │◄┘
               └────────────────────────────────┘
```

### 分层职责

| 层 | 职责 | 对应 PRD 能力域 |
|----|------|----------------|
| **接入层** | 协议入口：HTTP API、WebSocket、Webhook、SMTP | 1, 13, 14 |
| **核心服务层** | 业务逻辑：归一化、分诊、路由、排班、升级、事件管理、复盘 | 2,3,4,5,6,8,9,10,12 |
| **异步任务层** | worker 池执行延迟/定时/事件任务（升级计时、通知重试、排班计算） | 3,5,6,7,9 |
| **集成层** | 可插拔适配器：告警源、通知、执行器、LLM | 1,7,9,11,14 |
| **IM 层** | IM 双向通信（IM-first 的承载） | 8 |

---

## 三、核心引擎设计

Vigil 的复杂度集中在几个"引擎"——它们是设计的重点，也决定了产品能不能立住。

### 3.1 接入流水线（Ingestion Pipeline）

告警从进入到产生作用的完整流水线，**全异步、可重试、可观测**：

```
Webhook 接收 → 入队 → [归一化] → [去重] → [分诊聚合] → [路由] → [创建/合并 Incident]
                │                                                         │
                │  每步失败 → 死信队列（可重放）                          │
                │                                                         ▼
                └────────────────────────────────────► 触发 [通知流水线]
```

- **每一步是独立的 Asynq 任务**，通过 Asynq 队列串联。好处：单步失败不影响其他、可独立扩缩、失败自动重试、死信可重放（Asynqmon 可视化）。
- **幂等**：以 `source_event_id` 为幂等键，重复推送不产生副作用（PRD M1.8），适配 Asynq 的 at-least-once 语义。
- **背压**：队列积压超过阈值时，接入层返回 429 并告警（吃自己狗粮）。

### 3.2 分诊引擎（Triage Engine）

把海量 Event 聚合成少量 Incident 的核心。三个子机制：

| 机制 | 实现 | 阶段 |
|------|------|------|
| **去重** | Redis 以 `dedup_key` 作键，TTL 窗口内重复直接丢弃 | 归一化后立即 |
| **抑制** | 规则引擎评估（维护窗口/已知问题），命中则 `is_noise=true` | 去重后 |
| **相关性聚合** | 时间窗口内按 label（service/env）聚合成 Incident；PostgreSQL 窗口查询 | 抑制后 |

聚合策略（内建为主，可配置为辅）：
- 默认：相同 `service + severity` 在 5 分钟窗口内的 firing events → 一个 Incident。
- 可配置：高级用户可定义自定义聚合规则（工作流引擎挂钩点）。

### 3.3 排班引擎（Schedule Engine）

**核心问题**：实时回答"此刻谁在班"。

```
输入：Schedule（蓝图）+ 当前时间 + Override 层
   │
   ▼
引擎计算：
  1. 取 Schedule.timezone 把"当前时间"转成当地时间
  2. 遍历 layers（按 priority），每层根据 Rotation 算出该时段的参与者
  3. 应用 Override 层（临时换班覆盖）
  4. 输出有序的 oncall_users（primary → secondary）
   │
   ▼
输出：[user_id, ...]（带优先级）
```

- **不存储快照**：每次告警进来实时算（data-model §3.2）。排班变更立即生效，无一致性问题。
- **缓存**：计算结果按分钟级缓存到 Redis（排班不会秒级变化），降低计算压力。
- **预计算**：未来 N 天的排班可预计算展示（排班日历），但**生效判断永远实时算**。

### 3.4 升级引擎（Escalation Engine）★

oncall 产品的灵魂——"没人理找下一个"。

```
Incident 创建（triggered）
   │
   ▼
启动升级计时器（EscalationPolicy.level[0].delay_minutes）
   │
   ├── 若在 delay 内 ack ──► 停止计时，进入 acked 态
   │
   └── 超时未 ack
         │
         ▼
       触发 level[0] 通知（schedule/user/team）
         │
         ├── 启动 level[1] 计时器 + repeat 循环通知
         │
         └── 继续超时 → level[2] → ... → 末级
```

- **实现**：用 **Asynq 延迟任务**承载——`Client.Enqueue(escalationTask, asynq.ProcessIn(delay_minutes))`，到期由 Asynq worker 自动触发。升级任务幂等键 = `incident_id + level`，重复投递不产生副作用。
- **ack 即取消**：任何 ack（Web 或 IM）通过事件总线删除/作废该 Incident 的所有待触发升级任务（Asynq `DeleteTask`），并**用 incident 状态作守卫**——已 ack/resolved 的 incident 即使任务误触发也不动作（最终一致）。
- **状态流转**：每次升级触发都产生 TimelineItem + IncidentAction（data-model 状态机）。
- **可靠性**：升级任务的关键性最高，配置 Asynq 高优先级队列 + 高 MaxRetry；Asynq 基于 Redis 持久化，worker 重启后任务不丢。

### 3.5 通知引擎（Notification Engine）

```
通知触发（升级/手动）
   │
   ▼
解析 targets（schedule→算在班人 / user / team）
   │
   ▼
按 NotificationRule 选 channels + template
   │
   ▼
并发分发到各 Notifier（IM/电话/SMS/邮件/Webhook）
   │
   ├── 成功 → 记送达
   ├── 失败 → 退避重试（指数退避，上限 N 次）
   └── 最终失败 → 标记送达失败 + 告警（升级到下一通道）
```

- **模板引擎**：通知内容模板化，按 severity/team 区分（PRD M7.5）。
- **重试**：失败重试由 Asynq 承载（指数退避、MaxRetry、死信），通知任务幂等键 = `notification_id`，避免重试导致重复发送。
- **聚合**：短时间内对同一人的多条通知合并，避免轰炸（PRD M7.9）。
- **ack 闭环**：IM 卡片按钮 / Web 回调 → 写回 Incident 状态 → 取消后续通知。

### 3.6 IM 协同层（ChatOps Layer）★ 差异化核心

IM-first 的承载，**双向通信 + 状态同步**：

```
              ┌─────────── Vigil ───────────┐
              │                              │
  IM 事件 ───►│  IM Webhook Receiver         │
  (按钮/命令/ │  → 映射 IM账号→User           │
   @人/消息)  │  → RBAC 鉴权                  │──► 调用核心服务（ack/升级/拉人）
              │  → 执行业务逻辑               │
              │                              │
              │  状态变更 ──► IM Card Updater │──► 更新 IM 卡片（实时反映状态）
              └──────────────────────────────┘
```

关键设计：
- **IM 账号映射**：`User.im_accounts` 把 IM unionId 映射到 User，IM 操作走**与 Web 完全相同的鉴权链路**（不因在 IM 就放行）。
- **交互卡片**：告警通知以卡片形式发出，带 ack/升级/解决 按钮；按钮按权限渲染（无权不显示）。
- **卡片实时更新**：Incident 状态变化时，通过 IM 平台的卡片更新 API 实时刷新已发出的卡片（多人看到的卡片同步），避免"群里有张过时的卡片"。
- **作战室**：Incident 触发时自动建临时 IM 群、拉 responders、置顶事件卡片。
- **斜杠命令**：机器人接收 `/vigil <command>`，解析后调核心服务。
- ⚠️ **平台能力依赖**：卡片更新、建群、@人等 API 能力因 IM 平台而异，须 PoC 验证（PRD 风险表）。

---

## 四、关键数据流

### 4.1 端到端：一条告警的生命周期

```
1. Prometheus 触发告警 → POST /webhook/prometheus/{token}
2. 接入层校验 token → 入 ingestion 队列
3. worker: 归一化为 Event → 存 PostgreSQL
4. worker: 去重（Redis dedup_key）→ 抑制判定 → 聚合到 Incident
5. worker: 路由匹配 Service → 绑定 EscalationPolicy + Schedule
6. Incident 进入 triggered 态 → 入队升级延迟任务（Asynq ProcessIn）
7. 升级引擎到期 → 排班引擎算在班人 → 通知引擎分发
8. IM 通知以卡片送达值班人
9. 值班人点卡片 [ack] → IM 层映射→鉴权→核心服务 ack
10. ack 取消后续升级 → Incident 进入 acked 态 → 时间线记录
11. 处置：展示 runbook / 诊断执行 / 处置（人确认或外接）
12. 标记 resolved → AI 起草复盘草稿 → 人工校对 → published
13. 闭环：复盘进入知识库，反哺相似事件检索
```

### 4.2 IM 操作的鉴权流（IM-first 关键）

```
IM 用户点 [升级] 按钮
   │
   ▼
IM Webhook Receiver 收到回调（含 IM unionId + 按钮 action）
   │
   ▼
查 User.im_accounts → 映射到 User
   │
   ▼
解析 action → incident.escalate 权限
   │
   ▼
查该 User 在 incident.team_id 作用域的 RoleBinding → 合并权限点
   │
   ▼
判定 incident.escalate ∈ 权限点？
   ├── 否 → IM 返回"无权限"，记审计
   └── 是 → 核心服务执行 escalate → 更新卡片 → 时间线记录
```

> 这条流路与 Web 端完全一致，只是入口和身份解析不同。保证 IM 不成为权限后门。

---

## 五、模块划分（代码结构预留）

单二进制内按领域模块组织（对应核心服务层）：

```
vigil/
├── cmd/vigil/              # 入口
├── internal/
│   ├── api/                # 接入层：Echo handler（HTTP/WS/Webhook）
│   ├── ingestion/          # 接入流水线
│   ├── triage/             # 分诊引擎
│   ├── routing/            # 路由
│   ├── schedule/           # 排班引擎
│   ├── escalation/         # 升级引擎
│   ├── notification/       # 通知引擎
│   ├── incident/           # 事件管理 + 状态机
│   ├── runbook/            # 处置执行
│   ├── postmortem/         # 复盘
│   ├── ai/                 # AI Copilot（调 LLM Provider）
│   ├── im/                 # IM 协同层
│   ├── auth/               # RBAC 鉴权（Echo 中间件）
│   ├── store/              # ent 数据访问层（schema + 生成代码）
│   ├── queue/              # Asynq 任务定义 + handler + worker（含 Asynqmon 监控）
│   └── plugins/            # 可插拔：adapters/notifiers/executors/llm/im
├── ent/schema/             # ent schema 定义（实体图）
├── web/                    # 前端（React + Vite + shadcn/ui + Tailwind）
├── deploy/                 # docker-compose.yml / helm/
└── docs/                   # 文档
```

> 这是结构预留，不涉及实现。

---

## 六、横向关注点

### 6.1 鉴权与多团队隔离

- **统一鉴权中间件**：所有 API（Web 和 IM 调用的核心服务）都过同一 Echo 中间件，解析 `(user, action, resource)` → 查 RBAC（data-model §5.5）。
- **资源归属即作用域**：操作 Incident 时，取 `incident.team_id` 作为鉴权 scope。
- **跨团队拉人**：`add_responder` 创建事件级临时 RoleBinding（事件关闭即失效）。

### 6.2 配置驱动

- 告警源、通知通道、IM 平台、LLM provider 的启停与参数全走**配置 + 数据库**，不写死在代码。
- 插件注册表在启动时根据配置装载对应实现。

### 6.3 可观测性（吃自己狗粮）

- `/metrics`：接入量、队列深度、各引擎延迟、通知成功率、LLM 调用量/成本。
- `/health`：postgres/redis 连通性、队列积压、worker 存活。
- 结构化日志：每个事件带 `incident_id`/`event_id` 贯穿日志，便于追踪。
- **Vigil 自身告警**：队列积压/通知失败率超阈值时，Vigil 可对接自身（或外部）触发告警——形成闭环。
- **任务监控**：Asynqmon 提供任务队列的可视化（深度/状态/失败/重试），作为运维诊断的补充。

### 6.4 可靠性

| 风险 | 对策 |
|------|------|
| Redis 宕机（任务/升级计时） | Asynq 任务状态持久化在 Redis；生产部署用 Redis 持久化（AOF/RDB）+ 高可用（哨兵/集群）；Redis 不可用期间接入层降级（直接落 PostgreSQL 原始事件，恢复后回灌流水线） |
| 任务重复投递（at-least-once） | 所有 handler 幂等：升级任务 `incident_id+level`、通知 `notification_id`、流水线 `source_event_id` 去重 |
| 通知通道故障 | 多通道兜底（IM 失败→电话）；最终失败升级告警 |
| worker 崩溃 | Asynq 任务持久化 + 至少一次投递；崩溃重启后自动恢复未完成任务 |
| LLM 不可用 | AI 功能降级（非核心链路），不影响告警主流程 |

---

## 七、部署拓扑

### 7.1 单机（Docker Compose，默认）

```
3 容器：vigil（API+worker+前端） + postgres + redis
```
适用：中小团队、试用、PoC。

### 7.2 集群（Kubernetes + Helm）

```
vigil-api（Deployment，多副本，无状态）──┐
vigil-worker（Deployment，多副本）     ├──► 共享 postgres（含高可用）
                                       ├──► 共享 redis（含高可用）
                                       └──► 前端静态资源由 CDN/Ingress 提供
```
- API 与 worker 可独立扩缩：API 水平扩展承接流量，worker 按队列深度扩缩。
- 无状态设计：会话/队列状态全在 Redis，副本间对等。

---

## 八、与决策的对齐说明

本架构体现的核心产品决策（决策内容已内联于 PRD/data-model，此处仅标注架构落点）：

| 产品决策 | 架构落点 |
|---------|---------|
| 告警消费者定位（不做采集） | 接入层只接收，无采集组件 |
| IM-first | IM 协同层为一等公民，鉴权流与 Web 统一 |
| AI 横向 Copilot | 独立 `ai` 模块，AIInsight 承载，human-in-the-loop |
| Runbook 分两档 | `runbook` 模块 + 可插拔 Executor；写操作 require_approval |
| 单组织多团队软隔离 | 鉴权中间件 + 资源归属作 scope |
| RBAC 自配置 | `auth` 模块 + Role/Permission/RoleBinding 模型 |
| 可插拔 | `plugins` 统一注册表，5 类扩展点 |

---

## 九、开放问题

| # | 问题 | 状态 |
|---|------|------|
| A1 | 本土 IM 平台能力 PoC（卡片更新/建群/@人 API 边界） | 待验证，影响 IM 协同层可行边界 |
| A2 | 多 worker 的队列分片与任务去重细节 | 初期单实例，扩展时细化 |
| A3 | LLM 调用的配额/限流/缓存策略 | 设计阶段定 |
| A4 | 前端是否需要 SSR | 倾向 SPA，待定 |
