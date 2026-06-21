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

完整选型与论证见 [`docs/tech-stack.md`](./docs/tech-stack.md)。

---

## 仓库结构

```
vigil/
├── cmd/vigil/            # Go 入口
├── internal/             # 业务模块（按领域分）
│   └── auth/             # RBAC 权限点（permission.go）
├── ent/                  # ent ORM
│   ├── schema/           # ★ 实体定义（17 个实体，改 schema 后须 generate）
│   └── *.go              # ent 自动生成代码（提交入库）
├── web/                  # 前端（React + Vite）
├── docs/                 # 所有设计文档
│   ├── PRD.md            # 产品需求（15+2 能力域）
│   ├── data-model.md     # 数据模型 + RBAC
│   ├── architecture.md   # 系统架构（6 大引擎）
│   ├── tech-stack.md     # 技术选型
│   ├── development.md    # ★ 开发流程（必读）
│   └── capabilities/     # 10 份能力域详细设计
└── AGENTS.md             # 本文件
```

---

## 常用命令

### 后端（Go）

```bash
go build ./...              # 编译
go test ./...               # 测试
go run ./cmd/vigil/         # 运行
go generate ./ent/...       # ★ 改了 ent/schema 后必须重新生成
go mod tidy                 # 整理依赖
```

### 前端（web/）

```bash
pnpm --dir web install      # 安装依赖
pnpm --dir web dev          # 开发服务器（含 /api 代理到 :8080）
pnpm --dir web build        # 生产构建
```

### 整体验证（提交前）

```bash
go build ./... && pnpm --dir web build
```

---

## 开发约定（必读）

> 完整规范见 [`docs/development.md`](./docs/development.md)。以下为强制要点。

### 工作模式：worktree + 特性分支

- 主仓库目录**永远停在 `main`**，只用于合并，不直接开发。
- 每个特性在 `.worktree/<type>-<特性>/` 下独立开发（**目录名 = 分支名 = `<type>-<特性>`**，扁平名无斜杠；type 与提交信息一致，见 docs/development.md §4.2）：

  ```bash
  git worktree add .worktree/<type>-<name> -b <type>-<name>
  cd .worktree/<type>-<name>
  ```

- `.worktree/` 已 gitignore，不入库。

### 提交信息规范（Conventional Commits，调整版）

格式：`<type>(<scope>): <subject>`

**允许的 type**：`feat` `fix` `docs` `refactor` `perf` `test` `style` `build` `ci` `revert`

> **⚠️ 禁止使用 `chore`**。没有合适 type 时，说明提交不够原子或意图不清，应拆分或重新表述。
> 对照表见 [`docs/development.md`](./docs/development.md) §4.3。

### ent schema 变更

改了 `ent/schema/*.go` 后，**必须**执行 `go generate ./ent/...` 并把生成的代码一起提交。

---

## 关键设计原则

以下原则贯穿全项目，改动前先理解（详见 [`docs/data-model.md`](./docs/data-model.md) §1 设计基线）：

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
| 产品做什么 | [`docs/PRD.md`](./docs/PRD.md) |
| 实体/字段/关系 | [`docs/data-model.md`](./docs/data-model.md) + `ent/schema/` |
| 架构与引擎 | [`docs/architecture.md`](./docs/architecture.md) |
| UI/UX 设计 | [`docs/ui-ux.md`](./docs/ui-ux.md) |
| 某能力域怎么做 | [`docs/capabilities/`](./docs/capabilities/) |
| 怎么开发/提交 | [`docs/development.md`](./docs/development.md) |
| 权限点清单 | `internal/auth/permission.go` |

---

## 当前状态

- ✅ 立项文档（需求/架构/数据模型/技术选型/能力域设计）完成
- ✅ 全栈工程骨架可编译（ent 17 实体 + React 前端）
- ⏳ 业务模块实现中（ingestion/triage/escalation/...）

下一步计划见各文档的"开放问题"与"下一步"章节。
