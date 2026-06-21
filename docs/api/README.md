# Vigil OpenAPI

Vigil 的 REST API 通过 OpenAPI 3.0 描述，覆盖全部 `/api/v1` 端点。

## 访问方式

服务启动后：

- **Swagger UI**（交互式文档）：`http://<host>:8080/docs`
- **原始 spec**（供前端/外部工具消费）：`http://<host>:8080/openapi.yaml`

spec 文件权威源：[`docs/openapi.yaml`](../openapi.yaml)，编译时同时嵌入二进制（`internal/server/openapi.yaml`），保证运行时与文档同步。

## 端点总览

| 能力域 | 端点前缀 | 说明 |
|--------|---------|------|
| 事件（能力域 8/13） | `/incidents` | 列表/详情/ack/resolve/escalate |
| 时间线（10） | `/incidents/:id/timeline` | 查询 + 手动追加 |
| AI（11） | `/incidents/:id/diagnose`、`/similar`、`/ai-insights/:id/resolve` | 根因诊断 + 相似事件 + human-in-the-loop |
| 服务（4/13） | `/services` | CRUD |
| 排班（5） | `/schedules`、`/schedules/:id/oncall|preview` | CRUD + 在班查询 |
| Runbook（9） | `/runbooks`、`/runbooks/:id/execute` | CRUD + 受控执行 |
| 复盘（12） | `/postmortems`、`/postmortems/:id/transition`、`/action-items/:id` | 草稿/流转/改进项 |
| 通知（7） | `/notification-rules`、`/notification-templates` | 规则 + 模板 CRUD + preview |
| 抑制（3） | `/suppression-rules` | CRUD |
| 报表（15） | `/analytics/*` | 仪表盘/告警/事件/团队负载/复盘/趋势 |
| RBAC（13） | `/roles`、`/role-bindings` | 角色 + 绑定 |
| 接入（1/8） | `/webhook/:token`、`/im/:platform/callback` | 公开（token/签名鉴权，不走 RBAC） |

## 认证与权限

- **业务 API**：通过 `X-Vigil-User-ID` 头携带操作者身份（鉴权中间件解析）；各端点所需权限点见 [`internal/auth/permission.go`](../../internal/auth/permission.go)（`<resource>.<action>`）。
- **公开入口**：`/webhook/:token`（接入 token）、`/im/:platform/callback`（IM 平台签名）用各自机制鉴权，不走 RBAC。

## 维护

新增端点时同步更新 `docs/openapi.yaml`（权威源）并复制到 `internal/server/openapi.yaml`（运行时 embed）。
