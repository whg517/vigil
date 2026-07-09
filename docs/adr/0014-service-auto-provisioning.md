# ADR-0014: Service 自动供给(方案 C)

| 字段 | 内容 |
|------|------|
| **状态** | Accepted |
| **日期** | 2026-07-09 |
| **相关** | [`0013-deterministic-routing.md`](./0013-deterministic-routing.md)、[`0016-escalation-asynq-delayed.md`](./0016-escalation-asynq-delayed.md)、[`0028-single-org-soft-isolation.md`](./0028-single-org-soft-isolation.md)、`internal/servicesync`、`ent/schema/service.go` |

## 背景

100+ 微服务场景下,要求为每个服务 1:1 手工建 Service 不现实:新服务的告警首次进来时若无对应 Service,只能落入 unrouted 池,响应被延误。需要一种既能自动接住新服务、又不牺牲安全的机制。

## 决策

在 `route()` 未命中、进入 unrouted 之前插入懒供给:若开启 `auto_provision_enabled`(**默认关闭**),且满足**全部**条件:

- 服务键 label 通过 **slug 白名单正则**;
- 能解析归属团队;
- 该团队已配 `default_escalation_policy_id`;

则创建轻量 `source=auto` Service(记 `provisioned_at`),并继续走聚合建单。

配套机制:

- **主动同步**(`internal/servicesync`,push 模型,file / http 源):周期从外部源 upsert 服务目录。
- **过期清理 Pruner**:`source=auto` 且 N 天无 Event 的服务自动 `disable`。

方案 P1 懒供给 / P2 主动同步 / P3 过期清理均已在主线实现。

## 理由

- 让新服务告警首次进来即被接住,不落 unrouted。
- 把人工配置量从"N 个微服务各配一次"降到"M 个团队各配一次默认策略"。
- 对标 PagerDuty / Opsgenie 的 IaC / 目录同步 / 动态路由能力。

## 备选方案

- **要求 1:1 手工建 Service**:大规模微服务下配置量爆炸,新服务首告警必落 unrouted,否决。

## 影响 / 权衡

安全底线(设计的核心约束):

- **默认关闭**:无配置则行为不回归,零风险引入。
- **无默认策略则不创建**:否则会变成"已路由但静默",比 unrouted 更危险。
- **critical 仍走 unrouted 兜底**:关键告警不因自动供给失败而丢。
- **slug 唯一约束**:保证并发下只建一个 Service。
- **绝不触碰 `source=manual`**:自动机制只管自己创建的 `auto` 服务,不改人工配置。
