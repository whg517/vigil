# ADR-0006: 主存储选 PostgreSQL(含 pgvector 前置)

| 字段 | 内容 |
|------|------|
| **状态** | Accepted |
| **日期** | 2026-07-09 |
| **相关** | [ADR-0005](0005-data-access-ent-atlas.md)、[ADR-0007](0007-async-tasks-asynq.md)、[ADR-0023](0023-llm-provider-cost-control.md)、[ADR-0024](0024-similar-incident-pgvector.md)、[`../architecture.md`](../architecture.md) |

## 背景

Vigil 需承载多种数据形态:强关系型实体图、半结构化告警明细(Event.detail)、分诊聚合的窗口统计、以及相似事件检索所需的文本与向量检索。遵循「不拆组件」原则——能在一个存储内解决的不引入专用组件,以降低自托管部署复杂度(见 [ADR-0002](0002-product-positioning.md))。

## 决策

主存储采用 **PostgreSQL 作为唯一底座**,在一个 Postgres 内覆盖全部需求,而非引入专用检索/向量库。

- **关系完整性**:承载强关系型实体图。
- **JSONB**:存储半结构化的 Event.detail。
- **窗口函数**:支撑分诊聚合。
- **pg_trgm / pgvector**:分别做文本 / 向量检索。

**pgvector 为硬前置**:`Incident.embedding` 使用 `vector(1536)` 类型;推荐部署镜像 `pgvector/pgvector:pg16`;当维度不符或 pgvector 不可用时降级为 LIKE 文本匹配(见 [ADR-0024](0024-similar-incident-pgvector.md))。

## 理由

- 一个 Postgres 同时满足关系、半结构化、聚合、检索四类需求,契合「不拆组件」原则,组件越少自托管越简单。
- pgvector 让向量检索复用主库,无需额外引入专用向量数据库。
- `vector(1536)` 维度对齐默认 embedding 模型 GLM `embedding-3`(见 [ADR-0023](0023-llm-provider-cost-control.md))。

## 备选方案

- **引入专用检索/向量库**(如独立向量数据库或全文检索引擎):增加组件与运维负担,与「不拆组件」原则冲突,否决。

## 影响 / 权衡

- pgvector 成为硬前置依赖,部署须使用带扩展的镜像;未装扩展或维度不符时相似检索降级为 LIKE,功能可用但精度下降。
- 单库承载全部负载,规模化后关系型负载与向量检索负载可能相互影响,属已知权衡。
- 缓存/队列/锁等易失负载不放 Postgres,由 Redis 承担(见 [ADR-0007](0007-async-tasks-asynq.md))。

出处:tech-stack §二/§3.3、deployment §1/§3.3。
