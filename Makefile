##@ General

.DEFAULT_GOAL := help

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

COMPOSE_PROJECT_NAME := vigil
ENV_FILE := .env
GO_LINT  := golangci-lint run ./...

# docker compose 封装，统一注入两个环境变量：
# - COMPOSE_PROJECT_NAME：固定项目名。不固定时 compose 回退用目录名，在 worktree
#   （目录名 ≠ vigil）下 up 与 exec 会各自解析出不同项目，exec 定位不到刚起的
#   postgres/redis，就绪等待循环永远挂死。内联进本变量保证所有用点一致。
# - VIGIL_AUTH_JWT_SECRET：docker-compose.yml 对其用 ${...:?} 强校验
#   （生产部署缺失时 up 直接报错，防 crash-loop）。compose 在解析阶段对整个文件插值——
#   即使只起 postgres/redis 也会触发校验。dev 场景 vigil 容器不启动、用不到该值，
#   故注入占位符让解析通过；shell 已导出真实值时优先用真实值。
COMPOSE := COMPOSE_PROJECT_NAME=$(COMPOSE_PROJECT_NAME) VIGIL_AUTH_JWT_SECRET=$${VIGIL_AUTH_JWT_SECRET:-dev-only-placeholder} docker compose

##@ Setup

.PHONY: install-hooks
install-hooks: ## 安装 git 钩子（pre-commit 快检 + commit-msg 规范 + pre-push 门禁，各 worktree 共享）
	./scripts/install-hooks.sh

##@ Dependencies

# 确保 .env 存在
$(ENV_FILE):
	cp .env.example $(ENV_FILE)

.PHONY: dev-up dev-down
dev-up: $(ENV_FILE) ## 启动依赖服务（postgres + redis）并等待就绪
	$(COMPOSE) up -d postgres redis
	@echo "Waiting for postgres to be ready..."
	@until $(COMPOSE) exec -T postgres pg_isready -U vigil > /dev/null 2>&1; do sleep 1; done
	@echo "Waiting for redis to be ready..."
	@until $(COMPOSE) exec -T redis redis-cli ping > /dev/null 2>&1; do sleep 1; done
	@echo "✅ postgres + redis ready"

dev-down: $(ENV_FILE) ## 停止依赖服务
	$(COMPOSE) down

##@ Database

.PHONY: migrate
migrate: dev-up ## 执行数据库迁移
	go run ./cmd/vigil/ migrate

.PHONY: seed-demo
seed-demo: ## 灌演示数据（团队/用户/服务/接入点/升级策略/排班，幂等）并打印模拟告警 curl
	go run ./cmd/vigil/ seed-demo

##@ Code Generation

.PHONY: gen gen-ent gen-openapi gen-types
gen: gen-ent gen-openapi gen-types ## 一键代码生成（ent + OpenAPI + 前端 types）

gen-ent: ## 重新生成 ent 代码（改了 ent/schema/*.go 后必须执行）
	go generate ./ent/...

gen-openapi: ## 重新生成 OpenAPI spec（改了 handler 注解后必须执行）
	go generate ./cmd/vigil/...

gen-types: ## 根据最新 OpenAPI spec 生成前端 types.gen.ts
	pnpm --dir web gen:types

##@ Development

.PHONY: dev-setup dev-backend dev-frontend
dev-setup: migrate ## 一键启动：依赖服务 + 迁移 + 前端依赖
	@# 前端依赖一并装好：否则 dev-frontend 首次执行会因缺 node_modules 直接失败，
	@# 「一键起步」名不副实。pnpm install 幂等且有缓存，重复执行开销很小。
	pnpm --dir web install
	@echo ""
	@echo "✅ All services ready!"
	@echo ""
	@echo "   启动后端（终端 1）:  make dev-backend"
	@echo "   启动前端（终端 2）:  make dev-frontend"
	@echo ""
	@echo "   后端:  http://localhost:8080"
	@echo "   前端:  http://localhost:5173"
	@echo "   登录:  admin / changeme"

dev-backend: ## 启动后端（前台，自动运行迁移）
	go run ./cmd/vigil/

dev-frontend: ## 启动前端（前台，Vite dev server）
	pnpm --dir web dev

##@ Quality

.PHONY: lint lint-backend lint-frontend build build-frontend
lint: lint-backend lint-frontend ## 全量 lint（后端 golangci-lint + 前端 eslint）

lint-backend: ## 后端 lint（golangci-lint）
	$(GO_LINT)

lint-frontend: ## 前端 lint（eslint）
	pnpm --dir web lint

build: ## 后端构建
	go build ./...

build-frontend: ## 前端构建（含 tsc 类型检查）+ 同步到 internal/web/dist 供 embed
	@# pnpm build 的 postbuild 钩子（web/package.json）会自动清空旧产物并同步到
	@# internal/web/dist（保留 .gitkeep 占位）。故单独跑 `pnpm build` 即 embed-ready，
	@# `go build`/`go run` 前无需再手动 cp。
	pnpm --dir web build
	@echo "✅ frontend synced to internal/web/dist (embed ready)"

##@ Testing

.PHONY: test test-e2e test-e2e-web
test: ## 运行后端测试（默认不含 e2e，e2e 用 build tag 隔离）
	go test ./...

test-e2e: dev-up ## 运行端到端集成测试（需 docker 依赖，会自动 dev-up；基于 Ginkgo）
	@# 重置 vigil 库：atlas 版本化迁移要求目标库干净（无遗留表）。
	@# dev-up 起的 postgres 持久化卷会保留旧表，不重置会触发 atlas "脏库" 错误。
	@docker exec vigil-postgres-1 psql -U vigil -d postgres -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='vigil';" >/dev/null 2>&1 || true
	@docker exec vigil-postgres-1 psql -U vigil -d postgres -c "DROP DATABASE IF EXISTS vigil" >/dev/null 2>&1
	@docker exec vigil-postgres-1 psql -U vigil -d postgres -c "CREATE DATABASE vigil" >/dev/null 2>&1
	go test -tags=integration -timeout 5m ./test/e2e/...

test-e2e-web: ## 运行前端 Playwright e2e（Docker 全栈，禁 mock；自动起/停 compose）
	pnpm --dir web e2e

##@ Verification

.PHONY: check verify
check: lint test build build-frontend ## 提交前三道门禁（lint→test→build，对应 AGENTS.md「开发约定」/ ADR-0035）
	@echo "✅ Pre-commit checks passed (lint → test → build)"

verify: lint test build build-frontend ## main 复验（合入 main 后的最终校验，对应 AGENTS.md 闭环第 6 步）
	@echo "✅ main verification passed"

##@ Cleanup

.PHONY: clean
clean: dev-down ## 停止依赖服务并清理（容器 + 前端产物）
	rm -rf web/dist web/node_modules/.vite
