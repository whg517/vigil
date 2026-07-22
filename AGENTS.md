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
| 后端 | Go 1.25 · Echo · ent（ORM）+ Atlas（版本化迁移）· Asynq（异步任务） |
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
│   ├── schema/           # ★ 实体定义（以 schema 为准；改后须 generate）
│   └── *.go              # ent 自动生成代码（提交入库）
├── web/                  # 前端（React + Vite）
├── docs/                 # 设计文档
│   ├── architecture.md   # ★ 系统架构全景（怎么实现，唯一架构主文档）
│   ├── requirements.md   # ★ 需求文档（要什么/怎么算做到，FR/NFR 单一信源）
│   ├── user-stories/     # 用户故事（四角色典型场景，README.md 为索引）
│   ├── adr/              # ★ 架构决策记录（一决策一文件，README.md 为索引）
│   └── operations.md     # 运维手册（部署/升级/备份/排查）
└── AGENTS.md             # 本文件（协作指南 + 开发流程/命令）
```

---

## 环境前置

本地开发所需工具链版本（与 `.github/workflows/ci.yml` 对齐，CI 是权威版本源）：

| 工具 | 版本 | 说明 |
|------|------|------|
| Go | 1.25 | `go.mod` 声明 `go 1.25.0`，CI 用 `1.25` |
| Node.js | 22 | CI `setup-node` 锁 22 |
| pnpm | 9 | CI `pnpm/action-setup` 锁 9；`web/package.json` 的 `packageManager` 字段已固定（corepack 环境自动匹配） |
| golangci-lint | **v2**（CI 锁 v2.12.2） | ⚠️ `.golangci.yml` 是 v2 格式（`version: "2"`），v1 解析直接失败；版本须与 Go toolchain 匹配（v2.0.2 是 go 1.24 编译，不支持目标 go 1.25） |
| docker compose | v2 | Makefile 用 `docker compose`（空格）子命令语法，不兼容 v1 的 `docker-compose` |
| atlas CLI | 运行时镜像内置 `arigaio/atlas:1.2.3`；CI 经 `ariga/setup-atlas` 安装 | 本地 `atlas migrate diff`（schema 变更）与 `make test-e2e` 需本机可执行 `atlas` |

依赖容器（postgres 需 pgvector 扩展，推荐 `pgvector/pgvector:pg16`）由 `make dev-up` 自动拉起，无需手装。

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
go generate ./ent/...       # ★ 改了 ent/schema 后必须重新生成 ent 代码
go generate ./cmd/vigil/... # ★ 改了 handler 注解后必须重新生成 OpenAPI swagger spec
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

格式：`<type>(<scope>): <subject>`

**允许的 type**：`feat` `fix` `docs` `refactor` `perf` `test` `style` `build` `ci` `revert`

> **⚠️ 禁止使用 `chore`**。没有合适 type 时，说明提交不够原子或意图不清，应拆分或重新表述。
> 理由与 type 对照见 [ADR-0035](./docs/adr/0035-dev-workflow-gates.md)。

### ent schema 变更

改了 `ent/schema/*.go` 后，**必须**两步：

1. `go generate ./ent/...` 重新生成 ent 代码（一起提交）。
2. `atlas migrate diff <name> --env local`（见 `atlas.hcl`）生成新版本迁移 SQL，把 `internal/schema/migrations/*.sql` + `atlas.sum` 一起提交。

这样 schema 变更同时落进「ent 强类型 API」与「atlas 版本化迁移文件」两侧，运行时 `vigil migrate` apply 才能生效。

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
| 功能/非功能需求与验收口径 | [`docs/requirements.md`](./docs/requirements.md) |
| 角色视角的典型使用场景 | [`docs/user-stories/`](./docs/user-stories/)（[索引](./docs/user-stories/README.md)） |
| 某项设计"为什么这么定" | [`docs/adr/`](./docs/adr/)（[索引](./docs/adr/README.md)） |
| 产品定位与非目标 | [`docs/requirements.md`](./docs/requirements.md) + [ADR-0002](./docs/adr/0002-product-positioning.md) |
| 怎么部署/升级/备份/排障 | [`docs/operations.md`](./docs/operations.md) |
| 实体/字段/关系 | `ent/schema/` + [ADR-0010](./docs/adr/0010-event-incident-separation.md) |
| UI/UX 设计 | [ADR-0034](./docs/adr/0034-uiux-oncall-principles.md) |
| 怎么开发/提交 | 本文件「开发约定」+ [ADR-0035](./docs/adr/0035-dev-workflow-gates.md) |
| 外部贡献怎么提（fork + PR） | [`CONTRIBUTING.md`](./CONTRIBUTING.md) |
| e2e 测试怎么写/跑 | [ADR-0035](./docs/adr/0035-dev-workflow-gates.md) + `test/e2e/` |
| 权限点清单 | `internal/auth/permission.go` |
| 怎么扩展告警源/通知通道/IM 平台/执行器 | [`docs/extending.md`](./docs/extending.md) + [ADR-0009](./docs/adr/0009-pluggable-integrations.md) |

---

## 当前状态

- ✅ 文档体系收敛为 requirements 需求文档 + architecture 架构主文档 + user-stories 用户故事 + ADR（数量见 `docs/adr/`；活文档：operations）
- ✅ 全栈实现深入中：业务模块见 `internal/`、实体定义见 `ent/schema/`、前端页面见 `web/src/pages/`（全站 i18n）
- ⏳ 后续演进(AI Copilot 深化、复盘增强等)以 GitHub Issues 跟踪

设计取舍见 [`docs/adr/`](./docs/adr/);组件全景见 [`docs/architecture.md`](./docs/architecture.md)。
