# AGENTS.md

> 本文件是 Vigil 项目的 **协作指南** —— 告诉任何在这个仓库工作的人（或 AI 代理）：
> 这是什么项目、怎么跑起来、在哪里找信息、必须遵守哪些约定。
>
> 文档为单一信源索引，详细内容见 `docs/` 下各文档，避免重复维护。

---

## 项目简介

**Vigil（守夜人）** —— 开源、IM 原生、AI 原生的告警处置平台，聚焦告警的"下一步"问题
（谁来响应、怎么协同、怎么处置、怎么复盘）。详见 [`README.md`](./README.md)。

---

## 技术栈速览

| 层 | 选型 |
|----|------|
| 后端 | Go 1.25 · Echo · ent（+ Atlas 迁移） · Asynq（异步任务） |
| 存储 | PostgreSQL（主）· Redis（缓存/队列/锁） |
| 前端 | React · TypeScript · Vite · Tailwind CSS v4 · shadcn/ui |
| 部署 | Docker Compose · Helm |

完整选型与论证见 [`docs/adr/`](./docs/adr/) 的 ADR-0003～0009。

---

## 仓库结构

```
vigil/
├── cmd/vigil/            # Go 入口
├── internal/             # 业务模块（按领域分）
│   └── auth/             # RBAC 权限点（permission.go）
├── ent/                  # ent ORM
│   ├── schema/           # ★ 实体定义（25 个实体，改 schema 后须 generate）
│   └── *.go              # ent 自动生成代码（提交入库）
├── web/                  # 前端（React + Vite）
├── docs/                 # 设计文档
│   ├── architecture.md   # ★ 系统架构全景（唯一主文档）
│   ├── adr/              # ★ 架构决策记录（一决策一文件，README.md 为索引）
│   ├── backlog.md        # 待办单一信源（暂不做/待规划）
│   └── operations.md     # 运维手册（部署/升级/备份/排查）
└── AGENTS.md             # 本文件（协作指南 + 开发流程/命令）
```

---

## 常用命令

### 本地一键起步（make）

```bash
make dev-setup              # 初始化 .env + 起依赖 + 迁移（首次）
make dev-up / dev-down      # 起 / 停依赖容器（postgres + redis）
make dev-backend            # 后端开发服务器（:8080）
make dev-frontend           # 前端开发服务器（:5173）
make help                   # 全部 target 说明
```

### 后端（Go）

```bash
go build ./...              # 编译
go test ./...               # 测试（默认不含 e2e）
go test -tags=integration ./test/e2e/...  # ★ e2e 集成测试（Ginkgo，需 docker 依赖，见 §测试）
make test-e2e               # e2e 一键（自动 dev-up 起依赖）
go run ./cmd/vigil/         # 运行
go generate ./ent/...       # ★ 改了 ent/schema 后必须重新生成
go generate ./cmd/vigil/... # ★ 改了 handler 注解后必须重新生成 OpenAPI spec
go mod tidy                 # 整理依赖
```

### 前端（web/）

```bash
pnpm --dir web install      # 安装依赖
pnpm --dir web dev          # 开发服务器（含 /api 代理到 :8080）
pnpm --dir web build        # 生产构建
pnpm --dir web gen:types    # ★ 改了后端 spec 后必须重新生成 types.gen.ts
```

### 整体验证（提交前）

```bash
go build ./... && pnpm --dir web build
```

---

## 开发约定（必读）

> 本节是开发流程的**权威速查**;其背后的设计取舍(为何 worktree 闭环、为何禁 chore、门禁顺序理由)见 [ADR-0035](./docs/adr/0035-dev-workflow-gates.md)。

### 工作模式：worktree + 特性分支

- 主仓库目录**永远停在 `main`**，只用于合并，不直接开发。
- 每个特性在 `.worktree/<type>-<特性>/` 下独立开发（**目录名 = 分支名 = `<type>-<特性>`**，扁平名无斜杠；type 与提交信息一致）：

  ```bash
  git worktree add .worktree/<type>-<name> -b <type>-<name>
  cd .worktree/<type>-<name>
  ```

- `.worktree/` 已 gitignore，不入库。

### 完整闭环：每个特性必须合入 main 才算交付（★ 重要）

> 代码写完 ≠ 完成。**合入 main + main 复验**才算交付。详见 [ADR-0035](./docs/adr/0035-dev-workflow-gates.md) 闭环原子性。

每个特性按以下顺序一次性做完（**不要在中途停下来请示合并/清理**，属规范已授权的可逆操作）：

1. **从最新 main 拉分支**（`git worktree add ... -b <type>-<name>`）。**严禁在特性分支上叠特性分支**——有依赖时先把被依赖特性合并进 main，再从更新后的 main 拉下一个。
2. **worktree 内开发 + 提交**。
3. **worktree 内三道门禁全绿**（lint→test→build）：`golangci-lint run ./... && go test ./... && go build ./...` + `pnpm --dir web lint && pnpm --dir web build`。改了 schema 必须 `go generate ./ent/...` 且生成代码一起提交。注：默认 `go test ./...` **不含 e2e**（用 `//go:build integration` 隔离）；e2e 在独立 CI job / `make test-e2e` 跑，改动涉及核心流水线（ingestion/triage/escalation/auth）时应本地 `make test-e2e` 验证。
4. **回主仓库 squash 合并**：先 `git branch --show-current` 确认在 **main**（不是则 `git checkout main`），再 `git merge --squash <type>-<name>` → `git commit` → `git log --oneline -1` 确认新提交落在 main。
5. **删 worktree + 分支**：`git worktree remove` + `git branch -D`。
6. **main 复验**：`golangci-lint run ./... && go test ./... && go build ./... && pnpm --dir web build`。

中间状态（特性分支游离 main 之外、worktree 未清理）**不算交付**。

### 提交信息规范（Conventional Commits，调整版）

### 提交信息规范（Conventional Commits，调整版）

格式：`<type>(<scope>): <subject>`

**允许的 type**：`feat` `fix` `docs` `refactor` `perf` `test` `style` `build` `ci` `revert`

> **⚠️ 禁止使用 `chore`**。没有合适 type 时，说明提交不够原子或意图不清，应拆分或重新表述。
> 理由与 type 对照见 [ADR-0035](./docs/adr/0035-dev-workflow-gates.md)。

### ent schema 变更

改了 `ent/schema/*.go` 后，**必须**执行 `go generate ./ent/...` 并把生成的代码一起提交。

---

## 关键设计原则

以下原则贯穿全项目，改动前先理解（详见 [ADR-0002 产品定位](./docs/adr/0002-product-positioning.md) 与相关 ADR）：

1. **告警消费者定位**：只做告警"下一步"，不内置监控采集。
2. **Event / Incident 分离**：Event 是原始信号（海量不可变），Incident 是处理单元（少量有状态）。
3. **IM-first**：IM 是主交互面，IM 操作走与 Web 相同的鉴权链路。
4. **AI 横向 Copilot**：AI 产出带 evidence + human-in-the-loop。
5. **Runbook 分两档**：诊断只读内置执行；处置写操作须人确认或外接。
6. **单组织多团队软隔离**：团队是数据归属边界，不继承权限。
7. **RBAC 可自配置**：权限点是系统枚举，角色由使用者自由组合。

---

## 信息导航

| 要找什么 | 去哪里 |
|---------|--------|
| 系统架构全景 | [`docs/architecture.md`](./docs/architecture.md) |
| 某项设计"为什么这么定" | [`docs/adr/`](./docs/adr/)（[索引](./docs/adr/README.md)） |
| 产品定位与非目标 | [ADR-0002](./docs/adr/0002-product-positioning.md) |
| 还剩什么没做 / 什么暂不做 | [`docs/backlog.md`](./docs/backlog.md) |
| 怎么部署/升级/备份/排障 | [`docs/operations.md`](./docs/operations.md) |
| 实体/字段/关系 | `ent/schema/` + [ADR-0010](./docs/adr/0010-event-incident-separation.md) |
| UI/UX 设计 | [ADR-0034](./docs/adr/0034-uiux-oncall-principles.md) |
| 怎么开发/提交 | 本文件「开发约定」+ [ADR-0035](./docs/adr/0035-dev-workflow-gates.md) |
| e2e 测试怎么写/跑 | [ADR-0035](./docs/adr/0035-dev-workflow-gates.md) + `test/e2e/` |
| 权限点清单 | `internal/auth/permission.go` |

---

## 当前状态

- ✅ 架构文档收敛为 architecture 主文档 + 35 份 ADR（活文档：backlog / operations）
- ✅ 全栈实现深入中：`internal/` 35 个业务模块、ent 25 实体、前端 19 页面(全站 i18n)
- ⏳ 服务自动供给/治理(方案C)、AI Copilot、复盘等持续演进

设计取舍见 [`docs/adr/`](./docs/adr/);组件全景见 [`docs/architecture.md`](./docs/architecture.md)。
