# ADR-0005: 数据访问选 ent + Atlas

| 字段 | 内容 |
|------|------|
| **状态** | Accepted |
| **日期** | 2026-07-09 |
| **相关** | [ADR-0003](0003-backend-language-go.md)、[ADR-0006](0006-primary-store-postgresql.md)、[ADR-0032](0032-migration-backup-restore.md)、[`../../ent/schema/`](../../ent/schema/)、[`../architecture.md`](../architecture.md) |

## 背景

Vigil 的实体是强关系型图结构:User-Team-Service-Incident-Schedule-EscalationPolicy 之间存在大量多对多关系,且部分字段是半结构化 JSONB(如 Event.detail)。在 Go 生态内需要一个能直观建模关系图、避免手写 SQL 运行时错误、并支持版本化迁移的数据访问方案。遵循「能在一个 Postgres 解决的不拆组件」原则(见 [ADR-0006](0006-primary-store-postgresql.md))。

## 决策

数据访问采用 **ent 作 ORM,Atlas 作迁移**。

- 实体在 `ent/schema/` 用 ent 的 graph schema 建模。
- 生成强类型查询 API,避免手写 SQL 的运行时错误。
- JSONB 字段用 ent 的 JSON scalar 类型安全存取。
- Atlas 自动生成迁移,满足版本化迁移需求。
- 约定:改了 `ent/schema/*.go` 后**必须** `go generate ./ent/...` 并把生成代码一起提交。

## 理由

- 实体是强关系型图结构,ent 的 graph schema 建模直观,天然表达多对多关系。
- 生成的强类型查询 API 把 SQL 错误从运行时提前到编译期。
- JSONB 用 ent JSON scalar 类型安全存取,兼顾半结构化数据与类型安全。
- Atlas 自动生成迁移,配合声明式 schema 演进(回滚策略见 [ADR-0032](0032-migration-backup-restore.md))。

## 备选方案

- **tag-based ORM**(如基于 struct tag 的 ORM):对强关系图结构建模不直观,否决。
- **golang-migrate / goose**:作为迁移工具被 Atlas 自动生成迁移取代。

## 影响 / 权衡

- schema 变更需走 `go generate ./ent/...` 生成代码并提交,增加一步机械操作,但换来类型安全与迁移自动化。
- 强绑定 ent 的建模范式,后续实体演进须遵循 ent graph schema 约定。

出处:tech-stack §二/§3.3。
