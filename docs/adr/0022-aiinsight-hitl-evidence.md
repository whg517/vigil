# ADR-0022: AIInsight 横向承载 + HITL + 强制 evidence

| 字段 | 内容 |
|------|------|
| **状态** | Accepted |
| **日期** | 2026-07-09 |
| **相关** | [ADR-0025](./0025-no-auto-retrain.md)、[ADR-0026](./0026-postmortem-ai-draft.md)、`ent/schema/` |

## 背景

AI 能力横向贯穿 Vigil(分诊、诊断、复盘、Copilot),若每个环节各自定义 AI 数据结构,产出会碎片化、难以统一审计与溯源。更关键的是:AI 会犯错,若让 AI 直接改变告警/事件状态,一旦误判无人负责。需要一个统一的、可信可溯源、人始终在环的承载方式。

## 决策

所有 AI 产出统一经 `AIInsight` 承载,字段:

- `Stage`:`triage` | `diagnose` | `postmortem` | `copilot`。
- `Type`:产出类型。
- `Confidence`:0.0~1.0 置信度。
- `Evidence[]`:可溯源证据。
- `Status`:状态机 `suggested → accepted / rejected / applied`。

硬约束:AI 建议须人 **accept** 才生效;**无 evidence 的建议不展示**;accept / reject 均记审计。

时间线由 `TimelineItem` 承载(`Type`;`Actor` kind = `system`|`user`|`integration`|`ai`;`Source` = `web`|`im`|`api`|`system`|`ai`),**只追加不修改**(除人工备注),以自动捕获为主,服务"实时协同"与"事后复盘"两个下游。

## 理由

- 统一结构保证 AI 产出可信、可溯源、可审计,避免碎片化。
- 强制 evidence + 人 accept,让 AI 是"加钱 SKU"的竞品形成对比——Vigil 的 AI 是可信底座而非黑箱。
- 人机协同,AI 不直接改状态,误判由 accept/reject 这道人闸兜底。

## 备选方案

- **各环节独立设计 AI 数据结构**:产出碎片化、审计口径不一、难以统一溯源,否决。
- **AI 高置信度时自动生效**:绕过人确认,误判无人负责,与 HITL 基线冲突(参见 ADR-0025)。

## 影响 / 权衡

- 每条 AI 建议都要人过一遍,牺牲了"全自动"的即时性,换来可信与可追责,这是刻意取舍。
- "无 evidence 不展示"意味着 LLM 必须能给出可引用证据,对 prompt 与产出结构提出要求。
- `TimelineItem` 只追加保证了审计完整性,但也意味着修正只能靠新增条目而非原地改写。
