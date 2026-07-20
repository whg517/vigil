# ADR-0024： 相似事件检索 pgvector 主路径 + LIKE 降级

| 字段 | 内容 |
|------|------|
| **状态** | Accepted |
| **日期** | 2026-07-09 |
| **相关** | [ADR-0023](./0023-llm-provider-cost-control.md)、[ADR-0026](./0026-postmortem-ai-draft.md)、`internal/ai/diagnose.go` |

## 背景

处置一个事件时,最有价值的参考是"历史上类似的事件当时是怎么处理的"。要复用历史处置经验,需要一套相似事件检索。但检索能力不能成为硬依赖——AI 未配置(无扩展/无 key/测试环境用 sqlite)时,基础功能仍要可用。

## 决策

`FindSimilar` 主路径基于 pgvector,实现于 `internal/ai/diagnose.go`:

- `Incident.embedding` 为空时**懒计算**——用 LLM `Embed`(标题 + 摘要)生成向量,回写并持久化。
- 用 raw SQL 按余弦距离 `<=>` 排序取最相似的 Incident。
- pgvector 或 Embed 不可用时(无扩展 / 无 key / sqlite 测试)**降级为 LIKE 文本匹配**。

相似 Incident 连同其复盘一起呈现,反哺复盘知识库。

## 理由

- 复用历史处置经验是诊断的核心价值,向量检索比纯文本匹配更能捕捉语义相似。
- 懒计算避免为所有历史 Incident 预先算 embedding,按需生成并持久化,兼顾成本与命中后的复用。
- LIKE 降级保证 AI 未配置时相似检索仍可用,不把 AI 变成硬前置。
- 相似事件带出其复盘,形成"检索 → 复盘知识 → 反哺检索"的正循环(见 ADR-0026)。

## 备选方案

- **引入专用向量数据库**:违背"能在一个 Postgres 内解决就不拆组件"的原则,增加自托管复杂度,否决(参见 ADR-0006)。
- **只做 LIKE 文本匹配**:无法捕捉语义相似,检索质量差。
- **强制 pgvector 为硬依赖**:AI 未配置或测试环境即不可用,违背"可降级"基线。

## 影响 / 权衡

- 主路径依赖 pgvector 扩展与 LLM Embed,embedding 维度须与 `Incident.embedding` 列(`vector(1536)`)一致,切换 Provider 时须注意(见 ADR-0023)。
- 降级到 LIKE 时检索质量下降,但保证可用性,这是刻意的兜底取舍。
- 懒计算首次检索会触发 Embed 调用,有一次性延迟,后续命中缓存/持久化后消除。
