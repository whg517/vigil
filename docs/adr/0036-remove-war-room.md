# ADR-0036: 移除作战室(War Room)能力

| 字段 | 内容 |
|------|------|
| **状态** | Accepted(已执行;本文为墓碑恢复版,原全文见 git 历史 `f62ecc9`) |
| **日期** | 2026-07-10(墓碑恢复 2026-07-14) |
| **相关** | [ADR-0019](./0019-imbot-pluggable-degradation.md)、[ADR-0037](./0037-trim-deferred-features.md) |

> 本 ADR 曾随 `docs/design/` 文档层一并被整体删除(提交 `f071d4b`),致编号从 0035 跳到 0038、范围收敛决策失溯。按索引「编号单调递增、永不复用,改变决策新增 Superseded ADR 而非原地删除」的治理规则,恢复为保留决策精华的墓碑版本;实施细节以当时提交为准。

## 决策

**彻底移除作战室(Incident 自动建 IM 群、拉 responders、升级联动入群)的全部残留,不保留原语**:

- `IMBot` 接口删除 `CreateWarRoom`,各平台适配器删除实现及底层 `CreateChat` 封装;
- `Incident.war_room` 字段从 ent schema 删除,存量库以迁移显式删列(该列恒为 NULL,无数据损失);
- OpenAPI 契约与前端类型同步重生成,契约零残留。

## 动机与理由

- 作战室自设计起 live path 从未接通:`CreateWarRoom` 业务层零调用、`war_room` 字段无写入路径,属休眠代码,持续污染接口与 API 契约,文档需反复解释"已实现但未接通"。
- **「工作群 + 交互卡片 + 实时刷新」已满足协同诉求**,运行至今未出现作战室的真实需求。
- 保留原语的期权价值低于持有成本:被删的 `CreateChat` 封装仅 ~30 行,未来重启时从平台 SDK 重写成本可忽略。

## 影响 / 权衡

- [ADR-0019](./0019-imbot-pluggable-degradation.md) 的 IMBot 接口描述已同步更新;协同能力由「工作群 + 交互卡片 + 实时刷新」承载(见 [`../architecture.md`](../architecture.md) §5.6)。
- 未来若重启作战室,需先完成 IM 平台建群/群成员 API PoC 与跨平台编排设计,新增 ADR 并将本 ADR 标记 `Superseded`。
