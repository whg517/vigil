# 贡献指南（CONTRIBUTING）

感谢你考虑为 Vigil 做贡献！本文面向**外部贡献者**，说明从 fork 到 PR 合入的完整流程。
项目约定的单一信源是 [`AGENTS.md`](AGENTS.md) 与 [ADR-0035 开发流程与门禁](docs/adr/0035-dev-workflow-gates.md)，本文以链接为主、不重复维护。

> ⚠️ **安全漏洞请勿提公开 issue**，请走 [`SECURITY.md`](SECURITY.md) 的私密申报渠道。

---

## 贡献流程：fork + PR

[`AGENTS.md`](AGENTS.md) 描述的 worktree + squash 直推 main 是**维护者**的本地闭环流程；外部贡献者不具备直推权限，统一走 fork + PR，两者最终收敛到同一套门禁：

1. **先开 issue 对齐**（推荐）：非琐碎改动（新功能/设计调整）先用 [issue 模板](.github/ISSUE_TEMPLATE/) 描述问题与思路，避免方向不合白做。
2. **Fork 本仓库**，从最新 `main` 拉特性分支，分支名建议与提交 type 一致：`<type>-<name>`（如 `fix-oncall-tz`、`feat-slack-adapter`）。
3. **开发并本地过门禁**（见下文「测试与验证」）。
4. **提交 PR 到 `main`**：一个 PR 只做一件事；**PR 标题必须符合 Conventional Commits**（squash 合并时常取自标题，CI 会校验）；按 [PR 模板](.github/PULL_REQUEST_TEMPLATE.md) 完成自查清单。
5. **CI 门禁全绿 + 评审通过后**，由维护者以 **squash 方式合并**——所以无需强求 PR 内每条中间 commit 完美，但标题与最终信息必须规范。

CI 跑的门禁与维护者本地一致（[ADR-0035](docs/adr/0035-dev-workflow-gates.md)）：commit-lint → 后端 lint→test→build（含 spec/schema drift 检测与覆盖率门禁）→ 前端 lint→build → govulncheck → e2e。

## 开发环境

技术栈、仓库结构、常用命令见 [`AGENTS.md`](AGENTS.md)。最短路径：

```bash
make dev-setup      # 初始化 .env + 起依赖容器（postgres + redis）+ 迁移
make dev-backend    # 后端 :8080
make dev-frontend   # 前端 :5173
make help           # 全部命令说明
```

## 提交规范

格式 `<type>(<scope>): <subject>`，允许的 type：`feat` `fix` `docs` `refactor` `perf` `test` `style` `build` `ci` `revert`。

> **禁止使用 `chore`**——没有合适 type 说明提交不够原子或意图不清，应拆分或重新表述。
> 完整规则与理由见 [`AGENTS.md` 提交信息规范](AGENTS.md#提交信息规范conventional-commits调整版) 与 [ADR-0035](docs/adr/0035-dev-workflow-gates.md)。
> 仓库内置钩子可本地预检提交信息与门禁：`make install-hooks`。

## 测试与验证

提 PR 前请在本地确认（与 CI 同款）：

```bash
golangci-lint run ./... && go test ./... && go build ./...   # 后端三道门禁
pnpm --dir web lint && pnpm --dir web build                  # 前端两道门禁
```

- 默认 `go test ./...` **不含 e2e**；改动涉及**核心流水线**（ingestion / triage / escalation / auth）时请本地跑 `make test-e2e`（需 docker）。
- 生成物必须同步入库：改 `ent/schema/` 后 `go generate ./ent/...`；改 handler 注解后 `go generate ./cmd/vigil/...`；后端 spec 变更后 `pnpm --dir web gen:types`。CI 有 drift 检测，漏了会红。
- 新逻辑请附单测；修 bug 优先先写复现测试。

## 文档先行

本项目 docs-driven：**设计性改动先动文档再写代码**。

- 关键取舍新增/修订 [ADR](docs/adr/)（一决策一文件，[索引](docs/adr/README.md)），实现稳定后增量更新 [`docs/architecture.md`](docs/architecture.md)。
- 改动前先对照既有 ADR，避免与已定设计冲突（如 [Runbook 两档](docs/adr/0021-runbook-two-tier.md)、[IM 同权](docs/adr/0018-im-same-rbac-as-web.md) 等关键原则）。
- 用户可见的变更请在 [`CHANGELOG.md`](CHANGELOG.md) 的 `Unreleased` 节补一行。

## 行为准则

保持友善、就事论事。评审意见针对代码不针对人；不确定时先提问再断言。
