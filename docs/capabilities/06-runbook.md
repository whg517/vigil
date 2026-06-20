# 能力域 9：Runbook 处置

| 字段 | 内容 |
|------|------|
| **覆盖 PRD** | 能力域 9（处置执行）M9.1~M9.8 |
| **文档版本** | v0.1 |
| **创建日期** | 2026-06-20 |
| **关联** | [`data-model.md`](../data-model.md) §3.2 Runbook；[`architecture.md`](../architecture.md) §集成层 Executor |

---

## 1. 目标

告警响应时告诉响应者"**该干什么**"，并能在受控条件下**自动执行**处置动作。

核心设计（呼应设计基线第 8 条）：**Runbook 分两档**——
- 诊断类（只读）→ Vigil 内置执行，门槛低。
- 处置类（写操作）→ Vigil 不直接执行，生成指令、由人确认或对接外部平台。

避免 Vigil 变成"能搞垮生产的定时炸弹"。

---

## 2. Runbook 两档类型

### 2.1 文档式（document，M9.1）

```yaml
runbook:
  type: document
  content_markdown: |
    ## 支付 5xx 处置
    1. 先看 Grafana 大板 xxx，确认错误率
    2. 查 payment-api 日志：`kubectl logs ...`
    3. 若是 DB 连接满，扩容连接池
    4. 若是新版本上线，考虑回滚
```

- 纯 Markdown，给人看，无执行。
- 通知/作战室自动展示关联 runbook 链接（M9.3）。

### 2.2 可执行式（executable，M9.4~M9.7）

```yaml
runbook:
  type: executable
  steps:
    - id: s1
      name: "查错误日志"
      action:
        type: diagnose              # 诊断（只读）
        target:
          kind: http
          endpoint: "http://logs-api/query"
        readonly: true
      on_failure: continue

    - id: s2
      name: "回滚上一版本"
      action:
        type: execute               # 处置（写）
        target:
          kind: jenkins
          endpoint: "http://jenkins/job/rollback"
        readonly: false
        require_approval: true      # ★ 写操作必须人确认
      on_failure: escalate          # 处置失败则升级
```

---

## 3. 触发机制（M9.2）

```yaml
trigger:
  type: on_severity                 # manual | on_incident | on_severity | on_label_match
  condition: "severity >= warning"
```

| 触发类型 | 何时 |
|---------|------|
| `manual` | 响应者手动点"执行 runbook" |
| `on_incident` | Incident 创建即展示（但不自动执行） |
| `on_severity` | 达到某严重度展示 |
| `on_label_match` | 匹配 label（如 service=payment） |

> 默认行为：**展示 runbook 给人参考，不自动执行可执行步骤**（除非显式配置 auto-run 且步骤全为 readonly）。

---

## 4. 执行器（Executor，可插拔，M9.6）

```go
type Executor interface {
    Kind() string                              // "http" | "ansible" | "jenkins" | "internal"
    Run(ctx, step *RunbookStep) (*StepResult, error)
}
```

| 执行器 | 用途 | 风险 |
|--------|------|------|
| **HTTP** | 调任意 HTTP 端点（查日志 API、触发 webhook） | 诊断为主 |
| **内置诊断** | Vigil 内置的只读查询（查指标/拓扑/历史） | 只读，安全 |
| **Ansible** | 对接用户 Ansible playbook | 写操作，需确认 |
| **Jenkins** | 对接 Jenkins job（回滚/部署） | 写操作，需确认 |
| **内部平台** | 对接用户自研运维平台 | 写操作，需确认 |

---

## 5. 执行流程与安全控制

```
响应者点"执行 runbook"（或自动触发）
   │
   ▼
逐 step 执行：
   │
   ├── step.readonly == true（诊断）
   │     └─► Executor 直接执行（内置安全）
   │
   └── step.readonly == false（处置）
         │
         ├── require_approval == true（默认）
         │     └─► ★ 必须人确认（IM/Web 弹确认）
         │           │
         │           ├── 确认 → Executor 执行
         │           └── 拒绝 → 跳过/中止
         │
         └── require_approval == false（仅高度可信场景）
               └─► Executor 执行（需 admin 显式配置）
   │
   ▼
执行结果：
   ├── 成功 → 记 TimelineItem（type=runbook_executed）
   └── 失败 → 按 on_failure 处理（continue/abort/escalate）
```

### 安全约束

- **写操作默认 require_approval**：处置类步骤必须人确认，human-in-the-loop。
- **执行审计**：所有执行落 IncidentAction（who/when/what/result）。
- **超时**：每步有超时，避免卡死。
- **幂等**：执行器应支持幂等（重复执行结果一致），由调用方保证。

---

## 6. 与其他能力域联动

| 联动 | 说明 |
|------|------|
| **能力域 7 通知** | 通知/作战室自动展示关联 runbook（M9.3） |
| **能力域 8 IM** | IM 卡片可触发 runbook；写操作确认在 IM 完成 |
| **能力域 10 时间线** | 执行结果回写时间线（M9.7） |
| **能力域 11 AI** | AI 可建议"应执行哪个 runbook"（root_cause_hint 延伸） |
| **能力域 6 升级** | `on_failure: escalate` 处置失败自动升级 |

---

## 7. 开放问题

| # | 问题 | 倾向 |
|---|------|------|
| Q1 | 执行器的凭证管理（Ansible/Jenkins token） | 加密存储于 Vigil，admin 管理 |
| Q2 | 执行结果的结构化展示（非纯文本） | 支持结构化 result，UI 渲染 |
| Q3 | 并行步骤支持 | 初期串行为主，并行后置 |
