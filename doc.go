// Package vigil 是 Vigil（守夜人）告警处置平台的模块根占位包。
//
// 本文件不含任何业务逻辑，唯一目的是让仓库根目录存在一个可被 `go list` 识别的
// Go 包，从而让 OpenAPI 生成器 swag 能在 `-d ../..`（从根递归扫描）模式下正常
// 解析模块导入路径。缺少它时，swag 的 `go list .` 在无 .go 文件的根目录报错，
// 进而无法把 `httputil.ErrorResponse` 等 internal 具名类型映射到 schema，
// 导致 `go generate ./cmd/vigil/...` 整体失败。
//
// 实际可执行入口在 cmd/vigil；业务代码在 internal/*、ent/*。
package vigil
