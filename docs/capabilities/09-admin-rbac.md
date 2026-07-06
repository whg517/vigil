# 能力域 13：管理治理与 RBAC

| 字段 | 内容 |
|------|------|
| **覆盖 PRD** | 能力域 13（管理治理）M13.1~M13.7 |
| **文档版本** | v0.1 |
| **创建日期** | 2026-06-20 |
| **关联** | [`data-model.md`](../data-model.md) §5 RBAC、§3.1 User/Team |

---

## 1. 目标

回答"**谁能干什么**"，并提供配置治理能力：

- 用户/团队的增删管。
- **RBAC 自配置**：角色与权限由使用者自行配置管理（核心，呼应设计基线第 12 条）。
- 服务目录、审计日志、API Key 管理。

---

## 2. 用户管理（M13.1）

| 功能 | 说明 | 权限 |
|------|------|------|
| User CRUD | 增删改查用户 | `user.create/update/disable` |
| IM 账号绑定 | 绑定钉钉/飞书/企微 unionId（支持多平台） | `user.im.bind` |
| 启用/停用 | 停用用户保留历史但禁止登录 | `user.disable` |
| 时区/语言 | 用户个人偏好 | self |

- 一个 User 可绑**多个 IM 平台账号**（IM-first 前提）。
- 停用用户的排班/未完成 action_item 要有交接提示。

---

## 3. 团队管理（M13.2）

| 功能 | 说明 | 权限 |
|------|------|------|
| Team CRUD | 团队增删改 | `team.create/update/delete` |
| 团队树 | `parent_team_id` 嵌套，**仅组织展示，权限不继承** | — |
| 成员管理 | 加/移成员 | `team.member.manage` |

- 团队是**数据归属边界**（软隔离）。
- 团队树用于组织结构展示，权限不沿树向下（避免越权）。

---

## 4. RBAC 自配置（M13.3，核心）★

### 4.1 模型回顾（data-model §5）

```
User ──(RoleBinding, scope)──> Role ──> Permission
```

- **Permission**：系统内置的细粒度权限点枚举（`incident.ack` 等），系统能力边界，固定。
- **Role**：使用者自定义，自由组合权限点。
- **RoleBinding**：把 Role 授予 User，带 scope（org/team）。

### 4.2 角色 CRUD

```http
POST /api/v1/roles
{
  "name": "支付一线值班",
  "description": "...",
  "permissions": ["incident.ack","incident.view","runbook.execute","event.view"],
  "scope_level": "team"
}
```

- 创建/编辑角色需 `role.create/update` 权限。
- 内置角色（org_admin/team_admin/responder/...）可复制不可删。

### 4.3 授权（RoleBinding）

```http
POST /api/v1/role-bindings
{
  "user_id": "...",
  "role_id": "...",
  "scope": { "level": "team", "team_id": "..." },
  "expires_at": null            // 可选，临时授权
}
```

- 临时授权（`expires_at`）：值班期间临时给某人 team_admin，到期自动失效。

### 4.4 鉴权流程（data-model §5.5）

```
操作请求 (user, action, resource)
  │
  ├── 解析 action → permission_code
  ├── 解析 resource → scope（如 incident.team_id）
  ├── 查 user 在 org + team scope 的 RoleBinding
  ├── 合并权限点（org 和 team 取并集）
  └── 判定 permission_code ∈ 权限集？
```

---

## 5. 服务目录（M13.4）

| 功能 | 说明 | 权限 |
|------|------|------|
| Service CRUD | 服务增删改 | `service.create/update/delete` |
| Label 管理 | 路由匹配标签 | `service.update` |
| 绑定策略 | 绑定 escalation_policy/schedule/runbook | `service.update` |

服务是路由锚点，归属团队（软隔离）。

---

## 6. 审计日志（M13.5/M13.6）

### 6.1 审计范围

| 类型 | 记录 |
|------|------|
| **管理审计** | 角色变更、集成 token、用户停用、配置变更（需 `admin.audit.view`） |
| **操作审计** | 所有 IncidentAction（who/when/via/what） |

### 6.2 IncidentAction（data-model §3.3）

```go
type IncidentAction struct {
    ID          string
    IncidentID  string
    Type        string    // ack | escalate | resolve | runbook | ...
    Actor       Actor     // system | user | integration | ai
    Payload     map[string]any
    Via         string    // web | im | api | automation
    Result      string    // success | failed
}
```

- 所有事件操作落 Action，支持审计 + 回溯。
- `via` 字段标记来源，可统计"多少操作在 IM 完成"。

### 6.3 查询与导出端点

| 方法 | 路径 | 权限 | 说明 |
|------|------|------|------|
| GET | `/audit-logs` | `admin.audit.view` | 分页查询（`limit` 默认 50 / 上限 200，`offset`），`created_at` 倒序 |
| GET | `/audit-logs/export` | `admin.audit.view` | **CSV 导出**（附件下载，不分页，含上限保护） |

审计为 org 级（无 team scope），两端点权限一致。共用同一套筛选参数：
`actor_user_id` / `action` / `resource_type` / `resource_id` / `from` / `to`
（`from`/`to` 支持 RFC3339 或 unix 秒；解析失败宽松忽略该边界）。

**CSV 导出（M13.5）**：

- 列顺序固定：`created_at`(RFC3339) · `actor_user_id` · `actor_name` · `action` ·
  `resource_type` · `resource_id` · `resource_name` · `result` · `ip` · `user_agent` ·
  `detail`（JSON 压平成单列，逗号/引号由 CSV 标准转义）。
- 响应 `Content-Type: text/csv`，`Content-Disposition: attachment; filename=audit-logs_<时间戳>.csv`。
- **上限保护**：单次最多导出 **50000 行**（`created_at` 倒序取最近）。达上限**不静默截断**——
  记 `warn` 日志 + 置响应头 `X-Vigil-Truncated: true`，调用方应缩小 `from`/`to` 时间窗后重导。

---

## 7. API Key 管理（M13.7）

- 程序化接入的 key（开放 API 用）。
- org_admin 管理（`admin.apikey.manage`）。
- 每个 key 绑定权限范围（scoped key）。

---

## 8. 开放问题

| # | 问题 | 倾向 |
|---|------|------|
| Q1 | RBAC 权限点的版本化（新增能力时如何演进） | 权限点加版本，角色绑定兼容 |
| Q2 | 角色变更的影响范围预览 | 授权前预览将影响的用户/资源 |
| Q3 | 审计日志的保留策略 | 默认 90 天，可配 |
