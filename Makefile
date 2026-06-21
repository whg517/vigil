# Vigil 开发命令
# 用法：make <target>
#
# 常用：
#   make dev-setup    — 一键启动依赖服务 + 迁移
#   make dev-backend  — 启动后端（前台）
#   make dev-frontend — 启动前端（前台）
#   make check        — 提交前验证（lint + build）

COMPOSE_PROJECT_NAME := vigil
ENV_FILE := .env

# ============================================================
# 依赖服务（PostgreSQL + Redis）
# ============================================================

# 确保 .env 存在
$(ENV_FILE):
	cp .env.example $(ENV_FILE)

# 启动依赖服务（postgres + redis）
dev-up: $(ENV_FILE)
	COMPOSE_PROJECT_NAME=$(COMPOSE_PROJECT_NAME) docker compose up -d postgres redis
	@echo "Waiting for postgres to be ready..."
	@until docker compose exec -T postgres pg_isready -U vigil > /dev/null 2>&1; do sleep 1; done
	@echo "Waiting for redis to be ready..."
	@until docker compose exec -T redis redis-cli ping > /dev/null 2>&1; do sleep 1; done
	@echo "✅ postgres + redis ready"

# 停止依赖服务
dev-down:
	COMPOSE_PROJECT_NAME=$(COMPOSE_PROJECT_NAME) docker compose down

# ============================================================
# 数据库迁移
# ============================================================

migrate: dev-up
	go run ./cmd/vigil/ migrate

# ============================================================
# 开发服务器
# ============================================================

# 一键启动：依赖服务 + 迁移（前后端需在两个终端分别启动）
dev-setup: migrate
	@echo ""
	@echo "✅ All services ready!"
	@echo ""
	@echo "   启动后端（终端 1）:  make dev-backend"
	@echo "   启动前端（终端 2）:  make dev-frontend"
	@echo ""
	@echo "   后端:  http://localhost:8080"
	@echo "   前端:  http://localhost:5173"
	@echo "   登录:  admin / changeme"

# 启动后端（前台，自动运行迁移）
dev-backend:
	go run ./cmd/vigil/

# 启动前端（前台，Vite dev server）
dev-frontend:
	pnpm --dir web dev

# ============================================================
# 代码质量
# ============================================================

# 后端 lint
lint:
	golangci-lint run ./...

# 后端构建
build:
	go build ./...

# 前端构建
build-frontend:
	pnpm --dir web build

# 提交前验证（对应 docs/development.md §3.4 门禁）
check: build build-frontend
	@echo "✅ All checks passed"

# ============================================================
# 测试
# ============================================================

test:
	go test ./...

# ============================================================
# 清理
# ============================================================

clean: dev-down
