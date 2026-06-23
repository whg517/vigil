// Package e2e 提供 Vigil 的端到端集成测试基础设施。
//
// 与 internal/ 下各包的单元测试（sqlite + miniredis mock）不同，本包用真实依赖：
//   - PostgreSQL（含 pgvector 扩展，由 docker-compose 起）
//   - Redis（Asynq 队列的真实后端）
//   - 完整的 app.Bootstrap 装配（所有引擎/handler/路由）
//   - 真实 Asynq worker（消费归一化/分诊/升级任务）
//
// 覆盖单元测试无法触及的盲点：完整流水线时序（ingest→triage→escalate）、
// pgvector 相似检索、Asynq 延迟任务调度、鉴权三轨切换、HTTP 端到端契约。
//
// 注意：包名 e2e，目录 internal/e2e，避免与已有的业务包 internal/integration
// （能力域 1 接入点管理 handler）同名冲突。
//
// # Build tag 隔离
//
// 所有测试文件带 //go:build integration 标记，不参与默认 `go test ./...`
// （保持单测秒级回归）。需显式启用：
//
//	go test -tags=integration ./internal/e2e/...
//
// 或用 make test-e2e（会先起 docker-compose 依赖）。
//
// # 前置依赖
//
// 需要先启动 PG + Redis（make dev-up）。检测不到时测试自动 t.Skip，
// 避免在无依赖环境（如纯单测 CI job）误红。
//
// # 数据隔离
//
// 每个测试调用 fixture.ResetDB 清空所有表（TRUNCATE ... RESTART IDENTITY CASCADE），
// 保证测试间数据独立。schema 在 Setup 时通过 migrate.Run 幂等建好。
package e2e
