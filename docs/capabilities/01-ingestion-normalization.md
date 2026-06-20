# 能力域 1-2：告警接入与归一化

| 字段 | 内容 |
|------|------|
| **覆盖 PRD** | 能力域 1（接入）M1.1~M1.8、能力域 2（归一化）M2.1~M2.6 |
| **文档版本** | v0.1 |
| **创建日期** | 2026-06-20 |
| **关联** | [`data-model.md`](../data-model.md) §3.2 Integration、§3.3 Event；[`architecture.md`](../architecture.md) §3.1 接入流水线 |

---

## 1. 目标

让任意告警源（Prometheus、Zabbix、Grafana、云监控、自研系统）都能通过标准方式接入 Vigil，并把异构的告警 payload **归一化成统一的内部 Event 模型**，作为后续分诊/路由/通知的统一输入。

本域是整个数据流的**入口**，核心要求：**接得住、不丢失、说同一种话**。

---

## 2. 核心流程

```
┌──────────────┐   webhook/SMTP/API    ┌──────────────────────────┐
│ 告警源        │ ───────────────────► │  Echo Receiver           │
│ (Prom/Zabbix)│                       │  · 校验 token            │
└──────────────┘                       │  · 落原始 payload        │
                                       │  · 秒级返回 202           │
                                       └────────────┬─────────────┘
                                                    │ Enqueue(Asynq)
                                                    ▼
                                       ┌──────────────────────────┐
                                       │  归一化 Worker            │
                                       │  · 选 Adapter            │
                                       │  · Normalize → Event     │
                                       │  · 写 PostgreSQL          │
                                       └────────────┬─────────────┘
                                                    │ 交付给能力域 3（分诊）
                                                    ▼
                                              [分诊 → 路由 → ...]
```

**设计要点**：
- **接收与处理解耦**：Receiver 只做"校验 + 落库 + 入队"，秒级返回 202 Accepted。绝不在此同步做归一化/分诊（避免下游慢导致告警源超时丢告警）。
- **全异步**：归一化是独立 Asynq 任务，失败自动重试、死信可重放。

---

## 3. 接入方式

### 3.1 接入端点（Integration）

每个接入点是一个 Integration 实体（data-model §3.2），归属某个 Service，决定告警的默认路由。

| 接入类型 | 端点 | 说明 |
|---------|------|------|
| **通用 Webhook** | `POST /api/v1/webhook/{integration_id}` | 任意 JSON payload，用通用适配器尽力解析 |
| **专用适配器** | `POST /api/v1/webhook/{integration_id}` | 按 `integration.type` 选专用适配器（Prometheus/Zabbix/Grafana） |
| **邮件** | SMTP 收信地址 | 收到邮件转 Event，从主题/正文解析 |
| **开放 API** | `POST /api/v1/events` | 程序化推送，供自研系统接入，需 API Key |

### 3.2 鉴权

| 类型 | 鉴权方式 |
|------|---------|
| Webhook | 每个 Integration 一个 **token**，URL 路径携带或 `Authorization` 头校验 |
| 邮件 | 收信地址 + 发件白名单（可选 DKIM/SPF 校验） |
| 开放 API | **API Key**（`X-Vigil-Key` 头），org_admin 管理，权限点 `admin.apikey.manage` |

- token 失败返回 `401`，不落库。
- 鉴权失败也要记审计日志（防探测）。

### 3.3 背压与限流

| 机制 | 触发 | 行为 |
|------|------|------|
| 单接入点限流 | QPS 超 `integration.config.rate_limit` | 429 + 告警接入方 |
| 全局队列积压 | Asynq 队列深度超阈值 | 接入层返回 503，**但 payload 仍落原始表**，恢复后回灌 |
| 熔断 | 某接入点持续大量无效 payload | 临时封禁 + 告警 |

> 关键：**限流/背压时 payload 必须先落 `raw_event` 表**，绝不在内存丢弃——Vigil 漏一条告警可能导致一次故障无人响应。

---

## 4. 归一化：统一 Event 模型

### 4.1 Event 模型（呼应 data-model §3.3）

```go
type Event struct {
    ID            string                 // Vigil UUID
    IntegrationID string                 // 来源接入点
    ServiceID     string                 // 路由命中的服务（路由阶段填，初始空）
    SourceEventID string                 // 原始告警 ID（去重 + 幂等键）
    Source        string                 // 告警源，如 "prometheus"
    Severity      Severity               // critical | warning | info
    Status        EventStatus            // firing | resolved
    Summary       string                 // 一句话摘要
    Detail        map[string]any         // 原始 payload（不丢信息）
    Labels        map[string]string      // 路由用标签
    DedupKey      string                 // 去重键
    ReceivedAt    time.Time
    IncidentID    string                 // 聚合到的 Incident（分诊阶段填）
    IsNoise       bool                   // 分诊判定为噪音
}
```

### 4.2 归一化职责（每个 Adapter 实现）

```go
type Adapter interface {
    // Type 返回适配器类型标识
    Type() string
    // Normalize 把原始 payload 归一化为 Event
    Normalize(ctx context.Context, raw []byte, integration *Integration) (*Event, error)
}
```

每个适配器负责四件事（对应 M2.2~M2.5）：

| 职责 | 说明 | 例子（Prometheus） |
|------|------|-------------------|
| **字段映射** | 源字段 → Event 标准字段 | `alerts[0].status` → `Status`；`alerts[0].annotations.summary` → `Summary` |
| **严重度归一** | 源严重度 → critical/warning/info（M2.3，可配映射表） | Alertmanager 无 severity，按 label `severity=critical` 映射 |
| **标签提取** | 源标签 → `Labels`（用于路由匹配） | `alerts[0].labels.service` → `Labels["service"]` |
| **去重键生成** | 算 `DedupKey` | `sha1(source + fingerprint)` |
| **保留原文** | 原始 payload 存 `Detail`，不丢信息（M2.5） | 整个 alertmanager payload 存 Detail |

### 4.3 严重度归一映射

不同告警源严重度表达不一，统一归一到三级。映射表**可配置**（M2.3）：

```yaml
# 默认映射，可在 Integration 配置覆盖
severity_mapping:
  prometheus:
    "severity=critical": critical
    "severity=warning":   warning
    "severity=info":      info
  zabbix:
    "priority=disaster/critical": critical
    "priority=high/average":      warning
    "priority=warning/information": info
```

### 4.4 firing/resolved 配对

告警源通常成对发送 `firing`（触发）和 `resolved`（恢复）。归一化必须正确识别：

- `firing` Event 进入正常流水线。
- `resolved` Event **关联到同 DedupKey 的 firing**，触发 Incident 解决流程（分诊阶段处理，PRD M3.7）。

---

## 5. 内置适配器

| 适配器 | 源 | 归一化要点 |
|--------|----|-----------|
| **Prometheus/Alertmanager** | Alertmanager webhook | 解析 `alerts[]` 数组，每条 alert 一个 Event；fingerprint 作 DedupKey |
| **Zabbix** | Zabbix action script | 解析 trigger/priority；eventid 作 SourceEventID |
| **Grafana** | Grafana alerting webhook | 解析 `alerts[]`；按 label 提取 service |
| **云监控** | 阿里云/腾讯云/AWS SNS | 各云消息结构适配；统一抽 severity + resource |
| **邮件** | SMTP | 从主题解析 severity（关键词匹配），正文解析 detail |
| **通用 JSON** | 任意 | 用户在 Integration 配置"字段路径映射"，通用适配器按配置提取 |

> 所有适配器实现 `Adapter` 接口，注册到插件表。自研监控源可实现该接口接入（可插拔，见 tech-stack §4）。

---

## 6. 可靠性与幂等

| 要求 | 实现 |
|------|------|
| **不丢告警** | Receiver 先落 `raw_event` 表（含原始 payload），再入队；入队失败有原始记录可回灌 |
| **幂等** | 以 `source_event_id` 为幂等键（M1.8）；重复推送的同一告警不产生新 Event，仅更新状态 |
| **可重放** | `raw_event` 表保留原始 payload，任何阶段失败都可从原始重新触发归一化 |
| **可观测** | 每个接入点的接收量、归一化成功率、失败原因暴露到 metrics（`/metrics`） |
| **背压兜底** | 限流/熔断时 payload 落 `raw_event`，标记 `pending`，恢复后回灌（不丢） |

### 错误处理

| 错误类型 | 处理 |
|---------|------|
| 鉴权失败 | 401，记审计，不落库 |
| payload 格式错误 | 落 `raw_event` 标记 `parse_failed`，告警接入方，不阻塞 |
| 适配器归一化失败 | Asynq 重试 N 次；仍失败入死信，Asynqmon 可见，可手动重放 |
| 归一化成功但下游失败 | Event 已落库，下游（分诊）独立重试 |

---

## 7. 关键实体与状态

### 7.1 raw_event（原始告警暂存）

> data-model 未显式定义，本设计新增。用于"先落库再处理"的可靠性保证。

```go
type RawEvent struct {
    ID            string
    IntegrationID string
    Payload       []byte          // 原始 payload
    Headers       map[string]string
    ReceivedAt    time.Time
    Status        RawEventStatus  // received | normalized | parse_failed | requeued
    Error         string          // 失败原因
    EventID       string          // 归一化产出的 Event ID（成功后填）
}
```

### 7.2 Integration 状态机

```
enabled ──disable──► disabled
   ▲                    │
   └─────enable─────────┘
```

禁用的 Integration 拒绝接收（返回 404），但历史数据保留。

---

## 8. 开放问题

| # | 问题 | 倾向 |
|---|------|------|
| Q1 | 邮件接入的解析复杂度（HTML/附件/编码） | MVP 只支持纯文本主题+正文，复杂场景后置 |
| Q2 | 通用 JSON 适配器的字段映射配置 UI 形态 | JSONPath 表达式 + Web 表单双支持 |
| Q3 | `raw_event` 的保留策略（多大算旧可清理） | 默认保留 30 天，可配 |
