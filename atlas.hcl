// Vigil Atlas 配置 —— 版本化迁移（ADR-0005/ADR-0032）。
//
// 工作流（开发期生成迁移）：
//   1. 改 ent/schema/*.go
//   2. go generate ./ent/...                 （同步 ent 生成代码）
//   3. atlas migrate diff <name> --env local  （生成新版本迁移 SQL 到 internal/schema/migrations）
//   4. 提交 internal/schema/migrations/*.sql + atlas.sum
//
// 工作流（运行时 apply）：
//   - `vigil migrate` 子命令 shell out 调 `atlas migrate apply`（见 cmd/vigil/main.go）
//   - 迁移文件经 //go:embed 嵌入二进制（见 internal/schema/embed.go）
//   - Docker 镜像内置 atlas 二进制（见 Dockerfile）
//
// pgvector 扩展：本项目依赖 pgvector 类型（vector(1536)），baseline 迁移首行
//   已含 CREATE EXTENSION vector（生成后手工补，因 ent schema 不声明扩展）。

env "local" {
  // ent:// 是 ent 官方暴露给 atlas 的 schema 源（ent/schema/）。
  src = "ent://ent/schema"

  // dev-db：atlas 在此空库做 schema 比对，生成可重入的迁移 SQL。
  // 使用 pgvector 镜像确保 vector 类型可识别。
  dev = "docker://postgres/16/dev?search_path=public"

  // 迁移文件目录（atlas 格式 + atlas.sum 哈希校验）。
  // 放 internal/schema/migrations 以便 Go embed（embed 不支持 .. 路径）。
  migration {
    dir    = "file://internal/schema/migrations"
    format = atlas
  }
}
