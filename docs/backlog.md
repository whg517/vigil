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

## 📋 待规划（后续版本考虑）

> 以下为 PRD 设计目标但当前未排期的事项，列入以备规划。详细说明见各能力域文档的"开放问题"。

### 来自用户旅程完整性评估（2026-07-03）

| 项 | 出处 | 当前状态 | 说明 |
|----|------|----------|------|
| **subscriber / 团队 Leader 独立旅程** | personas.md P1-4 | 后端已覆盖，缺 UI | 订阅 Incident 后端已落地（T4.4，`f3cae2a`，`GET\|POST\|DELETE /subscriptions` 自管 + min_severity + 定向通知复用 T2.2）；团队看板/复盘质量跟进的前端页面待补（Phase 2 B 项） |
| **平台工程师 / API 消费者旅程** | personas.md P1-3 | 部分覆盖 | APIKey 创建已覆盖（B.1）+ 开放 API `POST /api/v1/events` 投递已落地（T5.1）；仍缺 webhook 出站**动态订阅 CRUD**（当前仅 `VIGIL_WEBHOOK_OUT_URLS` 环境变量，出站签名/死信/重放已由 T5.2 落地）、IaC/Terraform；集成向导 M14.6 后端辅助 `config-template` 端点已落地（T6.2），分步 wizard UI 待补 |
| **维护窗口 / 抑制操作流（独立旅程）** | 能力域 3 M3.2 | 配置在 B.7 | 抑制规则 `expires_at` 可设/清除并生效已落地（T2.4）；缺"为计划内变更立维护窗 → 自动到期"的独立操作流叙述 |
| **AI 反馈改进闭环** | 能力域 11 M11.5 | 部分覆盖 | accept/reject + 相似检索修复 + 反馈指标可查已落地（T3.4，`adc8385`）；**噪声学习自动闭环**（自动回训/自动生成 suppression 规则）仍未做——当前是「记录 + 可查」，不自动改变后续 AI 行为（Phase 2 C 项） |

### 既有项（前轮已记录）

| 项 | 出处 | 当前状态 | 说明 |
|----|------|----------|------|
| 跨团队 @人 → 事件级临时授权 + 关闭自动失效 | PRD M8.3 / data-model §5.6 | 🟡 部分（保留） | `AddResponder` 仅加入 responders 名单，**事件级临时授权端点仍未做**（不发临时 RoleBinding、关闭不自动失效）。保留为待做（Phase 2 C 项）。详见 user-journeys.md C.3.4 |
| IM 斜杠命令全量 | PRD M8.5 | 📋 部分（保留） | 部分命令已实现，runbook/oncall 等全量待补（Phase 2 D 项） |
| 首次部署向导（first-run wizard） | 待讨论 | 📋 无（保留） | 当前靠环境变量 + 种子超管（Phase 2 E 项） |
| **用户禁用自动交接提示** | 能力域 13 M13.1 | 🟡 部分（保留） | 鉴权侧已即时失效（T0.3）+ oncall 解算跳过禁用用户（T2.3），但仍不主动提示待交接的排班/Action Item（Phase 2 E 项）。详见 user-journeys.md B.14 |
| **migrate-down / 回滚** | H1.4 | ❌ 无（保留） | 无；回滚靠备份恢复（Phase 2 E 项）。详见 user-journeys.md D.1 |
| **webhook 出站动态订阅 CRUD** | 能力域 14 / personas P1-3 | 📋 无（保留） | 当前出站 URL 靠 `VIGIL_WEBHOOK_OUT_URLS` 环境变量（全局静态），缺 per-订阅动态管理端点（Phase 2 D 项）。注：出站签名 + 死信 + 重放已由 T5.2 落地 |
| ~~复盘 resolve 自动触发起草（critical 强制）~~ | PRD M12.7 | ✅ 已完成（`9f49f77`，T4.1） | `postmortem/engine.go` `OnIncidentResolved` 订阅 IncidentResolved：critical 强制自动起草、warning 可配、info 不起草；不再需手动调 draft |
| ~~未路由事件重路由端点~~ | 能力域 4 M4.3 | ✅ 已完成（`91143d5`，T2.4） | 新增 `POST /events/:id/reroute`（`triage/handler.go` + `Engine.Reroute`，权限 `service.route_override` 团队软隔离），可对已 unrouted Event 改派/重路由 |
| ~~排班/升级引擎解算 oncall 不查 User.status~~ | 能力域 5/6 · 审计 B21/C4 | ✅ 已完成（T2.3/T2.4） | `schedule/engine.go:105/170/241` 与 `escalation/engine.go:304` 均已加 `user.StatusEQ(user.StatusActive)` 过滤，禁用用户不再被解算为 oncall；空班检测记 metric+Warn+告警 |
| ~~报表/审计导出端点~~ | 能力域 15 / 13 M13.5 | ✅ 已完成（`2fbea67`，T6.1） | 新增 `GET /analytics/{alerts\|incidents\|team-load\|postmortems}/export` CSV 导出（`analytics/export.go`，复用 `analytics.view` + team 软隔离）。注：audit-logs 导出仍未做（如需可后续补） |
| ~~多副本 WebSocket pub/sub~~ | architecture §7 | ✅ 已完成（`014a0d0`，T6.4） | `internal/ws/pubsub.go` + `hub.go`：多副本 Redis pub/sub 跨副本广播，单副本退化 + 跨副本去重 |
| ~~Action Item 自动建工单 + 状态回写（M14.2）~~ | 能力域 12 §5 / 能力域 14 | ✅ 已完成（`466a01f`，T4.3） | 新增 `TicketIntegration` 实体 + `internal/ticket` 包：复盘发布经 `OnPostmortemPublished` 为未建单 ActionItem 建外部工单回写 tracker_url（通用 webhook + Jira/禅道预留适配器）、ActionItem→done 单向同步；`due_date` API 已暴露（T2.5）。**注**：工单侧反向回写仍单向未做（Phase 2 C 项）；完整 Jira/禅道 SDK 待补（Phase 2 D 项） |
| ~~analytics 报表团队 scope 隔离~~ | 能力域 11 / 审计 S14 | ✅ 已完成（T0.7，`4e0ba13`） | `analytics/engine.go` 已引入 `Scope` 结构（`OrgWide`/`TeamIDs`）按 team 过滤各维度查询（event→service→team、incident→team、postmortem→incident→team、aiinsight→incident→team），team scope 但无可见 team 时返空指标；6 个 `/analytics/*` 端点挂 `analytics.view` + team 软隔离 |
