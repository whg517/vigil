# 能力域 12：复盘

| 字段 | 内容 |
|------|------|
| **覆盖 PRD** | 能力域 12（复盘）M12.1~M12.7 |
| **文档版本** | v0.1 |
| **创建日期** | 2026-06-20 |
| **关联** | [`data-model.md`](../data-model.md) §3.3 Postmortem；能力域 10 时间线、11 AI |

---

## 1. 目标

让事件**闭环学习**——把每次故障变成组织知识，让复盘不再是 3-4 小时的苦差（社区数据：用自动化可降到 30 分钟）。

核心价值：**自动起草 + LLM 结构化 + 改进项跟踪 + 知识沉淀**。

---

## 2. 复盘生命周期

```
Incident resolved
   │
   ▼
触发复盘（按 severity 决定是否强制）
   │
   ▼
自动生成草稿（时间线 + AI）
   │
   ▼
status: draft
   │
   ▼
人工校对/补充 ──► in_review（评审）
   │
   ▼
发布 ──► published
   │
   ▼
进入知识库 ──► 反哺 AI 相似事件检索（能力域 11.4）
   │
   ▼
归档 ──► archived
```

---

## 3. 触发机制（M12.7）

| severity | 是否强制复盘 |
|----------|:----------:|
| critical | ✅ 强制（resolved 后自动创建 draft） |
| warning | 可配（默认建议但不强制） |
| info | 不强制 |

强制复盘的 Incident 在 resolved 后不直接 closed，停在"待复盘"状态。

---

## 4. 自动生成草稿（M12.1/M12.2/M12.3）

### 4.1 草稿来源

```yaml
postmortem_draft 来源:
  - 时间线（能力域 10）         # 事实依据，自动填充 timeline 章节
  - AI 起草（能力域 11.7）       # postmortem_draft，LLM 填充其他章节
  - Incident 元数据              # summary/impact/参与人
```

### 4.2 结构化模板（data-model §3.3）

```yaml
sections:
  summary:               # 摘要（AI 起草，人校对）
  impact:                # 影响：时长/用户数/损失（AI 从指标估算，人校对）
  timeline:              # 引用时间线（自动）
  root_cause:            # 根因（AI 给 hint，人确认）
  contributing_factors:  # 促成因素（人填写为主）
  what_went_well: []     # 做得好的（人填写）
  what_went_wrong: []    # 做得差的（人填写）
  action_items:          # 改进项
    - { description, owner_id, due_date, status, tracker_url }
```

### 4.3 LLM 辅助填充（M12.3）

- AI（能力域 11.7 `postmortem_draft`）填充 summary/impact/root_cause 草稿。
- 每个字段标记"AI 起草"来源，人可一键 accept/edit/reject。
- evidence 引用时间线条目，保证可溯源。

---

## 5. 改进项跟踪（M12.4）

```go
type ActionItem struct {
    ID          string
    Description string
    OwnerID     string
    DueDate     time.Time
    Status      string    // open | in_progress | done
    TrackerURL  string    // 对接外部工单（Jira/禅道）
}
```

- 每个 action_item 有 owner + due + status。
- 可对接外部工单系统（能力域 14）：复盘发布时自动在 Jira 建改进任务。
- 逾期未完成的 action_item 在报表（能力域 15）中高亮。

---

## 6. 知识沉淀（M12.6）

- published 的复盘进入**可检索知识库**。
- 反哺能力域 11.4 相似事件检索：新 Incident 触发时，检索历史相似复盘。
- 形成"故障 → 复盘 → 知识 → 下次更快解决"的闭环。

---

## 7. 与其他能力域联动

| 联动 | 说明 |
|------|------|
| **能力域 10 时间线** | 复盘的事实依据 |
| **能力域 11 AI** | 起草复盘内容（M11.7） |
| **能力域 8 IM** | 作战室聊天记录关联到复盘（M8.9） |
| **能力域 14 集成** | action_item 对接外部工单 |
| **能力域 15 报表** | 复盘完成率、action_item 闭环率度量 |

---

## 8. 开放问题

| # | 问题 | 倾向 |
|---|------|------|
| Q1 | 复盘的可见性（全公司公开 vs 团队内） | 默认团队内，critical 可设全公司（无指责文化） |
| Q2 | action_item 的逾期提醒机制 | 逾期通知 owner + team_admin |
| Q3 | 复盘模板的自定义 | 支持团队自定义模板章节 |
