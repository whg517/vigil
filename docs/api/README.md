# Vigil OpenAPI

Vigil 的 REST API 通过 OpenAPI 3.1 描述，覆盖全部 `/api/v1` 端点。

## 访问方式

服务启动后：

- **Swagger UI**（交互式文档）：`http://<host>:8080/docs`
- **原始 spec**（供前端/外部工具消费）：`http://<host>:8080/openapi.yaml`

## 代码优先（code-first）

spec 由 [swaggo/swag v2](https://github.com/swaggo/swag) `--v3.1` 从 handler 注解重新生成，权威源是各 `internal/<domain>/handler.go` 顶部的注解块，而非手写 YAML。生成产物落盘在 [`internal/server/gen/swagger.yaml`](../../internal/server/gen/swagger.yaml)（及 `.json`/`docs.go`），编译时 embed 进二进制（见 `internal/server/openapi.go`）。

重新生成：

```bash
go generate ./cmd/vigil/...
```

前端类型由同一份 spec 派生（`web/src/lib/api/types.gen.ts`）：

```bash
cd web && pnpm gen:types
```

CI 会同时校验 spec 与前端类型是否已重新生成（见 `.github/workflows/ci.yml`）——改动注解后忘记 `go generate`/`pnpm gen:types` 会导致 CI 失败，与 ent 的 `go:generate` 同款纪律。

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

- **业务 API（推荐/唯一声明方案）**：HTTP Bearer JWT，登录态由 `POST /auth/login` 换取，浏览器侧经 `Authorization: Bearer <token>` 注入。这是 OpenAPI `securitySchemes` 中声明的唯一方案（`bearerAuth`）。
- **本地/回退身份**：鉴权中间件另接受 `X-Vigil-User-ID` 头作为身份来源（可伪造，仅限受信网络，**生产禁用**）；该回退方案不在 `securitySchemes` 中声明，仅作内部/开发便利。
- **公开入口**：`/webhook/:token`（接入 token）、`/im/:platform/callback`（IM 平台签名）用各自机制鉴权，不走 RBAC。
- 各端点所需权限点见 [`internal/auth/permission.go`](../../internal/auth/permission.go)（`<resource>.<action>`）。

## 维护

新增/修改端点时：

1. 在对应 handler 函数上方编辑 swag 注解（`@Summary`/`@Tags`/`@Param`/`@Success`/`@Router` 等）；
2. 运行 `go generate ./cmd/vigil/...` 重新生成 `internal/server/gen/`；
3. 运行 `cd web && pnpm gen:types` 重新生成前端类型；
4. 提交生成产物。CI 会校验两者无漂移。
