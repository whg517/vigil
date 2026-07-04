# 能力域 7：通知

| 字段 | 内容 |
|------|------|
| **覆盖 PRD** | 能力域 7（通知）M7.1~M7.9 |
| **文档版本** | v0.1 |
| **创建日期** | 2026-06-20 |
| **关联** | [`data-model.md`](../data-model.md) §3.2 NotificationRule；[`architecture.md`](../architecture.md) §3.5 通知引擎 |

---

## 1. 目标

把 Incident/升级事件**可靠送达**到响应者。核心要求：**送得到、不轰炸、能 ack、可降级兜底**。

通知是 oncall 的"最后一公里"——前面分诊/路由/升级都对了，通知送不到就全盘皆输。

---

## 2. 通知触发源

通知不是独立发起的，而是由以下场景触发：

| 触发源 | 说明 |
|--------|------|
| **升级引擎** | 升级到某 level 时，通知该 level 的 targets（主要来源） |
| **Incident 创建** | 首次触发时首轮通知 |
| **手动触发** | 响应者主动 renotify / 拉新人 |
| **AI/自动化** | 严重度调整、合并建议等需要通知干系人 |

---

## 3. 通知流程（呼应 architecture §3.5）

```
通知触发（含 targets + 事件上下文）
   │
   ▼
解析 targets → 实际通知对象（人）
   │
   ▼
按 NotificationRule 选 channels + template
   │
   ▼
聚合判断（短时间内对同一人的多条通知是否合并）
   │
   ▼
并发分发到各 Notifier（IM/电话/SMS/邮件/Webhook）
   │
   ├── 成功 → 记送达
   ├── 失败 → Asynq 退避重试
   └── 最终失败 → 标记失败 + 升级到下一通道/告警
```

---

## 4. 通知通道（M7.1~M7.4）

所有通道实现统一接口，可插拔：

```go
type Notifier interface {
    Channel() string                       // "im" | "phone" | "sms" | "email" | "webhook"
    Send(ctx context.Context, msg *Message) (*SendResult, error)
}

type Message struct {
    Target       *NotifyTarget             // 通知对象
    Incident     *Incident                 // 事件上下文
    Template     *NotificationTemplate     // 渲染模板
    ActionURL    string                    // ack/查看链接
}
```

| 通道 | 实现 | 特点 |
|------|------|------|
| **IM 机器人**（M7.1） | 钉钉/飞书/企微机器人 + 私聊 | 主通道，支持交互卡片（ack 按钮）；详见能力域 8 |
| **电话/SMS**（M7.2） | 对接云厂商语音 API（阿里云/腾讯云） | 强打扰，仅用于升级兜底；不自己建通信设施 |
| **邮件**（M7.3） | SMTP | 低打扰，用于 subscriber 干系人订阅 |
| **Webhook**（M7.4） | 转发到自定义端点 | 供用户对接内部系统/其他告警平台 |

### 通道优先级与降级

- **IM 优先**：MVP 主战场是 IM（IM-first）。
- **电话/SMS 是升级兜底**：IM 未 ack 升级时才启用，避免无谓强打扰。
- **多通道兜底链**：IM 失败 → 电话 → SMS → 全员，按 NotificationRule 配置。

> **实现现状（T2.2）**：分发是**逐通道兜底降级链**而非并联（B7/C12/B22）：
> - `msg.Channels` 是一条**有序降级链**（主通道在前）。通道来源优先级：
>   ① 本层 `EscalationLevel.notify_channels`（T2.1）> ② 命中的 `NotificationRule.channels`（B7）> ③ 全局默认链 `[webhook?]+im+email+phone+sms`。
> - 对每个 target 按链顺序逐通道尝试：**首个成功即停止**（送达），失败才降级到下一通道——
>   而非每通道各发一份（避免重复打扰、保留「IM 优先、电话/短信仅兜底」的层次）。
> - **规则评估**（`RuleResolver`，B7/C12）：按 incident 的 `condition`（severity/team/service）
>   匹配规则，「条件更具体者优先」；无命中回落默认链（向后兼容）。
> - **全链失败**（B22）：某 target 整条链全失败 → 记 `failed` + 兜底告警 org_admin（`allFailedHook`），
>   使「有事件通知发不出去」不再静默无人知。
> - **电话/短信**（B8）：已纳入默认链；号码来自 `User.phone`（`PATCH /users/:id` 可写）。

---

## 5. 通知规则（NotificationRule，M7.5/M7.8）

```yaml
notification_rule:
  name: "支付 critical 通知"
  condition:                  # 触发条件
    severity: critical
    team_id: "..."
  channels: [im, phone]       # 启用通道
  template_id: "..."          # 通知模板
  quiet_hours:                # 静默时段（M7.8）
    enabled: true
    start: "22:00"
    end: "07:00"
    timezone: "Asia/Shanghai"
    bypass_for: [critical]    # critical 穿透静默
```

- **条件匹配**：按 severity/team/service 选择适用规则。
- **静默时段**：非 critical 在夜间 quiet_hours 不打扰（值班人除外，值班人始终通知）。

---

## 6. 通知模板（M7.5）

```yaml
notification_template:
  id: "default_im_card"
  channel: im
  format: interactive_card     # text | interactive_card
  fields:                      # 模板变量
    title: "[{{.Severity}}] {{.Incident.Summary}}"
    body: "服务: {{.Service.Name}}\n时间: {{.Incident.CreatedAt}}"
    actions:                   # 卡片按钮（IM）
      - { type: ack,     label: "确认", url: "{{.ActionURL}}/ack" }
      - { type: escalate,label: "升级", url: "{{.ActionURL}}/escalate" }
      - { type: resolve, label: "解决", url: "{{.ActionURL}}/resolve" }
```

- 模板按 severity/team 区分。
- IM 用交互卡片（带按钮），其他通道用纯文本。
- 模板渲染用 Go template，支持事件上下文变量。

> **实现现状**：`internal/notification/template.go` 的 `TemplateEngine` 用 Go text/template 渲染，
> 内置 3 个默认模板（`default_im_card`/`default_email`/`default_webhook`，启动 seed 幂等 upsert），
> 用户模板按 name 覆盖；渲染失败降级 `FormatTitle/FormatSummary` 兜底，不丢通知。
> API：`GET/POST/PATCH/DELETE /notification-templates` + `POST /:id/preview`（传 incident_id 所见即所得）。

---

## 7. 送达确认与重试（M7.6/M7.7）

### 7.1 送达状态

```go
// 已落地为 ent 实体（ent/schema/notification.go，T2.2 / B22 / M13）。
type Notification struct {
    ID         int
    IncidentID int    // 关联事件，0=未路由兜底（无单）
    UserID     int    // 关联用户，0=无（群/webhook）
    Channel    string // im | phone | sms | email | webhook
    Target     string // 送达目标标识：user id / email / phone / url
    Status     string // pending | sent | failed | suppressed
    Reason     string // 状态原因：失败错误 / 静默原因（quiet_hours）/ 兜底说明
    Level      int    // 触发时升级层级
    Severity   string // 严重度快照
    CreatedAt  time.Time
}
```

> **送达三态 + suppressed**（B22）：每次发送/静默/失败落一条（只追加，不修改）。
> - `sent`/`failed`：通道返回成功/失败；
> - `suppressed`：命中 quiet_hours 被静默（**不再直接丢弃无痕**，可查、可补发）；
> - `pending`：预留（Asynq 在途/重试中）。
>
> **查询端点**：`GET /incidents/:id/notifications`（权限点 `incident.view`，分页），
> 使「通知发给了谁、走哪个通道、送达/失败/被静默」可查，为夜间打扰/送达率指标提供数据源。

### 7.2 重试

- 失败由 **Asynq 承载**（指数退避，MaxRetry，死信）。
- 任务幂等键 = `notification_id`，重试不重复发送。
- 最终失败（重试耗尽）→ 标记 `failed` + 触发降级（升级到下一通道）+ 告警。

### 7.3 ack 闭环（M7.7）

- **IM 卡片按钮** / **Web 回调** / **电话按键** → 回写 Incident 状态（ack）。
- ack 后立即取消该 Incident 的所有后续通知任务（Asynq DeleteTask）。
- ack 与能力域 6 升级引擎联动（见 03 文档 §3.4）。

---

## 8. 通知聚合（M7.9）

**目标**：短时间内对同一人的多条通知合并，避免轰炸。

- **聚合窗口**：默认 30 秒。窗口内对同一 target 的多条通知合并成一条（列出多个事件）。
- **critical 例外**：critical 不聚合，立即单独通知。
- 实现：Redis 维护 `pending_notify:{target_id}` 队列，窗口结束时合并发送。

---

## 9. 可靠性

| 要求 | 实现 |
|------|------|
| **送得到** | 多通道兜底链；最终失败升级全员 + 告警 |
| **不轰炸** | 聚合窗口 + 静默时段 + repeat 上限 |
| **可 ack** | 多通道都支持 ack 闭环（IM 按钮/电话按键/Web） |
| **可观测** | 各通道送达率、延迟、失败原因暴露 metrics |
| **幂等** | notification_id 去重 |

---

## 10. 开放问题

| # | 问题 | 倾向 |
|---|------|------|
| Q1 | 电话通道的 IVR（语音菜单）设计 | 简单："按 1 ack，按 2 转人工"，复杂场景后置 |
| Q2 | 通知模板的可视化编辑器 | 文本编辑为主，预览为辅 |
| Q3 | 跨时区 quiet_hours（团队跨时区） | 按 target 用户时区计算，非全局 |

---

## 11. 实现映射（v0.1）

| 文档章节 | 代码位置 |
|---------|---------|
| §5 静默时段（M7.8） | `internal/notification/quiet_hours.go`（`QuietHours.ShouldSuppress`：critical/值班人穿透、跨午夜窗） |
| §5 静默配置接入 | `internal/notification/notifier.go`（`SetQuietHoursResolver` + `NotifyEscalation` 内评估）+ `main.go`（按 NotificationRule.quiet_hours 解析） |
| §4 降级链 + 规则评估（B7/C12） | `internal/notification/notifier.go`（`deliverChain` 逐通道兜底、`resolveChannels` 通道优先级）+ `rule.go`（`RuleResolver.Resolve`：condition 匹配、更具体者优先） |
| §7.1 送达三态落库（B22/M13） | `ent/schema/notification.go`（Notification 实体）+ `internal/notification/recorder.go`（`DeliveryRecorder.Record` + `QueryByIncident`）+ `internal/incident/handler.go`（`GET /incidents/:id/notifications`） |
| §4 全链失败兜底告警（B22） | `internal/notification/notifier.go`（`allFailedHook`）+ `internal/server/wire.go`（`buildAllFailedHook`：解算 org_admin 走非 IM 通道告警） |
| §4 电话/短信入链 + 号码可写（B8） | `internal/notification/channels_phone.go` + `internal/server/wire.go`（默认链含 phone/sms）+ `internal/auth/handler_user_team.go`（`updateUserReq.Phone` → `User.phone`） |
| §7.2 送达记录 + 重试 | `internal/notification/notifier.go`（`sendOne` + `recordResult` 回调 + `recordDelivery` 落库） |
| §8 通知聚合（M7.9） | `internal/notification/aggregator.go`（`Aggregator.Add/Flush`：Redis per-target 队列、30s 窗、critical 旁路）+ `Notifier.FlushAggregated` |
| §4 通道可插拔 | `internal/notification/channel.go`（`Channel` 接口）+ `channels_builtin.go`（webhook/email）+ `im/notification_channel.go`（IM） |
| §6 通知模板（M7.5） | `internal/notification/template.go`（`TemplateEngine.Render`：Go template、内置默认模板 seed、自定义覆盖、渲染失败降级兜底）+ `notifier.go`（`SetTemplateEngine` 注入）+ `handler.go`（template CRUD + `POST /:id/preview`） |
| 配置 API（规则 + 抑制 CRUD） | `internal/notification/handler.go`（NotificationRule / SuppressionRule CRUD + `POST /:id/test` dry-run） |

