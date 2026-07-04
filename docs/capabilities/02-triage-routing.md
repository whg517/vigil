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
  condition:                   # 触发条件（label 匹配 + 时间窗）
    labels:
      env: "maintenance"
    time_window: "2026-06-20 02:00-04:00"
  action: suppress             # suppress | reduce_severity
  expires_at: "..."
```

- **匹配**：规则引擎评估 Event 的 labels + 当前时间。
- **动作**：
  - `suppress`：Event 标记 `IsNoise=true`，不创建/并入 Incident，仅留痕（可审计）。
  - `reduce_severity`：降级处理（如 critical→warning）。
- **可配置**：规则增删改由 team_admin 负责（权限点 `notification.rule.update` 同类）。

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

---

## 7. 实现映射（v0.1）

| 文档章节 | 代码位置 |
|---------|---------|
| §2.2 去重（M3.1） | `internal/triage/engine.go`（`checkDedup`：Redis SETNX + 窗口） |
| §2.2/§2.4 窗口可配（C9） | `internal/triage/engine.go`（`SetWindows`）+ `internal/config/config.go`（`Triage.{Dedup,Aggregate}Window`，env `VIGIL_TRIAGE_*`，默认 5m） |
| §2.3 抑制规则（M3.2） | `internal/triage/suppression.go`（`SuppressionEngine.Evaluate/Apply`：label 全等 + 时间窗 + severity_filter + preserve_critical 守卫；过期规则跳过（B15）；多命中按 match_labels 具体度排序（B15）；action=suppress/reduce_severity） |
| §2.3 抑制接入流水线 | `internal/triage/engine.go`（`Process` 去重后、路由前评估，§2.1 三层顺序） |
| §2.4 相关性聚合（M3.3） | `internal/triage/engine.go`（`aggregate`：同 service+severity 窗口内并入/创建 Incident） |
| §2.5 噪音判定 | `Event.is_noise`（suppress/dedup 标记，留痕可申诉） |
| §2.7 resolved 处理（M3.7） | `internal/triage/engine.go`（`handleResolved`） |
| §3.1 路由匹配（M4.1/M4.2，C2/B14） | `internal/triage/engine.go`（`route`：slug 直达 → `Service.labels` 子集匹配（glob + 具体度优先）→ Integration 默认 service 兜底） |
| §3.2 未路由重路由（M6） | `internal/triage/handler.go`（`POST /events/:id/reroute`，`Engine.Reroute`；权限 `service.route_override`，团队软隔离） |
| 抑制规则 API（含 expires_at，B15） | `internal/notification/handler.go`（SuppressionRule CRUD + `expires_at` 可设/清除，权限 `suppression.*`） |

