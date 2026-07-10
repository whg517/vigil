# 0001. 移除作战室(War Room)

- 状态:已实现
- 关联:[ADR-0036](../adr/0036-remove-war-room.md)、原 [`backlog.md`](../backlog.md) §1.1(随本设计移除)

## 目标与非目标

**目标**:彻底移除作战室能力的全部残留代码与数据模型,消除"接口存在但 live path 未接"的休眠状态(评审确认:`CreateWarRoom` 业务层零调用、`Incident.war_room` 无写入路径)。

**非目标**:不影响既有 IM 协同主路径(卡片下发/更新、回调鉴权、斜杠命令、@人拉人)——这些与作战室无关,原样保留。

## 移除清单

### 数据模型
- `ent/schema/incident.go`:删除 `war_room` JSON 字段 → `go generate ./ent/...`。
- `internal/migrate/schema/baseline.sql`:`go run ./cmd/genmigration` 重新生成(新装库不再建该列)。
- 新增 post-migrate 迁移 `0003_drop_war_room.sql`:`ALTER TABLE incidents DROP COLUMN IF EXISTS war_room`(存量库删列;ent auto-migrate 不删列,须显式迁移;`IF EXISTS` 保证新装库幂等)。

### 后端
- `internal/im/bot.go`:`IMBot` 接口删除 `CreateWarRoom` 方法。
- `internal/im/noop.go` / `dingtalk/adapter.go` / `feishu/adapter.go`:删除各自实现。
- `internal/im/dingtalk/client.go` / `feishu/client.go`:删除 `CreateChat`(唯一调用方是 `CreateWarRoom`)。
- 相关测试:`dingtalk/adapter_test.go`、`im/handler_test.go` 中的作战室桩与断言。

### API 契约与前端
- `go generate ./cmd/vigil/...` 重生成 OpenAPI spec(`ent.Incident` 不再含 `war_room`)。
- `web/src/lib/types.ts`:删除 `Incident` 类型的 `war_room` 引用;`pnpm --dir web gen:types` 重生成。
- 前端页面无作战室使用(已核实),无 UI 改动。

### 文档
- `backlog.md`:删除 §1.1(不再是"暂不做",而是"已移除")。
- `architecture.md` §5.6:删除作战室句,改为指向 ADR-0036。
- ADR-0019:接口清单去掉 `CreateWarRoom`。
- ADR-0034 / ADR-0008:清理作战室字样的顺带提及。

## 边界与失败处理

- 存量库 `war_room` 列数据:该列从未有写入路径,列内容恒为 NULL,`DROP COLUMN` 无数据损失。
- 若未来重启作战室:按本目录流程重新设计,IM 平台建群 API 直接从各平台 SDK 接入,不受本次删除约束(删除的 `CreateChat` 仅 ~30 行封装)。

## 测试要点

- `go test ./internal/im/...` 全绿(删桩后无悬空引用)。
- e2e(`make test-e2e`)全绿:涉及 Incident schema 变更,验证核心流水线不受影响。
- OpenAPI spec 与前端 types 无 `war_room` 残留(CI drift 检测)。
