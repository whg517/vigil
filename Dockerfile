# Vigil Dockerfile —— 多阶段构建（前端 + 后端 → 单运行镜像）。
# 对应 tech-stack.md §部署：单二进制 + 前端静态资源。

# ===== Stage 1: 前端构建 =====
FROM node:22-alpine AS web-builder
WORKDIR /web
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
# CI=true：让 pnpm 在非 TTY 环境自动确认 modules 目录重建（避免 ERR_PNPM_ABORTED）
ENV CI=true
RUN pnpm build

# ===== Stage 2: 后端构建 =====
FROM golang:1.25-alpine AS go-builder
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# 前端产物嵌入（internal/web 包用 //go:embed all:dist 捕获此目录）
COPY --from=web-builder /web/dist ./internal/web/dist
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /vigil ./cmd/vigil

# ===== Stage 3: 运行 =====
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=go-builder /vigil /app/vigil
EXPOSE 8080
ENTRYPOINT ["/app/vigil"]
CMD []
