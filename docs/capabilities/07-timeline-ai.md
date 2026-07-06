# 能力域 10-11：事件时间线与 AI 智能

| 字段 | 内容 |
|------|------|
| **覆盖 PRD** | 能力域 10（时间线）M10.1~M10.6、能力域 11（AI）M11.1~M11.10 |
| **文档版本** | v0.1 |
| **创建日期** | 2026-06-20 |
| **关联** | [`data-model.md`](../data-model.md) §3.3 TimelineItem/AIInsight；[`architecture.md`](../architecture.md) §ai 模块 |

---

## 1. 目标

两个全程贯穿的能力：

- **时间线（能力域 10）**：自动捕获事件全程，为协同和复盘打基础——"全程留痕"。
- **AI 智能（能力域 11）**：作为横向 Copilot 贯穿分诊/诊断/复盘，让人更聪明——"AI 是底层能力，不是加钱 SKU"。

---

## Part A：事件时间线

### A1. 设计原则

- **自动捕获为主**：系统/人工动作自动记录，无需人手动维护。
- **时间线是事实记录**：只追加，不修改（除人工备注）。
- **服务两个下游**：实时协同（看事件进展）+ 事后复盘（复盘的事实依据）。

### A2. 自动捕获（M10.1/M10.2）

| 动作 | 来源 | 记录内容 |
|------|------|---------|
| Incident 创建 | system | 时间、触发源、首条 event |
| Event 聚合进来 | system | event 摘要 |
| 状态变更 | system/user | 触发态→升级/ack/resolve |
| 升级 | system | 升到哪级、通知谁 |
| 拉人 | user | 谁拉了谁 |
| runbook 执行 | system/user | 执行了哪步、结果 |
| AI 洞察 | ai | AI 给了什么建议 |
| 备注 | user | 人工笔记 |
| IM 消息（可选） | im | 作战室关键消息 |

### A3. TimelineItem 结构（data-model §3.3）

```go
type TimelineItem struct {
    ID          string
    IncidentID  string
    Timestamp   time.Time
    Type        TimelineType    // incident_created/event_attached/status_changed/...
    Actor       Actor           // system | user | integration | ai
    Content     string          // 人类可读描述
    Detail      map[string]any  // 结构化详情
    Source      string          // web | im | api | system | ai
}
```

### A4. 可视化与交互（M10.3~M10.5）

- **Web 时间线视图**：按时间倒序展示，支持筛选（类型/来源）。
- **IM 内摘要**：作战室定时/命令查看进展摘要。
- **手动追加**（M10.4）：响应者可添加备注条目（如"已联系 DBA"）。
- **IM 消息可选捕获**（M10.5）：作战室含关键词/@机器人的消息回写时间线。

---

## Part B：AI 智能（横向 Copilot）

### B1. 设计原则（呼应设计基线第 7 条）

- **横向贯穿**：AI 渗透分诊/诊断/复盘各环节，不是独立模块。
- **human-in-the-loop**：所有 AI 产出是"建议"，须人 accept/reject 才生效。
- **可溯源**：每条 AI 建议必须带 evidence（引用依据）。
- **可降级**：LLM 不可用时，AI 功能降级，不影响告警主流程。
- **可插拔**：支持云端 LLM 与本地模型。

### B2. AI 介入场景（M11.1~M11.7）

| 阶段 | 能力 | AIInsight.type | 说明 |
|------|------|----------------|------|
| **分诊** | 智能分诊 | `dedup_suggestion` | 建议合并相似 Event/Incident |
| **分诊** | 严重度建议 | `severity_adjustment` | 基于历史建议调高/调低 |
| **分诊** | 智能降噪 | （配合噪音判定） | 学习模式识别噪音 |
| **诊断** | 根因诊断 | `root_cause_hint` | 给根因线索，引用日志/指标/变更 |
| **诊断** | 相似事件 | `similar_incident` | 检索历史相似 Incident 及其复盘 |
| **处置** | Copilot | （建议 runbook） | "这类故障通常用 runbook X" |
| **复盘** | 摘要起草 | `draft_summary` | 为 Incident 起草 summary |
| **复盘** | 复盘起草 | `postmortem_draft` | 起草结构化复盘内容 |

### B3. AIInsight 承载与 human-in-the-loop（M11.8/M11.9）

```go
type AIInsight struct {
    ID          string
    IncidentID  string
    Stage       string          // triage | diagnose | postmortem | copilot
    Type        string          // dedup_suggestion | root_cause_hint | ...
    Content     any             // AI 产出
    Confidence  float32         // 0.0~1.0
    Evidence    []EvidenceRef   // ★ 依据（引用的 Event/日志/时间线）
    Status      AIStatus        // suggested | accepted | rejected | applied
    CreatedAt   time.Time
}
```

```
AI 产出 AIInsight（status=suggested）
   │
   ▼
展示给响应者（IM 卡片 / Web）
   │
   ├── accept ──► status=accepted ──► 应用（如合并 Incident / 填充复盘）
   └── reject ──► status=rejected ──► 不应用，留痕
```

- **evidence 强制**：无依据的 AI 建议不展示（保证可信度）。
- **接受/拒绝都记审计**：用于后续改进 AI。

### B4. 相似事件检索（M11.4）

- 基于 Incident 的 summary/labels/timeline 向量化。
- 初期：PostgreSQL `pgvector` + 文本特征。
- 检索历史相似 Incident，**连同其复盘一起呈现**——"上次类似故障是怎么处理的"。

> **实现现状**：`internal/ai/diagnose.go` 的 `FindSimilar` 主路径走 pgvector——
> `Incident.embedding`（vector(1536) 列）为空时懒计算（LLM Embed 标题+摘要）并回写持久化，
> 用 raw SQL 余弦距离 `<=>` 排序（`SQLRunner` 注入，避免 ai 包依赖 ent driver 内部）。
> pgvector/Embed 不可用时（无扩展、无 LLM key、sqlite 测试）降级回 LIKE 文本匹配。
> `Provider.Embed` 由 GLMProvider 实现（智谱 `embedding-3`，1536 维对齐列定义）。

### B5. LLM Provider 抽象（M11.10）

```go
type LLMProvider interface {
    Name() string
    Complete(ctx, prompt string, opts ...) (string, error)
    Embed(ctx, text string) ([]float32, error)   // 向量化（相似检索用）
}
```

| 类型 | Provider | 场景 |
|------|----------|------|
| 云端 | 智谱 GLM（默认） | 效果好，中文优先，需联网 |
| 本地 | Ollama | 数据不出境，隐私场景 |

- 配置驱动选择，业务层不感知。
- **成本控制**：缓存 + 限流 + 配额（详见开放问题）。

> **Provider 选择（实现现状）**：`VIGIL_LLM_PROVIDER` 选 `glm`（默认）或 `ollama`，未知值回退 glm。
> 装配在 `wire.go` 的 `buildLLMProvider`：按选型构造 `GLMProvider` 或 `OllamaProvider`，外层统一
> 包 `CostController`（缓存/限流/配额）——两条路径对上层同一 `Provider` 接口，业务层不感知。
>
> **Ollama 本地 Provider（`internal/ai/ollama.go`）**：走 Ollama 原生 HTTP 契约（非 OpenAI 兼容层）：
> · `Complete` → POST `{base_url}/api/chat`（`stream=false`，解析 `.message.content`）
> · `Embed` → POST `{base_url}/api/embeddings`（解析 `.embedding`）
> 配置 `VIGIL_LLM_OLLAMA_BASE_URL`（默认 `http://localhost:11434`）、`VIGIL_LLM_OLLAMA_MODEL`
> （默认 `llama3`）、`VIGIL_LLM_OLLAMA_EMBED_MODEL`（默认 `nomic-embed-text`）。`Available()` 只看
> base_url 是否配置，本地服务不可达时在调用层报错并降级（与 GLM 同款容错哲学，不在构造期探测）。
>
> **⚠️ embed 维度必须与 pgvector 列匹配**：`Incident.embedding` 列是 `vector(1536)`，对齐 GLM
> `embedding-3`（1536 维）。Ollama 默认 `nomic-embed-text` 是 **768 维**——切到 Ollama embed 前须：
> 要么把 pgvector 列维度改成与所选 embed 模型一致，要么接受相似检索降级为 LIKE 文本匹配
> （`diagnose.go` 的 `FindSimilar` 已有该降级路径，维度不符时向量写入/余弦检索不可用）。

> **置信度阈值配置化（Q2）**：AI 建议产出门槛由 `VIGIL_LLM_CONFIDENCE_THRESHOLD`（默认 0.6）控制，
> 低于此值的 LLM 建议不产出（避免低置信度建议打扰响应者、拉低 AI 可信度）。`wire.go` 装配时把
> `cfg.LLM.ConfidenceThreshold` 注入 `TriageAIEngine` 与 `CopilotEngine` 的 `SetConfidenceThreshold`
> （Setter 内部 `<=0 保留默认 0.6`，防误配为 0 使一切建议都产出）。

> **实现现状**：`internal/ai/cost.go` 的 `CostController` 实现 `Provider` 接口包装底层 GLM，
> Complete 内部按 缓存→限流→配额→真实调用 顺序过三道闸：
> · 缓存：Redis `vigil:llm:cache:`+sha256(prompt)，TTL 可配（默认 1h），命中省一次调用
> · 限流：Redis ZSET 滑动窗口，按维度每分钟最大请求数
> · 配额：Redis counter 累计 token，达上限拒绝
> 无 Redis 时三道闸全部降级跳过（透传，保证调用可达）。配置 `VIGIL_LLM_COST_*`。
> Embed 走限流但不缓存（向量检索语义稳定，由 ensureEmbedding 回写持久化去重）。

### B6. 可靠性与降级

| 场景 | 处理 |
|------|------|
| LLM 调用失败 | AI 功能降级（不展示建议），不影响告警主流程 |
| LLM 响应慢 | 异步任务承载，不阻塞分诊/响应 |
| LLM 误判 | human-in-the-loop 兜底；拒绝率高的建议类型可调优 prompt |

---

## 开放问题

| # | 问题 | 倾向 |
|---|------|------|
| Q1 | LLM 成本控制（限流/缓存/配额） | ✅ 已实现：CostController 三道闸（缓存+限流+配额），按 org 维度，无 Redis 降级 |
| Q2 | AI 建议的置信度阈值（低于多少不展示） | ✅ 默认 0.6，`VIGIL_LLM_CONFIDENCE_THRESHOLD` 可配（见 B5） |
| Q3 | 本地模型（Ollama）的效果兜底 | ✅ Ollama Provider 已实现（`VIGIL_LLM_PROVIDER=ollama`）；效果差时降级为规则式，不硬依赖 LLM |
| Q4 | 时间线 IM 消息捕获的噪音过滤 | 仅含关键词/命令/标记消息 |
| Q5 | 智能降噪是否做「自动学习/回训」 | ❌ **不做自动回训**（见下方决策）|

### Q5 决策：智能降噪不做无监督自学习 / 模型回训

**结论：不实现自动回训，保留「人确认沉淀」现状。**

现状闭环（已实现，无需回训即可持续优化）：
`TriageAIEngine.suggestNoise` 产出降噪建议 → handler resolve accept →
`diagnose.go` `applyNoiseSuggestion` 把建议沉淀为 `SuppressionRule`；
`/analytics/ai-feedback` 端点已提供各类建议的采纳率反馈，供人工调优 prompt / 规则。

**为什么不做自动回训**：无监督自学习 / 模型回训会**自动改变模型或规则行为**，
这与项目设计基线第 4 条「AI 产出带 evidence + human-in-the-loop」**直接冲突**——
自动改判绕过了人确认这道闸。降噪规则一旦被 AI 自动收紧，可能把真实告警误抑制，
且无人为此负责。因此本期只做「AI 建议 → 人确认 → 沉淀为显式规则」，不做任何
自动改变模型/规则行为的代码。

**未来若要做**：须作为独立能力单独评审，且必须保留人的否决权（建议先影子运行、
人工审阅采纳率与误抑制样本后再灰度，绝不无人值守地自动生效）。
