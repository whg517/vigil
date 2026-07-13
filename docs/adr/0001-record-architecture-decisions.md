# ADR-0001: 采用 ADR 记录架构决策

| 字段 | 内容 |
|------|------|
| **状态** | Accepted |
| **日期** | 2026-07-09 |
| **相关** | 全部 ADR、[`../architecture.md`](../architecture.md) |

## 背景

Vigil 是 docs-driven 项目:设计性改动先落文档再写代码。早期文档以「文档类型」组织(PRD、data-model、tech-stack、10 份 capabilities、user-journeys 等 20+ 份),问题逐渐显现:

- **决策与叙事混杂**:一个关键决策(如"排班不存快照")的"选了什么 / 为什么 / 否决了什么"散落在多份文档,难以定位、易失同步。
- **重复与漂移**:同一事实在 PRD、data-model、architecture 各写一遍,改一处忘改他处。
- **难以追溯**:看到一段代码想知道"当初为什么这么定",没有单一入口。

## 决策

文档以两部分为主干:

1. **[`architecture.md`](../architecture.md)** —— 唯一的架构主文档:产品定位、组件结构、数据流、引擎、横切关注点的**全景视图**。回答"系统长什么样、怎么运转"。
2. **`docs/adr/`** —— 一决策一文件的**架构决策记录(ADR)**。回答"为什么这么定、否决了什么"。

主文档只做全景与索引,**不重复决策理由**;理由沉淀在对应 ADR,主文档以链接引用。实体字段级真相以 `ent/schema/` 为准,权限点以 `internal/auth/permission.go` 为准,ADR 不复制这些代码即真相的清单,只记录其背后的设计取舍。

主干之外,保留少量**活文档**承载 ADR 不擅长的"面向未来的动作"(ADR 是决策记录,不是需求清单、功能 spec 或操作手册):

| 活文档 | 回答什么 | 与 ADR 的关系 |
|--------|---------|--------------|
| [`operations.md`](../operations.md) | 部署/升级/备份/排查的可执行步骤 | 每步背后的"为什么"引用 ADR |

> 已确认未修的缺陷记入 `known-issues.md`(按需创建;清单归零即删除文件,历史靠 git 追溯——2026-07-10 首轮 4 项已全部修复并删档)。

## ADR 格式

每份 ADR 用统一模板:

```markdown
# ADR-NNNN: <一句话决策标题>

| 字段 | 内容 |
|------|------|
| **状态** | Accepted / Proposed / Deferred / Superseded by ADR-XXXX |
| **日期** | YYYY-MM-DD |
| **相关** | 关联 ADR、ent/schema、internal 模块 |

## 背景
促成该决策的问题、约束、上下文。

## 决策
选了什么(含关键技术细节:接口、幂等键、env 变量、状态机等)。

## 理由
为什么这么选。

## 备选方案
考虑过、否决的方案及否决原因。

## 影响 / 权衡
带来的正/负面后果、已知限制。
```

**状态取值**:`Accepted`(已采纳,含已实现与设计已定)、`Accepted(部分实现)`(方向已定但落地未完整)、`Proposed`(提议中)、`Deferred`(暂缓)、`Superseded by ADR-XXXX`(被取代)。

## 理由

- ADR 是业界成熟实践(Michael Nygard 提出),轻量、贴近代码、可随代码演进。
- 一决策一文件,天然抗漂移:改主意时新增一份 `Superseded` 关系的 ADR,而非原地改写,保留决策演进史。
- 编号单调递增,永不复用,便于交叉引用。

## 影响 / 权衡

- 删除了原有 PRD / data-model / tech-stack / capabilities / user-journeys 等文档,其**决策**迁移进 ADR、**全景**迁移进 architecture.md、**实体/权限清单**归位到 `ent/schema/` 与 `internal/auth/permission.go`(代码即真相)。
- 开发流程、部署运维的**可执行速查**保留在 [`../../AGENTS.md`](../../AGENTS.md) 与 `Makefile`;其背后的**决策**(为何 worktree 闭环、为何禁 chore、为何备份即回滚)沉淀为 ADR。
- 新增设计决策时**必须**新增一份 ADR,并在需要时更新 architecture.md 的决策索引表。
