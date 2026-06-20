# 能力域 8：IM 协同（ChatOps）★

| 字段 | 内容 |
|------|------|
| **覆盖 PRD** | 能力域 8（IM 协同）M8.1~M8.9 |
| **文档版本** | v0.1 |
| **创建日期** | 2026-06-20 |
| **关联** | [`data-model.md`](../data-model.md) §3.3 Incident.war_room、User.im_accounts、§5.6 RBAC IM 延伸；[`architecture.md`](../architecture.md) §3.6 IM 协同层 |

---

## 1. 目标

让告警响应的**全流程在 IM 内完成**——ack、升级、拉人、作战室、状态同步，响应者不切系统。这是 Vigil **最核心的差异化**：一线工程师的"现场"就是 IM 群。

核心理念：**IM 不是通知通道，而是协同工作面**。

---

## 2. 支持的 IM 平台

| 平台 | 优先级 | 说明 |
|------|:------:|------|
| **钉钉** | P0 | 国内企业用户量大 |
| **飞书** | P0 | 技术团队渗透率高 |
| **企业微信** | P1 | 补齐主流三家 |
| Slack/Teams | P2 | 预留（国际/外企场景） |

> ⚠️ **平台能力 PoC 是前置依赖**：各 IM 的卡片更新、建群、@人、命令机器人 API 能力差异大，须先验证可行边界（见 PRD 风险表）。本设计假设这些能力可用。

---

## 3. 核心交互：交互卡片（M8.1）

告警通知以**交互卡片**形式发到 IM 群/私聊，带操作按钮：

```
┌─────────────────────────────────────────┐
│ 🔴 [critical] 支付服务 5xx 错误率 > 5%    │
│                                          │
│ 服务: payment-api   环境: prod           │
│ 时间: 2026-06-20 02:14                  │
│ 值班: 张三                              │
│                                          │
│ Runbook: [查看处置步骤]                  │
│                                          │
│ [✓ 确认]  [⬆ 升级]  [✓ 解决]  [📋 详情]  │
└─────────────────────────────────────────┘
```

### 3.1 卡片按钮（按权限渲染，M8.7）

| 按钮 | 动作 | 所需权限 |
|------|------|---------|
| 确认（ack） | 标记接手 | `incident.ack` |
| 升级（escalate） | 跳到下一 level | `incident.escalate` |
| 解决（resolve） | 标记已解决 | `incident.resolve` |
| 详情 | 打开 Web 详情页 | `incident.view` |

**无权按钮不显示**——卡片按当前用户的权限渲染。这是 IM 不成为权限后门的关键。

### 3.2 卡片实时更新（M8.4）

Incident 状态变化时，通过 IM 平台的**卡片更新 API** 实时刷新已发出的卡片：
- 张三 ack 后，卡片标题变为"✓ 已确认 by 张三"，按钮区折叠。
- 群里所有人看到的卡片同步更新，避免"过时卡片"。

> 依赖 IM 平台的卡片更新能力（部分平台只能发新消息不能改旧卡片，需 PoC 确认降级方案：发新消息标注最新状态）。

---

## 4. 作战室（War Room，M8.2）

Incident 触发时自动建临时 IM 群作为协同作战空间：

```
群名: [Vigil] INC-0042 支付5xx
成员: 自动拉入值班人 + service 归属团队 + (可选)订阅者
置顶: 事件卡片（含状态/时间线入口）
```

### 4.1 作战室生命周期

| 阶段 | 动作 |
|------|------|
| **创建** | Incident 首次触发 / 手动 `/vigil warroom` |
| **拉人** | 升级到新 level 时自动拉入新值班人；@人即加入（M8.3） |
| **运行** | 群内消息可选回写时间线（M8.9） |
| **归档** | Incident resolved 后保留聊天记录，关联到复盘（M8.9） |

### 4.2 data-model 承载

```go
Incident.war_room = {
    im_platform: "feishu",
    im_channel_id: "...",
    created_at: ...
}
```

---

## 5. 拉人协同（M8.3）

在卡片/作战室里 @某人 = 把他加入响应：

```
@李四 帮我看看 DB
   │
   ▼
IM 层收到 @ 事件
   │
   ▼
映射 李四im_id → User
   │
   ▼
调用 add_responder（需当前用户有 incident.add_responder 权限）
   │
   ▼
授予李四该 Incident 范围的临时 responder 权限（事件关闭失效）
   │
   ▼
李四可对该 Incident ack/操作（权限感知卡片启用）
```

- 拉人即授权（data-model §5.6 跨团队拉人）：被拉的人获得事件级临时权限。
- 跨团队拉人也走此路径（软隔离下的协作方式）。

---

## 6. 斜杠命令（M8.5）

机器人接收 `/vigil <command>`：

| 命令 | 动作 | 权限 |
|------|------|------|
| `/vigil ack <id>` | 确认事件 | `incident.ack` |
| `/vigil escalate <id>` | 升级 | `incident.escalate` |
| `/vigil resolve <id>` | 解决 | `incident.resolve` |
| `/vigil add @人 <id>` | 拉人 | `incident.add_responder` |
| `/vigil runbook <name> <id>` | 触发 runbook | `runbook.execute` |
| `/vigil status <id>` | 查看状态 | `incident.view` |
| `/vigil oncall` | 查看当前在班人 | `schedule.view` |

---

## 7. IM 账号映射与鉴权（M8.6，关键）

**核心原则**：IM 操作走与 Web **完全相同**的鉴权链路，IM 不成为权限后门。

```
IM 用户点击 [确认] 按钮
   │
   ▼
IM Webhook 回调（含 im_platform + im_unionid + action）
   │
   ▼
查 User.im_accounts[platform].account_id == im_unionid → User
   │
   ▼
解析 action → 权限点（如 incident.ack）
   │
   ▼
查 User 在 incident.team_id 作用域的 RoleBinding → 合并权限点
   │
   ▼
判定权限点 ∈ 权限集？
   ├── 否 → IM 回复"无权限"，记审计
   └── 是 → 核心服务执行 ack → 更新卡片 → 时间线记录
```

- `User.im_accounts` 是映射的桥梁（data-model §3.1），一个 User 可绑多 IM 平台。
- 未绑定 IM 账号的用户，IM 操作被拒（提示去 Web 绑定）。

---

## 8. 状态双向同步（M8.4）

```
Web 操作 ──► 核心服务 ──► 更新 IM 卡片
                              ▲
IM 操作 ──► 核心服务 ──► 更新 Web（WebSocket 推送）
```

- **IM→Web**：IM 内 ack 后，Web 端通过 WebSocket 实时刷新事件状态。
- **Web→IM**：Web 端操作（如管理员手动 resolve）后，更新对应 IM 卡片。
- 同一 Incident 的状态在任何端都一致。

---

## 9. IM 平台适配器（可插拔）

```go
type IMBot interface {
    Platform() string                              // "dingtalk" | "feishu" | "wecom"
    SendCard(ctx, channel string, card *Card) error
    UpdateCard(ctx, cardID string, card *Card) error   // 卡片更新（平台能力依赖）
    CreateWarRoom(ctx, name string, members []string) (roomID string, err error)
    ParseCallback(payload []byte) (*IMEvent, error)    // 解析按钮/命令/@人回调
}
```

各平台实现差异封装在适配器内，业务层不感知。这是支持多 IM + 后续扩展的关键。

---

## 10. 平台能力降级矩阵

不同 IM 平台能力不齐，需降级方案（PoC 后填实）：

| 能力 | 钉钉 | 飞书 | 企微 | 降级方案 |
|------|:--:|:--:|:--:|---------|
| 交互卡片 | ✅ | ✅ | ⚠️ | 降级为纯文本+链接 |
| 卡片更新 | ⚠️ | ✅ | ❌ | 降级为发新消息标注最新状态 |
| 建临时群 | ✅ | ✅ | ✅ | — |
| @人 API | ✅ | ✅ | ⚠️ | 降级为手动@ |
| 命令机器人 | ✅ | ✅ | ✅ | — |

> 这是 IM-first 最大的不确定性，PoC 结果直接决定能力域 8 的可行边界。

---

## 11. 可靠性

| 要求 | 实现 |
|------|------|
| **IM 操作不丢** | 回调处理失败可重放（记录回调原始 payload） |
| **鉴权统一** | IM 操作走标准 RBAC，不因在 IM 放行 |
| **状态一致** | 双向同步 + 卡片更新保证各端一致 |
| **平台故障** | IM 不可用时降级到电话/邮件（能力域 7 兜底链） |

---

## 12. 开放问题

| # | 问题 | 倾向 |
|---|------|------|
| Q1 | 卡片更新能力缺失平台的降级体验 | 发新消息 + 折叠旧消息，标注"最新状态见新消息" |
| Q2 | 作战室消息回写时间线的筛选（避免噪音） | 仅捕获含关键词/@机器人/带标记的消息 |
| Q3 | 多 IM 平台同时绑定的主从关系 | 用户操作以触发的平台为准，不区分主从 |
