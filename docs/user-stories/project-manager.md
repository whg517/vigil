# 用户故事：项目经理 —— 李强

| 字段 | 内容 |
|------|------|
| **角色** | 交付项目经理（跨团队干系人，非一线处置者） |
| **状态** | Living（随实现演进） |
| **需求追溯** | FR/NFR 编号与 [`../requirements.md`](../requirements.md) 共用，见各故事「关联」 |
| **事实基准** | 实体字段以 `ent/schema/` 为准，权限点以 `internal/auth/permission.go` 为准 |

> 本文以「项目经理」第一视角描述 Vigil 的真实使用场景与可验证的验收标准。
> 设计取舍不在此重复，以链接引用对应 [ADR](../adr/)；未实现的期望明确标注 **「规划中」**。

---

## 角色画像

**李强，37 岁，某软件公司交付事业部项目经理。** 同时负责 2～3 个企业客户交付项目，需求、进度、质量、客户关系一肩挑。研发资源分散在订单、支付、基础设施等多个团队，这些团队都不向他汇报——他靠"借人"和"刷脸"推进跨团队协作。

**背景（访谈还原）**

- 最大的客户是华源集团的电商中台项目，合同里写了可用性承诺，客户驻场经理每周盯周报。
- 线上一出故障，他的里程碑就有风险：延期要向客户解释，严重故障还要写说明函。
- 他不处置故障，但必须**随时说得清**："现在什么状态、谁在处理、多久能好、之后怎么改"。

**目标**

| # | 目标 | 对应能力域 |
|---|------|-----------|
| G1 | 关键服务出事，第一时间知道，而不是等客户来电话 | FR-NTF |
| G2 | 事件状态透明可跟踪，不用在群里追问打扰处置 | FR-INC |
| G3 | 跨团队卡壳时能快速拉到对的人，不走一周审批 | FR-INC / FR-SEC |
| G4 | 复盘改进项有人认领、有截止日、可跟踪到闭环 | FR-PMR |
| G5 | 改进项和公司工单系统联动，不做两套台账 | FR-INT |
| G6 | 面向管理层和客户的报表拿数据说话（故障次数 / MTTR） | FR-RPT |

**痛点（访谈原话）**

1. "上次支付超时是客户先在电话里告诉我的，我比客户晚知道 40 分钟，非常被动。"
2. "半夜出事我不敢在群里问'怎么样了'，问了像催命；不问第二天早上跟客户没法交代。"
3. "跨团队借 DBA，先找他老板、再找我老板，等审批下来故障都自己好了。"
4. "复盘会上大家都点头，改进项写在会议纪要里，三个月后原样再炸一次。"
5. "月报里的故障数据是我从群聊天记录和 Excel 里手工扒的，每月半天，还经常对不上。"

**在 Vigil 中的授权形态**

李强不是处置责任人。典型配置是：管理员把内置只读角色 `subscriber`（含 `incident.view` / `event.view` / `postmortem.view`）按 team scope 绑定到他关注的交付团队；再利用「权限点固定、角色自由组合」的 RBAC 模型（[ADR-0027](../adr/0027-rbac-permissions-roles.md)）创建自定义角色补充所需权限点。本文各故事假定他的「项目经理」自定义角色在 subscriber 基础上叠加了 `analytics.view`、`postmortem.actionitem.manage` 与 `incident.add_responder`。权限点清单以 `internal/auth/permission.go` 为准。

---

## 故事总览

| 编号 | 标题 | 关联域 | 优先级 | 状态 |
|------|------|--------|--------|------|
| [US-PM-01](#us-pm-01-订阅关键服务的事件动态) | 订阅关键服务的事件动态 | FR-NTF | P0 | 已实现 |
| [US-PM-02](#us-pm-02-只读跟踪事件状态与时间线) | 只读跟踪事件状态与时间线 | FR-INC / FR-SEC | P0 | 已实现 |
| [US-PM-03](#us-pm-03-跨团队拉人与事件级临时授权) | 跨团队拉人与事件级临时授权 | FR-INC / FR-SEC | P0 | 已实现 |
| [US-PM-04](#us-pm-04-发布日的-noc-值班大屏) | 发布日的 NOC 值班大屏 | FR-RPT | P2 | 已实现 |
| [US-PM-05](#us-pm-05-复盘改进项分派与闭环跟踪) | 复盘改进项分派与闭环跟踪 | FR-PMR | P0 | 已实现 |
| [US-PM-06](#us-pm-06-改进项外接工单与状态回写) | 改进项外接工单与状态回写 | FR-INT | P1 | 已实现 |
| [US-PM-07](#us-pm-07-critical-强制复盘与完成率督导) | critical 强制复盘与完成率督导 | FR-PMR / FR-RPT | P1 | 已实现 |
| [US-PM-08](#us-pm-08-面向管理层的报表与-csv-导出) | 面向管理层的报表与 CSV 导出 | FR-RPT | P0 | 已实现 |
| [US-PM-09](#us-pm-09-规划中sla-目标与达成率报表) | （规划中）SLA 目标与达成率报表 | FR-RPT | P2 | **规划中** |

> US-PM-01 定为 P0 的理由：它是本角色第一痛点（"比客户晚知道 40 分钟"，目标 G1）的直接解，也是干系人知情闭环的入口——没有定向订阅，李强无从知道该去打开 US-PM-02 的详情页。对应能力（FR-NTF-7）已实现且有验收保障，满足 [README](./README.md) 的 P0 定义。

---

## US-PM-01 订阅关键服务的事件动态

**故事**：作为项目经理，我想要订阅交付项目相关服务的事件动态，以便线上出问题时第一时间知道并评估对里程碑的影响，而不是等客户来电话。

**场景叙事**：7 月初，华源电商中台项目进入 UAT 冲刺，距切流上线还有三周。李强在 Vigil 设置页的「订阅」标签里给 `order-service` 和 `payment-service` 各建了一条订阅：最低严重度选 warning（info 级抖动他不关心），通道偏好只勾了邮件——他不想让告警混进钉钉工作消息里。周三 14:20，订单团队的一条 warning 事件创建，他的邮箱两分钟内收到定向通知，点开链接直达事件详情；他看一眼影响面判断不涉及 UAT 环境，继续开需求会。当晚 23 点又有一条 warning 变更，落在他的免打扰时段，通知被静默记录，第二天早上他在送达记录里补看——既没漏掉，也没被吵醒。

**验收标准**：

1. Given 李强对某 service（或 team）建立了订阅，When 该范围内的 Incident 发生生命周期变更（created / acked / escalated / resolved / closed / reopened / responder_added），Then 按其订阅通道偏好发送定向通知，且落 Notification 送达记录。
2. Given 订阅的 `min_severity` 为 warning，When info 级 Incident 变更，Then 不发送定向通知。
3. Given 订阅者非值班人且处于免打扰时段，When 非 critical 变更发生，Then 通知记为 `suppressed` 并落送达记录可查（补发能力**规划中**，现状靠事后查看送达记录，口径同 [`../requirements.md`](../requirements.md) FR-NTF-2）；critical 可按 `bypass_for` 配置穿透静默。
4. Given 订阅未配置通道偏好，Then 走全局默认降级链 `[webhook]→im→email`（逐通道尝试，首个成功即停）。
5. 订阅粒度为 team 或 service 二选一，李强可在 Web 设置页自助增删改自己的订阅。

**关联**：FR-NTF · [ADR-0017 通知降级链与送达三态](../adr/0017-notification-fallback-chain.md) · 实体 `ent/schema/subscription.go` · 优先级 P0（定级理由见[故事总览](#故事总览)）

---

## US-PM-02 只读跟踪事件状态与时间线

**故事**：作为项目经理，我想要以只读身份随时查看事件的状态和完整时间线，以便向客户和管理层准确交代进展，而不打扰正在处置的工程师。

**场景叙事**：周四 02:14，支付网关 critical 告警，值班的王磊被拉起来处理到凌晨四点。08:30 李强到公司，9 点要跟华源的周经理开电话会。放在过去，他只能硬着头皮在群里@王磊——人家刚睡下。现在他打开 Vigil 事件详情页，时间线一目了然：02:14 触发、02:17 升级至第二级、02:19 王磊在钉钉卡片上确认（渠道标记为 im）、03:05 执行 Runbook 诊断步骤、03:41 标记 resolved。电话会上他照着时间线复述，周经理问"现在还有风险吗"，他把详情页投屏——状态 resolved，复盘草稿已生成。整个过程他没有发过一条追问消息。

**验收标准**：

1. Given 李强仅持只读权限（`incident.view` / `event.view` / `postmortem.view`），When 打开事件详情，Then 可见状态、时间线、通知送达记录，但 ack / resolve 等处置操作被服务端 RBAC 拒绝并记审计；群内 IM 卡片全群共享一张、按代表接收者（当班值班人）权限渲染按钮，李强点击处置按钮时由回调硬鉴权拒绝并记审计（权威口径见 [`../requirements.md`](../requirements.md) FR-IM-4）。
2. Given Incident 发生任何状态变更，Then 时间线新增一条 TimelineItem，且时间线只追加、历史条目不可修改（[ADR-0022](../adr/0022-aiinsight-hitl-evidence.md) 硬约束）。
3. Given 事件详情页处于打开状态，When 状态变更，Then 经 `/ws/incidents/:id` WebSocket 实时刷新，无需手动刷新。
4. Given 处置操作分别发生在 Web、IM、API，Then IncidentAction 的 `via` 字段如实记录渠道，李强在时间线上可区分"谁在哪个渠道做了什么"。

**关联**：FR-INC / FR-SEC · [ADR-0010 Event/Incident 分离](../adr/0010-event-incident-separation.md) · [ADR-0018 IM 同 RBAC](../adr/0018-im-same-rbac-as-web.md) · 优先级 P0

---

## US-PM-03 跨团队拉人与事件级临时授权

**故事**：作为项目经理，我想要把其他团队的专家快速拉进正在处置的事件并让其立即获得处置权限，以便跨团队故障不因权限审批卡住，同时权限不残留。

**场景叙事**：大促前压测夜，21:40，订单团队的 Incident 卡在数据库层：连接池耗尽，值班的王磊判断需要 DBA 介入，但 DBA 赵敏属于基础设施团队，对订单团队的事件没有任何权限。放在过去，流程是"找赵敏老板→找自己老板→提权限工单"，至少半天。现在李强直接在事件详情页点「拉入响应者」选中赵敏——系统检测到她对该团队无处置权限，自动发放一条事件级临时授权（responder 角色、仅该团队 scope、24 小时有效、标记来源事件）。赵敏的钉钉马上弹出事件卡片，她点确认、贴上慢查询分析，23:10 事件标记 resolved，临时授权随之自动撤销——收口即回收（closed / merged 同理），权限不留尾巴；若事件后续 reopen 还需赵敏介入，重新拉入一次即可再次授权。李强在周报里写：跨团队响应耗时从"半天"变成"90 秒"。

**验收标准**：

1. Given 被拉人对该 Incident 所属 team 无处置权限，When 执行 add_responder（需 `incident.add_responder` 权限），Then 自动发放 role=responder、scope=该 team、带 `expires_at`（默认 24h）与 `source_incident_id` 的临时 RoleBinding，发放动作落审计。
2. Given 被拉人已具备该 team 的处置权限，Then 不重复发放临时授权。
3. When Incident 收口（closed / resolved / merged），Then 按 `source_incident_id` 自动撤销临时授权；即使联动撤销遗漏，`expires_at` 过期兜底失效（鉴权实时查库，撤销即生效）。
4. Given 临时授权生效期间，Then 被拉人仅对该 team 获得权限（team scope 非 org），对其他团队数据仍不可见——软隔离边界不被放宽。
5. Given 已有临时授权且尚未过期，When 重复拉入同一人，Then 幂等不重复发放，也**不延长** `expires_at`。
6. Given 临时授权已过期（处置超过 24 小时会出现权限空档，需感知），When 再次拉入同一人，Then 重新发放一条新的 24 小时临时授权——长处置以"到期后重新拉入"续权。

**关联**：FR-INC / FR-SEC · [ADR-0020 拉人即事件级临时授权](../adr/0020-responder-temp-grant.md) · [ADR-0028 软隔离](../adr/0028-single-org-soft-isolation.md) · 优先级 P0

---

## US-PM-04 发布日的 NOC 值班大屏

**故事**：作为项目经理，我想要在发布日把一块只读大屏挂在项目指挥室，以便包括客户在内的所有人一眼看清"现在有没有事、谁在值班"，减少口头同步。

**场景叙事**：8 月 8 日大促切流，项目指挥室坐了两排人，包括华源的周经理。李强把投影切到 Vigil 的 `/wall` 大屏：深色背景、超大字号，活跃事件数、近时段告警量、MTTA / MTTR 四个大数字排在顶部，下方是活跃事件滚动列表和当前值班人。切流后 20 分钟，一条 critical 红色闪烁着跳上大屏，全屋人同时看到——没人需要喊"出事了"；王磊确认后状态实时变化，闪烁消失。周经理事后跟李强说："你们这个屏比开十次同步会都有用。"

**验收标准**：

1. `/wall` 为独立只读路由（无侧边栏、不套应用框架），深色、大字号、高对比度，适合远距离观看，无需交互即可获取信息。
2. 大屏展示活跃事件列表（triggered / escalated / acked）、KPI 大数字（活跃事件数、近时段告警量、MTTA、MTTR）与当前值班人。
3. Given 事件状态变更，Then 大屏经 `/ws/dashboard` WebSocket 实时刷新，并有常规拉取兜底。
4. critical 事件以红色醒目高亮，且严重度以颜色 + 文字双编码呈现，不单靠颜色传达（遵循 [ADR-0034](../adr/0034-uiux-oncall-principles.md) 状态可见与可达性原则）。

**关联**：FR-RPT · [ADR-0034 UI/UX 设计原则](../adr/0034-uiux-oncall-principles.md) · 优先级 P2

---

## US-PM-05 复盘改进项分派与闭环跟踪

**故事**：作为项目经理，我想要把复盘产出的改进项落成有负责人、有截止日、有状态的结构化条目，以便像跟项目任务一样跟到闭环，而不是散落在会议纪要里。

**场景叙事**：大促后的复盘会开了两小时，产出四条改进项：连接池参数化改造（王磊，8/20）、慢查询治理（赵敏，8/29）、压测覆盖支付回调（测试组小郑，9/5）、告警阈值调优（王磊，8/15）。会一结束，李强当场在复盘页面把四条录成 ActionItem，指定负责人和截止日。此后每周一的项目例会，他打开复盘详情过一遍状态：done 两条、in_progress 一条、还有一条 open 且已过截止日——他直接在会上点名。三个月后同类故障没有复发，他在季度汇报里把这四条的闭环记录贴了出来。

**验收标准**：

1. ActionItem 支持描述、负责人、截止日、状态（open → in_progress → done）等字段（以 `ent/schema/postmortem.go` 为准），挂在对应 Postmortem 下。
2. Given 持 `postmortem.actionitem.manage` 权限，Then 可增删改改进项及其状态；仅持 `postmortem.view` 的只读角色不能改动。
3. Given 复盘已 published，Then 其改进项仍可继续更新状态直至闭环——发布不冻结改进项跟踪。
4. Given 按 `status` 与 `due_date` 查看改进项，Then 可识别未闭环与逾期条目（两字段均有索引支撑查询）。

**关联**：FR-PMR · [ADR-0026 复盘 AI 起草与改进项外接](../adr/0026-postmortem-ai-draft.md) · 优先级 P0

---

## US-PM-06 改进项外接工单与状态回写

**故事**：作为项目经理，我想要复盘改进项自动推送到公司工单系统、工单状态变化再自动回写 Vigil，以便团队在熟悉的工单流里干活，而我不用维护两套台账。

**场景叙事**：公司研发任务统一在禅道里管理，团队不可能天天开 Vigil 盯改进项。运维在 Vigil 的工单集成页配了一条指向公司自建 webhook 网关的集成（网关把 Vigil 的通用建单 payload 转成禅道 API 调用），并配置了回调验签密钥。大促复盘发布的那一刻，四条改进项自动在禅道建了任务，Vigil 侧回写了工单链接——李强点 ActionItem 上的 `tracker_url` 直达禅道任务页。两周后王磊在禅道里关闭连接池改造任务，网关回调 Vigil，对应 ActionItem 自动变成 done。李强的例会清单和禅道台账第一次天然对齐，他再也不用周五晚上手工核对两边状态。

**验收标准**：

1. When 复盘状态推进到 published，Then 为其下尚未建单的 ActionItem 经工单集成建外部工单，并回写 `tracker_url` 与 `external_id`；建单失败仅记日志，不阻断复盘发布（best-effort）。
2. 工单集成仅提供通用 webhook 形态（`type=webhook`）；对接禅道 / Jira 等具体系统由使用方 webhook 网关转换，不内置厂商 SDK（范围裁剪见 [ADR-0037](../adr/0037-trim-deferred-features.md)）。
3. Given 外部工单状态变更回调 `POST /webhooks/ticket/:id`，Then 经 HMAC-SHA256 验签且带时间戳防重放（偏移超 5 分钟拒绝）；未配置 `callback_secret` 的集成一律拒绝回调，不留"无密钥即放行"后门。
4. Given 回调验签通过，Then 按 `external_id`（辅以 `tracker_url`）匹配 ActionItem，把外部状态归一到 open / in_progress / done；匹配不到返回 `ignored`、重复回调返回 `unchanged`（幂等）；真实变更落审计。
5. Given ActionItem 状态在 Vigil 侧改为 done，Then 若配置了工单集成，向其 endpoint 单向同步一条 done 状态（best-effort）。
6. 建单凭据与 `callback_secret` 加密存储、任何接口不回显明文（[ADR-0030](../adr/0030-integrations-encrypted-openapi.md)）。

**关联**：FR-INT · [ADR-0030 四方向集成与凭据加密](../adr/0030-integrations-encrypted-openapi.md) · [ADR-0026](../adr/0026-postmortem-ai-draft.md) · 优先级 P1

---

## US-PM-07 critical 强制复盘与完成率督导

**故事**：作为项目经理，我想要严重故障必须完成复盘才能关单、且复盘完成率有数字可查，以便"复盘"从口头承诺变成流程约束，我能拿数据督导。

**场景叙事**：9 月客户例会，周经理翻着上月周报问："三次 critical，复盘都做了吗？改了什么？"李强打开 `/analytics/postmortems`：本季度复盘完成率清清楚楚，三次 critical 的复盘全部 published，改进项闭环情况见 US-PM-05 的跟踪记录。他心里有底是有原因的——月初有个团队想在 resolved 后直接把 critical 事件 close 掉，系统闸门直接拦住："须先完成复盘或显式跳过"；跳过是显式操作且记入时间线，谁跳过的、为什么，周会上躲不掉。李强不用再靠"下不为例"管复盘。

**验收标准**：

1. Given critical Incident 标记 resolved，Then 系统自动创建 draft 复盘（warning 是否自动起草可配置开关，info 不强制）。
2. Given critical Incident 的复盘未达 published / archived 且未显式跳过，When 执行 close，Then 拒绝并停在"待复盘"状态。
3. 跳过复盘为显式操作（`POST /incidents/:id/skip-postmortem`），需 `postmortem.update` 权限（复盘治理决策，非仅 `incident.close`），并记入时间线可追溯。
4. 复盘状态机为 draft → in_review → published → archived；状态推进需 `postmortem.update` / `postmortem.publish` 权限，仅 view 的只读角色不能推动。
5. `/analytics/postmortems` 提供复盘总数、已发布数与完成率（published 及 archived 占比），统计范围按用户可见团队隔离。

**关联**：FR-PMR / FR-RPT · [ADR-0026 复盘 AI 起草](../adr/0026-postmortem-ai-draft.md) · 优先级 P1

---

## US-PM-08 面向管理层的报表与 CSV 导出

**故事**：作为项目经理，我想要按时间范围查看故障次数、severity 分布、MTTA / MTTR 与趋势，并能导出 CSV，以便月度经营会和客户周报用一致的数据说话，不再手工扒群聊拼 Excel。

**场景叙事**：月底经营例会前一天，以前的李强要花半天从钉钉聊天记录、值班登记表和自己的 Excel 里拼"交付项目稳定性"一页纸，数字还常被质疑。现在他打开 Vigil 报表页选定本月：事件总数、severity 分布、MTTA、MTTR、近 7 天趋势和各团队负载一屏呈现；他把 incidents 维度导出成 CSV 附在汇报材料后面。会上他讲了一个数字："订单域 MTTR 从上季度均值 96 分钟降到 41 分钟"——这是升级链和 IM 卡片确认落地后的直接效果，数据来自系统而非手工台账，没人再质疑口径。

**验收标准**：

1. Given 持 `analytics.view` 权限，Then 可访问 `/analytics/dashboard`、`/analytics/alerts`、`/analytics/incidents`、`/analytics/team-load`、`/analytics/postmortems`、`/analytics/trend`。
2. 事件度量含 MTTA（acked_at − created_at 均值）与 MTTR（resolved_at − created_at 均值），支持按时间范围过滤。
3. alerts / incidents / team-load / postmortems 四个维度提供 CSV 导出端点，导出文件名带时间戳避免覆盖。
4. Given 用户仅有 team 级授权，Then 报表只统计其可见团队的数据；org 级授权可见全组织（报表遵循团队软隔离）。
5. 趋势报表按日输出事件数与告警数（默认 7 天，天数可调）。

**关联**：FR-RPT · [ADR-0029 导出不静默截断](../adr/0029-dual-audit-no-silent-truncation.md) · [ADR-0027 RBAC scope](../adr/0027-rbac-permissions-roles.md) · 优先级 P0

---

## US-PM-09 （规划中）SLA 目标与达成率报表

> **规划中**：当前 analytics 提供 MTTA / MTTR 均值、事件数量、趋势、团队负载与复盘完成率，**尚无 SLA 目标配置与逐单达成判定能力**。本条为合理的未来期望，落地前须按 [ADR-0001](../adr/0001-record-architecture-decisions.md) 流程新增 ADR 裁决。

**故事**：作为项目经理，我想要为关键服务设定响应与解决时限目标（如 critical 15 分钟内确认、4 小时内解决），并在报表中看到逐单达成情况与达成率，以便对客户合同里的 SLA 承诺有量化交代。

**场景叙事**：华源的合同附件里写着分级响应时限，每季度对账。李强目前的做法是用 MTTA / MTTR 均值近似回答——但均值会掩盖个别严重超时的单子，客户按单追责时他只能人工翻时间线核对。他期望的形态是：给 `payment-service` 配一组 SLA 目标，报表直接给出"本季度 critical 共 3 单，2 单达成、1 单确认超时 6 分钟"，超时单可下钻到时间线定位卡点。

**验收标准（期望，随规划细化）**：

1. Given 服务配置了分级响应 / 解决时限目标，When 查看报表，Then 输出逐单达成 / 超时判定与周期达成率。
2. Given 存在超时单，Then 可从报表下钻到该 Incident 的时间线定位超时环节。

**关联**：FR-RPT · 优先级 P2 · **规划中**

---

## 附：故事与需求域对照

| 故事 | FR/NFR 域 | 依赖的关键机制 | 权威 ADR |
|------|-----------|----------------|----------|
| US-PM-01 | FR-NTF | 定向订阅、送达记录、免打扰静默 | [0017](../adr/0017-notification-fallback-chain.md) |
| US-PM-02 | FR-INC / FR-SEC | 时间线只追加、via 渠道审计、WS 实时 | [0010](../adr/0010-event-incident-separation.md) / [0022](../adr/0022-aiinsight-hitl-evidence.md) |
| US-PM-03 | FR-INC / FR-SEC | 事件级临时授权、双重回收 | [0020](../adr/0020-responder-temp-grant.md) / [0028](../adr/0028-single-org-soft-isolation.md) |
| US-PM-04 | FR-RPT | 只读大屏、WS 实时刷新 | [0034](../adr/0034-uiux-oncall-principles.md) |
| US-PM-05 | FR-PMR | ActionItem 结构化跟踪 | [0026](../adr/0026-postmortem-ai-draft.md) |
| US-PM-06 | FR-INT | 通用 webhook 建单、HMAC 回调回写 | [0030](../adr/0030-integrations-encrypted-openapi.md) / [0037](../adr/0037-trim-deferred-features.md) |
| US-PM-07 | FR-PMR / FR-RPT | 复盘闸门、完成率度量 | [0026](../adr/0026-postmortem-ai-draft.md) |
| US-PM-08 | FR-RPT | MTTA/MTTR、CSV 导出、scope 隔离 | [0029](../adr/0029-dual-audit-no-silent-truncation.md) / [0027](../adr/0027-rbac-permissions-roles.md) |
| US-PM-09 | FR-RPT | （规划中）SLA 目标与逐单判定 | 待新增 ADR |
