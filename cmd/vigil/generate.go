package main

// OpenAPI spec 生成指令。
//
// 镜像 ent/generate.go 的 `go run -mod=mod` 纪律：
//   - --v3.1            生成 OpenAPI 3.1（内容为 openapi: 3.1.0，文件名仍为 swagger.yaml/json）
//   - -d ../..          从仓库根递归扫描，覆盖各 internal/* handler（filepath.Walk 递归）
//   - -g cmd/vigil/main.go  指定全局信息文件（@title/@servers/@securitydefinitions 等）
//   - --parseDependency 解析外部依赖（ent 实体），让 @Success {object} ent.Incident 真正生成 schema
//   - --parseInternal   解析 internal/* 包（httputil.DTO、各域 Result 类型）
//   - --output          指向 internal/server/gen，与 //go:embed 同包，零跨目录拷贝
//
// 第二步 dedupe-swag-enum.py（确定性后处理）：
//   Echo v5 的 binder_generic 引用 time.Second 等常量，触发 swag 重复收集标准库 time 包
//   常量到 time.Duration 的 enum（8 值变 16）；又因 swag 内部 map 迭代序不确定，每次产物顺序
//   不同，CI 漂移门随机失败。swag rc5 无关闭开关，故在生成后做稳定去重（保留各元素首次出现）。
//
// 改 handler 注解后必须重新执行 `go generate ./cmd/vigil/...` 并提交 internal/server/gen。
// CI 门禁（.github/workflows/ci.yml 后端 job）会校验生成产物无漂移。
//
//go:generate go run -mod=mod github.com/swaggo/swag/v2/cmd/swag init --v3.1 -d ../.. -g cmd/vigil/main.go --parseDependency --parseInternal --output ../../internal/server/gen
//go:generate python3 ../../scripts/dedupe-swag-enum.py ../../internal/server/gen
