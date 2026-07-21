# Vigil Dockerfile —— 多阶段构建（前端 + 后端 → 单运行镜像）。
# 对应 ADR-0031（单二进制 embed + Compose/Helm）：单二进制 + 前端静态资源。
# 运行时 `vigil migrate` 子命令 shell out 调 atlas CLI（ADR-0005 版本化迁移）。

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
# 安装 atlas CLI：供 `vigil migrate` 子命令 shell out apply 版本化迁移。
# 用官方 arigaio/atlas 镜像 COPY 二进制（版本锁定，避免运行时联网下载）。
# tag 格式注意：docker hub 是 1.2.x（无 v 前缀），GitHub releases 才是 v0.x.x——别混淆。
COPY --from=arigaio/atlas:1.2.3 /atlas /usr/local/bin/atlas
# SEC-05：以非 root 用户运行容器（最小权限原则）。
# 创建 vigil 用户/组（固定 UID/GID 65532，与 distroless 常见值对齐便于迁移）。
RUN addgroup -g 65532 -S vigil && adduser -u 65532 -S vigil -G vigil
WORKDIR /app
COPY --from=go-builder /vigil /app/vigil
# 二进制归属 root，仅运行权限给 vigil（防容器内篡改自身二进制）。
RUN chown root:root /app/vigil /usr/local/bin/atlas && chmod 0555 /app/vigil /usr/local/bin/atlas
USER 65532
EXPOSE 8080
ENTRYPOINT ["/app/vigil"]
CMD []
