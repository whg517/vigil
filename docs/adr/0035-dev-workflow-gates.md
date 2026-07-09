# ADR-0035: 开发工作流:worktree 闭环 + 提交规范 + 门禁

| 字段 | 内容 |
|------|------|
| **状态** | Accepted |
| **日期** | 2026-07-09 |
| **相关** | [`0005-data-access-ent-atlas.md`](./0005-data-access-ent-atlas.md)、[`0030-integrations-encrypted-openapi.md`](./0030-integrations-encrypted-openapi.md) |

## 背景

Vigil 是 docs-driven、多引擎、前后端一体的自托管项目,涉及 ent 生成代码、OpenAPI 契约、前端类型派生等多处"改动即须再生成"的关联产物。若开发流程松散,极易出现分支叠分支、生成物漂移、未验证即合并等问题。需要一套强制的工作流与质量门禁,把"代码写完"与"真正交付"严格区分。

## 决策

### 工作流:worktree + 特性分支闭环

- 主仓库目录**永远停在 main**,只接收 squash 合并,不直接开发。
- 所有开发在 `.worktree/<type>-<特性>/`(已 gitignore;**目录名 = 分支名 = `<type>-<特性>`**,扁平名无斜杠)。
- **串行依赖禁止叠分支**:被依赖特性 A 必须先完整闭环合并回 main,B 再从更新后的 main 拉;依赖通过 main 传递,不通过叠分支。
- 统一 **`git merge --squash`**(main 历史线性,一特性 = 一条规范 commit);squash 后删分支用 `-D`。
- **合并防呆**:合并前 `git branch --show-current` 必须输出 `main`;合并后 `git log --oneline -1` 确认 HEAD 是新提交。
- **闭环原子性**:开 worktree → 开发 → 验证 → squash 合并 → 删 worktree/分支 → main 复验,整条跑完才算交付,默认一次性跑完不中途请示。

### 提交规范(Conventional Commits)

- 格式 `<type>(<scope>): <subject>`,subject 祈使句现在时、首字母小写、≤72 字符。
- type 白名单:`feat` `fix` `docs` `refactor` `perf` `test` `style` `build` `ci` `revert`。
- **明确禁用 `chore`**(最易滥用),由 commit-msg 钩子 + CI 双重拦截。

### 质量门禁:lint → test → build(三道门,按序缺一不可)

- 后端:`golangci-lint run ./...` → `go test ./...` → `go build ./...`。
- 前端:`pnpm --dir web lint` → `pnpm --dir web build`(含 tsc)。
- 顺序理由:lint 问题是 bug 温床,先修可减少 test 排查成本。lint 优先真修,尽量不 `nolint`/`eslint-disable`,必须抑制时须写注释说明。
- 验证是**合并前置门**(worktree 内全绿才合并,不允许先合并再验证),合并后 main 复验收尾。

### Git 钩子

- `make install-hooks` 指向仓库内 `.githooks/`(各 worktree 共享),分层:
  - **pre-commit**:gofmt + go build(秒级)。
  - **commit-msg**:Conventional + type 白名单 + 拒 chore(毫秒级)。
  - **pre-push**:完整三门禁(分钟级)。
- 临时跳过:`--no-verify` 或 `VIGIL_SKIP_HOOKS=1`。钩子是本地便利,**真正强制门禁在 CI**。

### e2e 测试

- 顶层 `test/e2e/`,**Ginkgo + Gomega** BDD 风格,带 **`//go:build integration`** 隔离(不参与默认 `go test ./...`)。
- 显式运行:`go test -tags=integration ./test/e2e/...` 或 `make test-e2e`(先 dev-up)。
- 用真实 PG(pgvector)+ Redis + Asynq worker,覆盖单测盲点。
- BeforeSuite 只 bootstrap 一次(11 spec 共用约 13s),BeforeEach 只清数据(resetDB TRUNCATE + reseedAdmin);异步断言用 gomega `Eventually` 不 sleep;连不上依赖自动 `t.Skip`。
- 改动涉及核心流水线(ingestion/triage/escalation/auth)应本地跑 e2e。

### Make 速查

`dev-setup` / `dev-up` / `dev-down` / `migrate` / `dev-backend`(:8080) / `dev-frontend`(:5173) / `check`(lint→test→build 提交门禁) / `verify`(main 复验) / `test` / `lint` / `test-e2e` / `test-e2e-web`(Playwright)。

## 理由

- worktree + 主仓库停 main:避免在主目录误开发,squash 保证 main 历史线性、一特性一条可读提交。
- 禁止叠分支:依赖通过 main 传递,避免"父分支未合并子分支已开工"导致的合并混乱与重复审查。
- 合并防呆与闭环原子性:把"代码写完 ≠ 交付"制度化——只有合入 main 并复验才算完成,杜绝游离分支与未验证合并。
- 禁用 chore:chore 最易成为"意图不清"的垃圾桶,拒绝它逼迫拆分或重新表述,保证提交原子、可读。
- lint→test→build 定序:lint 先行减少后续排查成本;验证前置门确保不把坏代码合进 main。
- 钩子分层:轻量检查放前段(commit),重检查放 push,兼顾反馈速度与拦截力度;真正强制在 CI,钩子只是本地便利。
- e2e 用 build tag 隔离:保证默认 `go test ./...` 快速,同时对核心流水线有真实依赖的端到端覆盖。

## 备选方案

- **先合并再验证**:否决——验证必须是合并前置门,先合并会把未验证代码带进 main,破坏 main 始终可用的前提。
- **允许 `chore` type**:否决——最易滥用,削弱提交的原子性与可读性;无合适 type 时应拆分或重述。
- **单分支直接在主目录开发 / 允许叠分支**:否决——主目录开发易污染 main,叠分支使依赖关系与合并顺序复杂化,worktree + 经 main 传递依赖更清晰。

## 影响 / 权衡

- 正面:main 始终线性、可用、可追溯;提交规范统一;门禁前置杜绝坏代码入库;e2e 覆盖核心流水线。
- 负面/限制:worktree 闭环流程步骤多,对新贡献者有一定上手成本;严格门禁与钩子在快速迭代时可能被感知为拖慢(提供 `--no-verify`/`VIGIL_SKIP_HOOKS=1` 应急,但真正强制在 CI)。
- 禁止叠分支意味着强依赖的特性必须串行推进(A 合并后 B 才开工),牺牲部分并行度换取合并可预期性。
