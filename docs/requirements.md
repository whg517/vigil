# Vigil 需求文档

| 字段 | 内容 |
|------|------|
| **状态** | Living(现行需求口径,随实现演进) |
| **更新** | 2026-07-22 |
| **决策溯源** | 全部设计取舍见 [`adr/`](./adr/)([索引](./adr/README.md)) |
| **实现状态依据** | 以仓库代码盘点为准(`internal/` 模块、`ent/schema/`、`web/src/pages/`) |

> **文档定位**:本文是 Vigil 需求的**单一信源**,回答三个问题——**要什么**(功能与非功能需求)、**为什么值得做**(定位、用户、价值)、**怎么算做到**(验收要点与度量方式)。
> 与其他文档的分工:「怎么实现」见 [`architecture.md`](./architecture.md);「为什么这么定」见各 [ADR](./adr/);「怎么操作」见 [`operations.md`](./operations.md);「怎么扩展」见 [`extending.md`](./extending.md)。
> **代码即真相**:实体与字段以 [`ent/schema/`](../ent/schema/) 为准,权限点清单以 [`internal/auth/permission.go`](../internal/auth/permission.go) 为准,本文不复制这两份清单。
>
> **变更机制**:需求变更(新增/收敛/裁剪)先经 ADR 裁决,再回写本文为现行口径;历史裁决不删除,由 ADR 留档(如已裁剪能力见 [ADR-0036](./adr/0036-remove-war-room.md)/[ADR-0037](./adr/0037-trim-deferred-features.md) 墓碑)。

---

## 一、问题陈述与愿景

监控采集、APM、日志平台擅长「发现问题」,却普遍不解决告警产生之后的**接力问题**:谁来响应、怎么通知、多人怎么协同、按什么步骤处置、没人理时怎么升级、事后怎么复盘。国内团队还被本土 IM 生态(钉钉/飞书)绑定,而现有海外 oncall 工具的 IM 集成停留在「通知通道」层面。

**Vigil(守夜人)** 定位为**开源、IM 原生、AI 原生的告警处置平台**,只解决「告警之后的下一步」,不做监控采集。

**愿景:让每一条告警都被妥善接力到终点。**

定位与范围的完整裁决见 [ADR-0002 产品定位与非目标](./adr/0002-product-positioning.md)。

## 二、产品定位与差异化

三大差异化支柱(市场上三者同时满足者无同类,详见 [ADR-0002](./adr/0002-product-positioning.md)):

| 支柱 | 含义 |
|------|------|
| **自托管** | 数据不出企业网络,3 个容器即可起步 |
| **本土 IM 原生** | 钉钉/飞书是**协同工作面**而非通知通道,是最核心差异化 |
| **LLM 横向贯穿** | 分诊/诊断/复盘全程 AI 贯穿,是底层能力而非付费墙 |

### 设计基线(贯穿性红线)

以下基线贯穿所有能力域,任何需求的实现不得违背:

| 基线 | 含义 | 裁决 ADR |
|------|------|---------|
| 接得住不丢失 | 先落库再处理,全链路不静默丢告警 | [0011](./adr/0011-ingestion-decoupled-idempotent.md) |
| 软隔离 + 拉人临时授权 | 团队是数据归属软边界,跨团队靠拉人 + 事件级临时授权 | [0028](./adr/0028-single-org-soft-isolation.md) · [0020](./adr/0020-responder-temp-grant.md) |
| AI 全程 HITL + evidence,可降级 | AI 建议须人确认才生效,无 evidence 不展示,LLM 故障不影响主流程 | [0022](./adr/0022-aiinsight-hitl-evidence.md) · [0023](./adr/0023-llm-provider-cost-control.md) |
| 写操作不碰生产 | Runbook 处置写操作须人确认或外接,不做无人值守自动修复 | [0021](./adr/0021-runbook-two-tier.md) |
| RBAC 全程,IM 非后门 | IM 操作与 Web 走完全相同鉴权链路 | [0018](./adr/0018-im-same-rbac-as-web.md) · [0027](./adr/0027-rbac-permissions-roles.md) |
| 可插拔 | 告警源/通知通道/执行器/LLM/IM 平台经接口抽象 + 编译期注册扩展 | [0009](./adr/0009-pluggable-integrations.md) |
| 自身可观测(吃狗粮) | 暴露 metrics/health,能被自家告警监控 | [0033](./adr/0033-selfmon-and-auth.md) |

## 三、目标用户与画像

目标用户:中小技术团队,尤其受本土 IM 生态绑定的国内团队([ADR-0002](./adr/0002-product-positioning.md))。四类画像与用户故事编号前缀对应(故事见 [`user-stories/`](./user-stories/)):

| 画像 | 典型诉求 | 故事前缀 |
|------|---------|---------|
| **运维主管(Ops Lead)** | 告警降噪少打扰;排班升级兜底「没人被叫」零容忍;审计留痕可治理;用数据看团队负载与降噪效果 | US-OPS |
| **架构师** | 选型评估与部署运维成本;数据不出企业网;安全边界与可扩展性;平台可被自家监控 | US-ARC |
| **项目经理** | 只读跟踪事件进展、全程留痕可追溯;复盘不流于形式、改进项落到工单;管理层报表 | US-PM |
| **开发人员(轮值 oncall / 集成方)** | 半夜被叫醒 30 秒内决策;在 IM 里直接 ack/处置不切工具;一屏上下文与 Runbook 诊断;告警源自助接入与程序化 API | US-DEV |

## 四、用户价值与成功指标

来源 [ADR-0002](./adr/0002-product-positioning.md);度量方式与实测数据的单一信源见「八、验收与度量」。

| 指标 | 目标 | 性质 |
|------|------|------|
| 无意义告警打扰次数 | **↓ 50%+**(Event→通知的降噪率) | 用户价值指标,运行期由 analytics 持续度量 |
| 单次事件 MTTR | 明显缩短(IM 内闭环 + Runbook + AI 辅助) | 用户价值指标,运行期由 analytics 持续度量 |
| 接入吞吐 | ≥ 1000 events/min | 系统能力,见 NFR-PERF-1 |
| 通知送达延迟 | P95 < 5s | 系统能力,见 NFR-PERF-2 |
| 部署门槛 | Docker Compose 一键起步(**硬指标**) | 系统能力,见 NFR-DEP-1 |

## 五、功能需求(FR)

编号规则:`FR-<域>-<序号>`,与用户故事([`user-stories/`](./user-stories/))共用编号体系,保证互查。
实现状态取值:**已实现**(代码可验证)/ **部分**(核心已落地、部分子项未验证或在路线图)/ **规划中**(已决策未实现,严格区分见 [ADR-0039](./adr/0039-data-lifecycle.md) 的写法先例)。

### 5.1 FR-ING 告警接入

| 编号 | 需求 | 验收要点 | 状态 | 关联 ADR |
|------|------|---------|------|---------|
| FR-ING-1 | 系统必须提供通用 Webhook 告警入口,接入点(Integration)以 token 自证 | `POST /api/v1/webhook/{token}`;接收链路只校验 + 落库 + 入队,秒级返回 202 | 已实现 | [0011](./adr/0011-ingestion-decoupled-idempotent.md) |
| FR-ING-2 | 系统必须内置主流告警源适配器并归一化为统一 Event | 内置 prometheus/grafana/webhook/email 四种;严重度归一为 critical/warning/info;原始 payload 保留于 Event.detail 不丢 | 已实现 | [0011](./adr/0011-ingestion-decoupled-idempotent.md) |
| FR-ING-3 | 接收与处理必须解耦:先落 RawEvent 再异步归一化,失败可重放 | RawEvent 状态机(received/normalized/parse_failed/requeued);归一化失败重试→死信;raw-events 可查询、可 replay | 已实现 | [0011](./adr/0011-ingestion-decoupled-idempotent.md) |
| FR-ING-4 | 系统必须提供开放 API 供外部系统程序化投递事件 | `POST /api/v1/events`(API Key 鉴权,RBAC 资源级校验),走与 webhook 相同的分诊链路 | 已实现 | [0011](./adr/0011-ingestion-decoupled-idempotent.md) · [0030](./adr/0030-integrations-encrypted-openapi.md) |
| FR-ING-5 | 系统必须支持邮件(SMTP 入向)作为告警源 | 内置 SMTP 接收端,**默认关闭**;收件地址 local part 即 Integration token,查不到 RCPT 阶段拒收;主题解析 severity;单封上限 1MB | 已实现 | [0038](./adr/0038-smtp-inbound.md) |
| FR-ING-6 | 系统必须提供接入点全生命周期管理与接线指引 | Integration CRUD + token 轮换 + 测试投递 + 配置模板;前端分步接入向导 | 已实现 | [0030](./adr/0030-integrations-encrypted-openapi.md) |
| FR-ING-7 | 接入错误必须分级处理且不静默丢弃 | 限流 429;队列积压 503 但 payload 仍落库;鉴权失败 401 不落库但记审计(防探测);格式错误落库标 parse_failed | 已实现 | [0011](./adr/0011-ingestion-decoupled-idempotent.md) |

### 5.2 FR-TRI 分诊降噪(去重 · 抑制 · 聚合)

| 编号 | 需求 | 验收要点 | 状态 | 关联 ADR |
|------|------|---------|------|---------|
| FR-TRI-1 | 系统必须对重复告警去重 | Redis SETNX + 过期窗口,默认 5min,`VIGIL_TRIAGE_DEDUP_WINDOW` 可配;去重键 = `source:source_event_id` 纯拼接(prometheus/grafana 以 fingerprint 充当 `source_event_id`) | 已实现 | [0012](./adr/0012-triage-three-stage-pipeline.md) |
| FR-TRI-2 | 系统必须支持抑制规则(临时静默与维护窗口) | SuppressionRule 分 adhoc/maintenance 两类;**preserve_critical 默认守卫 critical 不被抑制**(降噪不误杀);前端维护窗口管理页 | 已实现 | [0012](./adr/0012-triage-three-stage-pipeline.md) |
| FR-TRI-3 | 系统必须将相关 Event 聚合进同一 Incident,并发下不重复建单 | 聚合键默认 service+severity,5min 窗口并入活跃 Incident(含 escalated);「查活跃单→建单」临界区以 PostgreSQL advisory lock 串行化 | 已实现 | [0012](./adr/0012-triage-three-stage-pipeline.md) |
| FR-TRI-4 | resolved 信号必须触发关联 Incident 自动解决而非被丢弃 | 同 DedupKey 的 resolved Event 关联 firing 触发解决;可配「仅提示等人确认」 | 已实现 | [0012](./adr/0012-triage-three-stage-pipeline.md) · [0013](./adr/0013-deterministic-routing.md) |
| FR-TRI-5 | 降噪优化必须走「AI 建议 → 人工确认 → 显式规则」闭环,**不做自动回训** | 噪音建议经人 accept 后沉淀为显式 SuppressionRule;任何模型/规则不得在无人确认时自动改变自身行为 | 已实现 | [0025](./adr/0025-no-auto-retrain.md) |

### 5.3 FR-RTE 路由

| 编号 | 需求 | 验收要点 | 状态 | 关联 ADR |
|------|------|---------|------|---------|
| FR-RTE-1 | 路由必须以 Service 为锚点、确定性裁决(同输入同结果,更具体者胜出) | 四级顺序:slug 直达 → 多标签子集匹配(值支持 glob)→ 多命中按匹配数降序/ID 升序裁决 → Integration 默认归属兜底 | 已实现 | [0013](./adr/0013-deterministic-routing.md) |
| FR-RTE-2 | 路由失败必须进 unrouted 池可申诉,不静默丢弃 | 查看需 `event.view_unrouted` 权限;`POST /events/:id/reroute` 手动改派(需 `service.route_override`);unrouted 的 critical 有兜底通知 | 已实现 | [0013](./adr/0013-deterministic-routing.md) |
| FR-RTE-3 | 系统必须提供服务目录作为路由锚点与归属载体 | Service CRUD + 依赖关系/影响面查询;团队归属即鉴权作用域 | 已实现 | [0013](./adr/0013-deterministic-routing.md) · [0028](./adr/0028-single-org-soft-isolation.md) |
| FR-RTE-4 | 系统应支持 Service 自动供给,且必须带安全护栏 | **默认关闭**;slug 校验 + 能解析归属团队 + 团队已配默认升级策略三条件齐备才创建 source=auto;critical 仍走 unrouted 兜底;绝不触碰 source=manual | 已实现 | [0014](./adr/0014-service-auto-provisioning.md) |
| FR-RTE-5 | 系统应支持从外部源主动同步服务目录并清理过期项 | servicesync 支持 file/http 源;Pruner 对 source=auto 且 N 天无 Event 的 Service 自动停用(非删除) | 已实现 | [0014](./adr/0014-service-auto-provisioning.md) |

### 5.4 FR-ONC 排班

| 编号 | 需求 | 验收要点 | 状态 | 关联 ADR |
|------|------|---------|------|---------|
| FR-ONC-1 | 排班必须是纯蓝图 + 实时计算,变更立即生效 | 不物化「当前值班人」;按 timezone+layers+Rotation+Override 实时算;Redis 分钟级缓存仅用于日历展示,生效判断永远实时算 | 已实现 | [0015](./adr/0015-schedule-realtime-no-snapshot.md) |
| FR-ONC-2 | 系统必须支持分层排班与多种轮换模式 | layers 按 priority 升序;支持 calendar / rotation / follow_the_sun(无工作时段取「最快上班」层兜底) | 已实现 | [0015](./adr/0015-schedule-realtime-no-snapshot.md) |
| FR-ONC-3 | 系统必须支持临时换班(Override),且指定他人顶班须更高权限 | Override 优先级最高;**顶替人为操作者本人**时仅需 `schedule.override`,**顶替人为他人**时须叠加 `schedule.update`(判定维度是顶替人是谁,防值班人越权指派他人替班) | 已实现 | [0015](./adr/0015-schedule-realtime-no-snapshot.md) |
| FR-ONC-4 | 空班(算不出任何在班人)必须被检测并告警 | 空班触发告警 team_admin;告警回调未注入时记 metric + Warn 日志,不静默 | 已实现 | [0015](./adr/0015-schedule-realtime-no-snapshot.md) |
| FR-ONC-5 | 系统必须提供值班日历预览与交接预览 | oncall/preview 端点 + 自研日历组件(primary/secondary 分层显示);用户交接预览(handover-preview) | 已实现 | [0015](./adr/0015-schedule-realtime-no-snapshot.md) · [0034](./adr/0034-uiux-oncall-principles.md) |

### 5.5 FR-ESC 升级

| 编号 | 需求 | 验收要点 | 状态 | 关联 ADR |
|------|------|---------|------|---------|
| FR-ESC-1 | 系统必须支持多级升级策略,每个 Service 显式绑定(不继承父服务) | EscalationPolicy 多层级;末级升级到全团队 + 多通道 | 已实现 | [0016](./adr/0016-escalation-asynq-delayed.md) |
| FR-ESC-2 | Incident 创建即排定时升级,ack 必须取消后续升级 | Asynq 延迟任务驱动;ack 经事件总线取消任务;handler 执行前以 incident 状态作守卫,误触发不动作 | 已实现 | [0016](./adr/0016-escalation-asynq-delayed.md) |
| FR-ESC-3 | 每层升级必须支持重复通知后再推进下一层 | repeat_times 是策略级字段,对每层生效,每层共 repeat_times+1 次;重复间隔 = 该层自身 delay_minutes | 已实现 | [0016](./adr/0016-escalation-asynq-delayed.md) |
| FR-ESC-4 | 升级链路必须能从底层存储故障中自愈,「没人被叫」零容忍 | 对账巡检(默认 2min)核对 DB 应然与 Redis 实然,Redis 丢数据后从 current_level 自动重排;恢复取「宁可升得更快」语义 | 已实现 | [0016](./adr/0016-escalation-asynq-delayed.md) |
| FR-ESC-5 | 升级任务必须幂等,重复投递不重复通知 | 幂等键 `esc:{inc}:{level}:{repeatSeq}` + 状态守卫 + Redis 一次性通知标记三层保障 | 已实现 | [0016](./adr/0016-escalation-asynq-delayed.md) · [0007](./adr/0007-async-tasks-asynq.md) |
| FR-ESC-6 | 响应者必须能手动升级 | `POST /incidents/:id/escalate`,Web 与 IM 均可触发 | 已实现 | [0016](./adr/0016-escalation-asynq-delayed.md) |

### 5.6 FR-NTF 通知

| 编号 | 需求 | 验收要点 | 状态 | 关联 ADR |
|------|------|---------|------|---------|
| FR-NTF-1 | 通知通道必须是有序降级链(非并联),内置通道为 webhook/im/email | 逐通道尝试首成功即停;链来源优先级:升级层级配置 > 通知规则 > 全局默认 `[webhook(若配置)]→im→email`(webhook 仅在配置了出向 URL 时进入链头);未知通道名跳过、链继续 | 已实现 | [0017](./adr/0017-notification-fallback-chain.md) · [0037](./adr/0037-trim-deferred-features.md) |
| FR-NTF-2 | 每条通知的送达状态必须落库可查 | 四态 `pending/sent/failed/suppressed`;suppressed(免打扰静默)落库可查、不丢数据(补发端点尚未实现,属规划中);Incident 详情可查通知记录(`GET /incidents/:id/notifications`) | 已实现 | [0017](./adr/0017-notification-fallback-chain.md) |
| FR-NTF-3 | 通知失败必须重试且整链失败不静默 | Asynq 重试(MaxRetry=5,指数退避),幂等键 = Notification 行 ID;耗尽落 failed + 兜底告警 org_admin(走非 IM 通道)+ 进死信;入队失败回退同步直投,绝不丢通知 | 已实现 | [0017](./adr/0017-notification-fallback-chain.md) |
| FR-NTF-4 | 系统必须聚合通知防轰炸,且 critical 不受聚合延迟 | 默认 30s 窗口合并同 target 通知;critical 不聚合立即单发 | 已实现 | [0017](./adr/0017-notification-fallback-chain.md) |
| FR-NTF-5 | 系统必须支持免打扰时段(quiet_hours) | 支持跨午夜;按 quiet_hours 规则配置的 IANA 时区计算(**规则级配置,非接收人个人时区**);**值班人始终通知**(静默仅作用于非值班目标,保证升级链不因免打扰断链);支持 `bypass_for:[critical]` 穿透 | 已实现 | [0017](./adr/0017-notification-fallback-chain.md) |
| FR-NTF-6 | 通知触达规则与内容模板必须可配置 | NotificationRule CRUD + 测试发送;NotificationTemplate(Go template)CRUD + 预览 | 已实现 | [0017](./adr/0017-notification-fallback-chain.md) |
| FR-NTF-7 | 用户必须能定向订阅关注对象的事件变更 | Subscription 按 team/service 订阅 incident 变更,设置页自助管理 | 已实现 | —(无独立 ADR) |

### 5.7 FR-IM IM 协同(ChatOps)

| 编号 | 需求 | 验收要点 | 状态 | 关联 ADR |
|------|------|---------|------|---------|
| FR-IM-1 | 系统必须真实接入飞书与钉钉双平台(仅此两平台) | IMBot 接口抽象,业务层不感知具体平台;企微已移除不支持 | 已实现 | [0019](./adr/0019-imbot-pluggable-degradation.md) · [0037](./adr/0037-trim-deferred-features.md) |
| FR-IM-2 | 值班人必须能在 IM 卡片内直接处置事件 | 交互卡片承载 ack/resolve 等操作;摘要 + 操作按钮一屏决策 | 已实现 | [0018](./adr/0018-im-same-rbac-as-web.md) · [0034](./adr/0034-uiux-oncall-principles.md) |
| FR-IM-3 | IM 操作必须走与 Web 完全相同的 RBAC 链路,**IM 非权限后门** | 回调经 IMAccountBinding(platform + account_id)映射 User → 权限点 → team scope RoleBinding 判定;未绑定 IM 账号的操作被拒;无权操作拒绝并记审计 | 已实现 | [0018](./adr/0018-im-same-rbac-as-web.md) |
| FR-IM-4 | 卡片按钮必须按权限裁剪渲染,安全边界以回调鉴权为权威判定 | 群卡片全群共享一张,按**代表接收者**(首个可解析 user_id 的通知目标)权限裁剪按钮,不随群内各成员自身权限变化;「无权按钮不显示」仅在可解析单一接收者的场景(如按用户单发)成立;无权点击一律由回调硬鉴权拒绝并记审计(FR-IM-3 是权威判定) | 已实现 | [0018](./adr/0018-im-same-rbac-as-web.md) |
| FR-IM-5 | 事件状态变化必须双向同步到 IM 与 Web | Web→IM 由领域事件驱动卡片刷新(飞书原地更新;钉钉平台限制降级为重发带状态徽章的新消息);IM→Web 走 WebSocket | 已实现 | [0019](./adr/0019-imbot-pluggable-degradation.md) |
| FR-IM-6 | IM 平台能力缺失或不可用时不得静默丢告警 | 能力降级矩阵;失败降级走通知兜底链;值班群未配置记 metric + Warn | 已实现 | [0019](./adr/0019-imbot-pluggable-degradation.md) |
| FR-IM-7 | 用户 IM 账号绑定必须可管理 | IMAccountBinding(platform+account_id→user);用户管理页维护 | 已实现 | [0018](./adr/0018-im-same-rbac-as-web.md) |

### 5.8 FR-INC 事件管理(状态机 · 时间线 · 协同)

| 编号 | 需求 | 验收要点 | 状态 | 关联 ADR |
|------|------|---------|------|---------|
| FR-INC-1 | Incident 必须有明确状态机 | `triggered→acked→resolved→closed` + `triggered→escalated→acked`;支持 reopen | 已实现 | [0010](./adr/0010-event-incident-separation.md) |
| FR-INC-2 | 任何状态变更必须产生 TimelineItem,时间线只追加(硬约束) | 全程留痕可溯源;修正只能新增条目,不能原地改写 | 已实现 | [0010](./adr/0010-event-incident-separation.md) · [0022](./adr/0022-aiinsight-hitl-evidence.md) |
| FR-INC-3 | 处置操作必须结构化留痕并记录操作渠道 | IncidentAction 带 via(web/im/api/automation),可统计 IM 操作占比 | 已实现 | [0029](./adr/0029-dual-audit-no-silent-truncation.md) |
| FR-INC-4 | 系统必须支持事件合并 | `POST /incidents/:id/merge`;合并属收口动作,触发临时授权回收 | 已实现 | [0010](./adr/0010-event-incident-separation.md) |
| FR-INC-5 | 拉人协同必须自动发放事件级临时授权并双重回收 | add_responder 时仅对无权限者发放 responder 角色绑定(team scope,默认 24h);Incident 收口自动撤销 + 过期兜底;发放/撤销落审计 | 已实现 | [0020](./adr/0020-responder-temp-grant.md) |
| FR-INC-6 | 事件与仪表盘状态必须实时推送 | WebSocket `/ws/incidents/:id` 与 `/ws/dashboard`,握手鉴权 | 已实现 | [0019](./adr/0019-imbot-pluggable-degradation.md) |
| FR-INC-7 | 系统必须提供事件列表/详情/仪表盘与 NOC 只读大屏 | incidents、incident-detail、dashboard、wall 页面;状态可见(颜色 + 文字双编码) | 已实现 | [0034](./adr/0034-uiux-oncall-principles.md) |

### 5.9 FR-RBK Runbook

| 编号 | 需求 | 验收要点 | 状态 | 关联 ADR |
|------|------|---------|------|---------|
| FR-RBK-1 | Runbook 必须分 document 与 executable 两档 | document 纯 Markdown 供人参考;executable 步骤化,每步带 readonly 标志 | 已实现 | [0021](./adr/0021-runbook-two-tier.md) |
| FR-RBK-2 | 只读诊断可内置执行;**处置写操作必须人工确认**,默认不自动执行 | 写步骤默认 `require_approval:true`,**须人在 Web 确认**(IM 内只读步骤照常执行,写步骤恒阻断待审,**IM 不承载审批**);默认行为是展示给人参考,auto-run 仅限步骤全 readonly 且显式开启 | 已实现 | [0021](./adr/0021-runbook-two-tier.md) |
| FR-RBK-3 | 每次执行必须留痕,失败行为可配置 | 执行落 IncidentAction;失败按 on_failure(continue/abort/escalate)处理 | 已实现 | [0021](./adr/0021-runbook-two-tier.md) |
| FR-RBK-4 | 执行后端必须可扩展 | Executor 接口抽象(HTTP/内置诊断等),编译期注册,扩展方式见 [`extending.md`](./extending.md) | 已实现 | [0021](./adr/0021-runbook-two-tier.md) · [0009](./adr/0009-pluggable-integrations.md) |
| FR-RBK-5 | 执行所需凭据必须托管加密、执行时解密注入 | 凭据密文落库(AES-256-GCM),执行时注入,明文永不回显 | 已实现 | [0030](./adr/0030-integrations-encrypted-openapi.md) |

### 5.10 FR-AI AI Copilot

| 编号 | 需求 | 验收要点 | 状态 | 关联 ADR |
|------|------|---------|------|---------|
| FR-AI-1 | 所有 AI 产出必须统一承载并强制附 evidence,**无 evidence 不展示** | AIInsight(stage/confidence/evidence);低于置信度阈值(默认 0.6)不产出 | 已实现 | [0022](./adr/0022-aiinsight-hitl-evidence.md) · [0023](./adr/0023-llm-provider-cost-control.md) |
| FR-AI-2 | AI 建议必须人工确认才生效(HITL),确认动作记审计 | 状态机 suggested→accepted/rejected/applied;accept/reject 落审计;**不做「高置信度自动生效」** | 已实现 | [0022](./adr/0022-aiinsight-hitl-evidence.md) |
| FR-AI-3 | AI 必须横向覆盖分诊、诊断、处置建议、复盘起草四场景 | triage-ai / diagnose / runbook 建议 / 复盘草稿端点与产物均经 AIInsight | 已实现 | [0022](./adr/0022-aiinsight-hitl-evidence.md) · [0026](./adr/0026-postmortem-ai-draft.md) |
| FR-AI-4 | 系统必须提供相似事件检索与相似复盘检索 | pgvector 余弦距离主路径(embedding 懒计算回写);pgvector/Embed 不可用降级 LIKE 文本匹配;相似事件(`GET /incidents/:id/similar`)与相似复盘(`GET /incidents/:id/similar-postmortems`)为独立端点,前端相似列表连同复盘链接一并呈现属**规划中** | 已实现 | [0024](./adr/0024-similar-incident-pgvector.md) |
| FR-AI-5 | LLM 必须经 Provider 抽象接入且成本可控 | 支持 glm(默认)/ollama(本地,数据不出境);成本三闸:缓存→限流→配额(无 Redis 时降级透传) | 已实现 | [0023](./adr/0023-llm-provider-cost-control.md) |
| FR-AI-6 | AI 是增强而非核心链路,LLM 不可用时必须整体可降级 | LLM 失败仅 AI 功能降级,告警接入/升级/通知主流程不受影响 | 已实现 | [0023](./adr/0023-llm-provider-cost-control.md) |
| FR-AI-7 | AI 建议采纳情况必须可度量,供人工调优 | `/analytics/ai-feedback` 提供采纳率;调优是人工动作,不自动回训 | 已实现 | [0025](./adr/0025-no-auto-retrain.md) |

### 5.11 FR-PMR 复盘

| 编号 | 需求 | 验收要点 | 状态 | 关联 ADR |
|------|------|---------|------|---------|
| FR-PMR-1 | 复盘必须有状态机管理 | draft→in_review→published→archived | 已实现 | [0026](./adr/0026-postmortem-ai-draft.md) |
| FR-PMR-2 | critical 事件必须强制复盘,可显式跳过留痕 | resolved 后自动建 draft,不直接 closed;提供 skip-postmortem 显式跳过;warning 可配,info 不强制 | 已实现 | [0026](./adr/0026-postmortem-ai-draft.md) |
| FR-PMR-3 | 复盘草稿由 AI 起草 + 逐字段人工校对,来源可辨 | 三路合成(时间线 + AI 起草 + 元数据);每字段标注「AI 起草」、evidence 引用时间线;逐字段 accept/edit/reject;generated_by 记 ai/human/mixed | 已实现 | [0026](./adr/0026-postmortem-ai-draft.md) |
| FR-PMR-4 | 改进项(ActionItem)必须能外接工单系统跟踪 | 经通用 webhook 工单出口推送,回写 tracker_url;不做具体厂商 SDK | 已实现 | [0026](./adr/0026-postmortem-ai-draft.md) |
| FR-PMR-5 | published 复盘必须进知识库反哺相似事件检索 | published/archived 复盘可经 `GET /incidents/:id/similar-postmortems` 按向量相似度检索(前端相似列表连同复盘呈现属**规划中**,见 FR-AI-4) | 已实现 | [0024](./adr/0024-similar-incident-pgvector.md) · [0026](./adr/0026-postmortem-ai-draft.md) |

### 5.12 FR-RPT 报表分析

| 编号 | 需求 | 验收要点 | 状态 | 关联 ADR |
|------|------|---------|------|---------|
| FR-RPT-1 | 系统必须提供多维运营报表 | /analytics/{dashboard,incidents,alerts,trend,team-load,postmortems,ai-feedback} | 已实现 | —(无独立 ADR) |
| FR-RPT-2 | 报表必须预计算,不拖累在线查询 | MetricsSnapshot 定时聚合(Asynq low 队列 + 每小时快照) | 已实现 | [0007](./adr/0007-async-tasks-asynq.md) |
| FR-RPT-3 | 报表数据必须可导出 | alerts/incidents/team-load/postmortems 各自 export 端点(CSV) | 已实现 | —(无独立 ADR) |
| FR-RPT-4 | 降噪率与 AI 采纳率必须可持续度量(支撑成功指标验证) | Event→通知降噪率、AI 采纳率由 analytics 承载,运行期指标 | 已实现 | [0002](./adr/0002-product-positioning.md) · [0025](./adr/0025-no-auto-retrain.md) |

> 备注:**SLA 目标配置与逐单达成判定尚未立项**(无 ADR,当前 analytics 仅提供 MTTA/MTTR 均值等运营指标,无逐单判定),该期望记录于 [US-PM-09](./user-stories/project-manager.md#us-pm-09-规划中sla-目标与达成率报表)(规划中);若立项,按 FR-ADM-4 先例先立 ADR 再补「规划中」条目。

### 5.13 FR-SEC 认证 · RBAC · 审计

权限点清单以 [`internal/auth/permission.go`](../internal/auth/permission.go) 为唯一权威源,本文与 ADR 均不复制。

| 编号 | 需求 | 验收要点 | 状态 | 关联 ADR |
|------|------|---------|------|---------|
| FR-SEC-1 | 业务 API 必须以可撤销的 Bearer JWT 为唯一声明鉴权方案 | login/refresh/change-password;webhook token 与 IM 签名各有独立校验、不走 RBAC | 已实现 | [0033](./adr/0033-selfmon-and-auth.md) |
| FR-SEC-2 | 系统必须提供程序化访问凭证(API Key) | SHA256 只存哈希;scoped;由 org_admin 管理(`admin.apikey.manage`) | 已实现 | [0029](./adr/0029-dual-audit-no-silent-truncation.md) · [0033](./adr/0033-selfmon-and-auth.md) |
| FR-SEC-3 | RBAC 必须是「权限点系统枚举 + 角色自由组合」 | User—(RoleBinding,scope)—Role—Permission 三元组;scope 分 org/team,鉴权取**并集**;平台级保留权限仅限 org scope | 已实现 | [0027](./adr/0027-rbac-permissions-roles.md) |
| FR-SEC-4 | 系统必须提供内置角色,不可删、不可改 | org_admin/team_admin/responder/responder_lead/subscriber/oncall,builtin 标记;定制须参照内置角色权限集新建自定义角色 | 已实现 | [0027](./adr/0027-rbac-permissions-roles.md) |
| FR-SEC-5 | 审计必须双轨:管理审计与操作审计分开承载 | AuditLog(角色/集成/配置变更,查看需 `admin.audit.view`)+ IncidentAction(处置操作,带 via);鉴权拒绝记审计 | 已实现 | [0029](./adr/0029-dual-audit-no-silent-truncation.md) |
| FR-SEC-6 | 审计导出达到上限时**绝不静默截断** | CSV 单次上限 50000 行,达上限记 warn + 响应头 `X-Vigil-Truncated: true`;调用方按时间窗分段拉取 | 已实现 | [0029](./adr/0029-dual-audit-no-silent-truncation.md) |
| FR-SEC-7 | 登录必须有防护机制 | auth 模块内置登录防护(防暴力尝试) | 已实现 | [0033](./adr/0033-selfmon-and-auth.md) |

### 5.14 FR-INT 集成管理(工单 · 凭据 · webhook 订阅)

| 编号 | 需求 | 验收要点 | 状态 | 关联 ADR |
|------|------|---------|------|---------|
| FR-INT-1 | 系统必须提供出向 webhook,可订阅 Incident 生命周期事件 | WebhookSubscription CRUD;WebhookDelivery 送达记录可查、可重放 | 已实现 | [0030](./adr/0030-integrations-encrypted-openapi.md) |
| FR-INT-2 | 工单集成必须经通用 webhook 出口,不绑定具体厂商 | TicketIntegration CRUD + 回调端点回写;复盘 ActionItem 推送外部工单 | 已实现 | [0026](./adr/0026-postmortem-ai-draft.md) · [0030](./adr/0030-integrations-encrypted-openapi.md) |
| FR-INT-3 | 集成凭据必须加密托管,明文永不回显 | Credential CRUD;AES-256-GCM 密文落库;外部 KMS 为后置增强 | 已实现 | [0030](./adr/0030-integrations-encrypted-openapi.md) |
| FR-INT-4 | API 契约必须 code-first 单一权威源并对外可发现 | spec 由 handler 注解生成,统一 `/api/v1/`;`/openapi.yaml` + Swagger UI `/docs`;前端类型同 spec 派生,CI 防漂移 | 已实现 | [0030](./adr/0030-integrations-encrypted-openapi.md) |

### 5.15 FR-OBS 自监控可观测

| 编号 | 需求 | 验收要点 | 状态 | 关联 ADR |
|------|------|---------|------|---------|
| FR-OBS-1 | 系统必须暴露 Prometheus 指标 | `/metrics`:HTTP 量与延迟、接入量、事件/升级/通知计数、队列分状态(含死信 archived)、LLM 调用与 token 成本 | 已实现 | [0033](./adr/0033-selfmon-and-auth.md) |
| FR-OBS-2 | 系统必须提供健康检查端点 | `/health` | 已实现 | [0033](./adr/0033-selfmon-and-auth.md) |
| FR-OBS-3 | 系统必须能监控自身(吃狗粮),且不得假装闭环 | selfmon **默认关闭**;巡检队列积压/通知失败率,经**排除 IM 的独立通道**自告警 org_admin;独立通道未配置启动即 warn;失败率统计排除自告警防自激 | 已实现 | [0033](./adr/0033-selfmon-and-auth.md) |
| FR-OBS-4 | 日志与任务面板必须支撑排障 | 结构化日志贯穿 incident_id/event_id;Asynqmon 任务面板 | 已实现 | [0033](./adr/0033-selfmon-and-auth.md) |

### 5.16 FR-ADM 系统管理(用户 · 团队 · 数据生命周期)

| 编号 | 需求 | 验收要点 | 状态 | 关联 ADR |
|------|------|---------|------|---------|
| FR-ADM-1 | 系统必须提供用户全生命周期管理 | users CRUD + reset-password + IM 账号绑定维护 | 已实现 | —(无独立 ADR) |
| FR-ADM-2 | 团队是数据归属边界,支持嵌套但**树不继承权限** | Team 可嵌套(parent_team_id 仅组织展示);跨团队管理须显式 org scope 授权 | 已实现 | [0028](./adr/0028-single-org-soft-isolation.md) |
| FR-ADM-3 | Event/RawEvent 必须有保留期清理,且保护在办证据 | 默认 Event 90 天 / RawEvent 30 天(`<=0` 关闭);未 closed Incident 引用的 Event 超期也保留;分页批量删除 | 已实现 | [0039](./adr/0039-data-lifecycle.md) |
| FR-ADM-4 | Notification/WebhookDelivery/MetricsSnapshot/AuditLog 等的保留策略 | 已决策未实现(审计类 365 天先归档后删等);落地前上述表无界增长是显式接受的债 | **规划中** | [0039](./adr/0039-data-lifecycle.md) |
| FR-ADM-5 | Event 表按月分区(应对行数 >5000 万或清理跟不上写入) | 有触发条件的预案,非现状;走原生 SQL 迁移、ent 无感 | **规划中** | [0039](./adr/0039-data-lifecycle.md) |
| FR-ADM-6 | 运行时配置必须环境变量驱动(12-Factor) | 全部 `VIGIL_` 前缀;告警源/通道/IM/LLM 的启停与参数走配置 + 数据库 | 已实现 | [0031](./adr/0031-single-binary-compose-helm.md) · [0009](./adr/0009-pluggable-integrations.md) |

> ⚠️ 保留期默认值是产品立场而非合规承诺,**不存在「默认即合规」**;合规环境应自行上调审计类保留期([ADR-0039](./adr/0039-data-lifecycle.md))。
>
> ⚠️ **保留期 ≠ RPO**:FR-ADM-3 的「保留期」是业务数据的留存窗口(过期的旧 Event 删掉);NFR-DR-1 的「RPO」是灾难恢复点目标(备份失败丢多少数据)。两者维度不同,不要混淆。

## 六、非功能需求(NFR)

目标值可度量;实测数据的单一信源是 [operations.md §10 容量规划](./operations.md#10-容量规划压测方法与基线实测),本文不复制实测明细。

### 6.1 NFR-PERF 性能

| 编号 | 需求 | 目标值 | 达成现状 | 关联 |
|------|------|--------|---------|------|
| NFR-PERF-1 | 接入吞吐 | ≥ 1000 events/min | **已实测达标**,余量约 2 倍(实测明细见 operations.md §10) | [0002](./adr/0002-product-positioning.md) |
| NFR-PERF-2 | 通知送达延迟 | P95 < 5s | 设计目标,**未实测**(内部链路约 0.7s;通道投递待 IM 沙箱补测) | [0002](./adr/0002-product-positioning.md) · [0017](./adr/0017-notification-fallback-chain.md) |
| NFR-PERF-3 | 告警接收响应 | 秒级返回 202,接收阶段绝不同步处理 | 已实现(接收与处理解耦) | [0011](./adr/0011-ingestion-decoupled-idempotent.md) |

### 6.2 NFR-RELY 可靠性

| 编号 | 需求 | 目标 | 达成现状 | 关联 |
|------|------|------|---------|------|
| NFR-RELY-1 | 不丢告警 | 先落库再处理;背压 503 时 payload 仍落库,恢复后回灌 | 已实现 | [0011](./adr/0011-ingestion-decoupled-idempotent.md) |
| NFR-RELY-2 | 任务重复投递无副作用 | at-least-once 语义下全 handler 幂等(`esc:` / `notif:{id}` / `source_event_id` 三类键;新增任务必须显式设计幂等键) | 已实现 | [0007](./adr/0007-async-tasks-asynq.md) |
| NFR-RELY-3 | 失败不静默 | 死信可重放;通知整链失败兜底告警;升级链对账巡检自愈;unrouted critical 兜底通知 | 已实现 | [0016](./adr/0016-escalation-asynq-delayed.md) · [0017](./adr/0017-notification-fallback-chain.md) |
| NFR-RELY-4 | Redis 故障可降级 | 接入层先落 PostgreSQL 恢复后回灌;通知入队失败回退同步直投;Redis 持久化/HA 由部署方自备(非内置) | 已实现 | [0007](./adr/0007-async-tasks-asynq.md) · [0031](./adr/0031-single-binary-compose-helm.md) |
| NFR-RELY-5 | worker 崩溃可恢复 | 任务持久化于 Redis,重启自动恢复 | 已实现 | [0007](./adr/0007-async-tasks-asynq.md) |
| NFR-RELY-6 | AI 故障不伤主流程 | LLM 不可用时 AI 功能整体降级,接入/升级/通知不受影响 | 已实现 | [0023](./adr/0023-llm-provider-cost-control.md) |

### 6.3 NFR-SEC 安全

| 编号 | 需求 | 目标 | 达成现状 | 关联 |
|------|------|------|---------|------|
| NFR-SEC-1 | 鉴权无后门 | 业务 API 唯一声明方案 Bearer JWT;IM 操作同 Web RBAC;测试用回退开关(`VIGIL_AUTH_HEADER_FALLBACK`/`VIGIL_TEST_ENDPOINTS_ENABLED`)**默认关闭**,production 无条件强制关闭,开启时打印 SECURITY WARN | 已实现 | [0033](./adr/0033-selfmon-and-auth.md) · [0018](./adr/0018-im-same-rbac-as-web.md) |
| NFR-SEC-2 | 敏感凭据不落明文 | 凭据 AES-256-GCM 加密入库、明文永不回显;API Key 只存哈希 | 已实现 | [0030](./adr/0030-integrations-encrypted-openapi.md) |
| NFR-SEC-3 | 写操作不碰生产 | Runbook 写步骤须人确认(HITL);不做无人值守自动修复 | 已实现 | [0021](./adr/0021-runbook-two-tier.md) |
| NFR-SEC-4 | 防探测 | 接入鉴权失败 401 不落库但记审计 | 已实现 | [0011](./adr/0011-ingestion-decoupled-idempotent.md) |
| NFR-SEC-5 | 入向 SMTP 网络面收敛 | SMTP 端口仅内网可达(部署要求,公网暴露属错误部署);默认关闭 | 已实现(部署约束见 operations.md) | [0038](./adr/0038-smtp-inbound.md) |

### 6.4 NFR-DEP 部署

| 编号 | 需求 | 目标 | 达成现状 | 关联 |
|------|------|------|---------|------|
| NFR-DEP-1 | 部署门槛(**硬指标**) | Docker Compose 一键起步,3 容器(vigil + postgres + redis) | 已落地 | [0031](./adr/0031-single-binary-compose-helm.md) |
| NFR-DEP-2 | 交付形态 | 单二进制(前端与 spec 编译期 embed),「一个镜像 + 两个依赖」 | 已实现 | [0031](./adr/0031-single-binary-compose-helm.md) |
| NFR-DEP-3 | 生产集群部署 | Helm Chart 可部署;迁移经 pre-install/pre-upgrade hook Job 自动执行 | **部分**:已验证单进程单副本;api/worker 拆分与多副本(WS 广播走 Redis pub/sub,代码已实现)未端到端验证,属路线图 | [0031](./adr/0031-single-binary-compose-helm.md) |
| NFR-DEP-4 | 存储前置 | PostgreSQL 需 pgvector 扩展(推荐 `pgvector/pgvector:pg16`);不可用时相似检索降级 LIKE,主流程不受影响 | 已实现 | [0006](./adr/0006-primary-store-postgresql.md) · [0024](./adr/0024-similar-incident-pgvector.md) |
| NFR-DEP-5 | 数据自主 | 全自托管,数据不出企业网;AI 可选 ollama 本地推理(数据不出境) | 已实现 | [0002](./adr/0002-product-positioning.md) · [0023](./adr/0023-llm-provider-cost-control.md) |

### 6.5 NFR-OBS 可观测

| 编号 | 需求 | 目标 | 达成现状 | 关联 |
|------|------|------|---------|------|
| NFR-OBS-1 | 可被外部监控 | `/metrics` + `/health` 满足外部监控接入(必抓指标与告警规则见 [operations.md §9](./operations.md#9-外部监控接入谁来监控守夜人)) | 已实现 | [0033](./adr/0033-selfmon-and-auth.md) |
| NFR-OBS-2 | 自监控有边界、不自欺 | 三红线:自告警走排除 IM 的独立通道;统计排除自告警防自激;独立通道未配置启动即 warn。selfmon 与进程共生死,**必须外部监控兜底** | 已实现 | [0033](./adr/0033-selfmon-and-auth.md) |
| NFR-OBS-3 | 全链路可追踪 | 结构化日志贯穿 incident_id/event_id | 已实现 | [0033](./adr/0033-selfmon-and-auth.md) |

### 6.6 NFR-I18N 国际化

| 编号 | 需求 | 目标 | 达成现状 | 关联 |
|------|------|------|---------|------|
| NFR-I18N-1 | 中文优先、全站双语 | i18next 全站中/英覆盖 | 已实现(前端全站 i18n) | [0034](./adr/0034-uiux-oncall-principles.md) |
| NFR-I18N-2 | 时区正确性 | 排班按 Schedule timezone 计算;免打扰按 quiet_hours 规则配置的时区计算(规则级配置,非接收人个人时区,见 FR-NTF-5) | 已实现 | [0015](./adr/0015-schedule-realtime-no-snapshot.md) · [0017](./adr/0017-notification-fallback-chain.md) |

**体验与可达性基线**(不另设编号域,单一信源为 [ADR-0034](./adr/0034-uiux-oncall-principles.md)):所有 UI/UX 决策须通过四原则自检——**半夜能用 · 一屏决策 · 降噪优先 · 状态可见**;五语义色带文字标签双编码(WCAG AA、色盲可达,已实现);核心响应页(事件列表/详情)暗色 + 夜间(22:00–07:00)首访强引导已实现——亮色默认、主题偏好本地持久化、暗色仅在核心响应页生效(非全站暗色),`/wall` 大屏保持固定深色;移动端为响应式 Web + PWA,**不做原生 App**。

### 6.7 NFR-COMP 兼容与升级

| 编号 | 需求 | 目标 | 达成现状 | 关联 |
|------|------|------|---------|------|
| NFR-COMP-1 | 升级可回滚 | Atlas 版本化迁移(`atlas_schema_revisions` 幂等追踪);**不做逆向迁移**,回滚 = 备份恢复(升级前备份是回滚的唯一前提;回滚会丢备份点之后的数据) | 已实现 | [0032](./adr/0032-migration-backup-restore.md) |
| NFR-COMP-2 | API 契约不漂移 | REST 统一 `/api/v1/`;spec 单一权威源为 handler 注解,前端类型同源派生,CI 校验无漂移 | 已实现 | [0030](./adr/0030-integrations-encrypted-openapi.md) |
| NFR-COMP-3 | 集群升级免手工迁移 | Helm 升级经 pre-install/pre-upgrade hook Job 自动执行迁移 | 已实现 | [0031](./adr/0031-single-binary-compose-helm.md) · [0032](./adr/0032-migration-backup-restore.md) |
| NFR-COMP-4 | 扩展不动核心 | 五类扩展点(告警源/通知通道/执行器/LLM/IM)为「Go 接口 + 编译期注册」——新增实现需改代码重编译(非运行时插件),第三方以 fork/PR 落地;触点清单见 [`extending.md`](./extending.md) | 已实现 | [0009](./adr/0009-pluggable-integrations.md) |

### 6.8 NFR-DR 平台自身灾备与可用性边界

平台自己挂了怎么办、能容忍丢多少数据——评估采用时必答的三问,在此给出统一口径(操作细节见 [operations.md §5](./operations.md#5-回滚-备份恢复)):

| 编号 | 需求 | 目标 | 达成现状 | 关联 |
|------|------|------|---------|------|
| NFR-DR-1 | 数据恢复点目标(RPO) | **RPO = 备份周期**(不做逆向迁移,恢复即回到备份点,丢备份点之后的数据,见 NFR-COMP-1)。产品默认立场:`scripts/backup.sh` 挂 cron **至少每日一备**([operations.md §5.1](./operations.md#51-备份挂-cron) 示例即每日),升级前必须额外做一次全量备份;合规环境按自身 RPO 要求上调频率 | 备份/恢复脚本与 cron 指引已实现;**实际 RPO 取决于部署方配置的备份周期**,平台不强制 | [0032](./adr/0032-migration-backup-restore.md) |
| NFR-DR-2 | 数据恢复时间目标(RTO) | 单机 Compose 形态整库恢复(stop → restore → 部署旧版本 → start)**≤ 1h**;以恢复演练验证并记录耗时(演练清单见 [operations.md §5.3](./operations.md#53-恢复演练清单)) | 设计目标;已按 [US-ARC-09](./user-stories/architect.md#us-arc-09-版本升级演练备份即回滚没有侥幸路径) 演练方法在预生产实测约 40 分钟,实际耗时随数据量与环境变化,部署方须自行演练确认 | [0032](./adr/0032-migration-backup-restore.md) |
| NFR-DR-3 | 平台自身可用性边界 | 当前已验证形态为**单进程单副本,不承诺可用性 SLO**;selfmon 与进程共生死(NFR-OBS-2),平台自身故障须**外部监控发现(NFR-OBS-1,[operations.md §9](./operations.md#9-外部监控接入谁来监控守夜人))+ 快速重启恢复**兜底;api/worker 拆分与多副本属路线图(NFR-DEP-3) | 现行边界如实声明(显式接受,非缺陷);任务与队列状态持久化于 Redis/PostgreSQL,重启不丢任务(NFR-RELY-5) | [0031](./adr/0031-single-binary-compose-helm.md) · [0033](./adr/0033-selfmon-and-auth.md) |

## 七、非目标(Non-Goals,现行口径)

### 7.1 一贯非目标(源 [ADR-0002](./adr/0002-product-positioning.md))

| 不做什么 | 边界说明 |
|---------|---------|
| 监控采集 / APM / 日志 | 定位为告警**消费者**,不与监控平台竞争 |
| SOC / SOAR 安全响应 | 领域不同,不扩张 |
| SaaS 硬多租户 | 采用单组织多团队**软隔离**([ADR-0028](./adr/0028-single-org-soft-isolation.md)),不承诺物理隔离 |
| Open Core 付费墙 / 版本裁剪 | 能力不做商业裁剪 |
| 自建全球电话网络 | 不内置电话/短信通道;电话强提醒经 webhook 出口对接自建语音网关 |
| 无人值守自动修复 | 坚持 human-in-the-loop,写操作不碰生产 |

### 7.2 已裁剪能力(墓碑,恢复须新增 ADR 并标 Superseded)

| 已移除 | 溯源 |
|--------|------|
| 作战室(War Room,自动建群/拉人/升级联动入群) | [ADR-0036](./adr/0036-remove-war-room.md);协同由「工作群 + 交互卡片 + 实时刷新」承载 |
| 电话 / SMS 通知通道 | [ADR-0037](./adr/0037-trim-deferred-features.md);默认降级链收敛为 `[webhook(若配置)]→im→email` |
| 企业微信(wecom) | [ADR-0037](./adr/0037-trim-deferred-features.md);IM 平台矩阵收敛为飞书 + 钉钉 |
| Jira / 禅道工单 SDK 占位 | [ADR-0037](./adr/0037-trim-deferred-features.md);工单只保留通用 webhook 出口 |
| Zabbix / 云监控接入类型占位 | [ADR-0037](./adr/0037-trim-deferred-features.md);存量迁移转 webhook |

裁剪原则:「不欺骗用户」——界面里每个选项必须真实可用。

## 八、验收与度量

每类需求的验证方式(不复制实测数据,指向各自单一信源):

| 需求类别 | 验证方式 | 信源 |
|---------|---------|------|
| NFR-PERF(吞吐/延迟) | 压测脚本 + `/metrics` 回归 | [operations.md §10](./operations.md#10-容量规划压测方法与基线实测) |
| 降噪率 / MTTR(成功指标) | 运行期业务指标,持续度量(FR-RPT-4),无法以合成压测断言 | `/analytics/*` |
| 功能流程(接入→分诊→升级→通知→处置) | e2e 集成测试(Ginkgo,`make test-e2e`)+ 前端全栈 e2e | `test/e2e/` + [ADR-0035](./adr/0035-dev-workflow-gates.md) |
| 部署门槛 | Compose 一键起步实操 | [operations.md §2](./operations.md#2-docker-compose-部署默认) |
| 安全红线(RBAC/HITL/审计) | e2e 授权隔离用例 + 审计留痕核查 | `test/e2e/` · [ADR-0027](./adr/0027-rbac-permissions-roles.md) |

## 九、追溯说明

FR/NFR、用户故事、ADR 三者的互查机制:

1. **需求 ↔ 用户故事**:共用同一编号体系。[`user-stories/`](./user-stories/) 中每条故事(US-OPS-xx / US-ARC-xx / US-PM-xx / US-DEV-xx)在「关联需求」字段引用本文的 FR/NFR 编号;从需求反查故事以故事文档内的引用为准,本文不维护反向清单(避免双向维护漂移)。
2. **需求 ↔ ADR**:每条 FR/NFR 的「关联 ADR」列是决策溯源入口;完整 ADR 索引见 [`adr/README.md`](./adr/README.md)。
3. **需求 ↔ 实现**:实现状态列以代码盘点为据;实体字段查 [`ent/schema/`](../ent/schema/),权限点查 [`internal/auth/permission.go`](../internal/auth/permission.go),API 面貌查 `/openapi.yaml`(code-first 生成,[ADR-0030](./adr/0030-integrations-encrypted-openapi.md))。
4. **需求变更**:先经 ADR 裁决(新增一份,不改写旧 ADR),再回写本文为现行口径;「规划中」条目落地后同步改状态。
