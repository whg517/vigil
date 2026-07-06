# Backlog（暂不做 / 待规划）

> 本文件记录**已评估并明确推迟**的需求项。每条注明：出处（PRD 需求 ID）、推迟原因、
> 现状（代码层面的保留物）、未来重启时的前置条件。
>
> 与各文档"开放问题"的区别：开放问题是**待讨论的决定**；本文件是**已决定推迟**的事项。
>
> 状态约定：🚧 暂不做（明确推迟） · 📋 待规划（纳入后续版本考虑）

---

## 🚧 作战室（War Room）相关 —— 现阶段不做

**影响范围**：PRD 能力域 8（IM 协同）的 M8.2 / M8.9，以及能力域 10（时间线）依赖作战室的 M10.5。

| PRD 需求 ID | 描述 | 状态 |
|-------------|------|------|
| **M8.2** | 一键作战室：Incident 触发时自动建临时 IM 群、拉相关人、置顶事件信息；升级到新层级自动把新 oncall 拉入群 | 🚧 暂不做 |
| **M8.9** | 作战室归档：事件关闭后保留作战室聊天记录，关联到复盘 | 🚧 暂不做（依赖 M8.2） |
| **M10.5** | IM 消息可选捕获：作战室关键消息回写时间线 | 🚧 暂不做（依赖 M8.2） |

### 推迟原因

1. **编排复杂度高，收益对当前阶段非必需**：自动建群 / 自动邀人 / 升级联动入群 / 关闭归档是一整套跨 IM 平台与业务事件的编排，落地成本高；当前阶段的协同诉求用「现有工作群 + 交互卡片（M8.1）+ 卡片实时刷新（M8.4）」已能满足。
2. **平台能力差异大**：飞书 / 钉钉 / 企微在「自动建群、群成员 API、消息回写」上能力参差，需先做技术 PoC 才能定边界（PRD §4.8 已标此项为风险点）。

### 现状（代码层面的保留物）

- ✅ adapter 层原语**保留**：飞书（`internal/im/feishu/adapter.go` `CreateChat`）、钉钉（`internal/im/dingtalk/adapter.go` `CreateChat`）的建群 API 调用已实现，企微为 `NoopBot` 占位。
- ❌ live path **未接**：Incident 触发 / 升级 / 关闭的事件链路均不调用 `CreateWarRoom`；没有自动建群、自动邀人、归档的编排代码。
- ❌ 数据模型字段 `Incident.war_room`（见 data-model §3.3）当前无写入。

### 当前替代方案（重启前用什么）

告警协同走 **M8.1 交互卡片 + M8.4 状态实时刷新**：卡片发到团队已有工作群，ack / 升级 / 解决都在卡片按钮完成，状态变更原地刷新卡片，群内所有人看到一致状态。

### 重启前置条件

- IM 平台建群 / 群成员 / 消息回写 API 的技术 PoC 完成，能力边界明确。
- M8.2 自动建群编排设计评审通过。
- 复盘（能力域 12）与 M8.9 归档、M10.5 消息回写的联动方案确定。

---

## 🚧 首次部署向导 / 企微完整 bot / 工单 SDK —— 现阶段不做

**影响范围**：H1（部署 onboarding）、能力域 8（IM 协同 · 企微适配器）、能力域 14（工单集成 · Jira/禅道 SDK）。三项均已评估并明确推迟（2026-07-06）。

| 项 | 出处 | 状态 |
|----|------|------|
| **首次部署向导（first-run wizard）** | 待讨论 / H1 | 🚧 暂不做 |
| **企微完整 bot 适配器** | 能力域 8 / PRD §4.8 | 🚧 暂不做 |
| **Jira / 禅道具体 SDK 适配器** | 能力域 14 M14.2 | 🚧 暂不做 |

### 推迟原因

1. **首次部署向导**：当前「环境变量 + 种子超管 `admin/changeme` + 强制首登改密」已能完成初始化；分步 web 向导（建组织/团队/首个服务/引导接入源）是纯 onboarding UX 大件，与 first-run 状态判定耦合，收益对当前阶段非必需，需产品设计评审。
2. **企微完整 bot**：飞书/钉钉已完整支持（卡片/mention/更新降级）；企微在应用配置、消息卡片、群成员 API 与授权模型上能力差异大，需先做企微应用注册 + 能力 PoC 才能定边界（与作战室同一 PoC 风险点，PRD §4.8）。
3. **Jira/禅道 SDK**：通用 webhook 工单适配器已能覆盖「自研工单系统 / 能收 webhook 的 Jira-禅道自动化」；完整 REST SDK（认证/字段映射/项目发现/双向回写）体量大、需真实实例联调，当前 ROI 不足。

### 现状（代码层面的保留物）

- 首次部署：`internal/server/wire.go` `SeedBuiltinRoles`/`SeedDefaultAdmin` 幂等种子 + `must_change_password` 强制改密链路完整；无 web 向导页。
- 企微：`NoopBot` 占位，`Available()==false` 被通知链排除、**不静默丢告警**（走邮件/电话/短信兜底）；adapter 接口已定义，补实现即可接入。
- 工单：`internal/ticket/adapter.go` 已有 `Adapter` 接口 + 通用 webhook 适配器 + `NewJiraAdapter`/`NewZentaoAdapter` 占位（返回 `ErrAdapterNotImplemented`，建单降级不阻断）；替换占位适配器即可，Engine 装配与建单触发链路不变。

### 当前替代方案（重启前用什么）

- 首次部署：文档化的 env + 种子超管 + Swagger/Web 手动建组织结构。
- 企微：邮件/电话/短信/webhook 兜底通道 + 飞书/钉钉 IM 卡片。
- 工单：通用 webhook 工单（POST 可配 URL、payload 含 ActionItem、SSRF 防护）+ 手填/回写 tracker_url。

### 重启前置条件

- 首次部署向导：onboarding 流程 UX 设计评审通过；first-run 状态判定（是否已初始化）方案确定。
- 企微 bot：企微应用注册 + 消息卡片/群 API 能力 PoC 完成。
- Jira/禅道 SDK：目标实例可联调，认证/字段映射方案确定。

---

## 📋 待规划（后续版本考虑）

> 以下为 PRD 设计目标但当前未排期的事项，列入以备规划。详细说明见各能力域文档的"开放问题"。

### 来自用户旅程完整性评估（2026-07-03）

| 项 | 出处 | 当前状态 | 说明 |
|----|------|----------|------|
| **subscriber / 团队 Leader 独立旅程** | personas.md P1-4 | 后端已覆盖，缺 UI | 订阅 Incident 后端已落地（T4.4，`f3cae2a`，`GET\|POST\|DELETE /subscriptions` 自管 + min_severity + 定向通知复用 T2.2）；团队看板/复盘质量跟进的前端页面待补（Phase 2 B 项） |
| **平台工程师 / API 消费者旅程** | personas.md P1-3 | 部分覆盖 | APIKey 创建已覆盖（B.1）+ 开放 API `POST /api/v1/events` 投递已落地（T5.1）；webhook 出站**动态订阅 CRUD** 已落地（N2.2，`5849755`）、出站签名/死信/重放（T5.2）；集成向导 M14.6 后端 `config-template`（T6.2）+ **分步 wizard UI 已落地**（P3.1，`5ae441a`，4 步向导：选类型→配置→生成接入信息→验证）；仍缺 IaC/Terraform |
| **维护窗口 / 抑制操作流（独立旅程）** | 能力域 3 M3.2 | 配置在 B.7 | 抑制规则 `expires_at` 可设/清除并生效已落地（T2.4）；缺"为计划内变更立维护窗 → 自动到期"的独立操作流叙述 |
| **AI 反馈改进闭环** | 能力域 11 M11.5 | 大部覆盖 | accept/reject + 相似检索修复 + 反馈指标可查（T3.4，`adc8385`）；**噪声学习「建议→规则沉淀」已落地**（N1.4，`e96c966`：`noise_suggestion` 建议 accept → 沉淀为 `source=ai` 的 SuppressionRule，下次自动抑制，team_admin 可撤）。仍未做：**模型自动回训**（当前是「AI 建议 + 人工确认沉淀规则」，非无监督学习） |

### 既有项（前轮已记录）

| 项 | 出处 | 当前状态 | 说明 |
|----|------|----------|------|
| ~~跨团队 @人 → 事件级临时授权 + 关闭自动失效~~ | PRD M8.3 / data-model §5.6 | ✅ 已完成（`bde581c`，N1.2） | `AddResponder` 拉人时经 `ResponderGranter`：被拉人对该 incident team 无 `incident.ack` 权限则自动发 team scope 的 responder 临时 RoleBinding（`expires_at` 默认 24h 兜底 + `source_incident_id` 标记来源）；`IncidentClosed/Resolved/Merged` 时按来源精确撤销，过期作兜底；team scope 不放宽软隔离、authz 实时失效；发放/撤销落审计 |
| ~~IM 斜杠命令全量~~ | PRD M8.5 | ✅ 已完成（`5849755`，N2.1） | `/vigil ack\|escalate\|resolve\|status\|add`（已有）+ 本轮补 `/vigil runbook <rb> <inc>`（两档安全，写操作 IM 内不放行只提示 Web 审批）与 `/vigil oncall [service\|team]`（查值班）；命令全量覆盖 |
| 首次部署向导（first-run wizard） | 待讨论 / H1 | 🚧 暂不做 | 已明确推迟，见上「🚧 首次部署向导 / 企微完整 bot / 工单 SDK」节 |
| ~~用户禁用自动交接提示~~ | 能力域 13 M13.1 | ✅ 已完成（`b4749e9`，N2.3） | 鉴权侧即时失效（T0.3）+ oncall 解算跳过禁用用户（T2.3）；本轮补 `GET /users/:id/handover-preview` 返回待交接四类（参与排班/owner 未完成 ActionItem/未过期 RoleBinding/IM 绑定），`PATCH` 禁用时有待交接项则响应附 `handover` 提示（不阻断）。详见 user-journeys.md B.14 |
| **migrate-down / 回滚** | H1.4 | ❌ 无（保留） | 无；回滚靠备份恢复（Phase 2 E 项）。详见 user-journeys.md D.1 |
| ~~webhook 出站动态订阅 CRUD~~ | 能力域 14 / personas P1-3 | ✅ 已完成（N2.2） | 新增 `WebhookSubscription` 实体 + `internal/webhook/subscription_handler.go`：`GET/POST/GET:id/PATCH/DELETE /webhook-subscriptions`（权限 `webhook_subscription.{view,create,update,delete}`，团队软隔离）。dispatcher 出站时合并 env 静态订阅（`VIGIL_WEBHOOK_OUT_URLS`）+ DB 动态订阅（`EntSubscriptionResolver`，按 `event_types` 过滤、每订阅独立 `signing_secret` 加密存储/出站前解密、同 URL 去重）。向后兼容 env（无解析器时退化为仅 env）。出站签名 + 死信 + 重放仍由 T5.2 复用 |
| ~~复盘 resolve 自动触发起草（critical 强制）~~ | PRD M12.7 | ✅ 已完成（`9f49f77`，T4.1） | `postmortem/engine.go` `OnIncidentResolved` 订阅 IncidentResolved：critical 强制自动起草、warning 可配、info 不起草；不再需手动调 draft |
| ~~未路由事件重路由端点~~ | 能力域 4 M4.3 | ✅ 已完成（`91143d5`，T2.4） | 新增 `POST /events/:id/reroute`（`triage/handler.go` + `Engine.Reroute`，权限 `service.route_override` 团队软隔离），可对已 unrouted Event 改派/重路由 |
| ~~排班/升级引擎解算 oncall 不查 User.status~~ | 能力域 5/6 · 审计 B21/C4 | ✅ 已完成（T2.3/T2.4） | `schedule/engine.go:105/170/241` 与 `escalation/engine.go:304` 均已加 `user.StatusEQ(user.StatusActive)` 过滤，禁用用户不再被解算为 oncall；空班检测记 metric+Warn+告警 |
| ~~报表/审计导出端点~~ | 能力域 15 / 13 M13.5 | ✅ 已完成（`2fbea67`，T6.1） | 新增 `GET /analytics/{alerts\|incidents\|team-load\|postmortems}/export` CSV 导出（`analytics/export.go`，复用 `analytics.view` + team 软隔离）。注：audit-logs 导出仍未做（如需可后续补） |
| ~~多副本 WebSocket pub/sub~~ | architecture §7 | ✅ 已完成（`014a0d0`，T6.4） | `internal/ws/pubsub.go` + `hub.go`：多副本 Redis pub/sub 跨副本广播，单副本退化 + 跨副本去重 |
| ~~Action Item 自动建工单 + 状态回写（M14.2）~~ | 能力域 12 §5 / 能力域 14 | ✅ 已完成（`466a01f`，T4.3） | 新增 `TicketIntegration` 实体 + `internal/ticket` 包：复盘发布经 `OnPostmortemPublished` 为未建单 ActionItem 建外部工单回写 tracker_url（通用 webhook + Jira/禅道预留适配器）、ActionItem→done 单向同步；`due_date` API 已暴露（T2.5）。**注**：工单侧反向回写已落地（N1.3，`e96c966`：`POST /webhooks/ticket/:id` HMAC 验签回调 → 更新 ActionItem 状态）；完整 Jira/禅道 SDK 已明确推迟（见「🚧 首次部署向导 / 企微完整 bot / 工单 SDK」节，通用 webhook 已可用） |
| ~~analytics 报表团队 scope 隔离~~ | 能力域 11 / 审计 S14 | ✅ 已完成（T0.7，`4e0ba13`） | `analytics/engine.go` 已引入 `Scope` 结构（`OrgWide`/`TeamIDs`）按 team 过滤各维度查询（event→service→team、incident→team、postmortem→incident→team、aiinsight→incident→team），team scope 但无可见 team 时返空指标；6 个 `/analytics/*` 端点挂 `analytics.view` + team 软隔离 |
