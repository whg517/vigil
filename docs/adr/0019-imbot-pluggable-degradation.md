# ADR-0019: IMBot 可插拔 + 平台能力降级矩阵

| 字段 | 内容 |
|------|------|
| **状态** | Accepted |
| **日期** | 2026-07-09 |
| **相关** | [`0018-im-same-rbac-as-web.md`](./0018-im-same-rbac-as-web.md)、[`0009-pluggable-integrations.md`](./0009-pluggable-integrations.md)、[`../architecture.md`](../architecture.md) |

## 背景

Vigil 把本土 IM(飞书/钉钉)当协同工作面而非通知通道,但各平台卡片/建群/回调能力差异大,且开放能力受限。业务层不应被任一平台的具体差异绑死,某平台能力缺失也不能导致告警静默丢失。

## 决策

抽象 `IMBot` 接口(`SendCard / UpdateCard / ParseCallback` 等)封装平台差异,业务层不感知具体平台。

- **平台矩阵**:飞书 P0 / 钉钉 P0,均真实接入。平台能力缺失或不可用时不静默丢告警,降级走 notification 兜底链。
- **状态双向同步**:IM → Web 经 WebSocket 实时刷新;Web → IM 由领域事件驱动 `card_refresher` 更新卡片。
- **关键降级(降级矩阵)**:
  - 钉钉 `sampleActionCard` 无法原地刷新 → `UpdateCard` 解出 cardID 编码的 channel,重发一条带状态徽章的新消息(**B16**)。
  - `CardStore` 从进程内存改为 Redis 持久化(7 天 TTL,进程重启后仍可刷新)(**B24**)。
  - 值班群未配置时记 metric + Warn,不静默(**B17**)。

## 理由

- 接口抽象使业务层不被任何单一 IM 平台绑死,新增平台只需实现接口。
- `Available()` 门控 + 降级到 notification 兜底链,保证平台能力缺失时告警仍送达。
- 降级矩阵把平台能力差异显式化,而非在业务代码里散落 if-else。

## 备选方案

- **只支持一个 IM 平台**:被单一生态绑死,不符合本土多 IM 定位——否决。
- **平台差异散落业务层**:每处调用都要判平台,不可维护——否决,改由 IMBot 接口 + 降级矩阵收拢。

## 影响 / 权衡

- 企微支持已随 [ADR-0037](./0037-trim-deferred-features.md) 移除(含 `NoopBot` 占位),平台矩阵收敛为飞书 + 钉钉。作战室能力已整体移除(含建群原语),见 [ADR-0036](./0036-remove-war-room.md)。
- 钉钉靠"重发带徽章的新消息"模拟原地刷新(B16),会在群里留下多条消息,是平台限制下的取舍。
- CardStore 依赖 Redis 持久化(B24),Redis 不可用则卡片刷新能力退化。
