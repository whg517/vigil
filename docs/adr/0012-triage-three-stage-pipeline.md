# ADR-0012: 三层分诊管线 —— 去重 / 抑制 / 聚合

| 字段 | 内容 |
|------|------|
| **状态** | Accepted |
| **日期** | 2026-07-09 |
| **相关** | [`0010-event-incident-separation.md`](./0010-event-incident-separation.md)、[`0013-deterministic-routing.md`](./0013-deterministic-routing.md)、`internal/triage/engine.go` |

## 背景

告警疲劳是 oncall 头号敌人:重复告警、维护窗口内的预期告警、同一故障的关联告警若逐条创建 Incident 会淹没值班人。降噪是 oncall 的核心价值。

## 决策

固定顺序 **去重 → 抑制 → 相关性聚合 → Incident**,实现于 `internal/triage/engine.go`。

- **去重**:Redis `SET vigil:dedup:{dedup_key} event_id EX <窗口>`(SETNX + 过期窗口)。默认 5min,可配 `VIGIL_TRIAGE_DEDUP_WINDOW`。resolved Event 不丢弃,而是关联同 `DedupKey` 的 firing 以触发解决。
- **抑制**(`internal/triage/suppression.go`、`ent/schema/suppression_rule.go`):`SuppressionRule.kind` 枚举 `adhoc`(日常降噪)/ `maintenance`(计划维护窗口,含 `time_window` RFC3339 + `expires_at` 自动软失效)。两类走同一 `matchRule`(label 全等 + time_window + severity_filter),`kind` 只是分类标签,供前端维护窗口专属入口。动作 `suppress`(标 `IsNoise=true` 仅留痕)或 `reduce_severity`。**守卫 `preserve_critical` 默认生效**:critical 不被抑制,避免维护期误杀真故障。
- **聚合**:聚合键默认 `service + severity`(+ 可选 label),默认 5min 窗口。窗口内同键并入活跃 Incident(状态 ∈ {`triggered`, `acked`}),否则按 `Service.auto_create_incident` 创建。用 PostgreSQL 窗口函数实现。

## 理由

- 降噪是 oncall 核心价值,直接减少告警疲劳。
- 抑制两类语义不同但匹配逻辑统一,避免代码分叉。
- `expires_at` 软失效让维护窗口用完即失效,无需额外定时清理任务。

## 备选方案

- **单一去重**:不足以覆盖维护窗口与关联聚合,否决。
- **ML 聚类做相关性**:初期过重,后置。
- **维护窗口单独建实体**:会重复一套匹配逻辑,改用 `kind` 分类标签复用 `matchRule`,否决。

## 影响 / 权衡

- 固定管线顺序简单可预测,但相关性仅做规则聚合,复杂关联场景暂靠人工提升,ML 聚类留待后续。
- `preserve_critical` 默认守卫牺牲了"维护期彻底静默"的可能,换取"绝不误杀真故障"的安全底线。
