# ADR-0036: 移除作战室(War Room)能力

| 字段 | 内容 |
|------|------|
| **状态** | Accepted |
| **日期** | 2026-07-10 |
| **相关** | [ADR-0019](./0019-imbot-pluggable-degradation.md)、[design/0001](../design/0001-remove-war-room.md)、原 backlog §1.1 |

## 背景

作战室(Incident 触发自动建 IM 群、拉 responders、升级联动入群、归档关联复盘)自设计起就处于半悬置状态:飞书/钉钉的 `CreateChat` 建群原语已实现,但 **live path 从未接通**——Incident 事件链不调用 `CreateWarRoom`,`Incident.war_room` 字段无任何写入路径。backlog 曾将其记录为"暂不做、原语保留",理由是跨平台编排成本高、平台 API 能力参差需 PoC。

休眠代码的持续成本:`IMBot` 接口带着一个永不被调的方法,每个新平台适配器都被迫实现它;`Incident` 数据模型带着一个恒为 NULL 的字段进入 API 契约与前端类型;文档需反复解释"已实现但未接通"。

## 决策

**彻底移除作战室的全部残留**,不再保留原语:

- `IMBot` 接口删除 `CreateWarRoom`,各平台适配器删除实现及底层 `CreateChat` 封装;
- `Incident.war_room` 字段从 ent schema 删除,存量库以 post-migrate 迁移显式删列;
- OpenAPI 契约与前端类型同步重生成;
- backlog §1.1 移除。

## 理由

- **「工作群 + 交互卡片 + 实时刷新」已满足协同诉求**——这是原推迟决定中已验证的替代方案,运行至今未出现作战室的真实需求。
- **保留原语的期权价值低于持有成本**:被删除的 `CreateChat` 封装仅 ~30 行,未来重启时从平台 SDK 重写的成本可忽略;而接口污染与"文档需解释休眠态"的成本持续存在。
- 与简化方向一致:能力边界只保留真实走通的路径。

## 备选方案

- **维持现状(原语保留 + backlog 挂起)**:否决——评审已确认这造成过文档误述("作战室自动建群拉人"被写成已实现),休眠代码是漂移之源。
- **接通 live path(把功能做完)**:否决——需先完成 IM 平台建群/群成员 API PoC 与跨平台编排设计,成本高且无真实需求牵引。

## 影响 / 权衡

- 未来若重启作战室,按 [`docs/design/`](../design/) 流程重新设计,平台建群能力从各 SDK 直接接入;本 ADR 届时标记 `Superseded`。
- 存量库 `war_room` 列恒为 NULL,删列无数据损失。
- [ADR-0019](./0019-imbot-pluggable-degradation.md) 的 IMBot 接口描述同步更新。
