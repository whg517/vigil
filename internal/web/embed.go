// Package web 嵌入前端构建产物，供 HTTP server 作为静态资源 + SPA serve。
//
// dist/ 是前端构建产物（由 web/ 下 pnpm build 生成，经构建脚本复制到本目录），
// 不入库（仅 dist/.gitkeep 占位入库），使「未构建前端」时本地 go build / CI backend
// job 仍能编译（embed 至少匹配到占位文件）。Dockerfile 构建阶段会用真实 dist 覆盖。
//
// embed 路径约束：//go:embed 禁止 .. 跨目录，故 dist 必须位于本包目录下，
// 由构建流程从 web/dist 复制到 internal/web/dist。
package web

import "embed"

// DistFS 前端构建产物。embed 根为 "dist/"，调用方需 fs.Sub(DistFS, "dist") 切到 dist 根。
//
// all: 前缀确保包含以下划线或点开头的文件（默认会被 go:embed 排除）。
//
//go:embed all:dist
var DistFS embed.FS
