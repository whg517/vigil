# 用户故事（User Stories）

> 本目录以**角色第一视角**描述 Vigil 的真实使用场景与可验证的验收标准，是 [`../requirements.md`](../requirements.md) 的场景化补充。

## 目录定位与追溯

- **需求追溯**：每条故事标注 FR/NFR 需求域（如 FR-TRI、NFR-DEP），编号定义与条目见 [`../requirements.md`](../requirements.md)，两者互相追溯、不互相复制。
- **决策理由**：故事只讲"要什么、怎么验收"；"为什么这么定"见对应 [ADR](../adr/README.md)，系统全景见 [`../architecture.md`](../architecture.md)。
- **事实基准**：实体字段以 [`ent/schema/`](../../ent/schema/) 为准，权限点以 [`internal/auth/permission.go`](../../internal/auth/permission.go) 为准；文中字段与权限点名仅为举例引用。
- **状态约定**：本目录为活文档，随实现演进；明确标注**「规划中」**的故事尚未实现，不构成能力承诺，落地前须按 [ADR-0001](../adr/0001-record-architecture-decisions.md) 先立 ADR。

## 角色一览

| 角色 | 文件 | 故事数 | 关注焦点 |
|------|------|--------|----------|
| 运维主管（Ops Lead） | [`ops-lead.md`](./ops-lead.md) | 10 | 告警降噪、升级接力、排班公平、审计与经营数据——让团队"接得住、有下文" |
| 架构师（Architect） | [`architect.md`](./architect.md) | 10 | 选型评估、部署运维成本、可扩展性与安全边界——平台本身值不值得引入 |
| 项目经理（PM） | [`project-manager.md`](./project-manager.md) | 9 | 只读跟踪、跨团队协同、复盘改进项闭环与管理层报表——干系人视角的可见性 |
| 开发人员（Developer） | [`developer.md`](./developer.md) | 9 | oncall 处置体验：IM 一键确认、一屏上下文、Runbook 诊断与告警自助接入 |

> **关于「纯一线 oncall 工程师」画像**：开发人员（developer.md）已覆盖轮值 oncall 的处置体验故事（US-DEV-01 IM 一键确认、US-DEV-02 一屏上下文、US-DEV-03 Runbook 诊断等），不单独立档。若你只关心一线处置视角，直接读 developer.md 的 US-DEV-01～US-DEV-07 即可。

## 编号规范

故事编号格式：`US-<角色前缀>-<两位序号>`，前缀与角色一一对应：

| 前缀 | 角色 | 示例 |
|------|------|------|
| `US-OPS` | 运维主管 | US-OPS-01 |
| `US-ARC` | 架构师 | US-ARC-03 |
| `US-PM` | 项目经理 | US-PM-05 |
| `US-DEV` | 开发人员 | US-DEV-02 |

编号在角色内单调递增、只增不改义；故事废弃时保留编号并标注，不复用。

## 优先级定义

| 级别 | 含义 |
|------|------|
| **P0** | 核心闭环，缺了角色的关键场景走不通；必须已实现且有验收保障 |
| **P1** | 显著提升效率或体验的重要能力，随核心版本交付 |
| **P2** | 增强项或规划项，可延后，部分标注「规划中」 |
