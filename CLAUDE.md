# CLAUDE.md

> 本文件是 Claude（及兼容 `@import` 语法的 AI 代理）在本仓库工作的入口。
>
> 项目协作指南统一维护在 [`AGENTS.md`](./AGENTS.md)，此处通过引用加载，不在本文件重复。

@AGENTS.md

---

## Claude 专属补充

以下为 Claude Code 特有的行为约束，是对 [`AGENTS.md`](./AGENTS.md) 的补充，不与其冲突。

### 工作前必做

1. **先读 [`AGENTS.md`](./AGENTS.md)**（已通过 `@AGENTS.md` 加载）—— 掌握项目约定、命令、结构。
2. **理解当前分支上下文**：确认在哪个特性分支、对应哪个能力域,先读 [`docs/architecture.md`](./docs/architecture.md) 定位。
3. **改动前对照设计**：任何实体/接口改动先核对 `ent/schema/` 与相关 [ADR](./docs/adr/)，避免偏离设计。

### 代码风格

- **Go**：遵循现有文件风格；ent schema 改动后必须 `go generate ./ent/...`。
- **前端**：用 `@/` 路径别名；优先复用 `web/src/components/ui/` 与 Tailwind 工具类；新增组件参考 shadcn 风格。
- **注释**：用中文（与现有代码一致），解释"为什么"而非"做了什么"。

### 提交

- 严格遵循 [`AGENTS.md`](./AGENTS.md) 「提交信息规范」与 [ADR-0035](./docs/adr/0035-dev-workflow-gates.md)。
- **绝不使用 `chore`**；拿不准 type 时拆分提交或询问。
- 在 worktree 内提交，不在主仓库目录直接提交业务改动。

### 边界

- **不直接在生产写操作逻辑上冒险**：Runbook 处置类（写）必须 `require_approval`，详见 [ADR-0021](./docs/adr/0021-runbook-two-tier.md)。
- **不绕过 RBAC**：IM 操作复用 Web 同一鉴权链路，绝不因"在 IM 里"放行，详见 [ADR-0018](./docs/adr/0018-im-same-rbac-as-web.md) 与 [ADR-0027](./docs/adr/0027-rbac-permissions-roles.md)。
- **文档先行**：设计性改动先落文档再写代码（本项目 docs-driven）。关键取舍写 [ADR](./docs/adr/)，稳定后增量更新 [`docs/architecture.md`](./docs/architecture.md)。

### 验证

与 [`AGENTS.md`](./AGENTS.md) 「开发约定」对齐：worktree 内提交前跑三道门禁，main 复验跑相同三道门禁。

```bash
# worktree 内提交前（三道门禁）
golangci-lint run ./... && go test ./... && go build ./...
pnpm --dir web lint && pnpm --dir web build
```

涉及 schema 变更时额外确认（两步）：

```bash
go generate ./ent/...                                    # 同步 ent 生成代码
atlas migrate diff <name> --env local                    # 生成新版本迁移 SQL（见 atlas.hcl）
```

涉及 handler 注解变更时额外确认：

```bash
go generate ./cmd/vigil/...                              # 重生成 OpenAPI swagger spec
pnpm --dir web gen:types                                 # 同步前端类型
```
