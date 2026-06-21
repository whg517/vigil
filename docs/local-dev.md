# 本地开发指南

> 从零开始搭建 Vigil 本地开发环境，到成功运行前后端的全流程。

---

## 1. 前置条件

| 工具 | 版本要求 | 说明 |
|------|---------|------|
| Go | 1.25+ | 后端语言 |
| Node.js | 22+ | 前端运行时 |
| pnpm | 最新（通过 corepack 启用） | 前端包管理 |
| Docker + Docker Compose | 最新 | PostgreSQL + Redis 依赖服务 |

```bash
# 检查版本
go version          # go1.25+
node -v             # v22+
pnpm -v             # 10.x
docker -v
docker compose version
```

---

## 2. 快速启动（5 分钟）

```bash
# 1. 克隆仓库
git clone <repo-url> vigil && cd vigil

# 2. 一键启动依赖服务 + 迁移
make dev-setup

# 3. 终端 1：启动后端
make dev-backend

# 4. 终端 2：启动前端
make dev-frontend

# 5. 打开浏览器
open http://localhost:5173
```

登录凭据：**admin / changeme**

---

## 3. 环境配置

### 3.1 `.env` 文件

首次运行 `make dev-setup` 会自动从 `.env.example` 复制 `.env`。

`.env` 文件会被 Go 程序自动加载（通过 godotenv），无需手动 `export`。

### 3.2 开发模式智能默认值

`development` 模式下，以下配置**无需手动设置**，系统自动填充：

| 配置项 | 自动值 | 说明 |
|--------|--------|------|
| `VIGIL_AUTH_JWT_SECRET` | 自动生成 | 登录功能开箱即用 |
| `VIGIL_AUTH_ENABLED` | `false` | 不强制鉴权（仅限开发） |
| DB/Redis | `127.0.0.1` | 连接本机 Docker 服务 |

> ⚠️ 生产环境（`VIGIL_APP_ENV=production`）不会自动填充任何默认值，所有敏感配置必须显式设置。

### 3.3 可选功能配置

| 功能 | 需要配置的变量 | 说明 |
|------|---------------|------|
| AI 诊断/复盘 | `VIGIL_LLM_API_KEY` | 智谱 GLM，为空时自动降级 |
| 飞书 IM | `VIGIL_IM_FEISHU_*` 四要素 | 为空时飞书通知禁用 |
| 钉钉 IM | `VIGIL_IM_DINGTALK_APP_KEY/SECRET` | 为空时钉钉通知禁用 |
| 邮件通知 | `VIGIL_NOTIFICATION_SMTP_HOST` | 为空时邮件通道禁用 |

---

## 4. 数据库迁移

### 迁移机制

Vigil 使用 **版本化 SQL 迁移 + ent auto-migrate** 双轨：

1. **pre-migrate**（`pre_` 前缀）：在 ent auto-migrate 之前执行，如安装 PostgreSQL 扩展
2. **ent auto-migrate**：根据 `ent/schema/*.go` 自动创建/更新表结构
3. **post-migrate**（其余 `.sql`）：在 ent auto-migrate 之后执行，如增量数据变更

### 常用命令

```bash
# 执行迁移（通常由 make dev-setup 自动完成）
go run ./cmd/vigil/ migrate

# 重新迁移（清库后重建，开发调试用）
# ⚠️ 这会删除所有数据！
docker compose exec postgres psql -U vigil -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public;"
go run ./cmd/vigil/ migrate
```

### 迁移文件位置

```
internal/migrate/migrations/
├── pre_0001_pgvector.sql   # pre-migrate: 安装 pgvector 扩展
└── 0002_baseline.sql       # post-migrate: 占位 baseline
```

---

## 5. 开发命令速查

```bash
# 依赖服务
make dev-up          # 启动 postgres + redis
make dev-down        # 停止依赖服务

# 迁移
make migrate         # 执行数据库迁移

# 开发服务器
make dev-backend     # 启动后端（前台，:8080）
make dev-frontend    # 启动前端（前台，:5173）

# 代码质量
make check           # 后端 + 前端构建验证（提交前必过）
make test            # 运行测试
make lint            # golangci-lint 检查

# 清理
make clean           # 停止依赖服务
```

---

## 6. 前端开发

### 技术栈

React 19 + TypeScript + Vite 8 + Tailwind CSS v4 + shadcn/ui

### 关键约定

- **路径别名**：`@/` → `web/src/`（Vite 配置 + tsconfig 已设置）
- **API 代理**：Vite dev server 自动将 `/api/*` 代理到 `http://localhost:8080`
- **UI 组件**：优先复用 `web/src/components/ui/` 中的 shadcn 组件
- **代码风格**：参考现有文件，注释用中文

### 常用命令

```bash
pnpm --dir web dev          # 启动开发服务器（HMR）
pnpm --dir web build        # 生产构建
pnpm --dir web lint         # ESLint 检查
```

---

## 7. 常见问题

### Q: `make dev-up` 报端口冲突怎么办？

本机有其他 Docker 容器或服务占用了 5432/6379 端口。查看占用的容器：

```bash
docker ps --format "{{.Names}} {{.Ports}}" | grep -E "5432|6379"
```

停止冲突容器后重试，或修改 `docker-compose.yml` 的端口映射（如 `5433:5432`），并同步修改 `.env` 中的连接地址。

### Q: 数据库迁移失败，提示表已存在/不存在？

可能是迁移部分执行后中断。开发环境最简单的方式是清库重建（见 §4 的重置命令）。

### Q: 修改了 ent/schema 后怎么办？

```bash
# 重新生成 ent 代码
go generate ./ent/...

# 重新执行迁移（ent auto-migrate 会同步新 schema）
go run ./cmd/vigil/ migrate
```

⚠️ 生成的代码必须一起提交（`ent/` 目录下的 `*.go` 文件）。

### Q: 后端改了端口/地址，前端连不上？

前端 Vite 代理配置在 `web/vite.config.ts`，默认指向 `http://localhost:8080`。
如修改了后端端口，同步更新 `vite.config.ts` 中的 `proxy.target`。

### Q: 如何启用 AI 功能？

在 `.env` 中设置智谱 API Key：

```bash
VIGIL_LLM_API_KEY=your-zhipu-api-key
```

未配置时 AI 诊断和复盘自动降级为规则草稿（不影响其他功能）。
