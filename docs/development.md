# Vigil 开发流程

| 字段 | 内容 |
|------|------|
| **文档版本** | v0.1 |
| **创建日期** | 2026-06-20 |
| **状态** | Active |
| **适用范围** | 所有 Vigil 代码与文档贡献 |

> 本文档定义 Vigil 的开发协作规范：基于 git worktree 的工作目录隔离、特性分支开发模型、以及统一的提交信息规范。所有贡献者须遵循。

---

## 一、目录结构约定

```
vigil/                      # 主仓库（始终保持在 main 分支，只接收 squash 合并，不直接开发）
├── .worktree/              # worktree 工作目录（已 gitignore，不入库）
│   ├── feat-xxx/           # 某特性分支的独立工作目录（目录名 = 分支名 = <type>-<特性>）
│   ├── fix-yyy/
│   └── docs-zzz/
├── docs/
├── ent/
├── internal/
├── web/
└── ...
```

- **主仓库目录**永远停在 `main`，仅用于：拉取最新、创建特性分支、接收 squash 合并、tag 发布。
- **所有实际开发**在 `.worktree/<type>-<特性>/` 下进行，每个特性一个独立目录，目录名与分支名同名（`<type>-<特性>`），互不干扰。

---

## 二、Worktree 工作模式

### 2.1 为什么用 worktree

传统单工作区下，切换分支会让工作目录文件突变，多个特性并行开发、IDE 索引、本地构建缓存都会互相干扰。git worktree 让**每个特性分支拥有独立的工作目录**：

- 多特性可并行开发，各开各的编辑器/终端，互不影响。
- 切分支不丢失本地构建缓存（每个 worktree 独立的 node_modules、构建产物）。
- 主目录始终保持 main 干净状态，随时可查看/对比线上版本。

### 2.2 创建特性 worktree

```bash
# 从 main 创建特性分支并检出独立工作目录
# 目录名与分支名统一为 <type>-<特性>（type 见 §3.1）
git worktree add .worktree/<type>-<特性> -b <type>-<特性>

# 例：开发"告警接入"特性（type=feat）
git worktree add .worktree/feat-ingestion -b feat-ingestion

# 例：修复"升级计时器漂移"问题（type=fix）
git worktree add .worktree/fix-escalation-timer-drift -b fix-escalation-timer-drift
```

进入工作目录开发：

```bash
cd .worktree/feat-ingestion
# 此时你已在 feat-ingestion 分支的独立目录
# 编辑代码、构建、测试都在这里进行
```

### 2.3 worktree 日常操作

```bash
# 列出所有 worktree
git worktree list

# 在某 worktree 内完成开发后提交（见 §四 提交规范）
cd .worktree/feat-ingestion
git add -A
git commit -m "feat(ingestion): ..."

# 同步 main 最新改动到特性分支（保持特性分支新鲜）
git fetch origin
git rebase main   # 或 merge main
```

### 2.4 清理 worktree

特性合并后，删除工作目录与分支：

```bash
cd /path/to/vigil            # 回到主仓库目录
git worktree remove .worktree/feat-ingestion
git branch -d feat-ingestion   # 分支已合并才能 -d，否则 -D 强删
```

> `.worktree/` 已在 `.gitignore`，不会被误提交。

---

## 三、特性分支开发模型

### 3.1 分支命名规范

分支名采用 **`<type>-<特性>`** 格式（**扁平名，无斜杠**），与 worktree 目录名、提交信息的 type 保持一致（type 见 §4.2）。

| 类型 | type | 格式 | 示例 |
|------|------|------|------|
| 新功能 | `feat` | `feat-<特性>` | `feat-ingestion` |
| Bug 修复 | `fix` | `fix-<问题描述>` | `fix-escalation-timer-drift` |
| 重构 | `refactor` | `refactor-<对象>` | `refactor-auth-middleware` |
| 文档 | `docs` | `docs-<主题>` | `docs-api-contract` |
| 构建/依赖 | `build` | `build-<对象>` | `build-docker-compose` |
| CI 配置 | `ci` | `ci-<对象>` | `ci-github-actions` |
| 性能优化 | `perf` | `perf-<对象>` | `perf-schedule-cache` |
| 测试 | `test` | `test-<对象>` | `test-escalation` |
| 风格 | `style` | `style-<对象>` | `style-import-sort` |
| 回滚 | `revert` | `revert-<对象>` | `revert-router-v7` |

- 分支名用**小写 + 短横线**，简短描述意图。
- **目录名 = 分支名**：worktree 目录与分支同名，统一为 `<type>-<特性>`，见 §2.2。
- 一个特性分支只做一件事，避免混合多个无关改动。

### 3.2 分支生命周期

```
特性分支（feat-a、fix-b）在 worktree 内自由开发、频繁提交；
合入 main 时用 squash 压缩成一个提交，main 历史保持线性：

main ──●──●──●──●──●──●── (每个 ● 是一个特性 squash 后的提交，始终稳定)
            ↑     ↑
       feat-a    fix-b   (开发完即删，不入 main 历史)
```

1. **创建**：从最新 main 拉出特性分支（`git worktree add ... -b <type>-<特性>`）。
2. **开发**：在对应 worktree 内频繁提交，保持提交原子化。
3. **同步**：开发期间定期 `rebase main` 保持分支新鲜、减少冲突。
4. **合并**：通过 **squash 合并** 入 main（见 §3.3）。
5. **清理**：合并后删除 worktree 与分支。

#### 3.2.1 串行依赖特性：先合并再开下一个，禁止叠分支（★ 重要）

特性间常有编译/逻辑依赖（如 B 用到 A 改的 ent schema 生成代码）。正确处理方式：

```
正确：每个特性独立从 main 拉，依赖项先完整合并回 main 再拉下一个
main ──┬─ feat-A ──squash──► main ──┬─ feat-B（从含 A 的 main 拉）──squash──► main

错误：在特性分支上叠特性分支（制造必须按序合并的链，且 main 长期不含新代码）
main ──┬─ feat-A ── feat-B ── feat-C  （越叠越深，main 一直停在原地）
```

- **禁止在特性分支之上再开特性分支**（`feat-B` 从 `feat-A` 拉）。这会造出一条必须按序合并、且 main 长期空转的链。
- **有依赖时**：先把被依赖的特性（A）走完完整闭环（开发→验证→squash 合并→main 复验→删分支），**再从更新后的 main** 拉下一个特性分支（B）。
- 这样每个特性面对的 main 都是"已含全部前置改动"的最新状态，rebase/合并冲突最小，main 也始终是集大成点。
- 判定依据：若 B 的代码离开 A 就编译不过，A 就是 B 的依赖 → A 必须先合并进 main。

> 把"编译依赖"误当成"分支依赖"是常见误区。依赖关系应通过 main 传递（A 进 main → B 从 main 拉），而不是通过叠分支传递（B 挂在 A 上）。

### 3.3 合并要求（squash 模式）

**统一采用 squash 合并**（`git merge --squash`），不保留特性分支的零散提交历史：

- 特性分支的多次 WIP/修复提交，压缩成 main 上的**一个干净提交**。
- main 历史线性、可读，每个提交对应一个完整特性，便于回溯与生成 changelog。
- squash 后的提交信息须遵循 §4 规范（一个特性 = 一条规范的 commit message）。

合入 main 前（**在 worktree 内完成验证，绿了再合并**）：
- 如有 schema 变更：`go generate ./ent/...` 已重新生成。
- **三道质量门禁，按顺序，缺一不可**（见 §3.4）：
  1. **lint 通过**：`go vet` + `golangci-lint run`（后端）/ `pnpm --dir web lint`（前端）
  2. **test 通过**：`go test ./...`（后端）/ `pnpm --dir web build`（前端构建即类型检查）
  3. **build 通过**：`go build ./...`（后端）+ `pnpm --dir web build`（前端）
- 验证顺序：开发 → worktree 内 lint/test/build 全绿 → 合并。**不要先合并再在 main 上验证**——main 必须只接收已验证通过的改动。

> 验证是合并的**前置门**，不是合并后的补救。worktree 验证未通过时，停在 worktree 修复，绝不带着红的状态进合并步骤。

### 3.4 提交前质量门禁（lint → test → build → commit）

**强制规则：开发代码后必须先 lint 检查并修复问题，通过后再执行 test 回归，全绿后再提交代码。**

完整闭环（每次提交前在 worktree 内执行）：

```bash
# ① Lint（先跑，发现问题立即修复，尽可能不忽略/不 nolint）
golangci-lint run ./...            # 后端
pnpm --dir web lint                # 前端
#   发现问题 → 修复 → 重新跑，直到 0 问题

# ② Test 回归（lint 通过后）
go test ./...                      # 后端（含所有包）
pnpm --dir web build               # 前端（build 含 tsc 类型检查）
#   测试红 → 修复 → 重新跑，直到全绿

# ③ Build 确认（最后）
go build ./...                     # 后端
#   build 失败 → 修复 → 重新跑

# ④ 全绿后才提交
git add -A
git commit -m "feat(scope): ..."
```

**lint 修复原则**：
- **尽可能不忽略 lint 问题**——优先真正修复，而非 `//nolint` 或 `eslint-disable`。
- 只有极少数确有正当理由的（如 ent 生成代码、第三方类型）才允许抑制，且必须注释说明原因。
- 提交信息里如涉及 lint 修复，单独说明（如 "lint: 修复 X 类未使用变量"）。

**为什么 lint 在 test 之前**：lint 问题（未使用变量、错误未处理、shadow 等）往往是 bug 的温床；先修 lint 能减少 test 阶段的排查成本。

squash 合并的标准操作（详见 §5 速查）：

```bash
cd /path/to/vigil                       # 回主仓库 main
git merge --squash <type>-<name>        # 把特性改动压到暂存区（不自动提交）
git commit -m "<规范的 commit message>" # 用一条规范信息完成合并提交
git branch -D <type>-<name>             # 删除已合并的特性分支
```

> 注意：`merge --squash` 不会创建 merge commit，特性分支的提交历史不进 main。
> 分支删除用 `-D`（squash 后 git 不认为分支"已合并"，`-d` 会拒绝）。

### 3.4 闭环原子性（★ 重要）

一个特性任务的"完成"，指 **§五 速查里的整条闭环全部跑完**：开 worktree → 开发提交 → 合入前验证 → squash 合并 → 删 worktree/分支 → main 复验。**中间状态（特性分支飘在 main 之外、worktree 未清理）不算交付。**

- **默认一次性跑完整条流程**，按 §五 速查从开 worktree 到删分支连续执行，**不要在流程中途停下来请求确认合并或清理**——这些都是本规范已定义、可预测、可逆（squash 错了能 reset、分支误删能重建）的操作。
- **仅当遇到本规范未覆盖、或需要决策的情况，才暂停请示**，例如：
  - 合并冲突无法自动解决；
  - 合入前构建/测试失败（停在 worktree 修复，不要带红进合并）；
  - 任务范围不清、需求有歧义；
  - schema 变更需斟酌取舍；
  - 破坏性操作（删库、force push 公共分支等）。
- 判定原则：**规范已授权、流程已定义、可逆的操作 → 直接做完**；需要价值判断或不可逆的操作 → 先确认。

> 这条规则的意义：让 main 始终处于正确状态（§一）。把"完成特性"理解成"代码写完"会让 feature 分支长期游离于 main 之外，正是本工作流要避免的。

---

## 四、提交信息规范

采用 **Conventional Commits** 规范，但按 Vigil 实际情况调整了 type 清单。

### 4.1 格式

```
<type>(<scope>): <subject>

<body>          # 可选，空一行后写

<footer>        # 可选，如 BREAKING CHANGE、关联 issue
```

- **type**：必填，见 §4.2。
- **scope**：可选，表示影响范围（模块/包名），如 `ingestion`、`auth`、`web`、`docs`。
- **subject**：必填，简明描述本次提交做了什么。
  - 用祈使句、现在时（"add" 而非 "added"）。
  - 首字母小写，末尾不加句号。
  - ≤ 72 字符。
- **body**：可选，说明"为什么"和"做了什么"（subject 装不下时）。
- **footer**：`BREAKING CHANGE: <说明>` 标注破坏性变更；`Closes #123` 关联 issue。

### 4.2 允许的 type（★ 重要）

| type | 用途 |
|------|------|
| `feat` | 新功能（用户可感知的新能力） |
| `fix` | Bug 修复 |
| `docs` | 文档变更（README、docs/、代码注释） |
| `refactor` | 重构（既非新增功能也非修 bug，如结构调整） |
| `perf` | 性能优化 |
| `test` | 测试相关（新增/修改测试，不改变生产行为） |
| `style` | 代码风格（格式化、空白，不改逻辑） |
| `build` | 构建系统、依赖、CI/CD、容器化（go.mod、Dockerfile、package.json、CI 配置等） |
| `ci` | CI 配置变更（GitHub Actions 等） |
| `revert` | 回滚某次提交 |

### 4.3 ⚠️ 禁止使用 `chore`

**本项目明确禁用 `chore` 类型。**

`chore` 是 Conventional Commits 里最模糊、最易滥用的 type（"杂务"），会导致提交历史难以分类、难以自动生成 changelog。请按实际语义选用更具体的 type：

| 想写 chore 的场景 | 应改用 |
|---|---|
| 升级依赖版本（go.mod / package.json） | `build` |
| 改 .gitignore / 仓库配置 | `build` |
| 改 CI 配置 | `ci` |
| 改 lint 规则 / 格式化 | `style` 或 `build` |
| 小重构 / 代码整理 | `refactor` |
| 杂项文档 | `docs` |

> 原则：**没有合适的 type 时，说明提交不够原子或意图不清，应拆分或重新表述**，而非塞进 chore。

### 4.4 示例

```
feat(ingestion): 实现告警 webhook 接入与归一化流水线

通过 Asynq 串联 归一化→去重→分诊 各阶段，Receiver 秒级返回
202，保证告警不丢。适配 Prometheus/Zabbix/Grafana 三源。

Closes #12
```

```
fix(escalation): 修复升级计时器在 ack 取消时的竞态

ack 与升级任务到期并发时，状态守卫判定可能读到中间态，
增加 incident.status 复核确保已 ack 的事件不再触发升级。
```

```
build(web): 升级 react-router 至 v7，调整路由 API

v7 移除部分 v6 API，相应调整 App.tsx 与 AppShell。
```

```
refactor(auth): 统一 Web 与 IM 的鉴权链路

原先 Web 与 IM 各有一套鉴权，现收敛为单一中间件，
IM 操作复用同一 (user, action, resource) 判定逻辑。
```

---

## 五、日常开发速查

```bash
# 开始新特性（目录名 = 分支名 = <type>-<特性>）
git worktree add .worktree/<type>-<name> -b <type>-<name>
cd .worktree/<type>-<name>

# 开发中提交
git add -A
git commit -m "feat(scope): 描述"

# 同步 main
git fetch origin && git rebase main

# 合入前验证（在 worktree 内，三道门禁全绿才合并，见 §3.4）
golangci-lint run ./... && go test ./... && go build ./...   # 后端：lint→test→build
pnpm --dir web lint && pnpm --dir web build                  # 前端：lint→build

# 完成（回主目录 squash 合并）
cd /path/to/vigil
git merge --squash <type>-<name>       # 压缩到暂存区（不自动提交）
git commit -m "feat(scope): 描述"      # 用一条规范信息完成 squash 提交
git worktree remove .worktree/<type>-<name>
git branch -D <type>-<name>            # squash 后用 -D（git 不认为已合并）

# main 复验（合并后确认 main 仍可编译，闭环收尾）
go build ./... && pnpm --dir web build
```

---

## 六、IDE / 工具配置提示

- **多 worktree 多编辑器窗口**：每个 `.worktree/<type>-<特性>` 单开一个 IDE 窗口，互不干扰。
- **TS/Go Server 缓存**：worktree 间独立，无冲突。
- **构建缓存**：各 worktree 独立的 `web/node_modules`（需各自 `pnpm install`）、Go 构建缓存共享（Go module cache 全局）。
- **husky / pre-commit hook**（如后续引入）：在主仓库配置，各 worktree 共享 git hooks。

---

## 七、FAQ

**Q: 为什么不用普通分支切换（git switch）而用 worktree？**
A: 切换会突变工作目录，多特性并行时 IDE 索引、构建缓存、未提交改动互相干扰；worktree 给每个分支独立目录，并行无冲突。

**Q: worktree 目录能改名/移动吗？**
A: 不建议。worktree 路径记录在 `.git/worktrees/`，移动会导致失效；如需调整用 `git worktree move <旧路径> <新路径>`。

**Q: 误删了 worktree 目录怎么办？**
A: `git worktree prune` 清理失效记录，分支本身仍在，可重新 `git worktree add`。

**Q: chore 真的一个都不能用吗？**
A: 是的，本项目禁用 chore。CI/钩子可配置为拒绝含 chore 的提交。
