# 用户旅程文档 × 源码全面核对结论（2026-07-03）

> **用途**：`docs/user-journeys.md` v0.3 扩写过程中，对文档全部断言逐项对照源码核实的**实现状态清单**，
> 供后续**修复排期**与**测试用例编写**引用。核对基线：2026-07-03 代码（`internal/` / `ent/schema/` / `web/src/` / `deploy/`）。
>
> 分四组：**安全类**（越权/旁路/留痕缺失）、**断链类**（代码在但环节未接通/不生效）、
> **纠偏类**（文档断言与代码相反，已在 user-journeys.md 就地纠偏）、**缺失端点类**（设计有、路由全集无）。
> 「章节」列指向 `user-journeys.md` 中的详细描述。

---

## 一、安全类

| # | 结论 | 依据（代码） | 章节 |
|---|------|------|------|
| S1 | 🚨 **Helm chart 不设 `VIGIL_APP_ENV`** → K8s 默认 development：① `POST /api/v1/__test__/reset` 被挂载（public 无鉴权，TRUNCATE 全库+清队列）；② `X-Vigil-User-ID` 头回退启用 = 鉴权旁路（`authEnabled=true` 挡不住）。唯一修法手改模板 | `deploy/helm/templates/deployment.yaml` env 全集、`server/testreset.go`、`auth/resolver.go` headerFallback | A.3 |
| S2 | `GET /users`、`PATCH /users/:id`（含改他人 status=disabled）**未登记权限点**——任何登录用户可禁用/修改他人；`GET /roles`、`GET /role-bindings` 同仅登录态 | `wire.go` registerSensitiveRoutePerms 未覆盖 | B.1.1 |
| S3 | 禁用用户的 **refresh token 仍可换 access token**（refresh 不查 `User.status`；access 15m / refresh 720h=30 天） | `auth/handler_auth.go` refresh | B.14 |
| S4 | 禁用用户的 **API Key 仍有效**（Verify 只查 Key 自身 status/expires；吊销=逐个硬删） | `auth/apikey.go` Verify、`authz.go` | F.2 / B.14 |
| S5 | 改密后旧 token 不失效（JWT 无状态、无黑名单/吊销机制） | `auth/jwt.go` | A.4 |
| S6 | **WS 端点无鉴权**：`GET /ws/incidents/:id` 挂 public 组，任意人可订阅任意 incident 状态快照 | `wire.go:237`、`ws/handler.go` | C.3.6 / F.4 |
| S7 | **复盘起草端点零权限校验**（`postmortem.create` 悬空）+ 对已存在复盘（含 published）**直接覆盖** sections | `wire.go`、`postmortem/handler.go` generateDraft、`engine.go:101-115` | C.6 |
| S8 | `POST /incidents/:id/timeline` 的 **actor/source 请求体自报**可冒充（服务端不回填登录态）；只读 subscriber 也能备注（仅校验 `incident.view`） | `timeline/handler.go` add | C.8 / E.5 |
| S9 | **IM 越权/denied 不落审计**（AuditLog 仍仅 7 种 action；C.3.2 图"拒绝→审计"为设计目标） | `im/handler.go` 无 AuditRecorder | C.3.2 |
| S10 | ✅ **已修复（2026-07-04）**：`RecordRunbook`/`RecordRunbookBlocked` 透传发起人 actorID（时间线记 `actor={kind:user,id}`、source=web），执行/阻断/升级均留痕；未获批写步骤按 on_failure 阻断（continue/abort/escalate）；前端弹窗真实传审批决策。⏳ 剩余：AuditLog/IncidentAction 未写、execute **无并发保护**（连点重复执行）、无独立审批人/pending 流 | `runbook/engine.go`、`timeline/recorder.go`、`web/src/pages/runbooks.tsx` | C.5.1/C.5.3 |
| S11 | AIInsight accept/reject 仅需 `incident.view`（subscriber 也能改判）、无前置状态校验（可反复改判）、无 resolver 留痕 | `ai/diagnose.go` ResolveInsight | C.4.4 |
| S12 | 钉钉回调**缺 sign 头时跳过验签**（只解密）；明文模式（无 aes_key）完全不校验 | `im/dingtalk/adapter.go` VerifyCallback | C.3.3 |
| S13 | 出站 webhook **无签名头**（仅 UA），接收端无法验源 | `webhook/dispatcher.go` | F.3 |
| S14 | analytics 6 端点**无权限点、无团队 scope**——任何登录用户可见全组织指标 | `wire.go:318` 直接 Register、`analytics/engine.go` 全量查询 | B.11 |
| S15 | 悬空权限点（定义了但功能/端点不存在或未挂）：`event.view_unrouted` / `incident.create` / `incident.reassign` / `postmortem.create`（未挂） / `team.member.manage` / `admin.global_integration` / `schedule.override` / `user.update`·`user.disable`（未挂） | `auth/permission.go` vs 路由全集 | 附录 A |
| S16 | Runbook 凭据**无加密托管**（vault 📋），凭据只能明文进 steps endpoint/params | 全仓无 vault | B.8 |
| S17 | ✅ 正向确认：登录暴破防护在（单 IP/用户名 10 次/分限流 + 连续 5 次失败锁 5 分钟 + denied 审计） | `auth/login_guard.go` | B.12 |
| S18 | ✅ 正向确认：Runbook SSRF 防护在（scheme 白名单 + 连接时校验真实 IP 防 rebinding） | `runbook/ssrf.go` | B.8 |

## 二、断链类（代码在，环节不通/不生效）

| # | 结论 | 依据（代码） | 章节 |
|---|------|------|------|
| B1 | **reopen 后升级链不重启**：escalation 只订阅 Created/Acked/Escalated——重开后不重发通知，静默停在 triggered | `wire.go:136-138/201-207` | C.2 |
| B2 | **closed 终态不可达**：无 close 端点、复盘 publish 不回写 `incident.status`，全仓无 SetStatus(closed) | 全仓 grep StatusClosed | C.2 / C.6.1 |
| B3 | **自动恢复不写时间线、不发领域事件**（WS/卡片/webhook 全无感）；按 service 维度取最新活跃单可能解错；已 acked 也被解决 | `triage/engine.go` handleResolved | C.2.1 |
| B4 | 时间线 12 类型**仅 7 有写入点**：`incident_created`/`event_attached`/`status_changed`/`ai_insight`/`im_message` 零写入 | 全仓 grep timelineitem.Type | C.8 |
| B5 | **IncidentAction 全仓零写入/查询**（via 统计"IM-first"不可行）；操作留痕实际=TimelineItem.source | 无 IncidentAction.Create 调用点 | B.12 |
| B6 | `EscalationLevel.notify_channels` 全仓无引用（通知走 wire.go 默认通道）；手动 escalate 不取消当前层 pending 任务 | `escalation/engine.go`、`wire.go` | B.6 |
| B7 | `NotificationRule.condition`/`channels` 不参与评估分发（resolver 取首条 enabled 规则的 quiet_hours/template_id） | `wire.go` resolver | B.7 |
| B8 | **电话/短信零触发路径**（phone/sms 已注册但不在默认链 `[webhook?]+im+email`）；`User.phone` schema 在但**无任何 API 可写** | `wire.go:388-399`、`channels_phone.go`、updateUserReq | C.9 |
| B9 | **escalation target=team 不解算成员**（占位 UserID=0）——邮件/电话对 team 型 target 不发，仅 IM 群卡片路径有效 | `escalation/engine.go` resolveTargets | F.3 / 剧本 2 |
| B10 | **自动升级不发布领域事件**（WS/卡片刷新/出站 webhook 全盲，仅手动/runbook escalate 走 Service 发布）；`incident.created` 不出站、不推 WS | `wire.go:201-211` 订阅集 | C.3.6 / F.3 |
| B11 | `timeline_added` WS 消息类型定义了但全仓无广播点 | `ws/hub.go` | C.8 |
| B12 | **AIInsight write-only**：无读取/list 端点（诊断结果刷新即丢，历史只能 DB 直查）；`applied` 状态无产生路径；`ai_insight` 时间线零写入；AI 建议不进卡片/通知/时间线 | `ai/handler.go` | C.4 |
| B13 | Runbook `trigger` 字段可存**不求值**（仅手动 execute）；`IsReadOnly` 无调用方（auto-run 📋）；Service↔Runbook/Schedule 关联 API 两侧均未暴露 | `runbook/`、`service/handler.go` | B.8 / C.5.0 |
| B14 | Integration 默认 `service_id` 可存**不参与路由** | NormalizeWorker 注释、`route()` | B.3 / B.4 |
| B15 | `SuppressionRule.expires_at` 请求体被 handler **忽略**（API 无法设置）；多规则命中实际无排序取首条 | `notification/handler.go`、`triage/suppression.go` | B.7 |
| B16 | **钉钉 UpdateCard 是 no-op**（连"降级发新消息"都没有，卡片永停下发时状态）；钉钉 mention 不解析（拉人不可用）；企微 NoopBot 完全占位 | `dingtalk/adapter.go:78-85`、ParseCallback、`noop.go` | C.3.3 |
| B17 | IM 值班群 `VIGIL_IM_ONCALL_CHANNEL` 未配 → Send 返回 nil,nil：**不报错、不计 metrics、无日志**（可观测性盲区） | `im/notification_channel.go:55-57` | C.9 |
| B18 | 复盘发布唯一副作用=embedding（失败静默无补算任务）；**archived 掉出相似检索**（SQL 硬编码 status='published'）；similar-postmortems 无 LIKE 降级（静默 `[]`） | `postmortem/engine.go`、检索 SQL | C.4.5 / C.6 |
| B19 | ✅ 已修（补 camelCase json tag，对齐 spec/前端）：analytics 响应结构体原**无 json tag**（PascalCase）≠ 前端 camelCase → Web Dashboard KPI 恒空 | `analytics/engine.go` | B.11 |
| B20 | ⚠️ 代码缺陷（同款）：Runbook ExecuteResult/StepResult 无 json tag → Web 执行结果不可见；engine 失败分支丢弃结构化 Output（削弱 FIX-E） | `runbook/engine.go` | C.5.2 |
| B21 | Schedule PATCH 只写 layers JSON **不重建 Rotation**（改参与人只能删除重建）；引擎不读 `Schedule.type`（三枚举无算法差异）、不查 `User.status`（禁用仍被解算通知） | `schedule/handler.go`、`engine.go` | B.5 / B.14 |
| B22 | quiet_hours 静默的通知**直接丢弃无补发**；通知全败无兜底告警；送达不落库（无 Notification 实体），仅 metrics+日志 | `quiet_hours.go`、`notifier.go` | B.7 / C.9 |
| B23 | dedup：Redis nil→放行不去重；Redis 故障→分诊任务报错走 Asynq 重试；SETNX 固定 TTL 不续期 | `triage/engine.go` checkDedup | C.1.1 |
| B24 | CardStore 进程内存 map——重启后旧卡片不可刷新（静默跳过） | `im/handler.go` | C.3.5 |
| B25 | 错误映射不一致：IM 对非法状态转换返 **500**（Web 400）；四操作端点对不存在 id 返 **400** failed_precondition（非 404）；diagnose/similar/resolve 对不存在 id 返 **500** | `im/handler.go`、`incident/handler.go`、`ai/` | C.3.2 / C.2 / C.4 |
| B26 | 升级策略/Runbook 创建请求体**无 team_id** → team 级用户建完 list 看不到（SEC-01 过滤无主资源） | createReq 无字段 | B.6 / B.8 |
| B27 | seed 失败仅 log.Warn 不退出；`/health` 只探 Redis+`SELECT 1`——未 migrate 实例探针全绿但业务全挂 | `wire.go:88-108`、`server.go` | A.3 |

## 三、纠偏类（文档断言与代码相反，已在 user-journeys.md 就地改标）

| # | 原断言 → 实际 | 依据（代码） | 章节 |
|---|------|------|------|
| C1 | "接入=webhook/SMTP/API" → **仅 webhook**（适配器仅 prometheus/grafana/genericJSON；SMTP 入站、开放 API 均无） | `ingestion/adapter.go` RegisterBuiltins | B.4 |
| C2 | "路由 labels 精确+glob" → 仅 `labels["service"]` **等值匹配** active `Service.slug`；`Service.labels` 不参与 | `triage/engine.go` route() | B.3 |
| C3 | "critical unrouted 兜底通知全员/admin" → **完全未实现**（unrouted 只标记返回，静默） | `triage/engine.go`、notification/escalation 全集 | B.13 |
| C4 | "空班检测告警 team_admin" → **不存在**（空层静默 continue；唯一信号=时间线"通知 0 人"） | `schedule/engine.go:94-96` | B.5.2 |
| C5 | "换班 Override" → **完全未实现**（无实体/端点/解算，oncall 响应 override 恒 false） | `ent/schema/schedule.go`、`schedule/` | B.5.1 |
| C6 | repeat_times 层级字段 → **策略级**（每层通知 repeat_times+1 次，间隔=该层 delay_minutes） | `escalation/engine.go:199` | B.6 |
| C7 | oncall 响应 `{primary,secondary,overrides}` → 实际 `{schedule_id,schedule_name,layers[]}` | `schedule/handler.go` | B.5 |
| C8 | "dedup 后 Event 仅 1 条" → 二次推送 Event **仍落库**（标 is_noise，action=dedup_skipped），Incident 不重复 | `triage/` | C.1.1 |
| C9 | 聚合窗滑动 → 锚 **Incident.created_at 固定 5min 不滑动**（长风暴每 5min 裂新单）；dedup/聚合窗均硬编码 | `triage/engine.go:50-51`、aggregate | C.1.1 |
| C10 | "Integration 禁用后推送 404" → 统一 **401** | `ingestion/handler.go:87-91` | B.4 |
| C11 | RawEvent "received→processed" → `received→normalized / parse_failed / requeued`（无 processed 态） | `ent/schema/event.go` | B.10 / B.15 |
| C12 | "通知兜底降级链" → **不存在**——启动时固定 im+email(+webhook) **并联**各发一份，无逐通道降级 | `wire.go` buildNotifier、`notifier.go:80` | C.9 |
| C13 | "卡片按看卡人权限渲染" → 群卡片按**首个通知 target** 的权限渲染（subscriber 看到按钮点了 403 群内静默）；候选按钮按权限裁剪**不按状态** | `im/notification_channel.go`、`card.go` | C.3.1 / E.4 |
| C14 | "require_approval 弹窗审批流" → 引擎凡写步骤只认 `approved`（与标志解耦，数据层强制写步骤 require_approval=true）；✅ **已修复**：前端弹窗真实传审批决策（复选框），未获批写步骤按 on_failure 阻断（continue/abort/escalate）。⏳ 剩余：仍是发起人自证（无独立审批人/pending/超时） | `runbook/engine.go`、handler、`runbooks.tsx` | C.5.1 |
| C15 | "AI 分诊/Copilot 推荐/draft_summary" → **完全无代码**（全仓唯一 AIInsight.Create 是诊断链）；置信度 0.6 过滤、evidence 强制亦未实现 | `ai/` 全集 | C.4.2 / C.4.3 |
| C16 | "resolve 自动起草复盘 / 复盘闸门 / 发布自动建工单" → 三者皆无（IncidentResolved 订阅方无复盘引擎；全仓无 Jira/禅道调用，tracker_url 纯手填） | `wire.go:203-210`、全仓 grep | C.6.1 / C.6.2 |
| C17 | "用户自助绑 IM" → `user.im.bind` 仅 org_admin（**代绑**）；无解绑端点、误绑不可迁移、无前端页 | `seed.go`、`im/mapper.go:88-99` | B.9 |
| C18 | 临时授权入参 `expires_at` 绝对时间 → 实际 **`expires_in_hours`**（相对小时）；到期 SQL 实时过滤自动失效 | `auth/handler.go:173`、`authz.go:84/120` | B.2 / 剧本 3 |
| C19 | "team_admin 可发 subscriber 绑定" → `role.assign` **仅 org_admin** 持有 | `seed.go` | E.1 / 剧本 3 |
| C20 | "同名自定义模板覆盖内置" → name 无唯一约束，同名导致 Only 歧义**降级回内置**（非覆盖） | `notification/template.go` | B.7 |
| C21 | 审计"覆盖配置变更/用户禁用" → 实际落审计**仅 7 种 action**（role.create/delete/assign/unassign、apikey.create/delete、auth.login）；`GET /audit-logs` 无 from/to 时间参数 | 全仓 AuditRecorder 调用点、`handler_audit.go` | B.12 |
| C22 | "JWT_SECRET 未设→登录禁用+告警日志" → production **拒绝启动**；development 自动填弱密钥 | `config.go:253-264` | A.4 |
| C23 | "浏览器登录被强制改密" → **Web 无改密页**（password 仅 login.tsx）——首登改密唯一途径=直调 API | `web/src` 全量 grep | A.4 / A.6 |
| C24 | "出站 webhook 无重试" → **有重试**（3 次，网络错线性退避 1s/2s，非 2xx 立即重试）；但仅 5 事件（无 created/自动升级）、无签名、全败无死信 | `webhook/dispatcher.go` push | F.3 |
| C25 | analytics Unrouted 计数 → 口径偏大（`Not(HasService())` 把被标噪 Event 一并计入） | `analytics/engine.go` | B.11 |
| C26 | deployment.md §7 "Ingress 模板已提供" → **无该模板**；`deploySubchart`/`asynqmon.enabled` 均 no-op；`redis.password` 渲染陷阱（values 无键，需显式加任意非空值才注入 env） | `deploy/helm/templates` 全集、`Chart.yaml` | A.3 |
| C27 | `/vigil runbook`/`oncall` "unsupported command" → 实际 **403 no permission mapping**（映射失败先于分发）；`/vigil add` 为 400 unsupported（有权限映射无 switch 分支） | `im/handler.go` handleCommand/commandToAction | C.3.5 |
| C28 | "AI 洞察/similar 随通知卡片下发" → 卡片**不带 AI 信息**，AI 呈现仅 Web 详情页 AI 诊断卡（手动触发） | `im/card.go` BuildCard | C.3.1 / C.4.1 |
| C29 | "复盘评审=逐段 accept/edit/reject" → 无 sections 编辑 API、无字段级标记；评审修改唯一手段=**重新起草覆盖**；`generated_by` 复盘级粗标记（有 LLM 恒 mixed） | `postmortem/handler.go` Register 全集、`engine.go:96-99` | C.6 |
| C30 | 时间线 source "api 归因" → Web 与 REST API 四操作都硬编码 `source=web`（无 api 归因；im ✅） | `incident/handler.go:200-276` | C.8 |

## 四、缺失端点类（设计有、路由全集核实无）

| # | 缺失项 | 影响 / 替代 | 章节 |
|---|------|------|------|
| M1 | `POST /users`（建用户）、管理员重置他人密码 | 用户只能种子/DB 直建；密码只能本人改 | B.1.1 |
| M2 | `PATCH /roles/:id`（编辑角色）、复制角色专用端点 | 只能删了重建；复制=GET 权限集+POST 新角色 | B.2.1 |
| M3 | 团队成员增删端点（`team.member.manage` 悬空）；`parent_team_id` API 未暴露 | Rotation.participants 亦无成员入口 | B.2 |
| M4 | `POST /api/v1/events`（X-Vigil-Key 开放投递） | 入向仅 Integration webhook | F.1 |
| M5 | `POST /integrations/:id/test`（干跑）、Integration token 轮换 | 验证=真发一条；轮换=删除重建 | B.4 |
| M6 | `GET /events`（unrouted/噪音池明细）、手动提升 Event 为 Incident | 仅 analytics 计数 + DB 直查 | B.13 |
| M7 | `POST /incidents`（手动建单）、reassign、merge、renotify、close 端点 | trigger_type=manual/merged 仅枚举；reassign 替代=escalate/add_responder | C.3.7 / C.3.8 |
| M8 | Override 换班（实体+端点+解算全无） | 只能改排班或删除重建 | B.5.1 |
| M9 | `PATCH /postmortems/:id`（sections 编辑） | 评审修改=重新起草覆盖 | C.6 |
| M10 | AIInsight 读取/list 端点 | 反馈数据只能 DB 直查 | C.4.4 |
| M11 | IM 解绑 `DELETE`、IM 绑定前端页面 | 误绑不可迁移（索引表跳过仍归原主） | B.9 |
| M12 | RawEvent 查询/重放端点、超限自动回灌任务 | parse_failed/received 卡死无人消费 | B.15 |
| M13 | 报表 CSV 导出、定时聚合任务、WebSocket 看板；Notification 实体/送达查询端点 | 夜间打扰等指标无数据来源 | B.11 / C.9 |
| M14 | Service↔Schedule/Runbook 关联 API；ActionItem `due_date` 入参 | schema 有边/字段，请求体不收 | B.3 / C.6.2 |
| M15 | 集成向导（M14.6）、服务拓扑 `depends_on`（M4.4）、Event 保留/清理任务、IM 聚合卡片[全部确认]、值班大屏/PWA、Asynqmon 编排 | 均为 📋 设计目标 | B.4 / B.3 / E.7 / D.5 |

---

> 修复建议优先级（供排期参考）：S1 最高（生产安全）；S2–S8 次之（越权/冒充家族）；
> B19/B20（json tag 前端断链）为低成本高收益的代码缺陷修复；其余按业务优先级排。
