# ADR-0005： 数据访问选 ent + Atlas

| 字段 | 内容 |
|------|------|
| **状态** | Accepted |
| **日期** | 2026-07-09 |
| **相关** | [ADR-0003](0003-backend-language-go.md)、[ADR-0006](0006-primary-store-postgresql.md)、[ADR-0032](0032-migration-backup-restore.md)、[`../../ent/schema/`](../../ent/schema/)、[`../architecture.md`](../architecture.md) |

## 背景

Vigil 的实体是强关系型图结构:User-Team-Service-Incident-Schedule-EscalationPolicy 之间存在大量多对多关系,且部分字段是半结构化 JSONB(如 Event.detail)。在 Go 生态内需要一个能直观建模关系图、避免手写 SQL 运行时错误、并支持版本化迁移的数据访问方案。遵循「能在一个 Postgres 解决的不拆组件」原则(见 [ADR-0006](0006-primary-store-postgresql.md))。

## 决策

数据访问采用 **ent 作 ORM,Atlas 作版本化迁移**。

- 实体在 `ent/schema/` 用 ent 的 graph schema 建模。
- 生成强类型查询 API,避免手写 SQL 的运行时错误。
- JSONB 字段用 ent 的 JSON scalar 类型安全存取。
- **版本化迁移**由 [Atlas](https://atlasgo.io) 承载:开发期 `atlas migrate diff` 生成可 review 的 SQL 文件(`internal/schema/migrations/`),运行时 `vigil migrate` 子命令 shell out 调 `atlas migrate apply`(atlas 二进制由 Docker 镜像内置,见 `Dockerfile`)。
- 迁移文件经 `//go:embed` 嵌入二进制(ADR-0031 单二进制 embed 原则),与二进制同发同止。
- 约定:改了 `ent/schema/*.go` 后**必须**两步 —— `go generate ./ent/...` 重新生成 ent 代码,`atlas migrate diff <name> --env local` 生成新版本迁移 SQL(见 `atlas.hcl`),二者一并提交。

## 理由

- 实体是强关系型图结构,ent 的 graph schema 建模直观,天然表达多对多关系。
- 生成的强类型查询 API 把 SQL 错误从运行时提前到编译期。
- JSONB 用 ent JSON scalar 类型安全存取,兼顾半结构化数据与类型安全。
- Atlas 版本化迁移产出可 review 的 SQL 文件,满足生产「迁移可审计、可回滚到备份点」的运维诉求(回滚策略见 [ADR-0032](0032-migration-backup-restore.md))。
- ent 官方与 Atlas 同源(Ariga 出品),`ent://` schema 源原生支持,工具链闭环。

## 备选方案

- **tag-based ORM**(如基于 struct tag 的 ORM):对强关系图结构建模不直观,否决。
- **golang-migrate / goose**:可作为迁移工具,但与 ent schema 无原生联动,需要手写双份 schema 定义,易漂移。Atlas 与 ent 同源,选 Atlas。
- **ent auto-migrate**(运行时声明式 diff):简单但无版本化迁移文件,生产不可审计、不可 review。仅保留在测试场景(`enttest.Open` 自洽性检测)。

## 影响 / 权衡

- schema 变更需走两步(`go generate` + `atlas migrate diff`)增加机械操作,换来类型安全、迁移可审计、生产可 review。
- 强绑定 ent + Atlas 的建模范式,后续实体演进须遵循 ent graph schema 约定 + Atlas 迁移文件管理纪律。
- 运行环境须提供 atlas CLI(Docker 镜像已内置;裸机部署需手动安装)。

出处:tech-stack §二/§3.3。
