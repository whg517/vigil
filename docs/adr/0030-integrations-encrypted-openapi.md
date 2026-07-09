# ADR-0030: 集成凭据加密存储 + 四方向集成 + code-first OpenAPI 契约

| 字段 | 内容 |
|------|------|
| **状态** | Accepted |
| **日期** | 2026-07-09 |
| **相关** | [`0009-pluggable-integrations.md`](./0009-pluggable-integrations.md)、`internal/*/handler.go`、`api/README.md` |

## 背景

Vigil 要融入企业既有工具链:既消费外部告警,也向外产出事件流。这意味着大量集成会持有第三方凭据(token、密钥),凭据泄漏是安全红线。同时,系统对外暴露完整 REST API,手写 OpenAPI spec 与后端实现极易漂移,前端类型再从漂移的 spec 派生会二次放大偏差。需要一套"凭据不明文、契约不漂移"的集成与 API 方案。

## 决策

- **凭据加密存储**:集成凭据(token/密钥)经 crypto 模块加密后落 `Credential` 实体,**不明文入库**;外部 KMS 作为后置增强,暂不强依赖。
- **四方向集成**:
  - 入向(告警源接入);
  - 双向 IM(收发消息/卡片/建群);
  - 出向(工单 Jira/禅道、云语音、Webhook 出口——可订阅 Incident 生命周期事件推送外部,带 token + 事件类型选择 + 轮换);
  - 开放 API。
- **API 契约**:REST 覆盖所有实体 + WebSocket 实时推送,统一挂在 `/api/v1/`,以 OpenAPI 3.1 为契约,API Key(scoped)鉴权;GraphQL 后置。
- **code-first 契约**:spec 由 `swaggo/swag` v2 加 `--v3.1` 从 handler 注解生成,权威源是 `internal/<domain>/handler.go` 的注解块,spec 编译期 embed 进二进制;前端类型由**同一 spec** 派生到 `web/src/lib/api/types.gen.ts`。改注解后流程:`go generate ./cmd/vigil/...` → `cd web && pnpm gen:types` → 提交生成产物;CI 校验 spec 与前端类型无漂移。Swagger UI 挂 `/docs`,原始 spec 挂 `/openapi.yaml`。

## 理由

- 凭据加密是安全底线,明文入库一旦库泄漏即全面失守。
- 四方向集成让 Vigil 既接得住告警,又能把事件流推回企业工具链,成为处置协同的中枢而非孤岛。
- code-first 以 handler 注解为单一权威源,spec 与前端类型都从它派生,天然抗漂移;CI 校验把"漂移"变成可拦截的门禁。

## 备选方案

- **明文存凭据**:实现最省事,但一旦库泄漏凭据全失守,安全红线,直接否决。
- **手写 OpenAPI spec**:与实现割裂,极易漂移,维护成本高。
- **前后端各自定义类型**:类型双份维护,接口变更时两端不同步,联调成本高。

## 影响 / 权衡

- 生成产物(spec、`types.gen.ts`)必须随注解改动一起提交,否则 CI 漂移校验会失败;开发者需记住"改注解→重生成→提交"三步。
- 凭据加密引入 crypto 模块与密钥管理成本;密钥本身的保管(env/KMS)是另一层责任。
- OpenAPI 3.1 依赖 swag v2 的 `--v3.1` 能力,工具链版本需锁定,升级需回归 spec 生成。
