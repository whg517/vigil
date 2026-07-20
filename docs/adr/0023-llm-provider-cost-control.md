# ADR-0023： LLMProvider 抽象 + 成本三闸 + 置信度阈值 + 可降级

| 字段 | 内容 |
|------|------|
| **状态** | Accepted |
| **日期** | 2026-07-09 |
| **相关** | [ADR-0006](./0006-primary-store-postgresql.md)、[ADR-0024](./0024-similar-incident-pgvector.md) |

## 背景

Vigil 的 AI 能力横向贯穿,但落地要解决三个现实问题:① 不被单一 LLM 厂商绑死,且要兼顾"数据不出境"的隐私场景;② LLM 调用有真实成本,不能失控;③ AI 是增强而非核心链路,LLM 挂了不能拖垮告警主流程。

## 决策

定义 `LLMProvider` 接口(`Complete` / `Embed`)。`VIGIL_LLM_PROVIDER` 选 `glm`(智谱 GLM,默认,中文优先)或 `ollama`(本地,数据不出境),未知值回退 `glm`。`wire.go` 的 `buildLLMProvider` 在外层统一包 `CostController`。Ollama 走原生 HTTP 契约(`/api/chat`、`/api/embeddings`),`Available()` 只看 `base_url`,不在构造期探测。

**成本三闸**:`CostController` 包装底层 Provider,`Complete` 按顺序过三道闸——缓存(Redis,`sha256(prompt)`,默认 1h TTL)→ 限流(Redis ZSET 滑动窗)→ 配额(token counter)。无 Redis 时三闸全部降级跳过、透传。`Embed` 走限流但不缓存。

**置信度阈值**:`VIGIL_LLM_CONFIDENCE_THRESHOLD` 默认 `0.6`,低于阈值不产出;Setter 对 `<=0` 保留 0.6,防误配。

**可降级**:LLM 失败则 AI 功能降级(不展示建议),不影响告警主流程;响应慢用异步任务承载,不阻塞分诊;误判由 HITL 兜底。

**⚠️ embed 维度须与 pgvector 列匹配**:`Incident.embedding` 是 `vector(1536)`,对齐 GLM `embedding-3`;Ollama 默认 `nomic-embed-text` 是 768 维,切换前须改列维度,否则接受降级 LIKE(见 ADR-0024)。

## 理由

- 接口抽象让业务层不感知具体 Provider,隐私场景可本地 Ollama 兜底。
- 成本三闸把 LLM 开销控制在缓存/限流/配额三层内,避免调用失控。
- 置信度阈值避免低质量建议拉低 AI 整体可信度。
- AI 是增强非核心链路,必须可降级,保证告警主流程稳定。

## 备选方案

- **直接耦合单一 LLM SDK**:被厂商绑死,无法满足数据不出境的本地部署,否决。
- **不设成本闸**:LLM 调用量与 token 消耗失控,自托管用户不可接受。
- **无置信度阈值全量产出**:低置信度噪声建议会侵蚀用户对 AI 的信任。

## 影响 / 权衡

- 切换 Provider(尤其 GLM↔Ollama)存在 embed 维度陷阱,须显式处理列维度或接受 LIKE 降级,这是已知限制。
- 三闸依赖 Redis,无 Redis 时降级为无成本保护的透传,单机极简部署下需自行注意用量。
- Provider 抽象与 CostController 包装增加了一层间接,换取可插拔与成本可控。
