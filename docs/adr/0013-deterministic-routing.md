# ADR-0013： 确定性路由裁决 + 未路由池可申诉

| 字段 | 内容 |
|------|------|
| **状态** | Accepted |
| **日期** | 2026-07-09 |
| **相关** | [`0012-triage-three-stage-pipeline.md`](./0012-triage-three-stage-pipeline.md)、[`0014-service-auto-provisioning.md`](./0014-service-auto-provisioning.md)、`internal/triage/engine.go` |

## 背景

分诊完成后要把 Incident 交给对的人。路由必须"同输入总得同一结果"——不能随机命中,否则同一告警时而找到 A 时而找到 B,责任无法追踪;同时匹配失败不能静默丢弃。

## 决策

路由以 **Service 为锚点**(`Event.labels` 匹配 `Service.labels` → 绑定 `EscalationPolicy` + `Schedule` + `Runbook`)。实现于 `engine.go` 的 `route()`,四级确定性顺序:

1. **slug 直达**:`labels["service"] == Service.slug`。
2. **多标签子集匹配**:`Event.labels ⊇ Service.labels`,值支持 glob(`path.Match`);无 labels 的 Service 不参与,避免空规则匹配一切。
3. **多命中裁决**:按匹配标签数降序;相同再按 Service ID 升序。
4. **Integration 默认归属兜底**:默认 service 停用则不回退。

未路由处理:

- 匹配失败进 `unrouted` 池(而非静默),需 `event.view_unrouted` 权限查看。
- team_admin 可 `POST /events/:id/reroute`(权限 `service.route_override`,遵守 team 软隔离)。
- `unrouted` 的 critical 有兜底通知(全员 / admin)。
- 被判噪音 / unrouted 的 Event 可见、可手动提升为 Incident。
- resolved 处理:默认自动 resolve,可配"仅提示等人确认"(更保守,避免误报 resolved 掩盖真故障)。

## 理由

- **确定性**:同输入总得同一结果,不随机命中,责任可追踪。
- **具体优先**:匹配标签数多者优先,更具体的 Service 胜出。
- **不越权 + 不漏响应 + 不误杀**:reroute 受 RBAC 与软隔离约束;失配进池可申诉而非丢弃;噪音/unrouted 仍可见可提升。

## 备选方案

- **优先级数字硬编码路由规则**:难维护且易冲突。
- **匹配失败静默丢弃**:漏响应风险,否决——改为进 unrouted 池 + critical 兜底。

## 影响 / 权衡

- 四级顺序确定但要求 Service.labels 配置得当;无 labels 的 Service 被排除,需使用者理解此约定。
- unrouted 池需人工关注与申诉入口,运营多一环;为消除大规模场景的手工建 Service 负担,引入自动供给(见 ADR-0014)。
