# 能力域 3-4：分诊降噪与路由

| 字段 | 内容 |
|------|------|
| **覆盖 PRD** | 能力域 3（分诊）M3.1~M3.7、能力域 4（路由）M4.1~M4.5 |
| **文档版本** | v0.1 |
| **创建日期** | 2026-06-20 |
| **关联** | [`data-model.md`](../data-model.md) §3.3 Event/Incident、§3.2 Service；[`architecture.md`](../architecture.md) §3.1/§3.2 |

---

## 1. 目标

承接归一化后的 Event，完成两件事：

1. **分诊降噪（能力域 3）**：把海量、嘈杂的 Event **聚合成少量、有上下文的 Incident**——该响应的留下，该忽略的标记为噪音。这是"少打扰"的核心。
2. **路由（能力域 4）**：把 Event/Incident **匹配到归属 Service**，从而绑定升级策略与排班。这是"派给谁"的前提。

本域体现 **Event/Incident 分离** 的核心设计——Event 是原始信号，Incident 才是人介入的对象。

---

## 2. 分诊：Event → Incident 的聚合机制

### 2.1 三层处理（呼应 architecture §3.2）

```
Event 进入 ──► [去重] ──► [抑制] ──► [相关性聚合] ──► Incident
                 │           │            │
                 ▼           ▼            ▼
              丢弃重复     标记噪音    合并/创建 Incident
```

### 2.2 去重（M3.1）

**目标**：相同告警的重复推送不重复处理。

- **依据**：Event 的 `DedupKey`（归一化阶段生成）。
- **实现**：Redis SET，`SET dedup:{dedup_key} event_id EX <窗口>`。窗口内重复 key 命中即丢弃。
- **firing/resolved 配对**：resolved Event 不丢弃，而是关联同 DedupKey 的 firing，触发解决流程。
- **窗口**：默认 5 分钟，可配（避免短时风暴导致去重表膨胀）。

### 2.3 抑制规则（M3.2）

**目标**：满足特定条件时主动抑制（维护窗口、已知问题、低优先级）。

```yaml
suppression_rule:
  name: "维护窗口抑制"
  kind: maintenance            # adhoc（默认）| maintenance
  condition:                   # 触发条件（label 匹配 + 时间窗）
    labels:
      env: "maintenance"
    time_window:               # RFC3339 计划起止（维护窗口靠此表达）
      start: "2026-06-20T02:00:00+08:00"
      end:   "2026-06-20T04:00:00+08:00"
  action: suppress             # suppress | reduce_severity
  expires_at: "..."            # 到期自动失效（软失效，无需人工清理）
```

- **匹配**：规则引擎评估 Event 的 labels + 当前时间。
- **动作**：
  - `suppress`：Event 标记 `IsNoise=true`，不创建/并入 Incident，仅留痕（可审计）。
  - `reduce_severity`：降级处理（如 critical→warning）。
- **可配置**：规则增删改由 team_admin 负责（权限点 `notification.rule.update` 同类）。

#### 2.3.1 kind：日常降噪 vs 维护窗口（维护窗口独立操作流）

`SuppressionRule.kind` 把两类语义不同的规则区分开，便于前端做**维护窗口专属入口/列表**：

| kind | 语义 | time_window | expires_at | 典型用法 |
|------|------|-------------|------------|----------|
| `adhoc`（默认） | 日常降噪抑制 | 可空（永久，靠 `enabled` 启停）或粗粒度时段 | 可选 | 已知噪音、低优先级告警长期压制 |
| `maintenance` | 计划内维护窗口 | **有明确起止**（`{start,end}` RFC3339） | 建议设置 → 到期自动失效 | 变更/升级窗口内静默相关告警 |

- **kind 只是分类标签，不改变匹配逻辑**：两类都走同一套 `matchRule`（label 全等 + `time_window` + `severity_filter`），`preserve_critical` 守卫一样生效（默认 critical 不被抑制，避免维护期误杀真故障）。
- **窗口语义**：`time_window.{start,end}` 存在时，仅当前时间落在 `[start, end]` 内才命中抑制；**窗口外正常告警**。
- **自动到期**：`expires_at` 到期后规则在评估时不再命中（软失效，无定时清理任务）——这就是维护窗口"用完即失效"，无需人工回收。
- **API**：
  - 创建/更新请求体支持 `kind`（可选，默认 `adhoc`）与完整 `time_window.{start,end}`；服务端校验 `start < end`、RFC3339 格式，`start`/`end` 须成对出现，违反返 `400`。
  - 列表支持 `GET /suppression-rules?kind=maintenance`（或 `adhoc`）过滤，供前端维护窗口专属列表。非法 `kind` 返 `400`。
  - 前端需暴露 `kind` / `time_window`（start/end）/ `expires_at` / `severity_filter` 表单，并为维护窗口提供独立入口。

### 2.4 相关性聚合（M3.3）★ 核心

**目标**：同一故障触发的多条 Event 聚合成**一个** Incident，避免轰炸。

**聚合键**：`service + severity`（默认）+ 可选额外 label 维度。

**算法**：
```
新 Event 进入，计算聚合键 K = hash(service, severity, ...)
  │
  ├── 存在活跃 Incident（同 K，且在聚合窗口内，状态 ∈ {triggered, acked}）
  │     └─► 并入该 Incident（related_events 追加，时间线记 event_attached）
  │
  └── 不存在
        └─► 创建新 Incident（若 Service.auto_create_incident=true）
```

- **聚合窗口**：默认 5 分钟。窗口外的同 K Event 视为新故障，创建新 Incident。
- **窗口查询**：PostgreSQL 窗口函数（`PARTITION BY 聚合键 ORDER BY received_at`）。
- **聚合策略可配**（内建为主，可配置为辅）：
  - 默认：`service + severity`，5 分钟窗口。
  - 高级：用户可定义自定义聚合规则（按更多 label 维度、不同窗口）——这是工作流引擎的挂钩点。

### 2.5 噪音判定（M3.4）

**目标**：识别并过滤低价值告警。

- **规则式（默认）**：基于抑制规则 + 历史模式（如某告警过去 24h 内 resolved 自动恢复 10+ 次且无人 ack → 判噪音）。
- **AI 辅助（能力域 11.5）**：LLM 学习模式识别噪音，产出 AIInsight 建议降级/抑制（human-in-the-loop）。
- 噪音 Event 的 `IsNoise=true`，仅留痕不进 Incident，但在分诊视图可见、可申诉（避免误杀）。

### 2.6 Incident 创建与合并（M3.5/M3.6）

- **自动创建**：按 `Service.auto_create_incident` 决定。critical 默认自动创建。
- **Incident 合并**：
  - 人工：响应者在 UI/IM 选 N 个 Incident 合并（`merged_into` 指向主 Incident）。
  - AI 建议：能力域 11.1 产出 `dedup_suggestion`，人确认后合并。
- 合并后：主 Incident 继承所有 related_events、时间线、responders。

### 2.7 resolved 处理（M3.7）

告警源发来 resolved Event 时：
- 关联同 DedupKey 的活跃 Incident。
- **自动 resolve**（可配）：默认自动把 Incident 推向 resolved 态。
- **仅提示**（可配）：只在时间线记一笔，等人确认 resolve（更保守，避免告警源误报 resolved 导致真故障被掩盖）。

---

## 3. 路由：找到归属 Service

### 3.1 Service 匹配（M4.1/M4.2）

**目标**：把 Event/Incident 绑定到正确的 Service，从而获得升级策略 + 排班。

```yaml
service:
  name: "payment-api"
  labels:                    # 路由匹配锚点
    service: "payment"
    env: "prod"
    tier: "1"
```

**匹配规则**（确定性裁决，同输入总得同一结果）：
1. **slug 直达**：`Event.labels["service"]` 等值匹配 `Service.slug`——最明确的直达路径，优先。
2. **多标签子集匹配**：`Event.labels ⊇ Service.labels`——Service 的每个标签 `k=pattern` 都能在
   Event.labels 中找到对应 `k` 且值匹配。值支持 **glob**（`path.Match`，如 `env=prod-*`）。
   无 labels 的 Service 不参与子集匹配（避免"空规则匹配一切"）。
3. **多条命中裁决（M4.2）**：按**匹配标签数**降序（更具体的 Service 优先），标签数相同再按
   Service ID 升序——保证多命中时结果稳定，不"随机命中一条"。
4. **Integration 默认归属兜底（B14）**：以上均未命中时，回退 Event 所属 Integration 预设的默认
   `service`（接入点直达归属，跳过 label 匹配；默认 service 已停用则不回退）。

**匹配时机**：
- 默认：归一化后立即路由（填 `Event.service_id`）。
- 也可由 Integration 预设 `service_id`（接入点直接归属某服务，作为 label 匹配的兜底，见规则 4）。

### 3.2 未命中路由（M4.3）

> **注**：开启自动供给（§3.5）时，匹配失败的 Event 在进入 unrouted 池**之前**先尝试自动创建 Service；
> 仅当自动供给也未产出 Service（未开启 / 无服务键 label / 团队或默认策略解析不到）时，才落入下列 unrouted 流程。

匹配失败的 Event 进入 `unrouted` 池：
- 需 `event.view_unrouted` 权限查看（避免越权）。
- team_admin 可手动指派 Service（`POST /events/:id/reroute`，权限 `service.route_override`，
  对目标 Service 按 team 软隔离；指派后立即按该 Service 聚合/建单），或补全路由规则。
- unrouted 的 critical 告警要有**兜底通知**（通知全员/admin，避免漏响应）。

### 3.3 服务拓扑（M4.4，可选）

- 定义服务依赖关系（payment-api → mysql → redis）。
- 用途：**影响面分析**——某底层服务故障时，标记其上层依赖服务可能受影响。
- 非核心，初期可不做，预留 Service 模型的 `depends_on` 字段。

### 3.4 路由命中后的绑定（M4.5）

Service 命中后，Incident/Event 自动继承：
- `service.escalation_policy_id`（升级策略）
- `service` 关联的 Schedule（排班）
- `service.runbook_ids`（处置手册，供能力域 9）

### 3.5 自动供给 Service（方案 C）★ 大规模场景

**背景**：路由靠 label 匹配既有 Service，但在 100+ 微服务 + 操作系统 + 中间件的规模下，
「一个微服务一个 Service」意味着上百次手工建服务 + 配策略/排班，配置负担极重（对标 PagerDuty/Opsgenie
均以 IaC/目录同步/动态路由规避）。自动供给让**新服务的告警首次进来即被接住**，无需预先手工建 Service。

**机制**：§3.1 全部匹配失败后、进入 unrouted（§3.2）**之前**，若开启自动供给且满足条件，即时创建一个
轻量 Service 并继续按其聚合建单。放在 route() 之后、以 Process 侧的独立步骤实现（保持 route() 为纯匹配、无副作用）。

**触发条件（全部满足才创建，任一不满足则回落 unrouted，不制造静默盲区）**：
1. 开关 `auto_provision_enabled=true`（**默认关闭**，未开启行为与今天完全一致——遵循「无配置不回归」）。
2. Event 携带**服务键** label（默认 key=`service`，可配），其值非空且通过 slug 白名单正则校验
   （防 `service=up` 之类垃圾值刷出脏服务）。
3. 能解析**归属团队**：优先用**团队键** label（默认 key=`team`）匹配 `Team.slug`，否则用配置的兜底团队 slug；
   都解析不到 → 回落 unrouted。
4. 该团队已配 `default_escalation_policy` → 作为新 Service 的升级策略。**无默认策略则不创建**
   （否则新服务无策略、不升级，等于把「未路由」换成「已路由但静默」，更危险）。critical 仍照旧走 unrouted 兜底通知。

**创建内容**：`slug=服务键值, name=服务键值, labels={<服务键>:值}, team=解析团队,
escalation_policy=团队默认策略, source=auto, provisioned_at=now, status=active, auto_create_incident=true`。
创建后即绑定到 Event、按该 Service 走既有聚合/建单/升级流程（与手工建的 Service 完全一致）。

**幂等与并发**：`Service.slug` 唯一约束兜底——并发同名告警撞 `ConstraintError` 时改为「查回既有同 slug Service」返回，
保证只建一个（复刻 `createIncident` 的唯一冲突重试模式）。

**治理（防实体泛滥）**：
- `source=auto` 标记 + `provisioned_at`：前端可按来源筛选、批量「转正」（改 `manual` 并补配 runbook/排班）。
- slug 白名单正则限制可自动创建的服务名，杜绝脏值。
- 自动创建即 `active`（不丢告警），但作为「待认领」由 team_admin 复核。
- **过期清理**（`internal/servicesync.Pruner`，见 §3.5.2）：`source=auto` 且 N 天无新 Event 的服务定时停用，防长尾泛滥。
- **绝不触碰 `source=manual`**：主动同步（P2，见 §3.5.1）与过期清理均只作用于 auto 服务。

**可观测**：每次自动供给打点 `metrics.ServicesAutoProvisioned` + 结构化日志（自动创建不静默，便于审计与容量观察）。

#### 3.5.1 主动同步（P2，push）—— 服务上线即存在

与懒供给（pull，未路由即时创建）互补：`internal/servicesync` 周期性从**外部源**拉取「期望服务清单」，
upsert `source=auto` 服务（挂解析出的团队、继承团队默认策略），使服务在**首条告警到来前**即已存在。

- **源**：`file`（本地 JSON 文件，GitOps 随卷/仓库更新）| `http`（返回 JSON 的目录/CMDB 端点）。
  清单条目：`{slug, name, team, labels}`。
- **调和规则**（与懒供给同款安全底线）：无 slug / 团队解析不到 / 团队无默认策略 → 跳过；
  slug 不存在 → 建 auto；slug 存在且 `auto` → 更新 name/labels/team/策略对齐清单；
  slug 存在且 `manual` → **跳过（尊重人工，绝不覆盖）**。单条失败不中断整批。
- **配置**：`VIGIL_SERVICE_SYNC_ENABLED`（默认 false）/ `_SOURCE_TYPE`（file|http）/ `_SOURCE_URL` /
  `_DEFAULT_TEAM` / `_INTERVAL`（默认 5m）。开关关不启动（无回归）。
- **可观测**：`metrics.ServicesSynced{result=created|updated|skipped}` + 结构化日志。

#### 3.5.2 过期清理（治理）—— auto 服务长尾自动收缩

`internal/servicesync.Pruner` 周期把过期的 auto 服务停用，避免自动供给出的服务无限堆积。

- **判定过期**（同时满足，缺一不可，避免误伤）：`source=auto` + `status=active` +
  `provisioned_at` 早于窗口（保护刚同步建出、尚无告警的新服务）+ 窗口内无任何 `received_at` 内的 Event。
- **只停用不删除**：`status=disabled`，保留历史 Incident 关联；人工可重新启用或转正。**绝不触碰 `manual`**。
- **配置**：`VIGIL_SERVICE_CLEANUP_ENABLED`（默认 false）/ `_STALE_DAYS`（默认 30）/ `_INTERVAL`（默认 6h）。
- **可观测**：`metrics.ServicesPruned` + 结构化日志（含被停用的 slug 列表）。

**分阶段**：懒供给（P1）+ 前端治理 UI（source 徽章/筛选/转正）+ 主动同步（P2 push）+ 过期清理均已落地。方案C 收尾完成。

---

## 4. 关键状态与数据流

### 4.1 Event 处理状态

```
received ──► normalized ──► [去重] ──┬──► dedup_skipped（重复，丢弃）
                                      │
                                      ├──► [抑制] ──► suppressed（噪音，留痕）
                                      │
                                      └──► [聚合] ──► aggregated（并入/创建 Incident）
                                                       │
                                                       ▼
                                                  Incident 诞生
```

### 4.2 Incident 诞生触发

聚合产生新 Incident 时，立即触发（交给能力域 5/6）：
1. 排班引擎算"此刻在班人"。
2. 启动升级计时（Asynq 延迟任务）。
3. 首轮通知（能力域 7）。
4. IM 作战室可选创建（能力域 8）。

---

## 5. 可靠性

| 要求 | 实现 |
|------|------|
| **不漏真告警** | 去重/抑制仅作用于重复/明确噪音；critical 默认不被抑制规则误杀（规则可设 `preserve_critical`） |
| **可观测** | 各阶段处理量、噪音率、unrouted 量暴露 metrics |
| **可申诉** | 被判噪音/unrouted 的 Event 在 UI 可见、可手动提升为 Incident |
| **幂等** | 聚合并入 Incident 的操作幂等（重复 Event 不重复并入） |

---

## 6. 开放问题

| # | 问题 | 倾向 |
|---|------|------|
| Q1 | 自定义聚合规则的表达形式（DSL / UI 配置） | UI 表单为主，DSL 为高级选项 |
| Q2 | 服务拓扑的存储与查询效率 | 邻接表 + 缓存，初期不优化 |
| Q3 | AI 噪音判定的介入时机（实时 vs 离线） | 离线建议为主，避免实时 LLM 拖慢分诊 |
| Q4 | 大规模（100+ 微服务）Service 配置负担 | **已裁决**：不要求 1:1 建服务。①路由靠 label 匹配（既有）；②未匹配时**自动供给** `source=auto` 服务（§3.5，本轮）；③后续 push 同步 + 前端治理。人工配置量从「N 个微服务」降到「M 个团队各配一次默认升级策略」 |

---

## 7. 实现映射（v0.1）

| 文档章节 | 代码位置 |
|---------|---------|
| §2.2 去重（M3.1） | `internal/triage/engine.go`（`checkDedup`：Redis SETNX + 窗口） |
| §2.2/§2.4 窗口可配（C9） | `internal/triage/engine.go`（`SetWindows`）+ `internal/config/config.go`（`Triage.{Dedup,Aggregate}Window`，env `VIGIL_TRIAGE_*`，默认 5m） |
| §2.3 抑制规则（M3.2） | `internal/triage/suppression.go`（`SuppressionEngine.Evaluate/Apply`：label 全等 + 时间窗 + severity_filter + preserve_critical 守卫；过期规则跳过（B15）；多命中按 match_labels 具体度排序（B15）；action=suppress/reduce_severity） |
| §2.3.1 kind（维护窗口独立操作流） | `ent/schema/suppression_rule.go`（`kind` 枚举 adhoc/maintenance）；`internal/notification/handler.go`（create/update 支持 kind + time_window `{start,end}` 校验、list `?kind=` 过滤）；语义走同一 `matchRule`，maintenance 靠 time_window 计划起止 + expires_at 自动到期 |
| §2.3 抑制接入流水线 | `internal/triage/engine.go`（`Process` 去重后、路由前评估，§2.1 三层顺序） |
| §2.4 相关性聚合（M3.3） | `internal/triage/engine.go`（`aggregate`：同 service+severity 窗口内并入/创建 Incident） |
| §2.6 人工合并（M3.5/M3.6） | `internal/incident/merge.go`（`Service.Merge`：源单 `merged_into` 指向主单 + 置 closed，events/responders 转移，pending 升级取消，双写时间线 + merge 审计）；`POST /incidents/:id/merge`（权限 `incident.merge`）；被合并单发 `IncidentMerged` 事件 → escalation `OnMerged` 取消 pending、多端同步 |
| §2.6 AI 合并联动 | `internal/ai/diagnose.go`（`applyDedupMerge`：accept `dedup_suggestion` 时经 `IncidentMerger` 接口调 `Service.Merge` 真正合并 `merge_candidate_ids` 并置 `applied`；未注入合并器时降级仅 `accepted`） |
| §2.5 噪音判定 | `Event.is_noise`（suppress/dedup 标记，留痕可申诉） |
| §2.7 resolved 处理（M3.7） | `internal/triage/engine.go`（`handleResolved`） |
| §3.1 路由匹配（M4.1/M4.2，C2/B14） | `internal/triage/engine.go`（`route`：slug 直达 → `Service.labels` 子集匹配（glob + 具体度优先）→ Integration 默认 service 兜底） |
| §3.2 未路由重路由（M6） | `internal/triage/handler.go`（`POST /events/:id/reroute`，`Engine.Reroute`；权限 `service.route_override`，团队软隔离） |
| §3.5 自动供给 Service（方案 C，懒供给） | `internal/triage/engine.go`（`tryAutoProvision`：服务键 label + slug 白名单 + 团队解析 + 团队默认策略；`Process` 在 route 未命中、unrouted 之前调用；slug 唯一冲突查回既有幂等）；`ent/schema/service.go`（`source`/`provisioned_at`）；`ent/schema/team.go`（`default_escalation_policy` 边）；`internal/config/config.go`（`Triage.AutoProvision*`，env `VIGIL_TRIAGE_AUTO_PROVISION_*`，默认关闭）；`internal/metrics`（`ServicesAutoProvisioned`） |
| §3.5.1 主动同步 Service（方案 C，push） | `internal/servicesync`（`Source`/`FileSource`/`HTTPSource` + `Syncer.Reconcile/Run`：拉清单 upsert auto 服务、跳过 manual、继承团队默认策略）；`internal/server/wire.go`（ticker 装配，`cfg.ServiceSync.Enabled` 门控）；`internal/config/config.go`（`ServiceSync`，env `VIGIL_SERVICE_SYNC_*`，默认关闭）；`internal/metrics`（`ServicesSynced{result}`） |
| §3.5.2 过期清理 auto 服务（方案 C，治理） | `internal/servicesync`（`Pruner.Prune/Run`：source=auto + active + provisioned_at<窗口 + 窗口内无 Event → disable，只停不删、不碰 manual）；`internal/server/wire.go`（ticker 装配，`cfg.ServiceCleanup.Enabled` 门控）；`internal/config/config.go`（`ServiceCleanup`，env `VIGIL_SERVICE_CLEANUP_*`，默认关闭）；`internal/metrics`（`ServicesPruned`） |
| §3.5 治理 API/UI（source 筛选/转正、团队默认策略） | `internal/service/handler.go`（list `?source=` + update 转正）；`internal/auth/handler_user_team.go`（team `default_escalation_policy_id` 读写 + `teamResponse`）；`internal/escalation/handler_policy.go`（list `?team_id=`）；前端 `web/src/pages/services.tsx`（source 徽章/筛选/转正）、`web/src/pages/users-teams.tsx`（默认策略选择器） |
| 抑制规则 API（含 expires_at，B15；kind + time_window，维护窗口） | `internal/notification/handler.go`（SuppressionRule CRUD + `expires_at` 可设/清除 + `kind` 分类 + `time_window.{start,end}` 校验 + list `?kind=` 过滤，权限 `suppression.*`） |

