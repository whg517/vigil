# Vigil 核心数据模型与权限模型

| 字段 | 内容 |
|------|------|
| **文档版本** | v0.1 |
| **创建日期** | 2026-06-20 |
| **状态** | Draft |
| **关联文档** | [`PRD.md`](./PRD.md)、[`personas.md`](./personas.md) |

> 本文档定义 Vigil 的核心实体、实体关系、状态机，以及作为底层权限控制的 RBAC 模型。
> 它是所有能力域功能设计的骨架——先定模型，再谈功能。

---

## 一、设计基线

本数据模型基于以下已确认的设计原则，它们决定了模型的形态：

1. **告警消费者定位**：Vigil 只做告警的"下一步"，不内置监控采集。Event 只来自外部接入（webhook/邮件/API），模型不涉及采集实体。
2. **开源 + IM 原生 + AI 原生**：User 须绑定 IM 账号（支持多 IM 平台）；AI 以横向 AIInsight 实体贯穿分诊/诊断/复盘全流程。
3. **完整形态定义**：模型按完整产品形态设计，不为阶段做裁剪或预留半成品字段。
4. **Event 与 Incident 分离**：Event 是原始告警信号（海量、不可变）；Incident 是"值得人介入的处理单元"（少量、有上下文、有状态机）。分诊层把 Event 聚合成 Incident。
5. **内建流程为主、工作流为辅**：模型支持内建最佳实践路径，仅在少数确有定制需求的节点（升级策略、通知、处置）留可配置 workflow 挂钩，不做通用低代码编排。
6. **IM-first**：实体支持从 IM 发起的操作与双向状态同步，IM 操作与 Web 操作走同一条鉴权链路。
7. **AI 横向 Copilot + human-in-the-loop**：以 AIInsight（带 evidence + 状态机 suggested→accepted/rejected）承载 AI 产出，所有 AI 建议须经人确认才生效。
8. **Runbook 分两档**：Runbook 步骤区分 `readonly`（诊断类，Vigil 内置执行）与 `require_approval`（处置类，人确认或对接外部平台），Vigil 不直接碰用户生产环境的写操作。
9. **单组织、多团队、软隔离**：Team 是数据归属边界（服务/排班/事件归团队），不做 SaaS 级硬隔离；跨团队协作靠 add_responder 拉人 + 事件级临时授权。
10. **团队嵌套不继承权限**：团队树（`parent_team_id`）仅用于组织结构展示，权限不沿树向下传递，避免越权漏洞。
11. **角色全局定义、不做团队私有角色**：Role 是全局实体，团队级差异化授权完全靠 RoleBinding 的 scope（org/team + team_id）承载。
12. **RBAC 为底层权限、可自配置**：权限点是系统内置的细粒度动作枚举（系统能力边界），角色不预设死档、由使用者自由组合权限点配置；详见 §5。

---

## 二、实体总览

```
┌─────────────────────────────────────────────────────────────────┐
│                        组织层（Organization）                     │
│                                                                  │
│   User ──< Member >── Team ──< Service >── Integration           │
│    │                  │         │                                │
│    │                  │         ├──< Schedule (排班) ── Rotation │
│    │                  │         ├──< EscalationPolicy (升级链)   │
│    │                  │         ├──< Runbook                     │
│    │                  │         └──< NotificationRule            │
│    │                  │                                          │
│    │                  └──< RoleBinding >── Role ──< Permission > │
│    │                                                             │
│    └──< Event (原始告警) ──聚合──> Incident (处理单元)            │
│                                    │                             │
│                                    ├──< TimelineItem             │
│                                    ├──< IncidentAction           │
│                                    ├──< Postmortem               │
│                                    └──< AIInsight                │
│                                                                  │
│   EscalationPolicy ──引用──> Schedule / User                     │
└─────────────────────────────────────────────────────────────────┘
```

### 实体分层说明

| 层 | 实体 | 职责 |
|----|------|------|
| **身份与组织** | User, Team, Member | 谁在哪、属于谁 |
| **权限** | Role, Permission, RoleBinding（详见第五章 RBAC） | 谁能干什么 |
| **配置（静态）** | Service, Integration, Schedule, EscalationPolicy, Runbook, NotificationRule | 告警怎么流转 |
| **运行时（动态）** | Event, Incident, TimelineItem, IncidentAction, Postmortem, AIInsight | 告警变成什么 |

配置实体是"蓝图"，运行时实体是"实例"。这是核心区分：排班是蓝图，某次告警算出来的"此刻在班人"是实例。

---

## 三、核心实体详述

### 3.1 身份与组织

#### User（用户）
```yaml
User:
  id:                 # UUID
  username:           # 登录名，唯一
  name:               # 显示名
  email:              # 邮箱，唯一
  phone:              # 电话（可选，用于 SMS/语音）
  im_accounts:        # IM 账号绑定，支持多 IM 平台
    - platform: dingtalk
      account_id: "..."   # 钉钉 unionId
    - platform: feishu
      account_id: "..."
  status: active | disabled
  timezone: "Asia/Shanghai"
  created_at / updated_at
```
- 一个 User 可绑定**多个 IM 平台账号**（钉钉+飞书同时用），这是本土化必需。
- `im_accounts` 是 IM-first 的前提：IM 里某人的操作要能映射回 User。

#### Team（团队）
```yaml
Team:
  id:
  name:               # 如 "支付SRE"
  slug:               # URL/标识用，唯一
  description:
  parent_team_id:     # 支持团队树（事业部>团队），仅组织展示用，权限不沿树继承
```
- 团队可嵌套（`parent_team_id`），用于"事业部 > 小组"这种结构，但**权限不沿树继承**——团队树仅作组织展示，避免越权。

#### Member（成员关系）
```yaml
Member:
  id:
  user_id:
  team_id:
  role_bindings: [role_binding_id, ...]   # 该用户在该团队的角色绑定
  joined_at:
```
- 同一个 User 在不同 Team 可有不同角色（A 团队是 responder，B 团队是 admin）。

---

### 3.2 配置实体（告警流转蓝图）

#### Service（服务）—— 路由的锚点
```yaml
Service:
  id:
  team_id:            # 服务归属团队，软隔离边界
  name:               # 如 "payment-api"
  slug:               # 唯一
  description:
  labels:             # 自由标签，用于路由匹配
    env: prod
    tier: 1
  escalation_policy_id:   # 绑定的升级策略
  runbook_ids: [...]      # 关联的 runbook
  auto_create_incident: bool   # 告警进来是否自动成 Incident
  status: active | disabled
```
- Service 是**软隔离的核心载体**：它把告警、排班、升级、复盘都"绑"到一个团队上。
- `labels` 是路由匹配的依据（告警的 label 匹配 Service 的 label → 命中）。

#### Integration（接入点）—— 告警的入口
```yaml
Integration:
  id:
  team_id:
  service_id:         # 该接入点的告警默认归属哪个 Service
  name:               # 如 "prometheus-payment"
  type: webhook | email | prometheus | zabbix | grafana | cloud | api
  config:             # 类型相关的配置（URL、过滤、鉴权方式）
  token:              # webhook 鉴权 token（加密存储）
  enabled: bool
```
- 一个 Service 可有多个 Integration（Prometheus + Zabbix 都往一个服务推）。
- `type` 决定用哪个适配器做归一化（见能力域 1/2）。

#### Schedule（排班）—— "此刻谁在班"的算法
```yaml
Schedule:
  id:
  team_id:
  name:               # 如 "支付一线值班"
  type: calendar | rotation | follow_the_sun
  timezone: "Asia/Shanghai"
  layers:             # 排班分层（primary / secondary / override）
    - id:
      name: "一线"
      priority: 1     # 数字越小优先级越高
      rotations: [Rotation, ...]
  # Schedule 本身不存"谁在班"，由引擎实时计算
```
```yaml
Rotation（轮班规则，Schedule 的子结构）:
  id:
  schedule_layer_id:
  participants: [user_id, ...]   # 参与轮班的人
  shift_length:        # 班次时长，如 24h / 1week
  handoff_time:        # 交接时间，如 "09:00"
  rotation_type: weekly | daily | custom
  start_date:
  end_date:            # 可选
```
- **Schedule 是纯蓝图**，不存当前值班人。每次告警进来，排班引擎根据 Schedule + 当前时间实时算出 `oncall_users`。
- `layers` 设计借鉴 PagerDuty：primary 没接到 → secondary；override 层用于临时换班覆盖。

#### EscalationPolicy（升级策略）—— "没人理找下一个"
```yaml
EscalationPolicy:
  id:
  team_id:
  name:
  repeat_times: 0       # 当前 level 未 ack 时重复通知几次
  levels:               # 有序的升级层级
    - level: 1
      delay_minutes: 1  # 进入此 level 后多久发通知
      targets:          # 通知目标（可混合）
        - type: schedule
          schedule_id: "..."
        - type: user
          user_id: "..."
        - type: team
          team_id: "..."   # 通知全团队
      notify_channels: [im, phone, sms, email]
    - level: 2
      delay_minutes: 10
      targets: [...]
```
- 升级链是**有序层级**，每层有延迟、目标、通道。
- 目标可以是 Schedule（=此刻在班人）、User（指定人）、Team（全员）。

#### Runbook（处置手册）—— "该干什么"
```yaml
Runbook:
  id:
  team_id:
  service_id:           # 可选，绑定到服务
  name:
  trigger:              # 何时展示/触发
    type: manual | on_incident | on_severity | on_label_match
    condition: "severity >= warning"
  type: document | executable   # 文档式 | 可执行
  # 文档式：
  content_markdown:     # Markdown 处置步骤
  # 可执行式：
  steps:                # 有序步骤
    - id:
      name:
      action:           # 动作类型（见能力域 Runbook）
        type: diagnose | execute | notify | wait | approve
        target:         # execute 时指向外部执行器
          kind: http | ansible | jenkins | internal
          endpoint: "..."
        readonly: bool   # diagnose 类强制只读
      on_failure: continue | abort | escalate
      require_approval: bool   # 写操作必须人确认（human-in-the-loop）
```
- **`type: document`** = 纯 Markdown，展示给人看。
- **`type: executable`** = 可执行步骤链。其中 `readonly:true` 的诊断动作 Vigil 自己跑；写操作 `require_approval:true` 强制人工确认或对接外部平台（见 §1 设计基线第 8 条）。

#### NotificationRule（通知规则）—— 通知层的配置
```yaml
NotificationRule:
  id:
  team_id:
  name:
  condition:            # 触发条件，如 severity=critical
  channels: [im, phone, sms, email, webhook]
  template_id:          # 通知模板
  quiet_hours:          # 静默时段（非 critical 不打扰）
  enabled: bool
```
- 与 EscalationPolicy 区别：升级策略管"找不到人怎么办"，通知规则管"用哪种通道、什么模板、何时静默"。

---

### 3.3 运行时实体（告警的实例化）

#### Event（原始告警）—— 归一化后的信号
```yaml
Event:
  id:                   # Vigil UUID
  integration_id:       # 来源接入点
  service_id:           # 路由命中的服务（可空，未命中=unrouted）
  source_event_id:      # 原始告警 ID（去重依据）
  source: "prometheus"  # 告警源
  severity: critical | warning | info
  status: firing | resolved
  summary:              # 一句话摘要
  detail: { ... }       # 原始 payload 归一化后的明细
  labels:               # 路由用标签
    service: payment
    env: prod
  dedup_key:            # 去重键（source_event_id + 关键字段哈希）
  received_at:
  incident_id:          # 聚合到的 Incident（可空）
  is_noise: bool        # 分诊判定为噪音则不进 Incident
```
- Event 是**不可变的历史记录**，只追加。`resolved` 由告警源的 resolved 事件触发。

#### Incident（处理单元）—— 人介入的对象 ★ 核心
```yaml
Incident:
  id:                   # 人类可读，如 INC-0042
  team_id:              # 归属团队
  service_id:
  title:                # 事件标题
  severity: critical | warning | info
  status:               # 见状态机
  priority:             # P1/P2/P3，可由 severity + service tier 派生
  summary:              # 当前概要（可随处置更新）
  related_events: [event_id, ...]   # 聚合进来的告警
  merged_into:          # 若被合并，指向主 Incident
  escalated_count:      # 已升级次数
  current_level:        # 当前升级层级
  assignee_id:          # 当前责任人（=当前在班人 or 拉进来的人）
  responders: [user_id, ...]   # 所有参与响应的人
  war_room:             # 作战室信息
    im_platform: feishu
    im_channel_id: "..."
    created_at:
  trigger:              # 触发方式
    type: auto | manual | merged
    source_event_id:    # auto 时的首条告警
  created_at / resolved_at / closed_at:
  created_by:           # 自动创建则为 system
```

#### Incident 状态机 ★
```
                  ┌──────── auto/manual 触发 ────────┐
                  ▼                                    │
              ┌────────┐   ack(超时升级)    ┌──────────┴──┐
   new event  │        │ ─────────────────▶ │            │
  ─────────▶  │ TRIGGER│                    │  ACKED     │
              │        │ ◀──── ack(及时) ── │  (处置中)   │
              └────────┘                    └──────┬──────┘
                  │                                 │
                  │ 超时未 ack                        │ 标记解决
                  ▼                                 ▼
              ┌────────┐  继续超时      ┌──────────┐
              │ESCALAT-│ ──────────▶   │          │
              │  ED    │               │ RESOLVED │
              └────────┘               │ (待复盘)  │
                  │                    └────┬─────┘
                  │ ack                      │ 复盘完成
                  └──────────▶ ACKED         ▼
                                       ┌──────────┐
                                       │  CLOSED  │
                                       └──────────┘
```

状态定义：
| 状态 | 含义 | 进入条件 | 出口 |
|------|------|---------|------|
| `triggered` | 刚创建，待响应 | Event 升级为 Incident | 被 ack 或超时 |
| `escalated` | 超时未响应，已升级 | 超时计时器触发 | 被 ack |
| `acked` | 有人接手，处置中 | 任意 level 有人 ack | resolved |
| `resolved` | 已解决，待复盘 | 用户标记解决 | 复盘完成→closed |
| `closed` | 终态，复盘完成 | postmortem 完成或跳过 | —— |

- **状态变更必须产生 TimelineItem**（见 3.4），保证全程留痕。

#### TimelineItem（时间线条目）
```yaml
TimelineItem:
  id:
  incident_id:
  timestamp:
  type:                 # 见下表
  actor:                # 谁干的
    kind: system | user | integration | ai
    id:
  content:              # 人类可读描述
  detail: { ... }       # 结构化详情
  source: web | im | api | system | ai
```
| type | 何时产生 |
|------|---------|
| `incident_created` | Incident 创建 |
| `event_attached` | 新 Event 聚合进来 |
| `status_changed` | 状态流转 |
| `escalated` | 升级发生 |
| `ack` / `resolved` / `reopened` | 关键动作 |
| `responder_added` | 拉人 |
| `note_added` | 人工备注 |
| `runbook_executed` | runbook 步骤执行 |
| `ai_insight` | AI 给出的洞察 |
| `im_message` | IM 内相关消息（可选捕获） |

#### IncidentAction（处置动作）
```yaml
IncidentAction:
  id:
  incident_id:
  type: ack | escalate | resolve | reopen | snooze | reassign | add_responder | runbook | custom
  actor: { kind, id }
  payload: { ... }       # 动作参数
  via: web | im | api | automation
  timestamp:
  result: success | failed | pending
```
- 所有对 Incident 的操作都落成 Action（审计 + 撤销/重放基础）。
- `via` 标记来源，IM-first 的关键：能看到多少动作是在 IM 完成的。

#### Postmortem（复盘）
```yaml
Postmortem:
  id:
  incident_id:
  status: draft | in_review | published | archived
  author_id:
  # 结构化内容（LLM 辅助填充）
  sections:
    summary:
    impact:              # 影响（时长、用户数、损失）
    timeline:            # 引用时间线
    root_cause:
    contributing_factors:
    what_went_well: []
    what_went_wrong: []
    action_items:        # 改进项
      - id:
        description:
        owner_id:
        due_date:
        status: open | in_progress | done
        tracker_url:     # 对接外部工单
  generated_by: ai | human | mixed
  created_at / published_at:
```

#### AIInsight（AI 洞察）—— 决策 4 的承载
```yaml
AIInsight:
  id:
  incident_id:           # 或 event_id（分诊阶段）
  stage: triage | diagnose | postmortem | copilot   # AI 介入的阶段
  type:                  # 见下
  content:               # AI 产出（文本/结构化）
  confidence: 0.0 ~ 1.0
  evidence: [ref, ...]   # 依据（引用的 Event/日志/时间线）
  status: suggested | accepted | rejected | applied
  created_at:
```
| type | 阶段 | 内容 |
|------|------|------|
| `dedup_suggestion` | triage | 建议把若干 Event 合并 |
| `severity_adjustment` | triage | 建议调高/调低严重度 |
| `root_cause_hint` | diagnose | 根因线索 |
| `similar_incident` | diagnose | 历史相似事件 |
| `draft_summary` | copilot | 起草事件摘要 |
| `postmortem_draft` | postmortem | 起草复盘 |

- `status` 实现 human-in-the-loop：AI 建议 → 人 accept/reject → 才真正生效。
- `evidence` 是 AI 可信度的关键：每条洞察必须能溯源到依据。

---

## 四、实体关系总表

| 从 | 到 | 关系 | 说明 |
|----|----|------|------|
| User | Team | N:N（via Member） | 一个用户多团队 |
| User | Role | N:N（via RoleBinding，限定 scope） | 角色绑定有作用域 |
| Team | Service | 1:N | 服务归属团队 |
| Service | Integration | 1:N | 多个接入点汇入一服务 |
| Service | Schedule | N:N | 服务可换班/复用排班 |
| Service | EscalationPolicy | N:1 | 一个服务一个升级策略 |
| Service | Runbook | 1:N | 服务关联处置手册 |
| EscalationPolicy | Schedule/User/Team | N:N | 升级目标 |
| Event | Incident | N:1 | 多条告警聚合成一事件 |
| Incident | TimelineItem | 1:N | 全程留痕 |
| Incident | IncidentAction | 1:N | 操作审计 |
| Incident | Postmortem | 1:1 | 一个事件一次复盘 |
| Incident | AIInsight | 1:N | AI 多次介入 |
| Postmortem | ActionItem | 1:N | 改进项跟踪 |

---

## 五、RBAC 权限模型 ★（底层权限控制）

### 5.1 设计原则

> **RBAC 作为 Vigil 的底层权限控制，角色和权限均可由使用者自行配置和管理**（见 §1 设计基线第 12 条）。

- **不预设死角色**：系统不强制"admin/responder/subscriber"这种写死的三档。内置几个常见角色仅为便利，使用者可自由增删改。
- **权限是细粒度动作**：权限点（Permission）是"对某资源能做什么操作"的最小单元，可组合成角色。
- **角色可自定义**：使用者按自己的组织架构创建角色，赋予任意权限点组合。
- **绑定有作用域（scope）**：角色绑定到"组织级 / 团队级"两个作用域，实现软隔离下的灵活授权。
- **平台级保留权限**：少量管理类权限（如"管理角色定义""管理全局集成"）只在组织级授予，避免团队管理员越权。

### 5.2 权限模型三元组

采用经典 RBAC，并扩展作用域：

```
User ──(RoleBinding, scope)──> Role ──> Permission
                                 │
                                 └─ 作用域 scope ∈ {org, team}
```

```yaml
Permission:            # 权限点，系统内置的细粒度动作
  code: "incident.ack" # <资源>.<动作>
  name: "确认事件"
  description:
  scope_level: org | team   # 此权限可授予的作用域层级

Role:                   # 角色，使用者可自定义
  id:
  name:                 # 如 "一线值班"
  description:
  builtin: bool         # 是否系统内置（内置可复制不可删）
  permissions: [permission_code, ...]
  scope_level: org | team   # 此角色可用于哪个作用域

RoleBinding:            # 把角色授予用户（带作用域）
  id:
  user_id:
  role_id:
  scope:                # 作用域实例
    level: org | team
    team_id:            # level=team 时必填
  granted_by:
  granted_at:
  expires_at:           # 可选，临时授权
  source_incident_id:   # 事件级临时授权来源（跨团队 @人自动发放，M8.3；0=非临时授权），供 incident 收口精确撤销
```

### 5.3 权限点清单（系统内置，使用者在此基础上配角色）

权限点按资源域组织，命名规范 `<resource>.<action>`：

| 资源域 | 权限点 | 典型语义 |
|--------|--------|---------|
| **incident** | `incident.view` `incident.create` `incident.ack` `incident.escalate` `incident.resolve` `incident.reopen` `incident.reassign` `incident.snooze` `incident.add_responder` `incident.runbook.execute` `incident.delete` | 事件全生命周期 |
| **event** | `event.view` `event.view_unrouted` | 告警查看 |
| **service** | `service.view` `service.create` `service.update` `service.delete` `service.route_override` | 服务管理 |
| **schedule** | `schedule.view` `schedule.create` `schedule.update` `schedule.delete` `schedule.override` | 排班 |
| **escalation** | `escalation.view` `escalation.create` `escalation.update` `escalation.delete` | 升级策略 |
| **runbook** | `runbook.view` `runbook.create` `runbook.update` `runbook.delete` `runbook.execute` | 处置手册 |
| **integration** | `integration.view` `integration.create` `integration.update` `integration.delete` | 接入点 |
| **postmortem** | `postmortem.view` `postmortem.create` `postmortem.update` `postmortem.publish` `postmortem.actionitem.manage` | 复盘 |
| **team** | `team.view` `team.create` `team.update` `team.delete` `team.member.manage` | 团队 |
| **user** | `user.view` `user.create` `user.update` `user.disable` `user.im.bind` | 用户 |
| **role** | `role.view` `role.create` `role.update` `role.delete` `role.assign` | 角色（管理角色定义本身）|
| **notification** | `notification.rule.view` `notification.rule.update` | 通知规则 |
| **admin** | `admin.settings` `admin.audit.view` `admin.apikey.manage` `admin.global_integration` | 平台级管理 |

> 权限点集合是**系统固定的枚举**（这是系统能力的边界），但**角色如何组合这些权限点完全由使用者决定**。需要新能力时通过新增权限点 + 版本化来扩展。

### 5.4 内置角色（仅为便利，均可改）

系统出厂自带几个常用角色（角色均可自配置、可改可复制），使用者可直接用、复制或忽略：

| 角色 | 作用域 | 权限点（简） | 定位 |
|------|--------|------------|------|
| `org_admin` | org | 全部 | 组织超管，仅谨慎授予 |
| `team_admin` | team | team/service/schedule/escalation/runbook/integration 的全管理 + team.member.manage | 团队管理员 |
| `responder` | team | incident.* + event.view + runbook.view/execute + postmortem.view | 一线值班，处置事件 |
| `responder_lead` | team | responder 的权限 + incident.reassign + postmortem.create/publish | 值班长/技术负责人 |
| `subscriber` | team | incident.view + event.view + postmortem.view | 只读干系人（如业务方） |
| `oncall` | team | responder 的权限 + schedule.override（限自己） | 值班人，可换班 |

> 这些角色 `builtin:true`，可被复制成自定义角色，但内置定义不可删除（保证系统开箱可用）。

### 5.5 授权与鉴权流程

**授权（配角色）**——管理者操作：
1. 管理者创建/编辑 Role，勾选权限点（需有 `role.create/update` 权限）。
2. 通过 RoleBinding 把 Role 授予 User，选择作用域（org 或具体 team）。
3. 临时授权可设 `expires_at`（如值班期间临时给某人 team_admin）。

**鉴权（检查权限）**——每次操作：
```
操作请求 (user, action, resource)
  │
  ├── 1. 解析 action → permission_code（如 "incident.ack"）
  ├── 2. 解析 resource → 所属 scope（如 incident.team_id → team=X）
  ├── 3. 查 user 在 scope={org} 和 scope={team:X} 下的所有 RoleBinding
  ├── 4. 合并这些 RoleBinding 的权限点集合 P
  └── 5. 判定 permission_code ∈ P ?
         是 → 允许；否 → 拒绝（记审计日志）
```
- **权限合并规则**：org 级和 team 级权限取**并集**（任一授予即生效），符合"组织超管能管所有团队"的直觉。
- **作用域约束**：一个在 team=A 有 responder 角色的人，对 team=B 的事件默认无权（软隔离）。
- **资源归属即作用域**：Incident 归属 team_id，对它操作就检查该 team 作用域的权限。

### 5.6 RBAC 与软隔离的配合

| 场景 | 行为 |
|------|------|
| 团队 A 的事件 | 默认只有 A 团队成员（有相应角色）可见可操作 |
| 跨团队协作 | 通过 `add_responder` 把 B 团队的人拉进事件，拉进来的人获得**该事件范围**的临时 responder 权限（事件关闭即失效） |
| 组织级管理者 | 在 org scope 授 role，跨所有团队生效 |
| 平台级配置（角色定义、全局集成） | 仅 `org_admin`，不可下放到团队 |

> **实现（M8.3）**：临时授权是一个 **team scope**（该 incident 所属 team，非 org）的内置 `responder`
> RoleBinding，带 `expires_at`（默认 24h 兜底）与 `source_incident_id`（标记来源）。
> 仅在被拉人对该 team 无处置权限时发放（不放宽软隔离），incident 收口（closed/resolved/merged）时按
> `source_incident_id` 撤销，`expires_at` 作过期兜底。见 `internal/incident/temp_grant.go`、
> capabilities/05-im-chatops.md §5。

### 5.7 RBAC 在 IM-first 下的延伸

IM-first 意味着大量操作发生在 IM。RBAC 必须：
- **IM 操作同样鉴权**：IM 里某人的 ack 操作，先通过 `im_accounts` 映射到 User，再走标准鉴权流程，绝不因"在 IM 里"就放行。
- **IM 内权限感知**：交互卡片只展示用户**有权操作**的按钮（无 `incident.escalate` 权限则不显示"升级"按钮）。
- **拉人即授权**：IM 里把某人拉进作战室 = 触发 `add_responder`，自动授予事件级临时权限。

---

## 六、已决设计点说明

以下两点已在 §1 设计基线中确认，此处说明它们在模型中的具体体现：

1. **团队嵌套不继承权限**：团队树（`Team.parent_team_id`）仅用于组织结构展示，**权限不沿树向下传递**。父团队管理员默认无权操作子团队数据。若确需跨团队管理，按"在更高层级（org scope）授予相应角色"处理，不在模型中引入继承语义——避免越权漏洞。

2. **不做团队私有角色**：Role 是**全局实体**，所有团队共享同一套角色定义。团队级的差异化完全靠 RoleBinding 的 scope（org/team + team_id）承载。同一角色可在不同团队以不同 scope 绑定给不同用户，已能覆盖绝大多数授权场景，不引入"团队私有角色"以免增加管理负担。

---

## 七、下一步

数据模型与 RBAC 作为骨架已定型。后续按以下顺序展开各能力域的详细功能设计（每域一份 `docs/capabilities/` 文档）：

1. **能力域 1-2：告警接入与归一化**（Event 怎么进来、怎么归一）
2. **能力域 3-4：分诊降噪与路由**（Event 怎么变成 Incident、怎么找到 Service）
3. **能力域 5-6：排班与升级**（谁在班、没人理怎么办）
4. **能力域 7：通知**（怎么送达）
5. **能力域 8：IM 协同** ★（差异化核心，IM-first）
6. **能力域 9：Runbook 处置**（诊断内置 / 处置外接，分两档）
7. **能力域 10-11：时间线与 AI**（AI 横向 Copilot）
8. **能力域 12：复盘**
9. **能力域 13-17：管理/集成/报表/部署/可观测性**

> 你可以基于本数据模型文档补充澄清，然后告诉我从哪个能力域开始深入。
