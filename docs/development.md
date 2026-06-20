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
vigil/                      # 主仓库（始终保持在 main 分支，只做合并，不直接开发）
├── .worktree/              # worktree 工作目录（已 gitignore，不入库）
│   ├── feature-xxx/        # 某特性分支的独立工作目录
│   └── feature-yyy/
├── docs/
├── ent/
├── internal/
├── web/
└── ...
```

- **主仓库目录**永远停在 `main`，仅用于：拉取最新、创建/合并特性分支、tag 发布。
- **所有实际开发**在 `.worktree/<特性>/` 下进行，每个特性一个独立目录，互不干扰。

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
git worktree add .worktree/feature-<特性名> -b feature/<特性名>

# 例：开发"告警接入"特性
git worktree add .worktree/feature-ingestion -b feature/ingestion
```

进入工作目录开发：

```bash
cd .worktree/feature-ingestion
# 此时你已在 feature/ingestion 分支的独立目录
# 编辑代码、构建、测试都在这里进行
```

### 2.3 worktree 日常操作

```bash
# 列出所有 worktree
git worktree list

# 在某 worktree 内完成开发后提交（见 §四 提交规范）
cd .worktree/feature-ingestion
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
git worktree remove .worktree/feature-ingestion
git branch -d feature/ingestion   # 分支已合并才能 -d，否则 -D 强删
```

> `.worktree/` 已在 `.gitignore`，不会被误提交。

---

## 三、特性分支开发模型

### 3.1 分支命名规范

| 类型 | 前缀 | 格式 | 示例 |
|------|------|------|------|
| 新功能 | `feature/` | `feature/<特性>` | `feature/ingestion` |
| Bug 修复 | `fix/` | `fix/<问题描述>` | `fix/escalation-timer-drift` |
| 重构 | `refactor/` | `refactor/<对象>` | `refactor/auth-middleware` |
| 文档 | `docs/` | `docs/<主题>` | `docs/api-contract` |
| 构建配置/CI | `build/` | `build/<工具>` | `build/docker-compose` |
| 性能优化 | `perf/` | `perf/<对象>` | `perf/schedule-cache` |

- 分支名用**小写 + 短横线**，简短描述意图。
- 一个特性分支只做一件事，避免混合多个无关改动。

### 3.2 分支生命周期

```
main ──────────●───────────●───────────●────────── (始终稳定)
                \         /           /
                 feature/a ───── PR ─         (开发)
                              feature/b ── PR ─ (开发)
```

1. **创建**：从最新 main 拉出特性分支（`git worktree add ... -b`）。
2. **开发**：在对应 worktree 内频繁提交，保持提交原子化。
3. **同步**：开发期间定期 `rebase main` 保持分支新鲜、减少冲突。
4. **合并**：通过 PR/合并请求合入 main（或本地 `git merge --no-ff`）。
5. **清理**：合并后删除 worktree 与分支。

### 3.3 合并要求

合入 main 前：
- 本地构建通过：`go build ./...`（后端）+ `pnpm --dir web build`（前端）。
- 如有 schema 变更：`go generate ./ent/...` 已重新生成。
- 提交历史整洁：必要时 `git rebase -i` 压缩/整理 commit。
- main 始终保持可编译状态。

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
# 开始新特性
git worktree add .worktree/feature-<name> -b feature/<name>
cd .worktree/feature-<name>

# 开发中提交
git add -A
git commit -m "feat(scope): 描述"

# 同步 main
git fetch origin && git rebase main

# 构建验证
go build ./... && pnpm --dir web build

# 完成（回主目录合并）
cd /path/to/vigil
git merge --no-ff feature/<name>
git worktree remove .worktree/feature-<name>
git branch -d feature/<name>
```

---

## 六、IDE / 工具配置提示

- **多 worktree 多编辑器窗口**：每个 `.worktree/<特性>` 单开一个 IDE 窗口，互不干扰。
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
