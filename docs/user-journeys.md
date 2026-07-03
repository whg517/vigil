# 用户旅程与操作流程

> 本文档基于 `PRD.md`（15+2 能力域）、`architecture.md`（6 大引擎）、`data-model.md`（RBAC + 实体）、
> `capabilities/`（能力域详设）、`deployment.md` / `local-dev.md` / `personas.md` 综合整理，
> 描述 Vigil 在六条典型旅程（A 部署 / B 配置 / C 处置 / D 保障 / E 订阅 / F 集成）下的端到端操作流程。
>
> **阅读对象**：产品/设计/测试/QA，用于评审交互流程、编写测试用例、对照验收标准。
>
> **状态**：Draft v0.3（2026-07-03）。文档为 PRD 的"动起来"投影，需求变更以 PRD 为准。

---

## ⚠️ 实现状态约定（务必先读）

本文档描述的是**目标产品行为**（来自 PRD/架构/能力域设计），**不等于当前代码已全部实现**。
本次 v0.3 已对照 2026-07-03 源码（`internal/` / `ent/schema/` / `web/src/` / `deploy/`）**逐项全面核实**，
状态标注以代码实况为准；与能力域设计冲突处采用「当前行为 / 设计目标（📋）」双段式标注（核对结论清单见
[`audit/journey-code-audit-2026-07-03.md`](./audit/journey-code-audit-2026-07-03.md)）。
阅读时请注意每节的内联标记，以及下方的功能状态总表：

- ✅ 已实现：代码已落地，可据此编写验收/测试用例。
- 🟡 部分实现：核心逻辑在，但某个编排/触发环节缺失；用例需标注前置条件。
- 🚧 暂不做：设计保留，当前版本明确不做（见下表"备注"）。
- 📋 未实现：PRD 设计目标，未排期，作为 backlog。
- ❌ 不存在：端点/功能在代码中不存在（多用于强调文档或权限矩阵曾断言"有"而实际没有）；与 📋 的区别：📋 是"设计了但未排期"，❌ 是"当前就是没有，勿按存在编写用例"。

### 功能状态总表（对照源码核对，2026-07-03）

| 功能 | 状态 | 说明 / 备注（关键限制） |
|------|------|-------------|
| **· 接入与分诊 ·** | | |
| 接入 + 归一化流水线 | 🟡 | **仅 webhook ✅**（`POST /api/v1/webhook/{token}`，prometheus/grafana/generic JSON 三适配器）；SMTP 入站 📋、开放 API `POST /api/v1/events` 📋（B.4/F.1）；限流 429 / 背压 503 已实现 |
| 分诊三级（dedup/suppression/aggregation） | ✅ | 去重窗/聚合窗均 5min **硬编码**；聚合窗锚 `Incident.created_at` 不滑动（长风暴每 5min 裂新单）；同 dedup_key 二次推送 Event 仍落库标 `is_noise`（C.1.1） |
| 路由匹配 Service | 🟡 | 仅 `Event.labels["service"]` **等值匹配** active `Service.slug`；无 glob/多标签/优先级；`Service.labels` 与 Integration 默认 `service_id` 均不参与（B.3） |
| 兜底通知（critical unrouted） | 📋 | **完全未实现**——critical 配错 labels 静默落 unrouted 池，无任何通知（B.13） |
| 回灌重放（RawEvent） | 📋 | 无查询/重放端点、无自动回灌巡检；parse_failed/received 卡死无人消费（B.15） |
| **· 事件处置 ·** | | |
| Incident 状态机（5 态） | ✅ | `ent/schema/incident.go`；非法转换 400 `failed_precondition`；⚠️ 操作端点对不存在 id 也返 400 非 404（C.2） |
| reopen（`POST /incidents/:id/reopen`） | ✅ | resolved/closed→triggered + 写时间线 + 发事件；⚠️ **升级链不重启**（不重发通知，静默停在 triggered）；oncall/subscriber 无权限（C.2） |
| closed 终态 | 📋 | **不可达**——无 close 端点、复盘发布不回写 `incident.status`（C.2/C.6.1） |
| 告警源自动恢复 | 🟡 | 简化实现：resolved 事件按 **service 维度**解最新活跃单（已 acked 也被解决）；⚠️ **不写时间线、不发领域事件**（WS/卡片/出站 webhook 全无感）（C.2.1） |
| Incident 合并 | 📋 | schema 仅预留 `merged_into`；无 merge 端点；AI dedup 建议 accept 不触发合并（C.3.7） |
| reassign | 📋 | 无端点（`incident.reassign` 权限点悬空）；替代 = escalate / add_responder（C.3.8） |
| 手动建单 | 📋 | 无 `POST /incidents`；`trigger_type=manual` 仅枚举；ui-ux 的 [+新建事件] 为幻影（C.3.8） |
| 时间线全程留痕 | 🟡 | 12 类型仅 **7** 有写入点（`incident_created`/`event_attached`/`ai_insight`/`im_message`/`status_changed` 零写入）；自动恢复不写；备注 actor/source 请求体**自报**可冒充（C.8） |
| IncidentAction 操作审计（via 统计） | 📋 | schema 在、**全仓零写入**；操作留痕实际靠 `TimelineItem.source`（B.12/C.5.3） |
| **· 排班与升级 ·** | | |
| 排班实时 oncall 计算 | ✅ | `schedule/engine.go` 实时算（**无 Redis 缓存**）；⚠️ 引擎**不读 `Schedule.type`**（三枚举无算法差异）、**不查 `User.status`**（禁用用户仍被解算通知）（B.5/B.14） |
| Override 换班 | 📋 | 无实体/无端点/引擎不解算（`override` 恒 false）；`schedule.override` 权限点悬空（B.5.1） |
| 空班检测 | 📋 | 空层**静默 continue**，不告警 team_admin；唯一信号 = 升级时间线「通知 0 人」（B.5.2） |
| 升级引擎（Asynq ProcessIn） | ✅ | ack 取消 pending 任务 ✅；⚠️ **手动 escalate 不取消当前层任务**（靠幂等去重兜底）；`levels.notify_channels` 全仓无引用；target=team **不解算成员**（邮件/电话对 team 型 target 不发，仅 IM 群卡片有效）；**自动升级不发布领域事件**（WS/卡片刷新/出站 webhook 全盲）（B.6/F.3） |
| **· 通知 ·** | | |
| 通知引擎 | 🟡 | 实际 = 启动时固定 im+email(+webhook) **并联**，`rule.channels` 不参与分发；**电话/短信零触发路径**（不在默认链，`User.phone` 也无 API 可写）；**无兜底降级链、无手动重发**；送达不落库仅 metrics+日志（B.7/C.9） |
| quiet_hours / 抑制 / 聚合（通知级） | ✅ | quiet_hours 跨午夜 ✅、critical 默认 bypass；被静默通知**直接丢弃无补发**；通知聚合 30s 窗硬编码；SuppressionRule `expires_at` API 无法设置（handler 忽略）（B.7） |
| 通知模板 | ✅ | 3 内置 seed（改/删 403）；语法错降级；"同名覆盖内置"实为歧义降级非覆盖（B.7） |
| **· IM ·** | | |
| IM 交互卡片（按权限渲染按钮） | 🟡 | 飞书全功能 ✅；**钉钉卡片刷新 no-op**（连降级发新消息都没有）、@人拉人解析缺失；企微 NoopBot 占位；**群卡片按首个通知 target 的权限渲染**（非看卡人）；投递固定全局值班群（未配则静默不发）；卡片不带 AI 洞察/runbook 链接（C.3.1/C.3.3/E.4） |
| IM 操作同链鉴权（非后门） | ✅ | `im/handler.go` 走同一 Authorizer；⚠️ 越权拒绝**不落审计**、群内无提示；状态机错误映射 **500**（Web 为 400）（C.3.2） |
| IM 斜杠命令 | 🟡 | `ack`/`escalate`/`resolve`/`status` ✅；`add` ❌（400 unsupported）；`runbook`/`oncall` 📋（403 no permission mapping）（C.3.5） |
| IM 账号绑定 | 🟡 | `POST /users/:id/im-accounts` ✅ 幂等；但 `user.im.bind` 仅 org_admin（**代绑**，非自助）；无解绑端点、误绑不可迁移、无前端页面（B.9） |
| 跨团队 @人 → 事件级临时授权 | 🟡 | `AddResponder` 加入 responders，**不创建临时 RoleBinding**；被 @人能否操作取决于已有权限（C.3.4）；钉钉侧 mention 解析缺失（C.3.3） |
| 作战室 War Room（M8.2/M8.9/M10.5） | 🚧 **暂不做** | 已记录至 [`backlog.md`](./backlog.md)；协同改走 IM 交互卡片 + 状态实时刷新 |
| **· AI ·** | | |
| AI 根因诊断链 | ✅ | 端到端可测（diagnose → AIInsight → accept/reject）；⚠️ **write-only 缺陷**：AIInsight 无读取端点（刷新即丢，历史只能 DB 直查）；置信度阈值过滤/evidence 强制 📋；accept/reject 零留痕可反复改判；仅 Web 详情页手动触发，不进卡片/时间线/通知（C.4.1/C.4.4） |
| 分诊 AI（dedup 建议/severity 调整/噪声学习） | 📋 | **完全无代码**——当前分诊纯规则，不调 LLM（C.4.2） |
| Copilot 处置推荐 / draft_summary | 📋 | 完全无代码（C.4.3） |
| 相似检索（事件/复盘） | 🟡 | `similar` ✅（pgvector + LIKE 降级）；`similar-postmortems` 仅后端（前端无 UI、无降级静默 `[]`）；仅 published 入检索（**archived 掉出**）（C.4.5） |
| **· Runbook ·** | | |
| Runbook 两档（readonly 自动 / 写操作人确认） | 🟡 | 写步骤强校验（须 `require_approval=true`）✅ + 引擎双保险（凡写步骤只认 approved）✅；未获批写步骤**按 on_failure 阻断**（continue 干跑/abort 中止/escalate 升级）✅；前端弹窗**真实传递审批决策**（复选框，不再恒 approved:true）✅；**执行/阻断/升级均记 actor**（时间线，source=web）✅；⚠️ 仍：无独立审批人/双人复核、无 pending 审批流、AuditLog/IncidentAction 不写、无并发保护；执行器仅 http/internal（ansible/jenkins 📋）；trigger 不求值仅手动执行（B.8/C.5） |
| **· 复盘 ·** | | |
| 复盘草稿生成（`GenerateDraft`） | ✅ | 手动端点 + Web 弹窗；时间线 + AI 填充 + 规则化降级；⚠️ **起草端点零权限校验**、重复起草**直接覆盖**（含 published）（C.6） |
| 复盘自动触发 / 闸门 | 📋 | resolve 不触发起草（未接 `IncidentResolved`）；"未复盘不可 close" 两头皆无（C.6.1） |
| 复盘编辑 API | 📋 | 无 `PATCH /postmortems/:id`（sections 不可改）；评审修改唯一手段 = 重新起草覆盖（C.6） |
| Action Item 工单联动 | 📋 | 无 Jira/禅道集成，`tracker_url` 纯手填；`due_date` schema 有、API 不收；手动 CRUD ✅（C.6.2） |
| **· 报表 / 审计 / 治理 ·** | | |
| 数据报表 / 分析（6 端点） | ✅ | dashboard/alerts/incidents/team-load/postmortems/trend；响应键已补 json tag，camelCase 与前端/spec 一致（**Web KPI 正常**）；team-load/postmortem 指标缩水；无 export（B.11） |
| 报表 scope 隔离 | 📋 | 6 端点**无权限点、无团队 scope**——任何登录用户可见全组织指标（B.11） |
| 审计日志查询 | ✅ | `GET /audit-logs`（`admin.audit.view`）；⚠️ 实际落审计**仅 7 种 action**（role/apikey/login）；无时间筛选参数、无导出（B.12） |
| 未路由事件池查看 | 📋 | **无端点无页面**（`event.view_unrouted` 权限点悬空）；仅 analytics 计数 + DB 直查（B.13） |
| 用户禁用 | ✅ | 登录 403 + denied 审计 ✅；⚠️ 存量 **refresh token（30 天）与 API Key 不失效**、排班/升级仍解算其班次；无自动交接提示（B.14/F.2） |
| 角色治理 | 🟡 | 建/删/绑定 ✅（临时授权 `expires_in_hours` 到期实时失效 ✅）；无 `PATCH /roles/:id`（不可编辑）；重名/FK 冲突返 500 非 409（B.2.1） |
| **· 集成与运维 ·** | | |
| 出站 Webhook | ✅ | 仅 5 事件（ack/resolve/reopen/escalate(手动)/add_responder，**无 created/自动升级**）；无签名头不可验源；重试 3 次无死信（F.3） |
| WS 实时推送 | 🟡 | `GET /ws/incidents/:id` 推 5 事件快照；**Created/自动升级不推**；`timeline_added` 定义了无广播（C.3.6） |
| WS 鉴权 | 📋 | **无鉴权**——任意人可订阅任意 incident 快照（C.3.6/F.4） |
| API Key | ✅ | 签发/列举/吊销 + 审计 ✅；⚠️ scope 仅存储不收敛；吊销=硬删（无 disabled 产生路径）；禁用用户的 Key 仍有效（F.2） |
| Helm 生产部署 | 🟡 | 仅 deployment/service/pdb 3 模板（ingress/subchart/asynqmon values 键为 no-op、无 migrate Job）；🚨 **不设 `VIGIL_APP_ENV`** → K8s 默认 development（`__test__/reset` 无鉴权可清库 + `X-Vigil-User-ID` 头鉴权旁路）（A.3） |
| 备份脚本（`scripts/backup.sh`） | ✅ | PG pg_dump + Redis BGSAVE；脚本本身不轮转 |
| 恢复脚本（`scripts/restore.sh`） | ✅ | 含 Redis 丢失场景的升级计时器处置 |
| migrate-down / 回滚 | ❌ | 无；回滚靠备份恢复 |

> 用作验收依据前，🚧/🟡/📋 项需在用例中显式标注前置条件或排除范围。
> 各行「关键限制」的完整依据（代码文件/行为断言）见对应章节与 [`audit/journey-code-audit-2026-07-03.md`](./audit/journey-code-audit-2026-07-03.md)。

---

## 0. 角色与全景

### 0.1 旅程角色总表（A–F）

| 旅程 | 主角色 | 角色定位 | 主要权限点 | 主战场 |
|------|--------|----------|------------|--------|
| **A 首次部署** | 平台运维 / SRE Lead | 把系统跑起来、初始化超管 | （系统级，无业务权限点） | 宿主机 / K8s / 终端 |
| **B 管理员配置** | `org_admin` + `team_admin` | 把组织结构、服务、排班、升级、通知、Runbook 配好 + 报表/审计/分诊/交接 | `team.*` / `service.*` / `schedule.*` / `escalation.*` / `runbook.create` / `integration.*` / `role.*` / `admin.audit.view` | Web 控制台 |
| **C 告警处置** | `responder` / `oncall` | 半夜被叫醒 → 在 IM 内 ack / 诊断 / 处置 / 解决 / 复盘 | `incident.*` / `event.view` / `runbook.execute` / `postmortem.*` | **IM（首选）** + Web（补充） |
| **D 运维保障** | 平台运维 / SRE Lead | 长期运行：升级/迁移/备份/灾难恢复 | （系统级，无业务权限点） | 宿主机 / K8s / 终端 |
| **E 只读订阅** | `subscriber`（团队 Leader） | 不值班的干系人：被告知 + 看全貌 + 看复盘 | `incident.view` / `event.view` / `postmortem.view` | Web（看）+ IM 群（围观） |
| **F 程序化集成** | 平台工程师（API Key） | 把 Vigil 嵌进自家平台：推事件/拉状态/收 webhook | （继承 Key 归属 User 的权限） | API / Webhook / WS |

> 旁路角色：`responder_lead`（responder 权限 + reassign 权限点 + 发起/发布复盘，与各角色差异见 E.6）。
> 旅程 C 中"在 IM 内"是核心差异化，但 Web 仍是兜底与全局视图。

### 0.2 贯穿全程的 7 条设计基线（决定了流程形态）

这些原则决定了"为什么流程是这样设计的"，理解它们才能理解旅程中的分支：

1. **告警消费者定位** —— Vigil 不采集，所有 Event 必须从外部进来（webhook / 邮件 / API）。所以旅程 B 必须先配 Integration，旅程 C 才有信号。
2. **Event / Incident 分离** —— Event 是海量不可变原始信号，Incident 是少量有状态的人工处置单元。旅程 C 的"看到告警"看到的是 Incident，不是 Event。
3. **IM-first** —— 一线工程师的"现场"是 IM 群；ack / 升级 / 拉人都在 IM 完成，且走与 Web **完全相同**的鉴权链路。Web 是管理配置与全局视图的补充。
4. **AI 横向 Copilot + human-in-the-loop** —— 每个 AI 建议都带 evidence，必须人确认才生效；LLM 挂了自动降级为规则化草稿，告警主流程不中断。
5. **Runbook 分两档** —— 诊断（readonly）Vigil 直接执行；处置（写操作）必须人确认或外接，Vigil **绝不**直接动生产。
6. **单组织多团队软隔离** —— 团队是数据归属边界，权限**不**沿团队树继承。跨团队协作的设计意图是 `add_responder` + 事件级临时授权（🟡 当前仅加入 responders 名单，临时授权未实现，见 C.3.4）。
7. **RBAC 可自配置** —— 权限点是系统枚举（固定），角色由使用者自由组合。旅程 B 的"建角色"是核心治理动作。

### 0.3 全景图：一个 Incident 的一生

```
            ┌──────────── 旅程 B：管理员配置（一次或低频）────────────┐
            │  组织 → 角色 → 团队 → 服务 → Integration → 排班 →       │
            │  升级策略 → 通知规则 → Runbook →（用户绑 IM）             │
            └────────────────────────┬─────────────────────────────────┘
                                     │ 配置就绪
                                     ▼
   外部告警源 ──webhook──▶ ┌──── 旅程 C：告警处置（高频，IM 内）────┐
   Prometheus/Zabbix/...   │  接入 → 归一化 → 分诊 → 路由 →          │
                           │  建 Incident → 升级计时 → 通知 →         │
                           │  ┌── IM 卡片：ack/升级/详情 ──┐         │
                           │  │  ↓                         │         │
                           │  ack → 诊断(AI) → Runbook →  解决       │
                           │  └────────────────────────────┘         │
                           │           ↓                              │
                           │      复盘(草稿→评审→发布) → 知识库        │
                           └──────────────────────────────────────────┘
                              │                        │
              ┌───────────────┘                        └───────────────┐
              ▼                                                        ▼
   ┌─ 旅程 E：Leader 围观（只读）──────┐    ┌─ 旅程 F：程序化集成 ──────────────┐
   │ IM 群卡片围观 + Dashboard +       │    │ webhook in →（C 主链）→          │
   │ 复盘阅读（subscriber，见 E.1–E.7）│    │ webhook out / WS / API 拉状态     │
   └───────────────────────────────────┘    │（API Key 生命周期，见 F.1–F.6）   │
                                            └───────────────────────────────────┘
                                     │
            ┌──────────── 旅程 A：首次部署（一次性）────────────────────┐
            │  拉镜像/起依赖 → migrate → 启动 → 种子超管 → 改密码 →     │
            │  健康检查 → 接入 IM/LLM（可选）                            │
            └────────────────────────────────────────────────────────────┘

            ┌──────────── 旅程 D：运维保障（长期运行，低频高危）────────┐
            │  升级/迁移（双轨 migrate，无 down，靠备份回滚）            │
            │  备份（PG pg_dump + Redis BGSAVE，cron 定时）              │
            │  恢复/DR（含 Redis 丢失 = 升级计时器丢失的处置）           │
            └────────────────────────────────────────────────────────────┘
```

---

## 旅程 A：首次部署与初始化

**主角色**：平台运维 / SRE Lead（部署决策者，参见 `personas.md`）
**目标**：让 Vigil 在自己的环境跑起来、初始化超管、验证可用，然后把钥匙交给 `org_admin`。
**特点**：一次性、命令行驱动、无 Web 向导（设计上无 first-run wizard，靠环境变量 + 种子）。

> 本旅程只覆盖**从零到跑起来**。长期运行的升级/备份/恢复见 [旅程 D](#旅程-d运维保障升级--迁移--备份--灾难恢复)。

### A.1 前置条件

- **PostgreSQL 13+ 且带 pgvector 扩展**（硬要求，`Incident.embedding` 是 `vector(1536)`）。推荐镜像 `pgvector/pgvector:pg16`。无 pgvector → migrate 报 `extension "vector" does not exist`。
- **Redis 6+**（缓存 / 队列 / 锁；升级计时器存活于此，生产须开 AOF/RDB + HA）。
- Docker + Docker Compose（单机）或 kubectl + Helm（集群）。
- 已知要接入的 IM 平台凭据（飞书/钉钉 App 凭证，**可选**，先跑起来后补）。
- 已知要接入的 LLM API Key（智谱 GLM，**可选**，无则 AI 降级为规则化）。

### A.2 单机 Docker Compose（默认路径）

```
1. git clone <repo> vigil && cd vigil
2. cp .env.example .env
3. 编辑 .env（必改：DB/Redis 密码；可选：IM_* / LLM_* / SMTP_* / WEBHOOK_OUT_URLS）
4. docker compose up -d            # 起 postgres(pgvector) + redis + vigil
5. docker compose exec vigil vigil migrate   # 建表 + 启用 pgvector（一次性）
6. 浏览器打开 http://localhost:8080          # Web UI
   打开 http://localhost:8080/docs            # Swagger
   curl http://localhost:8080/health          # 健康检查
```

**容器拓扑（硬指标 H1.1：3 容器一键起）**：

| 容器 | 镜像 | 职责 | 备注 |
|------|------|------|------|
| `postgres` | `pgvector/pgvector:pg16` | 主存储 | healthcheck `pg_isready`，持久化卷 `pgdata` |
| `redis` | `redis:7-alpine` | 缓存/队列/锁 | 升级计时器在此 |
| `vigil` | 本地 Dockerfile 构建 | API + Worker（单二进制多角色）+ 前端静态资源 | 依赖前两者 healthy，`VIGIL_APP_ENV=production` |

> ⚠️ compose 默认**不自动 migrate**（`command: ["migrate"]` 被注释），第 5 步必须手动跑一次。

### A.3 K8s + Helm（生产路径）— 🟡 chart 可用但缺口多，逐项如实标注

```
1. 准备 Secret（不放明文进 chart，三个键名固定）：
   kubectl create secret generic vigil-secrets \
       --from-literal=db-password=<...> \
       --from-literal=redis-password=<...> \
       --from-literal=jwt-secret=<...>
2. helm install vigil ./deploy/helm -f values-prod.yaml
3. DB/Redis 必须用外部实例（见下：subchart 是空承诺）
4. kubectl exec deploy/vigil -- /app/vigil migrate     # ★ 手动跑迁移（chart 无 migrate Job）
5. 自建 Ingress + HTTPS 终止（见下：chart 的 ingress 配置是空承诺）
```

**chart 真实内容清单**（`deploy/helm/`，对照 2026-07-03 代码）：`Chart.yaml` + `values.yaml` + templates 仅 **3 个**——`deployment.yaml` / `service.yaml`（ClusterIP）/ `pdb.yaml`。以下 values 键**有键无模板，设了不生效**：

| values 键 | 声称 | 实际 |
|-----------|------|------|
| `ingress.*` | Ingress + TLS | ❌ **无 `templates/ingress.yaml`**——`ingress.enabled=true` 是 no-op，需自建 Ingress 资源指向 Service `vigil:8080`。`deployment.md` §7 称"Ingress 模板已提供"与实际不符（纠偏） |
| `database.deploySubchart` / `redis.deploySubchart` | true 部署内置 PG/Redis（dev 用） | ❌ **`Chart.yaml` 无 dependencies**——no-op，K8s 路径必须自备外部 PG（含 pgvector）与 Redis |
| `asynqmon.enabled` | 部署 Asynq 监控 UI | ❌ 无对应 Deployment 模板（B.15/D.5 相关，需自行部署） |

**values-prod.yaml 必改/必审键**（真实键名，从 `values.yaml` 抄）：

| 键 | 默认值 | 说明 |
|----|--------|------|
| `image.repository` / `image.tag` | `ghcr.io/kevin/vigil` / `0.1.0` | 占位仓库，需自构建推送后改为自己的镜像 |
| `database.host` / `port` / `name` / `user` | `""` / 5432 / vigil / vigil | host 空则连不上（无 subchart 兜底），**必填** |
| `database.existingSecret` | `vigil-secrets` | 须含 `db-password`、`jwt-secret` 两个键（JWT 也从这个 Secret 读，键名 `jwt-secret`） |
| `redis.host` / `port` | `""` / 6379 | **必填** |
| `redis.password` | **键不存在** | ⚠️ 陷阱：`deployment.yaml` 用 `if .Values.redis.password` 决定是否注入 `VIGIL_REDIS_PASSWORD`（值仍从 Secret 的 `redis-password` 读）。**带密码的 Redis 必须在 values 里显式加 `redis.password: "true"`（任意非空）**，否则密码 env 根本不渲染，连接失败 |
| `config.authEnabled` | `"true"` | 保持 true |
| `config.ingestionRateLimitPerMin` / `ingestionBackpressureDepth` | 600 / 10000 | 按量调（B.4） |
| `config.smtpHost` / `config.phoneWebhookUrl` | `""` | 可选；⚠️ 注意 chart 只暴露 SMTP 的 **host** 一个键，`_PORT/_USER/_PASS/_FROM` 均无法通过 values 配置 |
| `replicaCount` / `resources` / `podDisruptionBudget` | 1 / 250m·256Mi / minAvailable=1 | 单副本起步（多副本见 D.4） |

> ⚠️ **chart 未暴露的 env（配了也没入口）**：`VIGIL_IM_*`（飞书/钉钉/值班群）、`VIGIL_LLM_*`、`VIGIL_WEBHOOK_OUT_URLS`、SMTP 除 host 外全部——K8s 下要配 IM/LLM/出站 webhook 只能改 `templates/deployment.yaml` 加 env（chart 也无通用 `extraEnv` 机制）。📋 chart 完整化待补。

> 🚨 **重大缺口：chart 不设置 `VIGIL_APP_ENV`**（compose 设了 `production`，chart 的 env 清单里没有，values 也无对应键）。config 默认 `development`，后果（`wire.go` 核实）：
> ① **`POST /api/v1/__test__/reset` 测试端点被挂载**——public group 无任何鉴权，一击 TRUNCATE 全库 + 清空队列，集群内任何能访问 Service 的负载都能调；
> ② **`X-Vigil-User-ID` 头回退启用**（`IdentityResolver` headerFallback=!IsProduction）——不带 JWT/APIKey、只带 `X-Vigil-User-ID: 1` 即可冒充任意用户，**等于鉴权旁路**（`config.authEnabled=true` 挡不住这条轨）。
> **当前唯一修法 = 手改 `templates/deployment.yaml` 加 `VIGIL_APP_ENV: production`**。开放问题 + 建议单独修（chart 加 appEnv 键并默认 production）。

**migrate 在 K8s 下的执行方式**（chart 无 Job/initContainer/helm hook，`templates/` 全集核实）：

- 首次安装：`helm install` 后 pod 会正常 Ready——注意 **`/health` 只做 Redis PING + `SELECT 1`，不碰业务表**（`server.go` health），所以**未 migrate 的实例探针全绿但业务 API 全部报错**（relation does not exist；启动 seed 只 log Warn 不退出）。Ready ≠ 可用，必须手动跑：`kubectl exec deploy/vigil -- /app/vigil migrate`（镜像 ENTRYPOINT `/app/vigil`，migrate 是子命令，幂等可重复跑，见 D.1）。
- 替代：一次性 Pod（`kubectl run vigil-migrate --image=<镜像> --restart=Never -- migrate`，需手动补 DB env）；📋 设计目标是 helm pre-upgrade hook Job。

**Secret 缺失/键错的故障表现**（可断言）：`vigil-secrets` 不存在或缺 `db-password`/`jwt-secret` 键 → pod 卡在 **`CreateContainerConfigError`**（容器不启动，不是 CrashLoop），`kubectl describe pod` Events 显示 `secret "vigil-secrets" not found` 或 `couldn't find key db-password in Secret`；补建 Secret 后 kubelet 自动重试，无需重建 pod。

**helm upgrade 与 D.1 的衔接**（版本升级走这里）：

```
1. 备份（D.2；K8s 下 backup.sh 需能连到外部 PG/Redis，或在 DB 侧做快照）
2. helm upgrade vigil ./deploy/helm -f values-prod.yaml --set image.tag=<new>
   # 默认 RollingUpdate：1 副本时先起新 pod 再杀旧（maxUnavailable=0 向下取整）
3. kubectl exec deploy/vigil -- /app/vigil migrate     # 仍是手动，新 pod 内跑
4. 验证：/health、发测试告警走通 C 主链路（同 D.1 步骤 2e）
```

> ⚠️ 顺序缺陷（如实）：新代码先跑起来、migrate 后补——若新版依赖新列，窗口期内相关 API 报错。ent auto-migrate 是加法型变更（加表加列）时窗口影响小；破坏性变更须停机升级。回滚同 D.1：**无 migrate-down，靠备份恢复**。

**生产安全加固（chart 已内置，SEC-05，`deployment.yaml` 核实）**：`runAsNonRoot`、UID/GID 65532、`readOnlyRootFilesystem: true`（仅挂 emptyDir `/tmp`）、drop 全部 capabilities、`allowPrivilegeEscalation: false`、seccomp `RuntimeDefault`；liveness/readiness 均打 `/health`（3s 超时探活 PG+Redis，任一挂 → 503 → pod NotReady）。
**多副本**：API 按 QPS 横扩、Worker 按队列深度横扩；多副本 WebSocket 广播需 Redis pub/sub（当前单实例优先，见 D.4）。

**A.6 验收清单的 K8s 版本差异**：

- [ ] `kubectl get pods` 全 Ready 且 **无 CreateContainerConfigError**（Secret 三键齐）
- [ ] `kubectl exec deploy/vigil -- /app/vigil migrate` 已跑过且输出 `migrate: schema applied`（Ready ≠ 已建表，见上）
- [ ] `/health`、登录、强制改密：`kubectl port-forward svc/vigil 8080:8080` 后同 A.6 通用项
- [ ] **`curl -X POST <svc>/api/v1/__test__/reset` 必须 404**——若 200/500 说明 APP_ENV 还是 development（见上重大缺口），禁止上生产
- [ ] **带 `X-Vigil-User-ID: 1` 裸调 `GET /api/v1/users` 必须 401**——同上验证 header 回退已关闭
- [ ] `/metrics` 抓取：chart 无 ServiceMonitor 模板，需在 Prometheus 侧自配 scrape（或 pod annotation），见 D.5
- [ ] Ingress/TLS 为自建资源（chart 不管），域名可达

### A.4 初始化超管（自动种子，无向导）

启动时 `internal/server/wire.go` 自动执行（幂等）：

```
1. SeedBuiltinRoles           # 种内置角色：org_admin / team_admin / responder /
                              #   responder_lead / subscriber / oncall
2. 初始化 JWT 签名器          # 读 VIGIL_AUTH_JWT_SECRET（未设的行为见下，分环境）
3. auth.SeedDefaultAdmin      # 仅当 JWT 可用：
                              #   username=admin / password=changeme /
                              #   email=admin@vigil.local
                              #   绑 org_admin 角色（org scope，FIX-A）
                              #   must_change_password=true
```

**种子幂等（可断言，`seed_admin.go`）**：幂等靠 `username=admin` 唯一约束——已存在则 Create 撞 ConstraintError 直接跳过，**不重置密码、不重置 `must_change_password` 标志、不补绑角色**。所以：改过密码后重启服务，密码保持改后的（不会被 seed 打回 changeme）；把 admin 的 org_admin 绑定删了再重启，seed 也**不会**补回（测试 reset 场景走专用的 `EnsureAdminOrgAdminBinding`）。

**`VIGIL_AUTH_JWT_SECRET` 未设时的真实行为**（⚠️ 分环境，`config.go` 核实，与旧文"登录被禁用+告警日志"不同）：

| 环境（`VIGIL_APP_ENV`） | 行为 |
|------|------|
| `production` | **进程拒绝启动**——config 加载即报 `production requires VIGIL_AUTH_JWT_SECRET to be set (auth is enforced)`，fail-fast |
| `development`（默认） | 自动填充固定弱密钥 `dev-jwt-secret-not-for-production`——登录**可用**（不报错），但 token 可被任何知道该串的人伪造，仅限本地 |
| 兜底路径 | 若签名器仍不可用（理论边缘），`POST /auth/login` 返回 500 `{"error":"jwt not configured"}`，且 admin 种子被跳过 |

#### 首登强制改密：API 层可断言行为（`middleware.go` RequireUserWithGuard + `wire.go` forcePasswordGuard）

`must_change_password=true` 期间，v1 组的 UserGuard 拦截业务 API：

| 请求 | 预期 |
|------|------|
| `POST /auth/login`（admin/changeme） | ✅ **200 正常发 token**（login 在 public 组，不受守卫管；响应的 `user` 对象**不含** `must_change_password` 字段，客户端只能靠首个业务请求的 403 感知要改密） |
| 改密前调任意业务 API（如 `GET /incidents`、`GET /users`） | **403 `{"error":"must_change_password"}`**（不是 401） |
| 豁免清单（守卫放行的仅 3 个路径） | `POST /auth/change-password`、`GET /auth/me`、`/health` |
| 改密成功后 | 标志清零，同一个 token 立即可调业务 API（无需重新登录） |

> ⚠️ **Web 无改密页面**（`web/src` 全量 grep：password 仅出现在 login.tsx；login 响应/`GET /auth/me` 也不含 `must_change_password` 字段）——首登后 Web 界面表现为"登录成功但所有页面数据加载 403"，无任何改密引导。**当前改密唯一途径 = 直调 API**（curl / Swagger `/docs`）：
>
> ```bash
> TOKEN=$(curl -s localhost:8080/api/v1/auth/login -H 'Content-Type: application/json' \
>      -d '{"username":"admin","password":"changeme"}' | jq -r .access_token)
> curl -s localhost:8080/api/v1/auth/change-password -H "Authorization: Bearer $TOKEN" \
>      -H 'Content-Type: application/json' -d '{"old_password":"changeme","new_password":"<新密码>"}'
> ```
>
> A.6 验收项已按此表述（见 A.6）；前端改密页 📋。

#### `POST /auth/change-password` 契约（`handler_auth.go` + `password.go`）

请求体：`{"old_password": "...", "new_password": "..."}`。**密码策略（`ValidatePasswordStrength`）：长度 ≥ 8，且至少含两类字符（字母/数字/符号）**——纯 8 位数字不合规、`abcd1234` 合规。

| 分支 | 预期（错误文案从代码抄，可直接断言） |
|------|------|
| 成功 | 200 `{"status":"ok"}`，清 `must_change_password` |
| 任一字段缺失 | 400 `{"error":"old_password and new_password required"}` |
| 新密码 <8 位 | 400 `{"error":"password must be at least 8 characters"}` |
| 新密码单一字符类 | 400 `{"error":"password must contain at least two of: letters, digits, symbols"}` |
| 新旧相同 | 400 `{"error":"new password must differ from old"}` |
| 旧密码错 | **401** `{"error":"invalid old password"}` |
| 未登录 | 401 `{"error":"not authenticated"}` |

> ⚠️ **改密后旧 token 不失效**（JWT 无状态，无黑名单/版本号）：改密前签发的 access token 继续有效到过期（默认 **15 分钟**），refresh token 继续可换新 access（默认 **720h=30 天**，且 refresh 端点既不查 `User.status` 也不感知改密——与 B.14 的"禁用不吊销会话"同一个洞）。若怀疑 `admin/changeme` 期间已泄露 token，唯一硬止血 = 轮换 `VIGIL_AUTH_JWT_SECRET` 重启（全员重登）。开放问题候选。

### A.5 接入外部依赖（可选，可后补）

| 依赖类 | 环境变量 | 不配的后果 |
|--------|----------|------------|
| LLM（智谱 GLM） | `VIGIL_LLM_API_KEY` / `_MODEL` / `_BASE_URL` + cost 控制 `_COST_*` | AI 降级为规则化草稿，诊断跳过；告警主流程不受影响 |
| IM（飞书） | `VIGIL_IM_FEISHU_APP_ID/_SECRET/_TOKEN/_ENCRYPT_KEY` | 该平台不发卡片；通知走兜底通道 |
| IM（钉钉） | `VIGIL_IM_DINGTALK_APP_KEY/_SECRET/_ROBOT_CODE/_TOKEN/_AES_KEY` | 同上 |
| IM 目标群 | `VIGIL_IM_ONCALL_CHANNEL` | 告警卡片无处投递 |
| 邮件 | `VIGIL_NOTIFICATION_SMTP_HOST/_PORT/_USER/_PASS/_FROM` | 邮件通道禁用 |
| 电话/短信 | `VIGIL_NOTIFICATION_PHONE_WEBHOOK_URL` / `_SMS_` | 占位转发器且不在默认通道链（im/email/webhook），配置了也零触发（📋，见 C.9） |
| Webhook 出站 | `VIGIL_WEBHOOK_OUT_URLS` | 不向外部系统推送 Incident 生命周期 |
| 限流/背压 | `VIGIL_INGESTION_RATE_LIMIT_PER_MIN` / `_BACKPRESSURE_DEPTH` | 无 Redis 时退化为放行 |

> 设计原则：**所有外部依赖都"优雅降级"** —— 不配 LLM 不会让告警断流；不配 IM 时 email/webhook 照常并联发送（通道为并联，无兜底切换语义，见 C.9）。

### A.6 验收清单（部署完成判据）

- [ ] `curl /health` 返回 200
- [ ] `SELECT extversion FROM pg_extension WHERE extname='vector';` 有结果（pgvector 装好）
- [ ] 能登录 admin/changeme；经 API（curl/Swagger，见 A.4）完成强制改密后 Dashboard 可访问（Web 无改密页，📋）
- [ ] 改密后能访问 Dashboard（说明权限链通）
- [ ] `/metrics` 暴露 Prometheus 指标
- [ ] （如配了 IM）测试回调能收到响应
- [ ] 生产 checklist：DB/Redis 密码已改、HTTPS、Redis 持久化、LLM cost 控制、备份脚本 `scripts/backup.sh` cron

---

## 旅程 B：管理员配置闭环

**主角色**：`org_admin`（组织级）+ `team_admin`（团队级）
**目标**：把"组织 → 角色 → 团队 → 服务 → 接入 → 排班 → 升级 → 通知 → Runbook"这条链配通，让告警能正确路由到对的人。
**特点**：Web 控制台驱动、有严格依赖顺序、低频（配好基本不动）。

### B.0 配置依赖图（★ 决定先后顺序）

```
User ──┐
       ├──▶ Team ──▶ Service ──┬──▶ Integration（绑默认 service）
       │              │         │
       │              ├──▶ EscalationPolicy ◀──┐
       │              ├──▶ Schedule ────────────┤
       │              └──▶ Runbook ─────────────┘（都回绑到 Service）
       │
Role ──▶ RoleBinding（scope=team）──▶ User（拿到团队内权限）
       │
NotificationRule ──▶ Template（按 severity/team 选）
SuppressionRule（维护窗/已知问题）
```

**关键约束**：
- **Service 是路由锚点** —— 它的 `labels` 是 Event 路由匹配的依据，同时聚合了 escalation/schedule/runbook。所以 Service 是配置枢纽。
- **Schedule 是蓝图，不存快照** —— "现在谁值班"是实时算的（引擎 3）。所以排班配错会立刻在告警里暴露。
- **EscalationPolicy 的 target 可以是 schedule** —— schedule 变了，下一次升级立刻生效。
- **权限不沿团队树继承** —— 给了父团队 RoleBinding，子团队用户**不**自动有权限，必须各自绑。

### B.1 组织级配置（org_admin）

> 触发权限点：`user.*` / `role.*` / `admin.apikey.manage` / `admin.global_integration`

| 步骤 | 操作 | 端点 | 权限点 | 状态 |
|------|------|------|--------|------|
| 1 | 建用户（或对接 SSO/LDAP） | —（**无创建端点**，见 B.1.1） | `user.create` | ❌ |
| 2 | 建自定义 Role（组合权限点） | `POST /roles` | `role.create` | ✅（详见 B.2.1） |
| 3 | 创建 API Key（供外部系统调用） | `POST /api-keys`（明文仅创建响应回显一次） | `admin.apikey.manage` | ✅ |
| 4 | 配全局 Integration 凭据（多团队共用 token 池） | —（权限点已预留，无对应端点/页面） | `admin.global_integration` | 📋 |

#### B.1.1 用户生命周期 — 🟡 部分实现（无创建/重置密码端点）

**当前系统唯一的用户来源是种子超管**：启动时 `auth.SeedDefaultAdmin` 幂等种入 `admin/changeme`（见 A.4）。
`internal/auth/` 下**没有 `POST /users` 创建端点**——`user.create` 权限点已定义（permission.go）但无处可用；
多用户场景当前只能 DB 直插（📋 backlog）。

**已实现的用户操作**：

| 操作 | 端点 | 说明 | 状态 |
|------|------|------|------|
| 列表 | `GET /users` | `password_hash` 自动脱敏（ent Sensitive） | ✅ |
| 更新 | `PATCH /users/:id` | 仅 `name` / `status` / `timezone` 三个字段，**不改密码** | ✅ |
| 禁用 | `PATCH /users/:id` 传 `{"status":"disabled"}` | 禁用后登录返回 **403 `user disabled`**（并记 denied 审计）；交接处理见 B.14 | ✅ |
| 本人改密 | `POST /auth/change-password` | 唯一的密码修改路径（须已登录） | ✅ |
| 管理员重置他人密码 | — | **无端点**；用户忘记密码只能 DB 直改 `password_hash` | ❌ |
| 绑 IM 账号 | `POST /users/:id/im-accounts` | 权限 `user.im.bind`，详见 B.9 | ✅ |
| 删除用户 | — | 无端点（设计倾向"禁用保历史"而非删除） | ❌ |

**首登强制改密（对齐 A.4）**：`must_change_password=true` 的用户登录后，除改密端点外所有业务 API
被 `forcePasswordGuard` 拦截（403 引导改密），改密成功后放开——种子超管即走此流程。

**可断言测试要点 / 负向用例**：

| 场景 | 预期 |
|------|------|
| PATCH 不存在的用户 | 404 `user not found` |
| 重复 username / email | User schema 两字段均 Unique；因无创建端点，当前仅在种子/DB 直插层面触发约束 |
| 无权限者改用户 | ⚠️ `GET /users`、`PATCH /users/:id` **未登记权限点**（wire.go `registerSensitiveRoutePerms` 未覆盖，RouteGuard 渐进启用下仅要求登录态）——现状是任何登录用户可改他人 status，用例按此断言；安全收口见开放问题/备忘 |

> 📋 设计目标（capability 09 §2）：`POST /users`（username/email 必填 + 初始密码策略/邀请激活）、
> 管理员重置密码、禁用时自动提示待交接项（见 B.14）。

### B.2 团队与权限（team_admin）

> 触发权限点：`team.*` / `role.assign` / `team.member.manage`

#### 团队 CRUD（✅ 已实现）

```
1. 建 Team：POST /teams {name, slug, description}（team.create）
   ├─ name/slug 必填 → 缺失 400 "name and slug required"
   └─ slug 全局唯一 → 重复 409 "team slug or name already exists"
2. 改 Team：PATCH /teams/:id {name?, description?}（team.update）
3. 删 Team：DELETE /teams/:id（team.delete，影响面见下）
```

> ⚠️ 纠偏：`parent_team_id` 在 ent schema 存在（仅组织展示、权限不继承），但**创建/更新请求体均未暴露该字段**——
> 当前 API 无法配置团队树（📋）。

#### 成员管理 — ❌ 端点未实现

`ent/schema/team.go` 定义了 Team↔User 多对多边，`team.member.manage` 权限点也已定义，
但 `TeamHandler` 只有 Team 本体 CRUD，**无 `/teams/:id/members` 类端点**——无法通过 API 加/移成员。

**当前替代路径**：运行时鉴权其实不依赖成员边——"某人在某团队有什么权限"完全由 **team-scope RoleBinding** 表达（见下）。
所以"让张三能处理 payment 团队的告警"的实际配置动作是发 RoleBinding，而非加成员。

> 📋 设计目标：成员增删端点；移成员时联动处理其 RoleBinding 与 Rotation.participants（排班参与人当前同样无管理入口）。

#### 授权与临时授权（✅ 已实现）

```
发 RoleBinding：POST /role-bindings（role.assign）
{
  "user_id": 42, "role_id": 7,
  "scope_level": "team",       // org | team；非法值缺省 team
  "team_id": "3",              // team scope 必填（字符串），缺 → 400 "team_id required for team scope"
  "expires_in_hours": 8        // ★ 可选，临时授权：相对小时数（不是绝对时间戳）
}
撤销：DELETE /role-bindings/:id（role.assign）
```

- **到期自动失效 ✅**：鉴权器每次请求实时查库，SQL 端过滤 `expires_at IS NULL OR expires_at >= now()`
  （`internal/auth/authz.go`）——到期即失效，不依赖任何后台任务。
- **提前撤销 ✅**：DELETE 后下一个请求即失效（同因实时查库）。
- **落审计 ✅**：创建记 `role.assign`（detail 含 user_id/role_id/scope），撤销记 `role.unassign`。
- 负向：`user_id`/`role_id` 缺失 → 400 `user_id and role_id required`。

> ⚠️ 纠偏：capability 09 §4.3 示例入参为绝对时间 `expires_at`，实际实现是 **`expires_in_hours` 相对小时数**
> （`internal/auth/handler.go`）。附录 C 剧本 3 第 3 步已按此表述。

#### 鉴权链路（运行时）

```
action→permission_code → resource→scope(team_id)
→ 单次 SQL 查 User 在 org + 该 team 的未过期 RoleBinding
→ 合并各 Role.permissions（并集）→ 判断 code ∈ 集合
```

**生效时机（可断言）**：无缓存、每请求实时查库——角色/绑定的任何变更对**下一个请求立即生效**。

#### 删团队的影响面（✅ 端点在，注意副作用）

| 关联对象 | 行为（FK 策略，见 `ent/migrate/schema.go`） | 后果 |
|----------|------|------|
| Service / Schedule / EscalationPolicy / Runbook / 通知与抑制规则 | `SET NULL`，本体保留 | 服务变"无主"但 status 仍 active，**路由仍会命中**其 slug；⚠️ 但建 Incident 时查不到归属团队而失败（triage `createIncident` 报 `query service team`）——该服务后续告警**建不了单** |
| 未关闭 Incident | `SET NULL`，保留 | 失去团队归属，团队维度的列表隔离/报表看不到它 |
| 成员关系（team_users join 表） | 级联删除 | — |
| RoleBinding | join 边级联删，但 `role_bindings.team_id` 字符串字段残留 | 残留 binding 指向已删团队，鉴权无实际效果（无害脏数据） |

> **建议操作顺序**：删团队前先处理名下 Service（迁移或删除，见 B.3）并关闭 Incident，
> 避免出现"路由命中但建单失败"的悬空服务。

#### B.2.1 角色治理（org_admin）— 🟡 部分实现（无编辑端点）

**权限点清单（单一信源）**：`internal/auth/permission.go`，72 个系统枚举权限点（命名 `<resource>.<action>`）。
角色配置只能从此集合选取——这就是"权限点固定、角色自由组合"基线的落点。

| 操作 | 端点 | 状态 | 说明 |
|------|------|------|------|
| 建角色 | `POST /roles`（`role.create`） | ✅ | `{name, description, scope_level, permissions[]}`；scope_level 取 org/team，非法值缺省 team |
| 列角色 | `GET /roles` | ✅ | 含 6 个内置角色（builtin=true）；⚠️ 该端点未登记权限点，仅需登录态 |
| 编辑角色权限集 | — | ❌ | **无 `PATCH /roles/:id`**（`role.update` 权限点已定义但无端点）。改权限集只能**删除重建**（内置角色连这条路都没有） |
| 复制内置角色 | — | ❌ 无专用端点 | 替代路径：`GET /roles` 读出内置角色的 permissions → `POST /roles` 建同权限集的自定义角色 |
| 删角色 | `DELETE /roles/:id`（`role.delete`） | ✅ | 负向行为见下表 |

**可断言的负向用例**：

| 场景 | 预期 |
|------|------|
| permissions 含非法权限点 | 400 `invalid permission: <code>` |
| name 为空 | 400 `name required` |
| name 与既有角色重复（Role.name Unique） | ⚠️ 当前返回 **500**（handler 未走 FailConstraint 转 409，按此现状断言） |
| 无 `role.create` 权限调 POST /roles | 403（RouteGuard 已登记 role.create） |
| 删内置角色 | **403 `builtin role cannot be deleted`**（`internal/auth/handler.go` deleteRole） |
| 删不存在角色 | 404 `role not found` |
| 删仍被 RoleBinding 引用的角色 | ⚠️ FK 为 `NoAction`，DB 拒绝 → 当前返回 **500**；须先删光其全部 binding 才能删角色 |

**权限变更生效时机**：同鉴权链路——实时查库，删旧建新角色 + 重新绑定后，下一个请求即按新权限集判定。

### B.3 服务目录（team_admin，★ 配置枢纽）

> 触发权限点：`service.create` / `service.update` / `service.delete`

| 步骤 | 操作 | 关键字段 | 状态 |
|------|------|----------|------|
| 1 | 建 Service：`POST /services` | `name`/`slug` 必填（slug 全局唯一，重复 → 409 `service slug already exists`）、`team_id` | ✅ |
| 2 | 设 `labels` | ⚠️ 当前路由**不用**此字段，见下"路由匹配的真实语义" | 🟡 |
| 3 | 绑升级策略 | `escalation_policy_id`（PATCH 三态：不传=不改 / 0=解绑 / >0=关联） | ✅ |
| 4 | 关联排班 | schema 有 Service↔Schedule 边 | 📋 **API 未暴露**（create/update 请求体无此字段） |
| 5 | 绑 Runbook | schema 有 Service↔Runbook 边 | 📋 **API 未暴露** |
| 6 | 设 `auto_create_incident`（默认 true） | false 时非 critical 不自动建单 | ✅ |

> ⚠️ 路由是配置命脉：匹配不到 → Event 落 `unrouted` 池（⚠️ 池无查看端点，`event.view_unrouted` 权限点悬空，见 B.13）；
> critical 落 unrouted 的"兜底通知全员/admin"📋 **未实现**——配错 labels 即静默丢失（B.13 纠偏）。

#### 路由匹配的真实语义（⚠️ 纠偏）

> 本节原表述"labels（精确 + glob）"来自能力域设计（M4.1/M4.2），**当前代码不是这样**。

**当前行为（✅，`internal/triage/engine.go` `route()`）**：

```
取 Event.labels["service"] 的值
   └─ 等值匹配 status=active 的 Service.slug
       ├─ 命中 → Event 绑定该 Service
       └─ labels 无 "service" 键 / 值匹配不到任何 slug / Service 已停用 → unrouted（见 B.13）
```

即：**只看一个固定键 `service`，只做 Service.slug 等值匹配**。`env`/`tier` 等其余 labels 不参与；
无 glob 通配；slug 全局唯一，因此不存在"多条命中按优先级取一"的场景。

**可断言测试要点**：

| 场景 | 预期 |
|------|------|
| Event.labels["service"]=payment 且存在 active Service(slug=payment) | 路由命中 |
| Service `status=disabled` | 该服务**全部新 Event 落 unrouted**（停用即摘流，route 查询过滤 active） |
| Service.labels 配了 `service=payment` 但 slug=pay-api | **不命中**——匹配的是 slug 而非 Service.labels（该字段当前实际不参与路由） |
| `auto_create_incident=false` 且 severity≠critical | Event 绑定 Service 但**不建单**（等人工提升；critical 始终自动建单） |
| 改 slug / Event labels 后 | 下一条 Event 立即按新配置路由（实时查库无缓存）；⚠️ 但 dedup 5min 窗口内同 dedup_key 的事件仍被去重丢弃，验证时需换 dedup_key |

> 连带纠偏：Integration 上绑定的默认 `service_id` 当前也不参与路由（见 B.4 第 4 步）。

**📋 设计目标（M4.1/M4.2，未排期）**：多 label 组合匹配 + 通配 `*` + 多条命中按优先级取一，
Service.labels 成为真正的匹配锚点。

#### 删除与影响面

`DELETE /services/:id` ✅（`service.delete`；不存在 → 404）。关联 FK 均 `SET NULL` / join 级联，
**不会因存在关联数据而删除失败**：

| 关联对象 | 行为 |
|----------|------|
| 历史 Event / Incident | 保留，service 关联清空（进行中 Incident 状态不变，但失去 service 上下文） |
| Integration.service | 清空（接入点保留、token 继续有效） |
| Service↔Schedule / Runbook 关联 | 关联行级联删除，排班/Runbook 本体保留 |
| 后续同 label 的新 Event | **落 unrouted 池**（衔接 B.13；critical 也无兜底通知，📋 未实现） |

> 只想"摘流"不想丢配置时，用 `PATCH /services/:id {"status":"disabled"}` 代替删除——
> 新 Event 同样落 unrouted，但配置可随时恢复。

#### 服务拓扑（📋 完全未实现）

M4.4 服务依赖拓扑（影响面分析）未做：`ent/schema/service.go` **无 `depends_on` 字段**，
无任何拓扑端点/页面。capability 02 §3.3 本身也标注"非核心，初期可不做"。

### B.4 接入源 Integration（team_admin / org_admin）

> 触发权限点：`integration.create` / `integration.update` / `integration.delete`

#### 操作流（对照实现标注）

```
1. 建接入点：POST /integrations（integration.create）
   {name, type, config?, team_id?, service_id?}
   ├─ name/type 必填 → 缺失 400
   └─ 响应含 token（vig_int_ + 32 位 hex）：★ 明文仅此一次，之后 GET 不回显（Sensitive）
2. 告警源配置 webhook：POST /api/v1/webhook/{token}
   （token 走路径段鉴权 ✅；Authorization 头方式 📋 未实现）
3. 设限流：config.rate_limit（每分钟次数，覆盖全局默认，见"限流与背压"）✅
4. 绑默认 service_id：🟡 字段可传、可入库，但归一化/路由当前不读它——
   路由仍完全依赖 Event.labels["service"]（见 B.3 纠偏）；"跳过标签匹配直达"📋 未实现
5. severity 映射覆盖：📋 未实现（见"severity 归一映射"）
6. 干跑验证：⚠️ POST /integrations/:id/test 在代码中不存在（📋）。
   替代：直接向 webhook URL 发一条测试 payload，按 B.10 观察全链路
```

**鉴权模型**：
- Webhook：per-Integration token（✅ 路径段）
- Email：地址 + 发件白名单（可选 DKIM/SPF）（📋，见类型矩阵）
- 开放 API：API Key（`X-Vigil-Key`）（📋，`POST /api/v1/events` 未实现；API Key 签发本身 ✅ 见 B.1）

#### 类型支持矩阵

`Integration.type` 枚举 7 种（`ent/schema/service.go`），适配器只注册了 3 个
（`internal/ingestion/adapter.go` `RegisterBuiltins`）：

| type | 适配器 | 状态 | 说明 |
|------|--------|------|------|
| `prometheus` | PrometheusAdapter | ✅ | Alertmanager webhook；`alerts[]` 每条独立成 Event |
| `grafana` | GrafanaAdapter | ✅ | unified alerting；结构同 Alertmanager |
| `webhook` | GenericJSONAdapter | ✅ | 通用 JSON 固定契约（见下） |
| `zabbix` | — | 📋 | 枚举已预留，无适配器 |
| `cloud` | — | 📋 | 云厂商，枚举已预留 |
| `email` | — | 📋 | **无 SMTP 入站**（代码中 SMTP 仅用于出站通知邮件）；目标流程见下 |
| `api` | — | 📋 | 开放 API `POST /api/v1/events` 未实现 |

**选了未实现类型会发生什么（可断言）**：创建 Integration 成功（无类型可用性校验）→ webhook 推送仍返回 202
（payload 先落库）→ 归一化 worker 找不到适配器 → RawEvent 标 **`parse_failed`**
（error=`no adapter for source type "zabbix"`），不产 Event、不通知——排查路径见 B.15。

**邮件接入目标流程（📋 整体未实现，capability 01 §3 设计）**：为 Integration 分配收信地址 →
告警源发邮件 → 发件白名单（可选 DKIM/SPF）校验 → 主题关键词解析 severity、正文入 detail → 归一化为 Event。
MVP 设计只支持纯文本主题+正文（HTML/附件后置）。

**已实现适配器的归一化差异**：

| | prometheus | grafana | webhook（通用） |
|--|--|--|--|
| source_event_id | `fingerprint`，缺省 `alertname:instance` | 同左 | `source_event_id`→`id`→`event_id` 按序取首个非空 |
| severity 来源 | `labels.severity` 按 Prometheus 映射表 | **原生 `severity` 字段优先**（通用映射表），归 info 时回退 `labels.severity` | `severity` 字段按通用映射表 |
| summary 兜底 | `annotations.summary`，缺省 `[alertname] instance` | 同左（alertname 再缺省用 `__alert_rule_uuid__`） | `summary`→`message`→`"告警（通用接入）"` |
| 多条 alert | `alerts[]` 逐条成 Event | 同左 | 单条 |
| 去重键 | `prometheus:<srcID>` | `grafana:<srcID>` | `generic:<srcID>` |

#### severity 归一映射（默认硬编码，覆盖 📋）

两套默认映射（`internal/ingestion/adapters_builtin.go`），**未知值一律兜底 `info`**：

| 归一结果 | Prometheus 映射（mapPromSeverity） | 通用映射（normalizeSeverity，grafana/webhook 用） |
|----------|-----------------------------------|--------------------------------------------------|
| `critical` | critical / error / page | critical / error / high / p1 / sev1 / urgent |
| `warning` | warning / warn | warning / warn / medium / p2 / sev2 |
| `info` | 其余全部（含空值） | 其余全部（含空值） |

> `Integration.config` 覆盖映射（M2.3"可配映射表"）：📋 未实现（代码注释明确"后续实现"）。
> 测试断言按上表硬编码值编写。

#### 自定义源接入（通用 JSON 契约）

type=`webhook` 的 GenericJSONAdapter 按**固定约定字段**解析（不支持自定义映射）：

| 字段 | 取值 | 缺省行为 |
|------|------|----------|
| `source_event_id` / `id` / `event_id` | 三选一（按序取首个非空字符串） | 都缺 → `generic-<payload字节长度>`，⚠️ 同长度的不同告警会共用去重键被误判重复——**强烈建议带 id** |
| `severity` | 字符串，按通用映射表归一 | 缺省归 `info` |
| `status` | `firing` / `resolved` | 缺省视为 `firing` |
| `summary` / `message` | 按序取首个非空 | 缺省 `"告警（通用接入）"` |
| `labels` | 子对象，值统一转字符串 | 缺失 → 空 labels → **必落 unrouted**（无 `labels.service` 无法路由，见 B.3） |
| 其余字段 | 整个 payload 存 `Event.detail` | 原文不丢 |

**操作流**：
```
1. POST /integrations {"name":"my-source","type":"webhook","team_id":...}，记下 token
2. 自研系统推送：POST /api/v1/webhook/<token>
   {"id":"evt-1","severity":"high","summary":"磁盘将满",
    "labels":{"service":"payment","env":"prod"}}
3. 断言：202 + raw_event_id → Event(severity=critical, status=firing, source=generic)
   → 路由命中 slug=payment 的 Service
```

> 📋 设计目标：JSONPath 自定义字段映射（当前只有固定约定字段契约）。

#### 生命周期（启停 / token 轮换 / 删除）

| 操作 | 端点 | 状态 | 备注 |
|------|------|------|------|
| 启停 | `PATCH /integrations/:id` `{"enabled":false}` | ✅ | PATCH 只支持 `name`/`enabled` 两个字段（config/type/归属不可改 ❌） |
| 禁用后推送 | — | ✅ | **返回 401 `invalid token`**，不落库。⚠️ capability 01 §7.2 写"返回 404"，以代码为准：为不暴露"存在但禁用"与"不存在"的差别，统一 401 |
| token 轮换 | — | ❌ 无端点 | 泄露时只能删除重建（新 token 需重新配到告警源） |
| 删除 | `DELETE /integrations/:id`（`integration.delete`） | ✅ | 原 token 立即 401；历史 RawEvent/Event 保留但来源关联清空（FK SET NULL） |

**刷屏源处置操作流（推荐演练）**：
```
1. 发现某源刷屏（B.11 报表告警量 / /metrics 的 vigil_alerts_received）
2. PATCH /integrations/:id {"enabled":false}    # 摘流，历史数据保留
3. 观察：该源推送变 401；unrouted / Incident 增量停止
4. 源侧修复后 {"enabled":true} 恢复 → 发一条测试 payload 验证 202
```

#### 限流与背压（✅ 已实现）

**顺序关键**：payload **先落 RawEvent 再检查限流/背压**（`internal/ingestion/handler.go`）——
超限也不丢告警（capability 01 §3.3 铁律：漏一条告警可能等于一次无人响应的故障）。

| 触发 | 响应码 | 可断言响应体 |
|------|--------|--------------|
| 单接入点超限 | **429** | `{"status":"rate_limited","raw_event_id":<N>,"retry_after":60}` |
| Asynq 队列积压超阈值 | **503** | `{"status":"backpressure","raw_event_id":<N>,"retry_after":30}` |

- 限流优先级：`Integration.config.rate_limit` > 全局默认（`VIGIL_INGESTION_RATE_LIMIT_PER_MIN`，代码缺省 **600/min**）。
- 无 Redis 时限流器不可用 → 放行（优雅降级，对齐 A.5）。
- ⚠️ **超限的 RawEvent 无自动回灌**：状态停在 `received`（入队失败则标 `requeued`）；
  代码注释虽写"恢复后回灌"，但**巡检回灌任务未实现（❌）**——需人工处理，见 B.15。
- 熔断（持续无效 payload 临时封禁源）📋 未实现。

#### 📋 设计目标：集成向导（M14.6）

「接入 Prometheus」式分步配置指引（选类型 → 生成配置片段 → 在线验证）未实现。
**当前替代路径**：按本节手工建 Integration → 把 webhook URL 配到告警源 → 用 B.10 验收链路验证
（无 test 端点，直接发真实/模拟 payload 观察）。

### B.5 排班 Schedule（team_admin）

> 触发权限点：`schedule.view` / `schedule.create` / `schedule.update` / `schedule.delete`（写操作已在 RouteGuard 登记）

#### 操作流（✅ CRUD 已实现，`internal/schedule/handler.go`）

```
1. 建 Schedule：POST /schedules
   {
     "name": "payment-oncall",              # 必填，缺失 400 "name required"
     "type": "rotation",                    # calendar | rotation | follow_the_sun（见下"类型的真实语义"）
     "timezone": "Asia/Shanghai",           # 缺省 Asia/Shanghai；每个 Schedule 独立时区
     "team_id": 3,
     "layers": [                            # 分层（primary/secondary 用 priority 表达）
       {"name":"一线","priority":0,
        "participants":[42,43],             # 值班人 user id；★ 有 participants 才会建 Rotation
        "rotation_type":"daily",            # daily | weekly | custom；非法值 400 "invalid layer 一线: invalid rotation_type"
        "shift_length":"24h",               # 缺省 24h；支持 Go duration（"168h"）与 "1week"
        "handoff_time":"09:00",             # 交接时刻，缺省 09:00
        "start_date":"2026-07-01T09:00:00+08:00"},  # RFC3339，非法 400；缺省=创建时刻
       {"name":"二线","priority":10,"participants":[44]}
     ]
   }
2. 改 Schedule：PATCH /schedules/:id {name?, type?, timezone?, layers?}
3. 删 Schedule：DELETE /schedules/:id（不存在 404）
4. 列表：GET /schedules（团队数据隔离：team 级用户只见自己团队的排班）
```

> ⚠️ **PATCH 的 layers 只更新 JSON 层信息，不创建/修改 Rotation 实体**（`buildRotation` 只在 create 走）。
> 即：**改参与人/班次参数无法通过 PATCH 完成**，当前只能删除重建 Schedule。写用例时按此现状断言（📋 backlog：Rotation 管理端点）。
> 同因，`Rotation.participants` 无独立管理入口（B.2 已提及）——人员进出只能重建排班。

**计算算法**（`schedule/engine.go`，与 capability 03 §2.2 一致）：
班次序号 = `floor((T − start_date) / shift_length)`，在班人 = `participants[序号 mod 人数]`，`handoff_time` 前沿用上一班。
时间按 **Schedule.timezone** 换算后计算；时区字符串非法时**降级 UTC 继续算，不报错**（跨时区验证时注意这一兜底）。

**类型的真实语义（⚠️ 纠偏）**：`type` 三枚举可存库，但**引擎计算完全不读该字段**——
`calendar` / `rotation` 走同一套 Rotation 轮转算法，无任何行为差别；`follow_the_sun`（跨时区接力）📋 无专门实现。
测试用例不必按 type 区分预期。

#### 查看与验证

| 途径 | 端点/入口 | 状态 | 说明 |
|------|-----------|------|------|
| 实时查在班 | `GET /schedules/:id/oncall?time=<RFC3339>` | ✅ | time 缺省 now；非法格式 400 `invalid time (use RFC3339)` |
| 预览未来 N 天 | `GET /schedules/:id/preview?days=14` | ✅ | days 默认 14 上限 90；**每天取正午 12:00 作为代表时刻**（一天多班次时预览只显示正午那班） |
| Web 排班页 | `web/src/pages/oncall.tsx` | ✅ | 当前在班 + 未来 N 天预览（**列表式，非日历网格**）+ 创建/编辑表单 |
| IM 查询 | `/vigil oncall` | ❌ | 命令未实现（返回 403 `no permission mapping`，非 unsupported，见 C.3.5） |

> ⚠️ **oncall 响应结构纠偏**：实际返回 `{schedule_id, schedule_name, layers:[{name, priority, users:[{id,name,username,override}]}]}`
> （按 priority 升序），**不是**能力域文档写的 `{primary, secondary, overrides}`。`users[].override` 字段恒为 `false`（见 B.5.1）。

**"预览仅展示，谁在班实时算"**：Schedule 是纯蓝图不存快照，升级触发时的值班人以**触发时刻**实时计算为准——
预览页看到的和真正被通知的可能因配置变更而不同，这是设计使然（B.0 关键约束）。
`OncallNow` 当前**无 Redis 缓存**（代码 TODO），每次实时算；设计目标的"分钟级缓存"未实现，对正确性无影响。

#### B.5.1 换班 Override 操作流 — 📋 完全未实现（⚠️ 纠偏）

> （附录 A"自己换班 Override"行已同步标 📋。）

**当前行为（核对 2026-07-03 代码）**：
- **无 Override 实体**：`ent/schema/schedule.go` 只有 Schedule / Rotation 两实体，layers 注释虽提"override 层"但无时间窗结构；
- **无写入口**：`schedule/handler.go` 仅 CRUD + oncall + preview，无 `/schedules/:id/overrides` 类端点（`wire.go` 仅注释提及 M5.3）；
- **引擎无解算逻辑**：`engine.go` 的 `OncallUser.Override` 布尔字段存在但**恒为 false**（`userModels` 全部调用点传 false），没有任何"时间窗内顶替"计算；
- **权限点悬空**：`schedule.override` 已定义且内置 `oncall` 角色持有（`seed.go`），但无任何路由/逻辑引用它。

**当前替代路径（如实）**：没有优雅的换班手段——只能删除重建 Schedule 调整 participants 顺序或 `start_date`（高危、影响整层），
或由顶班人自觉响应（升级链通知的仍是原值班人）。离职/请假场景的处置建议见 B.14。

**设计目标（📋，capability 03 §2.4，供后续验收）**：

```
POST /schedules/:id/overrides                # 发起人：oncall 用户自助 或 admin 代操作
{ "user_id": 44,                             # 顶班人
  "start": "2026-07-05T09:00:00+08:00",      # 时间按 Schedule.timezone 解释
  "end":   "2026-07-06T09:00:00+08:00",
  "reason": "张三请假" }
权限：schedule.override —— 限"自己的班"或 admin（越权替别人换班应 403）
生效验证：窗口内 GET oncall → users[].override=true 且为顶班人；窗口外自动恢复原 Rotation 结果
与升级联动：升级触发实时解算 → Override 生效期间升级通知发给顶班人（无需改升级策略）
```

#### B.5.2 空班检测 — 📋 未实现（⚠️ 纠偏：与现文/B.14/C.7 断言相反）

> "空班 → 告警 team_admin"在**代码中不存在**。B.14/C.7 的相关表述已按此纠偏。

**当前实际行为**：
- `schedule/engine.go` 某层算不出在班人（无 participants / shift_length 非法）时**静默 `continue`**（L94–96）——该层直接从结果中消失，无日志无告警；
- `escalation/engine.go` `resolveTargets` 对 schedule target 拿到空 layers 时**无人可通知也不报错**（仅 target 级查询失败才 Warn 日志）；升级链照常推进；
- **唯一可观测信号**：时间线仍会记 `升级到 level N，通知 0 人`（TimelineItem detail `notified:0`）——用例可据此断言空班。

**空班的产生路径（枚举，供测试构造）**：

| 路径 | 结果 |
|------|------|
| layer 创建时不带 participants | 不建 Rotation → 该层永远为空 |
| Schedule 无任何带参与人的 layer | oncall 返回 `layers: []`（HTTP 200，空数组） |
| 查询时刻早于 start_date | **不空**——取 participants 首人（引擎兜底） |
| shift_length 写错（如 "1天"） | **不空**——解析失败降级默认 24h |
| 值班人被禁用（B.14） | **不空但等于空**——引擎不查 User.status，仍会通知已禁用用户 ⚠️ |

**当前兜底**：完全依赖升级策略**末级 target=team** 的配置兜底（B.6 推荐末层"全团队"）——排班空转时至少末级还有人收到。
**设计目标（📋，capability 03 §2.4）**：引擎检测到计算结果为空 → 主动告警 `team_admin`，避免"无人值班"静默发生。

**可断言测试要点**：

| 场景 | 预期（按当前实现） |
|------|------|
| 两人 daily 轮转，T 在第 2 个班次 | oncall 返回第 2 人（`floor(elapsed/24h) mod 2`） |
| handoff_time=09:00，T=第 2 天 08:00 | 仍返回第 1 人（未到交接时刻沿用上一班） |
| timezone 写成 "Asia/Beijing"（非法） | 不报错，按 UTC 计算（结果可能偏移 8h）⚠️ |
| 所有 layer 无 participants | oncall 200 + `layers` 为空；升级时间线记"通知 0 人" |
| PATCH layers 换 participants | rotation 不变，在班人**不变**（见上纠偏） |

### B.6 升级策略 EscalationPolicy（team_admin）

> 触发权限点：`escalation.view` / `escalation.create` / `escalation.update` / `escalation.delete`

#### 配置结构（✅ CRUD 已实现，`escalation/handler_policy.go`）

```
POST /escalation-policies
{
  "name": "payment-escalation",     # 必填，缺失 400 "name required"
  "repeat_times": 1,                # ★ 策略级字段（见下纠偏）
  "levels": [                       # 有序升级层级（允许为空——空则该策略等于"无需升级"）
    {"level":0, "delay_minutes":5,  "targets":[{"type":"schedule","target_id":"1"}],
     "notify_channels":["im","phone"]},
    {"level":1, "delay_minutes":10, "targets":[{"type":"team","target_id":"3"}]}
  ]
}
PATCH /escalation-policies/:id {name?, repeat_times?, levels?}   # 部分更新
GET/DELETE 常规；列表按团队隔离
```

> ⚠️ **repeat_times 纠偏**：原文"本层重复通知次数"不准确——`repeat_times` 是 **EscalationPolicy 策略级字段**
> （`ent/schema/escalation_policy.go`；`escalation/engine.go` 判定 `p.RepeatSeq < policy.RepeatTimes`）。
> 语义：**每一层**都按同一个 repeat_times 重复，每层实际通知次数 = `repeat_times + 1`，
> 重复间隔 = **该层自己的 delay_minutes**（重复也走同样的延迟入队）。按层配置重复次数 📋 未实现。

> ⚠️ **notify_channels 当前不生效**：levels 里的 `notify_channels` 可存库但**无任何代码读取**——
> 通知统一走全局默认通道（`wire.go`：`[webhook?, im, email]`，webhook 仅在配置了出站 URL 时加入）。按层选通道 📋。

> ⚠️ 建策略请求体**无 team_id 字段**：策略不归属任何团队 → team 级用户创建后**在列表里看不到自己刚建的策略**
> （SEC-01 按 team 过滤把无主策略滤掉，仅 org 级用户可见）。当前实操建议由 org_admin 建策略，或按此现状断言。

#### target 解析语义（升级触发时实时解算，`engine.go` resolveTargets）

| type | 解析结果 | 备注 |
|------|----------|------|
| `schedule` | 该排班**所有层的并集**（primary + secondary 一起通知） | 不是只通知第一层；层内空见 B.5.2 |
| `user` | 直接查库 | 用户不存在 → Warn 日志跳过，不阻断 |
| `team` | 占位 target（`team:<id>`），**成员展开未实现** 🟡 | 按 user 解析的通道（邮件/电话）收不到；IM 群播通道不受影响 |

多 target 命中同一人时**按 user id 去重**（同一次升级同一人只通知一次）。

#### 完整升级时间轴示例（policy：repeat_times=1，level0 delay=5m targets=schedule，level1 delay=10m targets=team）

```
t0        Incident 创建（triggered）→ 入队 level0 延迟任务（ProcessIn 5m）
t0+5m     level0 首次触发：实时解算排班 → 通知在班人（全层并集）
          status→escalated，current_level=1，escalated_count=1，时间线"升级到 level 1，通知 N 人"
t0+10m    level0 重复（repeat_seq=1）：再通知一次同批人，escalated_count=2
t0+20m    level1 首次触发：通知全团队，current_level=2，escalated_count=3
t0+30m    level1 重复 → 末级重复用完，不再入队任何任务（升级链自然终止）
任意时刻 ack → CancelOnAck 删除全部待触发任务（level × repeat 组合遍历删）
          + 状态守卫兜底：漏删的任务到点发现 status∉{triggered,escalated} 则 no-op
          （日志 "escalation: skip, incident not active"）
```

- `delay_minutes=0` 合法：任务立即执行（e2e 用例即用 delay=0 验证多层推进，见 `test/e2e/escalation_test.go`）。
- `levels` 为空合法：创建成功，Incident 绑定后 `StartEscalation` 直接跳过（视为无需升级）——**无参数校验拦截，注意别误配**。

#### 手动升级（escalate）的行为

`POST /incidents/:id/escalate`（或 IM 按钮/命令）→ 目标层 = `current_level`（即下一层，0-based），
经事件总线调 `TriggerLevelNow`：**ProcessIn(0) 立即触发**，并从该层起继续排后续链（重复/下一层）。

> ⚠️ **手动升级不取消已排定的延迟任务**：原 level 的 pending 任务到点仍会触发（escalated 属活跃态，状态守卫不拦），
> 表现为**额外一轮通知 + escalated_count 再加一**；下一层任务靠 TaskID（`esc:<inc>:<level>:<seq>`）幂等去重不会重复入队。
> 写用例时对"手动升级后通知次数"按此断言。
> resolved/closed 状态下手动 escalate → 400（`invalid transition`）。

#### 策略修改对在飞 Incident 的影响（可断言）

Asynq 任务 payload 只带 `{incident_id, level_idx, repeat_seq}`——**每次任务触发时从 DB 重读策略**。
因此：改 levels 的 targets/repeat_times 对在飞 Incident 的**下一次触发即生效**（用新参数）；
但已入队任务的**触发时刻不变**（delay 在入队时已确定）；删除策略或 level_idx 越界 → 任务静默结束。

**可断言观测点**：TimelineItem `type=escalated` + detail `{level, notified}`；`incident.current_level` / `escalated_count`；
Prometheus `vigil_escalations_triggered_total`。

**与排班联动**（保留）：target.type=schedule 时每次升级实时解算值班人——排班变了，下一次升级立刻跟上。

### B.7 通知规则与模板（team_admin）

> 触发权限点：`notification.rule.*` / `notification.template.*`（team_admin 内置角色已含全套）

#### 建规则操作流（✅ CRUD 已实现，`notification/handler.go`）

```
1. 建规则：POST /notification-rules
   {
     "name": "payment-night",            # 必填
     "condition": {"severity":"critical"},  # ⚠️ 可存库但当前不参与评估（见下纠偏）
     "channels": ["im","email"],         # 缺省 ["im","webhook"]；⚠️ 当前同样未按规则分发（见下）
     "template_id": "my_card",           # ★ 存的是模板 name（字段名叫 id，实为名称字符串）
     "quiet_hours": {"enabled":true,"start":"22:00","end":"07:00",
                     "timezone":"Asia/Shanghai","bypass_for":["critical"]},
     "team_id": 3, "enabled": true
   }
2. 干跑验证：POST /notification-rules/:id/test?incident_id=N   # 见下"干跑"
3. 改/停用：PATCH /notification-rules/:id {enabled:false, ...}   # 实时生效（每次通知评估都查库）
```

#### 多规则同时命中的裁决语义（⚠️ 纠偏：当前无匹配逻辑）

运行时（`wire.go` 装配的 resolver）**取"首条 enabled 规则"** 的 quiet_hours 与 template_id——
`condition`（severity/team/service）**完全不参与评估**，`channels` 字段也不用于分发（升级通知走全局默认通道）。
即：多条规则并存时只有查询返回的第一条生效；"按团队/severity 匹配规则"📋 未实现。
**当前实操建议：全组织只维护一条 enabled 规则**，多规则语义留给用例的"已知限制"栏。

#### 干跑（✅ dry-run）

`POST /notification-rules/:id/test?incident_id=N` → `{rule_id, rule_name, channels, quiet_hours_suppress, quiet_hours, severity}`，无副作用。
注意两点：① **按"非值班人"判定**（无 target 上下文，isOncall=false）——真实发送时值班人会穿透静默，干跑结果比实际更保守；
② incident 不存在时返回 **200 + `error:"incident not found"` 字段**（不是 404），断言按此写。

#### 模板（✅ 引擎 + CRUD + preview）

- **内置 3 个**（启动时幂等 seed，`template.go`）：`default_im_card` / `default_email` / `default_webhook`；每次启动会**覆写回代码定义**（改内置=白改）。
- **变量**：`{{.Incident.Number}}` `{{.Incident.Severity}}` `{{.Incident.Title}}` `{{.Incident.Status}}` `{{.Incident.Summary}}`
  `{{.Service.Name}}` `{{.Level}}` `{{.Now.Format "…"}}`；辅助函数 `upper`/`lower`/`trim`。
  ⚠️ `{{.ActionURL}}` 字段存在但**当前无注入点，渲染恒为空**。
- **自定义生效方式**：`POST /notification-templates`（name + title_template 必填）→ 在 NotificationRule.template_id 填**模板名**。
- **内置模板保护**：PATCH/DELETE 内置 → **403** `builtin template cannot be modified/deleted`。
- ⚠️ **"同 name 覆盖内置"纠偏**：模板 name **无唯一约束**，创建与内置同名的模板不会"覆盖"——
  反而使按名查询歧义（`Only` 报错）→ 渲染**降级回内置代码常量**。想自定义请用新名字 + 规则指过去。
- **语法错误降级 ✅**：title/body 模板 parse 或渲染失败 → 自动用 `FormatTitle`/`FormatSummary` 兜底文案，**不丢通知**。
- **preview ✅**：`POST /notification-templates/:id/preview?incident_id=N` → 渲染后的 `{title, body}`（所见即所得）。

#### quiet_hours 细则（✅ `quiet_hours.go`，可直接做验收矩阵）

判定顺序（`ShouldSuppress`）：
1. 未启用（`enabled=false` 或未配）→ 不静默；
2. severity ∈ `bypass_for`（**缺省 ["critical"]**）→ 穿透，任何时刻都通知；
3. **值班人始终通知 ✅**：isOncall=true 不静默——实现上"值班人"= 升级 target **来源是 schedule**（`notifier.go` `t.Source=="schedule"`），user/team 类 target 视为非值班人；
4. 当前时刻落在 `[start, end)` 窗内 → 静默。

- **跨午夜窗口 ✅**：`start > end`（如 22:00–07:00）按 `[start,24:00)∪[00:00,end)` 判定；`start==end` 视为未配置（不静默）；HH:MM 非法保守不静默。
- **时区依据**：按规则里的 `quiet_hours.timezone`（IANA）换算，非法降级 UTC；⚠️ **不是按通知目标用户的时区**（capability 04 Q3 的"按 target 用户时区"📋 未实现，User.timezone 不参与，见 B.9.1）。
- **被静默通知的去向（⚠️ 重要）**：直接**不发送、不入聚合队列、无补发机制**——该次触发的通知就没了。
  实际"窗口结束后还能收到"依赖升级链的后续触发（repeat / 下一 level）在窗口外重新评估。静默期结束补发 📋。

**验收矩阵（severity × isOncall × 时刻，规则 22:00–07:00 Asia/Shanghai）**：

| severity | target 来源 | 23:30 | 08:00 |
|----------|-------------|-------|-------|
| critical | 任意 | 通知（bypass 穿透） | 通知 |
| warning | schedule（值班人） | 通知（值班人不静默） | 通知 |
| warning | user/team | **静默（丢弃）** | 通知 |
| info | user/team | **静默（丢弃）** | 通知 |

#### 风暴场景下你会收到什么（✅ 聚合，`aggregator.go` + `notifier.go`）

- **机制**：Redis per-target 队列（`vigil:pending_notify:user:<id>`）。非 critical 通知入队不立即发；
  独立 `:win` 标记 key 记录窗口（**默认 30s**，`wire.go` 硬编码）；后台 flusher **每 15s**（窗口/2）扫描，`:win` 过期即合并发送。
- ⚠️ 窗口标记**每次入队都重置**——实际是"自最后一条起 30s"的滑动窗口；风暴持续期间聚合会一直攒着，风暴停 30s 后一次性合并。
- **critical 旁路 ✅**：不进队列，立即单发。无 Redis 时聚合降级为"全部立即发"（保送达）。
- **合并消息形态**（`FlushAggregated`，能写多细写多细）：标题 `[聚合] <首条标题>`，正文 `<首条 Summary>（含 N 条聚合通知）`——
  只展示首条内容 + 总数，**不逐条列出**（"列出多个事件"📋）；送达记录记在首条的 incident 上。
- **延迟上界**：最后一条通知时刻 + 30s 窗口 + ≤15s flusher 周期。

**验收**：30s 内向同一 target 发 5 条 warning + 1 条 critical → 收到 **1 条独立 critical（即时）+ 1 条合并消息（约 30–45s 后，标注"含 5 条聚合通知"）**。
观测：`vigil_notifications_sent_total{channel,result}` 与日志 `notification delivered`。

#### B.7.1 为计划内变更立维护窗（✅ 规则 CRUD + 引擎已实现）

> 触发权限点：`suppression.*`（team_admin 内置角色已含）；引擎在 `triage/suppression.go`，评估点=去重后、路由前。

**场景**：周六 02:00–04:00 计划内升级 payment 服务，期间告警不打扰人、但要留痕可复盘。

```
1. 建规则：POST /suppression-rules
   {
     "name": "payment-maintenance-0705",
     "match_labels": {"service":"payment"},        # 全等匹配：每个 k=v 都必须出现在 Event.labels
     "time_window": {"start":"2026-07-05T02:00:00+08:00",   # RFC3339 绝对时间（按处理时刻判定）
                     "end":  "2026-07-05T04:00:00+08:00"},
     "severity_filter": ["info","warning"],        # 空=所有严重度
     "action": "suppress",                         # suppress | reduce_severity
     "preserve_critical": true,                    # ★ 缺省即 true（schema Default）
     "team_id": 3, "enabled": true
   }
2. 窗口内命中：Event 标 is_noise=true，留痕不建单不通知（triage 返回 suppressed，B.13.1 可复核）
3. reduce_severity 分支：{"action":"reduce_severity","reduce_to":"warning"}
   → 只降级不拦截，Event 以新严重度继续路由/聚合（如 critical 风暴降为 warning 以便聚合少打扰）
   reduce_to 缺省 = 逐级降一档；配置成"升级"会被拒绝（保持原级，绝不升 severity）
4. 窗口过后：time_window 不再命中，规则自动失效但本体保留 —— 记得删除或 enabled=false 防误留
5. 误配回退：PATCH /suppression-rules/:id {"enabled":false} 即时生效（每条 Event 实时查库评估）
```

**守卫与语义细则（对照代码，可断言）**：
- `preserve_critical` 默认 **true**：critical 的 Event 命中规则时（suppress 和 reduce_severity 两种动作都）**跳过该规则**继续评估下一条——维护窗内真故障不会被误杀。要连 critical 一起压须显式传 `preserve_critical:false`。
- ⚠️ **expires_at 纠偏**：schema 有 `expires_at` 字段、引擎也会跳过已过期规则，但 **create/update 请求体里的 expires_at 被 handler 忽略**（未 SetExpiresAt）——当前**无法通过 API 设置规则到期时间**（📋）。时限语义请用 `time_window` 表达；"到期自动清理规则本体"同样未实现，需人工删。
- 多条规则同时命中：**取查询顺序的首条**（代码注释声称按 expires_at 排序，实际未加排序，实践为 id 升序）——无优先级语义，避免依赖多规则叠加。
- `status=resolved` 的 Event 不评估（恢复信号放行，保证能关单）。
- 观测：抑制量反映在 B.11 `GET /analytics/alerts` 的降噪率（noise 计数）；被误压的告警在 B.13.1 噪音池复核。

**验收断言表**：

| 场景 | 预期 |
|------|------|
| 窗口内 warning，labels 含 service=payment | Event 落库 is_noise=true，无 Incident、无通知 |
| 窗口内 critical（preserve_critical 默认） | **不抑制**，正常建单通知 |
| 窗口内 labels 无 service 键 | 不命中（match_labels 全等），走正常路由（大概率 unrouted） |
| 窗口外同样的 Event | 不命中，正常处理 |
| time_window 只写 start 不写 end | end 解析失败 → **保守不命中**（规则等于没配窗口条件之外的部分仍需满足） |
| name 缺失 | 400 `name required` |

### B.8 Runbook（team_admin）

> 触发权限点：`runbook.view` / `runbook.create` / `runbook.update` / `runbook.delete` / `runbook.execute`

#### 类型与两档安全（设计基线，保留）

| 类型 | 触发 | 执行 | 安全 |
|------|------|------|------|
| `document` | 展示给人看 | 不执行 | 无风险 |
| `executable`（diagnose） | manual（自动触发见下 ⚠️） | readonly → Vigil 直接跑 | 默认安全 |
| `executable`（remediation） | 同上 | 写操作 → `require_approval:true` 人确认 | **绝不**直接动生产 |

#### 操作序列（✅ CRUD + execute 已实现，`runbook/handler.go`；Web 页 `web/src/pages/runbooks.tsx`：列表/详情 markdown/创建/execute）

```
1. 创建：POST /runbooks
   {"name":"restart-pool",                       # 必填
    "type":"executable",                         # document | executable
    "content_markdown":"…",                      # 文档正文（document 型主体）
    "trigger":{"type":"manual"},                 # ⚠️ 可存库但不评估，见下
    "steps":[
      {"name":"查连接池水位","action":{"type":"diagnose",
        "target":{"kind":"internal","endpoint":"https://pool.example.com/health","readonly":true}},
        "on_failure":"continue"},
      {"name":"重启连接池","require_approval":true,"action":{"type":"execute",
        "target":{"kind":"http","endpoint":"https://ops.example.com/hooks/restart-pool","readonly":false},
        "params":{"pool":"payment"}},
        "on_failure":"escalate"}]}
2. 验证执行：POST /runbooks/:id/execute {"incident_id":42,"approved":false}
   → 只读步骤真实执行；写步骤 skipped=true（error="write action requires approval, skipped"）
3. 正式执行（人已确认）：{"incident_id":42,"approved":true} → 写步骤真实执行
4. 更新/删除：PATCH / DELETE —— 即时生效，无版本化、无历史留存（改错即所有后续执行按新definition跑）
```

**强校验 ✅（数据层兜底，create 与 update 都拦）**：写步骤（`target.readonly=false`）必须 `require_approval=true`，
否则 **400** `step "X" is a write action (readonly=false) and must set require_approval=true`。
引擎侧双保险：凡 readonly=false 且 `approved=false` 一律 skip（哪怕数据里 require_approval 被绕过成 false）。
Skipped 步骤**不写时间线**；成功/失败步骤记时间线（IncidentAction 审计 📋 未实现，见 C.5.3）。

**绑定 Service 的方式（⚠️ 当前断链）**：schema 有 Service↔Runbook 边，但 **service 与 runbook 两侧的 create/update 请求体都不暴露关联字段**——
Runbook 无法通过 API 挂到 Service，"告警命中服务自动展示关联 Runbook"链路不通（📋）。
当前用法 = 靠命名约定人工找 + execute 时在请求体里直接给 `incident_id`。

> ⚠️ 建 Runbook 请求体也**无 team_id**：与 B.6 策略同款问题——team 级用户创建后 list 被团队过滤看不到自己刚建的（org 级可见）。

#### 执行器现状（⚠️ 纠偏）

| kind | 状态 | 行为 |
|------|------|------|
| `http` | ✅ | POST JSON(params) 到 endpoint；返回结构化输出 `{"status_code":N,"body":"…"}`；HTTP ≥400 视为失败走 on_failure |
| `internal` | ✅ | `params.action=check_http`（GET 探活，返回 status_code+latency_ms）/ `info`（回显 target 元信息） |
| `ansible` / `jenkins` | 📋 | **无执行器**——步骤结果 `error: no executor for kind "jenkins"`，按该步 on_failure 处理 |

> "Jenkins 重启连接池"情节按当前实现应理解为 **http 执行器调用 Jenkins 的 webhook**（附录 C 剧本 1 第 6 步已按此表述）。
> `trigger` 字段（manual/on_incident/on_severity/on_label_match）**可存库但无任何评估代码**——自动触发 📋，当前只有手动 execute（衔接 C.5.0）。

**SSRF 防护 ✅（`runbook/ssrf.go`，SEC-03）**：scheme 仅 http/https（file:// 等直接拒）；
私网/loopback/云元数据（169.254.169.254）地址在 **TCP 连接建立时**校验真实 IP（防 DNS rebinding，无 TOCTOU 间隙）→ 拒绝报 `endpoint blocked by SSRF protection`。
`SetAllowPrivate(true)` 仅供测试（httptest 绑 127.0.0.1）；生产保持默认拒私网——**内网处置端点需经公网可达的网关/跳板暴露**。

**凭据托管（⚠️ 纠偏）**：原文"Executor 凭据由管理员加密托管"是设计目标（capability 06 Q1），📋 **未实现**——
当前调用凭据只能**明文写进 steps 的 endpoint/params**（随 Runbook 存库、`runbook.view` 权限即可读到）。
敏感凭据建议放在外部执行侧（如 webhook 网关校验来源），不要写进 Runbook。

**list 团队隔离 ✅**：team 级用户仅见绑定了自己团队的 Runbook（无主 Runbook 仅 org 级可见，同上 quirk）。

### B.9 用户绑 IM 账号 — 🟡 已实现但为 org_admin 代绑（⚠️ 纠偏："自助"是设计目标）

> 触发权限点：`user.im.bind`

**这是旅程 C 能在 IM 操作的前提**：User 绑定 `platform + account_id`（IM unionId）后，IM 回调才能映射回 Vigil User 走鉴权链。

**权限纠偏**：`user.im.bind` 内置角色中**仅 org_admin 持有**（`seed.go`，org_admin=全权限点；team_admin/oncall/responder 均无）——
原文"每个 oncall 用户自助"当前不成立，**实际 = org_admin 代绑**；"用户自助绑定"（本人 OAuth 扫码/免密流程）📋。

**前端现状**：**无绑定页面**——`settings/im-tab.tsx` 只展示各平台适配器就绪状态（`GET /im/platforms`），
users-teams 页也无绑定入口。实操 = org_admin 拿 JWT 直接调 API：

```
POST /users/42/im-accounts        # user.im.bind
{"platform":"feishu",             # dingtalk | feishu | wecom
 "account_id":"on_xxxxxxxx"}      # IM 平台 unionId
→ 201；platform/account_id 缺失 → 400 "platform and account_id required"
→ IM 模块未装配 → 503 "im account binding not configured"

GET /users/42/im-accounts         # 查询已绑列表 ✅
```

**unionId 从哪来**：无自助流程时，从 IM 平台管理后台（通讯录/成员详情）查，或从 Vigil 收到的 IM 回调日志里捞——
两者都是管理员操作，进一步说明当前是"代绑"模式。

**语义细则（对照 `im/mapper.go`，可断言）**：
- **幂等 ✅**：同 platform+account_id 重复 POST 不重复添加（返回成功）。
- **多平台绑定 ✅**：一人可绑多个平台（feishu+dingtalk 各一条）；`ResolveUser(platform, unionId)` 按平台独立解析（双写 IMAccountBinding 索引表 + User.im_accounts JSON）。
- ⚠️ **绑错无法自愈**：**无 DELETE 端点**；且同 platform+account_id 已绑给 A 后再 POST 给 B **不会迁移**——
  索引表因"已存在"跳过（仍归 A），JSON 字段却追加到 B（数据分叉），ResolveUser 优先走索引表**仍解析为 A**。
  绑错当前只能 DB 直改（📋 解绑端点）。用例应覆盖"重复绑给他人不生效"这一负路径。
- **负路径（衔接 C.3.2）**：未绑定用户在 IM 点卡片按钮/发命令 → **403** `im account not bound, please bind in web`。
- **与 B.14 衔接**：禁用用户的绑定关系**不自动清**；离职交接须手动处理（当前也没有解绑手段，见上）。

#### B.9.1 用户自助设置（时区/语言）

| 项 | 现状 | 说明 |
|----|------|------|
| 时区 | 🟡 可改但形同展示字段 | `PATCH /users/:id {"timezone":"Europe/London"}` ✅；⚠️ 该端点**未登记权限点**（见开放问题 8）——**任何登录用户可改任何人**的 timezone/status，"仅本人"是设计目标（📋，见开放问题/备忘） |
| 时区生效范围 | ⚠️ 当前不参与任何计算 | 排班按 **Schedule.timezone** 算（B.5）、quiet_hours 按**规则配置的 timezone** 算（B.7）——`User.timezone` 存了不用，"按用户时区展示/静默"📋 |
| 语言偏好 | ❌ | User schema 无 language/locale 字段，通知与界面语言不可按人配置 |

### B.10 验收：发一条测试告警走通全链路

> 依赖：B.3 已有 active Service（slug=payment）、B.4 已建 type=webhook 的 Integration（记下 token）、
> B.5/B.6 排班与升级策略已挂到 Service。走通本节，旅程 B 才算交付完成。

#### 可执行 curl 样例（generic JSON 契约，对照 B.4）

```bash
TOKEN=vig_int_xxxxxxxx    # 建 Integration 时唯一一次回显的 token
curl -i -X POST "http://localhost:8080/api/v1/webhook/$TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "id": "drill-001",                       # ★ 务必带 id：dedup_key = generic:drill-001
    "severity": "critical",                  # 通用映射表归一（critical/error/high/p1/sev1/urgent → critical）
    "summary": "支付服务 5xx 错误率 > 5%（演练）",
    "labels": {"service": "payment", "env": "prod"}   # labels.service 必须等于 Service.slug
  }'
```

#### 每步观察点（含无 UI 时的替代观测手段）

| # | 环节 | 怎么观察 | 预期 |
|---|------|----------|------|
| 1 | 接入 | curl 响应 | **202** `{"status":"accepted","raw_event_id":N}`（429/503 见 B.4 限流背压） |
| 2 | RawEvent 落库 | ⚠️ 无查询端点，psql：`SELECT id,status FROM raw_events ORDER BY id DESC LIMIT 1` | status `received→normalized`（失败=`parse_failed`；无 `processed` 态，见 B.15） |
| 3 | 归一化 | worker 日志 `normalize worker: processing/done` | Event 落库（⚠️ **无 `GET /events` 端点**——DB 直查 `events` 表，或看 `GET /analytics/alerts` 的 total 增量） |
| 4 | 分诊+路由 | worker 日志 `triage worker: done event_id=… action=incident_created incident_id=…` | action=incident_created；unrouted 则检查 labels.service 与 slug |
| 5 | 建单 | `GET /incidents?status=triggered` | 新 Incident（number/severity/team 归属正确） |
| 6 | 升级计时 | 等 level0 的 delay 到点后 `GET /incidents/:id/timeline` | TimelineItem `escalated`"升级到 level 1，通知 N 人"（N=0 即空班，见 B.5.2） |
| 7 | 通知送达 | `/metrics` 的 `vigil_notifications_sent_total{channel,result}` 增量 + 日志 `notification delivered/failed` | 每通道一条结果记录 |
| 8 | IM 卡片 | oncall 的 IM 收到交互卡片 | 未配 IM 的替代通道：`GET /im/platforms` 确认就绪状态；用 email/webhook 通道验证送达（A.5 降级设计） |

其余关键指标：`vigil_alerts_received_total` / `vigil_incidents_created_total` / `vigil_escalations_triggered_total` / `vigil_incident_resolve_duration_seconds`。

#### 幂等 / 聚合验收（分诊三级的可断言行为）

| 动作 | 预期（对照 `triage/engine.go`） |
|------|------|
| **同一告警推两次**（同 id，5 分钟 dedup 窗内） | raw_event **2 条**、Event **2 条（第二条 is_noise=true，action=dedup_skipped）**、Incident **1 条** ——注意不是"Event 1 条"：Event 先落库后去重，去重是标噪不拦截落库 |
| 换 id 再推（同 service+severity，5 分钟聚合窗内） | 不新建单——Event 并入既有活跃 Incident（action=aggregated，事件挂同一 incident_id） |
| Prometheus 源推 `alerts[]` 含 3 条 | **逐条产 3 个 Event**（dedup_key 各自独立 `prometheus:<fingerprint>`），聚合与否按上一行规则 |
| 推 `status:"resolved"` 的同源告警 | 不去重（resolved 是收尾信号），触发既有 Incident 解决流程（见 C.2.1） |

#### 收尾（人侧闭环）

```
1. oncall 在 IM 点 [ack]（或 POST /incidents/:id/ack）
   → status=acked，升级任务全部取消（CancelOnAck + 状态守卫），卡片实时刷新
2. 处置后标记 resolved
   →（🟡 自动起草未接）手动调 POST /incidents/:id/postmortem/draft 起复盘草稿（见 C.6）
```

#### 与 e2e 测试的对应关系（`test/e2e/`，`make test-e2e` 一键跑）

| 本节环节 | e2e 用例 |
|----------|----------|
| 步骤 1–5（接入→归一化→分诊建单，Event/Incident 分离） | `pipeline_test.go` |
| 步骤 6 + ack 取消（2 层 delay=0 推进、状态守卫） | `escalation_test.go` |
| 聚合验收（同窗并入） | `aggregation_test.go` |
| B.9 绑定 + ResolveUser + 未绑定拒绝 | `im_binding_test.go` |
| ack/resolve/escalate 动作与时间线 | `incident_action_test.go` |
| 登录/改密/RBAC 前置 | `auth_test.go` / `change_password_test.go` / `rbac_test.go` |

> 手工验收覆盖不了的（IM 卡片真实送达、聚合合并消息形态）在预生产环境配真实 IM 后补验。

### B.11 数据报表与分析（能力域 15）

> 触发权限点：⚠️ **无**——`/analytics/*` 未在 RouteGuard 登记权限点，仅需登录态；
> 且引擎查询**不做团队 scope 过滤**（`internal/analytics/engine.go` 全量查询，`TeamLoad` 遍历全部团队）。
> 原文"数据按团队 scope 隔离——只看到自己团队"与代码相反（纠偏）：**任何登录用户可见全组织指标与所有团队负载**。
> 按团队隔离 / 独立报表权限点 📋（开放问题候选）。

**角色**：`team_admin` / `responder_lead` / `subscriber`（团队 Leader 看全貌——当前实际是"组织全貌"）
**Web 入口**：仪表盘首页（`web/src/pages/dashboard.tsx`，固定近 7 天：4 KPI + severity 分布 + 团队负载；无时间选择器，其余维度无下钻页，深挖走 API）。

#### 端点与参数（均 ✅，`internal/analytics/handler.go`；查询时实时聚合）

| 端点 | 参数（默认/边界，可断言） | 内容 |
|------|------|------|
| `GET /analytics/dashboard` | `days`：默认 7；非数字/≤0 → 7；窗口=[now−days, now] | 一次返回 Alert+Incident+Load+Postmortem 四块汇总 |
| `GET /analytics/alerts` | `start`/`end`（RFC3339）；**缺省=不限=全量历史**；非法格式**静默忽略**（不报 400） | 告警量 / 降噪率 / unrouted 数 |
| `GET /analytics/incidents` | 同上 | 数量 / severity / status 分布 / MTTA / MTTR |
| `GET /analytics/team-load` | 同上；⚠️ **无 `team` 参数** | 各团队 Incident 数（仅此一项，见纠偏） |
| `GET /analytics/postmortems` | 同上 | 复盘 Total / Published / 完成率 |
| `GET /analytics/trend` | `days`：默认 7；`end`：窗口锚点（缺省 now）；⚠️ `start` 参数被忽略（窗口恒 = end−days） | 逐日 Incidents/Events 序列 |

> ⚠️ 纠偏（对照原文/设计 M15.3/M15.4）：
> - **无"选团队"**——不存在 team 参数，团队维度只有 team-load 的按团队分组（且全部团队可见）。
> - **team-load 只有事件数**：响应为 `{TeamID, TeamName, Incidents}`——"oncall 次数 / 夜间打扰次数 / 人均事件数"📋 未实现（引擎注释提及设计，结构体无字段）。"夜间打扰 = quiet_hours 内通知值班人的次数"是设计口径；当前通知结果不落库（见 C.9），该指标暂无数据来源。
> - **postmortems 只有完成率**：响应为 `{Total, Published, CompletionRate}`——"Action Item 闭环率 / 超期数"📋 未实现（ActionItem 的 due_date 字段连 API 都未暴露，见 B.14）。

#### 指标口径（从代码抄的公式，可直接写断言）

| 指标 | 公式（`engine.go`） | 注意 |
|------|------|------|
| Total（alerts） | 窗口内 Event 计数（按 `received_at`） | 含噪音与 unrouted |
| Notified | `is_noise=false` 的 Event 数 | ⚠️ 字段名义"已通知"，实际口径=**非噪音**（与真实通知发送量无关） |
| NoiseRate（降噪率） | `1 − Notified/Total`（Total=0 时恒 0） | 分子=dedup 标噪 + 抑制标噪（quiet_hours 静默不参与——那发生在通知层且不落库） |
| Unrouted | `service` edge 为空的 Event 数 | ⚠️ 口径偏大：被标噪的 Event 也没有 service，会一并计入（严格 unrouted 见 B.13 SQL） |
| MTTA | `avg(acked_at − created_at)`，秒 | 仅统计有 acked_at 的；无数据 → 0（前端显示 "—"） |
| MTTR | `avg(resolved_at − created_at)`，秒 | 仅统计有 resolved_at 的；同上 |
| CompletionRate | `(published + archived) / Total` | archived 也算"完成" |
| Trend 分桶 | `floor((t − start)/24h)`，start = end − days | 绝对 24h 桶（非日历日、与时区无关），Date 标签取桶起点日期 |

#### 响应结构示例（键名 = 结构体 json tag，camelCase）

> ✅ **键名为 camelCase**：analytics 结构体已补 json tag（`internal/analytics/engine.go`），echo 走
> encoding/json 输出 `total`/`noiseRate` 等键，与 **OpenAPI spec 及前端 types.gen.ts 完全一致**，
> 前端仪表盘 KPI 正常取值。注：`mttaratio`/`mttratio` 沿用 spec 既有拼写（swag camelcase 策略对
> `MTTARatio`/`MTTRatio` 的产物），json tag 已对齐，勿改。**测试断言以运行时 camelCase 为准。**

```jsonc
// GET /analytics/dashboard?days=7
{
  "alert":    {"total": 1284, "notified": 402, "noiseRate": 0.686, "unrouted": 37},
  "incident": {"total": 56,
               "bySeverity": {"critical": 8, "warning": 31, "info": 17},
               "byStatus":   {"triggered": 2, "acked": 5, "resolved": 40, "closed": 9},
               "mttaratio": 312.5, "mttratio": 5420.8, "resolvedCount": 49},
  "load":     [{"teamID": 3, "teamName": "payment", "incidents": 34},
               {"teamID": 4, "teamName": "infra",   "incidents": 22}],
  "postmortem": {"total": 12, "published": 9, "completionRate": 0.75}
}

// GET /analytics/team-load?start=2026-06-26T00:00:00Z&end=2026-07-03T00:00:00Z
[{"teamID": 3, "teamName": "payment", "incidents": 34}]
// ⚠️ 组织内无团队时返回 null（nil slice），不是 []

// GET /analytics/trend?days=3
{"days": [{"date": "2026-07-01", "incidents": 4, "events": 120},
          {"date": "2026-07-02", "incidents": 2, "events": 80},
          {"date": "2026-07-03", "incidents": 0, "events": 12}]}
// 注意 trend 外面包了一层 {"days": [...]}（handler 固定包装）
```

#### 数据时效与边界（可断言）

- **查询时实时聚合 ✅**：每次请求直查 PostgreSQL，无缓存、无预聚合——数据零延迟；代价是 Event 量大时变慢（IncidentMetrics/Trend 为全行拉取内存聚合）。
- 📋 设计目标（capability 10 §B）：定时 Asynq 聚合任务（每小时/每日）+ WebSocket 实时看板推送、CSV 导出（M15.6）均未实现。导出替代 = 数据库直查。
- 空库/空窗口：各端点 200；Total=0、rate 恒 0、`BySeverity`/`ByStatus` 为 `{}`、team-load 为 `null`。
- 无 Incident 时 MTTA/MTTR：`MTTARatio`/`MTTRatio` 均为 **0**（不是 null 不是报错；前端把 0 显示为 "—"）。
- TeamLoad 循环内单团队查询失败会**静默 continue**——结果可能缺某团队，无错误提示。

#### trend 判读示例（写用例/评审用）

| 形态 | 解读 |
|------|------|
| Events 高、Incidents 低且平 | 聚合/降噪在生效（多信号归并少量事件），健康 |
| Events 与 Incidents 同步上升 | 真实故障变多——看 incidents 的 BySeverity 定位 |
| Events 陡增、Incidents 不变、NoiseRate 上升 | 噪音源刷屏——去 B.13.1 复核 + 回查 SuppressionRule / 接入源限流（B.4） |
| Events 归零 | 先别高兴：大概率接入断流（B.15），不是天下太平 |

#### 操作流程（修正版）

```
1. 登录 → 仪表盘首页看近 7 天概览（KPI 固定 7 天）
2. 深挖：直接调 API 带 start/end（Web 无各维度下钻页）
3. 重点看：NoiseRate（分诊效果）、MTTA/MTTR（响应速度）、Unrouted（配置健康度，衔接 B.13）
4. 📋 导出 / 夜间打扰 / Action Item 闭环率：未实现（见上纠偏），需要时数据库直查
```

### B.12 审计调查（能力域 13 M13.5）

> 触发权限点：`admin.audit.view`（✅ 已在 RouteGuard 登记，内置角色**仅 `org_admin`** 持有，见附录 A）。
> 无团队 scope 概念——审计是组织级视角，`team_admin` 看不了；团队内合规问题须上报 org_admin。

**角色**：`org_admin`（合规/追责场景）
**端点**（✅ `GET /audit-logs`，`internal/auth/handler_audit.go`；只读——审计只追加，无修改/删除 API）
**Web 入口**：设置 → 审计 tab（`settings/audit-tab.tsx`：倒序列表 + action 下拉筛选，固定 limit=100，只读）。

```
GET /audit-logs?actor_user_id=42&action=role.assign&resource_type=role_binding&resource_id=7&limit=50&offset=0
```

| 参数 | 说明 | 默认/边界（可断言） |
|------|------|--------------------|
| `actor_user_id` | 按操作者过滤（int） | 非数字**静默忽略**（不报 400） |
| `action` | 按操作类型全等过滤 | 取值见下"实际落审计的操作全集" |
| `resource_type` / `resource_id` | 按对象类型/ID 过滤 | 同上静默忽略非法值 |
| `limit` | 分页大小 | **默认 50，上限 200**；≤0、>200 或非数字 → 回落 50 |
| `offset` | 分页偏移 | 默认 0；负数 → 0 |
| ⚠️ 时间参数 | **不存在** | 原文的 `from`/`to` 与代码不符（纠偏）——无时间范围筛选，只能靠倒序 + 分页翻到目标时段（📋） |

**响应**：`{"items": [...], "total": N, "limit": 50, "offset": 0}`（`created_at` 倒序；count 失败时 `total=-1` 不阻塞列表）。

条目字段（`ent/schema/audit_log.go`；actor/resource 均存**名字快照**，用户改名/删除后审计仍可读、不悬空）：

```jsonc
{
  "id": 101,
  "actor_user_id": 42,          // 0 = 系统/匿名（如登录失败时的未知用户名）
  "actor_name": "alice",
  "action": "role.assign",
  "resource_type": "role_binding",
  "resource_id": 7,
  "resource_name": "",
  "result": "success",          // success | failed | denied（denied = 潜在越权/攻击探测）
  "detail": {"user_id": 42, "role_id": 7, "scope": "team"},   // 按操作类型填充
  "ip": "10.0.0.8",             // X-Forwarded-For 优先，回退 RemoteAddr
  "user_agent": "Mozilla/5.0 …",
  "created_at": "2026-07-03T02:11:00Z"
}
```

#### 实际落审计的操作全集（⚠️ 纠偏：比设计范围窄得多）

全仓 `AuditRecorder` 调用点核实，当前**只有 7 种 action** 会落库：

| action | 触发点 | result 场景 |
|--------|--------|------------|
| `role.create` / `role.delete` | POST/DELETE /roles | success |
| `role.assign` / `role.unassign` | POST/DELETE /role-bindings | success（detail 含 user_id/role_id/scope） |
| `apikey.create` / `apikey.delete` | POST/DELETE /api-keys | success |
| `auth.login` | POST /auth/login | success / **failed**（密码错）/ **denied**（限流 `rate_limited`、禁用 `user_disabled`） |

> ⚠️ schema 注释与能力域设计声称还覆盖"Integration token、用户禁用、配置变更"——**均未接入**：
> `PATCH /users/:id`（含禁用）、Integration 增删、通知/抑制/服务等配置变更 handler **不写审计**（📋 纠偏）。
> 用户被禁用只能间接从其后续 `auth.login` denied（reason=user_disabled）观察到。写用例按上表 7 种断言。

#### 两类留痕的真实分工（⚠️ via 字段纠偏）

| 类 | 实体 | 查询入口 | 说明 |
|----|------|----------|------|
| 管理审计 | AuditLog | `GET /audit-logs` ✅ | 上表 7 种；**无 via 字段** |
| 事件操作留痕 | TimelineItem（`source`: web/im/api/system/ai） | `GET /incidents/:id/timeline?source=im` ✅（按单个事件） | ack/resolve/升级/Runbook/备注全程 |
| 操作审计 IncidentAction（含 `via`: web/im/api/automation） | schema 已定义 | — | 📋 **全仓无任何写入/查询代码**（`IncidentAction.Create` 零调用点） |

即原文"via 字段的价值：统计多少操作发生在 IM（验证 IM-first）"当前**无法通过 API 实现**——
via 只存在于 schema；替代观测 = 按事件逐个看 timeline 的 `source` 字段，跨事件统计 📋。

#### 典型调查流程（修正版）

```
1. 起因：某权限被不当授予 / 可疑登录
2. Web：设置 → 审计 tab（action 下拉筛选）；或 API：GET /audit-logs?action=role.assign
3. 定位条目 → actor_name（谁）+ detail（授了什么）+ ip/user_agent（从哪来）
4. 连查：可疑登录 → ?action=auth.login 看 result=failed/denied 的密集尝试
   （暴破痕迹：连续 5 次失败锁 5 分钟；单 IP/用户名每分钟超 10 次尝试记 denied rate_limited）
5. 事件误操作（误 resolve 等）→ 走 GET /incidents/:id/timeline（本端点管不了，见上分工）
6. 📋 导出：无端点（保持原状），截图或数据库直查
```

### B.13 未路由事件分诊（能力域 4 M4.3） — 🟡 池在数据里，入口未实现

> 触发权限点：`event.view_unrouted`——⚠️ **悬空**：权限点已定义（`permission.go`，前端角色编辑器可勾选），
> 但**没有任何路由/页面引用它**（unrouted 查询端点本身不存在，见下）。

**背景**：Event.labels["service"] 匹配不到 active Service.slug → 分诊 action=`unrouted`，
Event 落库但 service 外键留空（`triage/engine.go`）。这些事件**不建单、不通知**——
配置错误的告警静默堆积，是顶级运维痛点。

#### 访问方式（⚠️ 重大缺口，如实标注）

核实全部后端路由与 `web/src/pages/`：**无 `GET /events` 类端点、无 unrouted 池页面**——
unrouted Event 当前**没有任何产品内明细入口**（📋 backlog，重大缺口）。可用的观察替代：

| 途径 | 能看到什么 |
|------|----------|
| `GET /analytics/alerts` 的 `Unrouted` 计数（仅登录态） | 只有总数，无明细；口径偏大（含被标噪的，见 B.11） |
| worker 日志 `triage worker: done … action=unrouted` | 逐条（含 event_id），需日志采集配合 |
| **DB 直查（当前唯一明细手段）** | 见下 SQL |

```sql
-- unrouted 池明细（service 外键为空且非噪音 = 真·未命中路由）
SELECT id, severity, summary, labels, received_at
FROM events
WHERE service_events IS NULL AND is_noise = false
ORDER BY received_at DESC LIMIT 50;

-- 按缺失的 service 标签值聚类，定位是哪个（哪些）源没配对上
SELECT labels->>'service' AS wanted_service, count(*) AS cnt, max(received_at) AS latest
FROM events
WHERE service_events IS NULL AND is_noise = false
GROUP BY 1 ORDER BY 2 DESC;
```

> 附注：命中服务但 `auto_create_incident=false` 且非 critical 的 Event，分诊结果也叫 `unrouted`
> （日志同名），但它已绑 service、不在上面 SQL 的池里——语义是"等人工提升"而非"路由失败"。

#### critical 兜底通知（⚠️ 纠偏：未实现）

本文多处（含 B.3/B.4 与原文本节）写"critical 落 unrouted 会兜底通知全员/admin"——**代码中不存在该逻辑**：
`triage/engine.go` 对 unrouted 只标记返回，不发领域事件、不通知任何人（notification/escalation 均无 unrouted 处理，
也没有"全员/admin"这样的兜底收件人概念）。**当前行为：critical 与普通 unrouted 一样静默落池**——
labels 配错的 critical 告警**无人知晓**，是当前最危险的静默失败路径之一。
📋 设计目标（capability 02 §3.2：unrouted critical 兜底通知）未排期；B.3/B.4/C.7 等章节的同款表述已按此纠偏。
**当前唯一防线**：接入验收时先发测试 payload（B.10）+ 例行盯 `Unrouted` 计数增量（建议 cron 化上面的 SQL）。

#### 复核与处置流程（按当前实现改写）

```
1. 发现：analytics Unrouted 计数增长 / 例行 SQL 巡查
2. 逐条看 labels：
   ├─ labels 无 "service" 键          → 源侧没带标签（改源的告警规则/接入配置，见 B.4 通用契约）
   ├─ labels.service=xxx 但无此 slug  → 新服务未登记 → 建 Service（slug=xxx，B.3）
   └─ slug 存在但 Service 已停用      → PATCH 恢复 status=active
3. ⚠️ 修复手段对齐 B.3 纠偏：路由只做 labels["service"] 与 active Service.slug 的等值匹配——
   改 Service.labels 字段**无效**；要么让源带正确的 labels.service，要么建/改成对应 slug 的 Service
4. 已落池的这批 Event 不会被回溯路由（无重路由端点 📋），修复只对后续新 Event 生效
5. 若是噪声 → 建 SuppressionRule（B.7.1）防再次落池
```

**修复后的回归验证（可断言）**：

```
1. 换 id 重发（⚠️ 同 id 在 5min dedup 窗内会被标噪丢弃，验证必须换 dedup_key）：
   curl -X POST /api/v1/webhook/$TOKEN -d '{"id":"verify-'$RANDOM'","severity":"warning",
     "summary":"unrouted 修复验证","labels":{"service":"payment"}}'
2. 断言：worker 日志 action=incident_created（或 aggregated，5min 聚合窗内并入旧单也算命中）；
   GET /incidents 出现新单 / unrouted SQL 计数不再增长
3. 负向回归：发一条 labels.service=不存在的值 → 仍应落池（确认没把路由改得过宽）
```

**保留时长/清理（核实）**：**无任何清理任务/retention 配置**（全仓无定时清理代码，Event 设计即"不可变只追加"）——
unrouted 与全部 Event 一样**永久保留**，池只增不减；清理只能人工 DB 操作（📋 保留策略）。

> 📋 backlog：unrouted 池查询/重路由端点（标 service_id 或并入既有 Incident）、critical 兜底通知、Event 保留策略。

#### B.13.1 噪音复核（is_noise 池）— 🟡 数据在，入口同样缺失

**噪音的三个来源**（复核前先分清，处置手段不同）：

| 来源 | 标记点 | DB 里怎么认 | 误杀处置 |
|------|--------|------------|----------|
| 去重 dedup | 5min 窗内同 dedup_key 重复（`action=dedup_skipped`） | 存在更早的同 dedup_key 非噪音 Event | 一般无需处置（首条已正常走流程） |
| 抑制规则命中 | SuppressionRule action=suppress（`action=suppressed`） | labels 匹配某启用规则的 match_labels | 停用/收窄规则（见下） |
| AI 判噪 | — | — | 📋 未实现（M3.3，AI 分诊未接 triage 流水线，衔接 C.4） |

> ⚠️ Event 上**不落命中的规则名**（`suppression.go` Apply 只 SetIsNoise）——复核时只能拿 Event.labels
> 手动比对 `GET /suppression-rules` 里启用中的规则。
> 📋 规则式"历史模式判噪"（如同一告警 24h 内自动恢复 10+ 次且无人 ack → 判噪，capability 02 §2.3 设计）
> **未实现**——当前判噪只有 dedup + 显式规则两条路，抖动告警（flapping）不会被自动识别。

**入口（与 unrouted 同款缺口）**：无 `GET /events`、无噪音池页面——`GET /analytics/alerts` 只有
NoiseRate 比率，明细只能 DB 直查：

```sql
-- 近 24h 噪音明细（重点复核 critical/warning 的）
SELECT id, severity, summary, labels, dedup_key, received_at
FROM events
WHERE is_noise = true AND received_at > now() - interval '24 hours'
ORDER BY received_at DESC;

-- 区分来源：同 dedup_key 存在更早非噪音记录的 = dedup 标噪；其余 = 抑制命中
SELECT e.id, e.severity, e.dedup_key,
       EXISTS(SELECT 1 FROM events p
              WHERE p.dedup_key = e.dedup_key AND p.id < e.id AND p.is_noise = false)
       AS is_dedup_noise
FROM events e
WHERE e.is_noise = true AND e.received_at > now() - interval '24 hours';
```

**复核流程**：

```
1. 例行（建议每周 / 每个维护窗结束后）跑上方 SQL
2. 判断误杀：
   ├─ 确属重复/维护窗内计划告警 → 无动作（降噪符合预期）
   └─ 误杀（真告警被压）：
       a. 定位肇事规则：labels 比对 GET /suppression-rules 的 match_labels/time_window/severity_filter
       b. PATCH /suppression-rules/:id {"enabled":false}（或收窄条件）——实时生效
          （每条 Event 处理时实时查库评估，见 B.7.1）
       c. 已被压的这条 Event：📋 手动提升为 Incident 未实现——无 POST /incidents 手动建单、
          全仓无 promote 类端点（capability 02"可申诉…可手动提升为 Incident"仅是设计）。
          只能等告警源再次触发（注意换 dedup_key 或等 5min 去重窗过期），或线下拉起处置
3. 对照降噪率：处理后看 B.11 NoiseRate 是否回落到预期区间；
   NoiseRate 突增通常 = 新规则过宽 / 某源刷屏（回查 B.7.1 / B.4）
```

**少误杀的三道守卫（回顾，对照 B.7.1）**：`preserve_critical` 默认 true（critical 不被 suppress/降级）、
`status=resolved` 事件不评估（恢复信号必达，保证能关单）、过期规则自动跳过。
📋 AI 噪声学习闭环（误杀反馈 → 优化判噪，衔接 C.4）未实现。

### B.14 用户禁用与交接（能力域 13 M13.1） — 🟡 部分实现

> 触发权限点：`user.disable`（⚠️ 实际端点 `PATCH /users/:id` 未登记权限点，任何登录用户可操作——见 B.1.1 负向用例）

**角色**：`team_admin` / `org_admin`（员工离职/转岗场景）

#### 禁用的真实效果（对照代码，可断言）

| 影响面 | 行为 | 状态 |
|--------|------|------|
| 新登录 | 403 `user disabled` + `auth.login` denied 审计（reason=user_disabled） | ✅ |
| 已持有的 access token | ⚠️ **继续有效**直到过期（默认 15 分钟；鉴权中间件不查 User.status） | ⚠️ |
| 已持有的 refresh token | ⚠️ **`POST /auth/refresh` 不查 status**——被禁用户可用手里的 refresh token（默认 **30 天**）持续换新 access token 正常调 API。**禁用 ≠ 吊销会话**；紧急场景（恶意离职）需同时轮换 `VIGIL_AUTH_JWT_SECRET`（代价：全员重登） | ⚠️ 高危纠偏 |
| 排班/升级通知 | ⚠️ **照常通知**——排班/升级引擎不查 User.status（B.5.2 已核实），禁用用户仍会被解算为在班人并收到 IM/邮件通知 | ⚠️ 纠偏 |
| RoleBinding | 不自动失效（鉴权只过滤过期时间，不看用户状态；因新登录被拦，风险主要经由上面的存量 token 路径） | ⚠️ |
| 历史数据 | 保留（审计/时间线/复盘归属不变，设计即"禁用保历史"） | ✅ |

#### 交接清单（修正版——全靠手动，系统零提示、零兜底）

```
1. 排班：把该用户移出 Rotation.participants
   ├─ ⚠️ 无 Rotation 管理入口（B.5 纠偏）——只能删除重建 Schedule
   ├─ ⚠️ "建 Override 覆盖其班次"不可行——Override 📋 未实现（B.5.1）
   └─ ⚠️ 纠偏：不移除**不会**有"空班检测告警 team_admin"——空班检测 📋 未实现（B.5.2），
      引擎静默跳过；且更糟：禁用用户根本不产生"空班"（引擎不查 status，仍指派 TA）——
      结果是升级通知发给一个永不响应的人，**静默无人接手，必须主动检查排班**
2. Action Item：该用户 owner 的未完成项 reassign
   ├─ ✅ 端点：PATCH /action-items/:id {"owner_id":"<新负责人>"}（可同时改 status/tracker_url；
   │   权限 postmortem.actionitem.manage，scope 经 action_item→postmortem→incident→team 三级回溯）
   └─ ⚠️ 纠偏："不交接会超期高亮"不成立——ActionItem 有 due_date 字段但 **API 未暴露**
      （add/update 请求体均无该字段），也无任何超期展示/统计（B.11 复盘度量只有完成率）；
      找遗留项只能 DB 直查：SELECT id FROM action_items WHERE owner_id='<uid>' AND status <> 'done'
3. 角色：撤销其全部 RoleBinding（GET /role-bindings 按 user_id 找全 → 逐条 DELETE /role-bindings/:id）
4. IM 绑定：⚠️ 无解绑端点（B.9 纠偏）——im_accounts 残留，只能 DB 直改；
   残留风险有限（IM 操作走同链鉴权，撤销 RoleBinding 后无权限），但建议清理防混淆
5. 会话：如需立即失效，轮换 JWT secret（见上表 refresh token 风险）
```

**验证（可断言）**：
- 用被禁账号登录 → 403 `user disabled`；审计 tab 出现 `auth.login` denied 条目（B.12）。
- 重建排班后 `GET /schedules/:id/oncall` 各层 users 不含该用户。
- Action Item 遗留 SQL 返回空。

> 📋 设计目标（capability 09 §2）：禁用时自动提示待交接项清单；禁用即吊销存量会话。
> **建议顺序**：先做交接（步骤 1–4）再禁用，且当天完成——存量 token 的风险窗口越短越好。

### B.15 接入失败排查 — 告警"没到"的三层定位

> 场景：源侧说"我发了"，Vigil 里却没有 Incident。与 B.13 的分工：
> **B.13 = 到了但路由没命中**（Event 存在、service 为空）；**本节 = 更上游**——
> payload 没进来，或进来了没变成 Event（RawEvent `parse_failed` / `requeued`）。

#### 症状 → 层级速查

| 症状 | 大概率层级 | 去哪查 |
|------|-----------|--------|
| curl 收到 4xx/5xx（401/429/503/500） | 接收层 | 下面步骤 1 |
| 202 accepted 但 `vigil_alerts_received_total` 不涨 | 归一化层 | 步骤 2–3 |
| metrics 涨了但没 Incident | 分诊层（unrouted/噪音/聚合/不建单） | B.13 / B.13.1 / B.10 幂等表 |

#### 已实现的失败落点（✅ `ingestion/handler.go`，状态机可断言）

RawEvent.status 共 4 态：`received → normalized`（成功终态）/ `parse_failed` / `requeued`：

| 失败点 | RawEvent 状态 | error 字段 | 对源的响应 |
|--------|--------------|-----------|-----------|
| token 无效 / Integration 禁用 | **不落库** | — | 401 `invalid token`（唯一"不落 payload"的路径——鉴权语义，非丢失） |
| RawEvent 落库失败（DB 不可用） | 不落库 | — | 500 `persist failed`（让源重试，最严重场景） |
| 超限 / 背压 | 停在 `received` | 无 | 429 / 503（payload 已保住但**未入队**，见 B.4） |
| 入归一化队列失败（Redis 挂） | `requeued` | `enqueue failed: …` | **仍 202**（Vigil 侧负责，源不必重试） |
| 无适配器（type=zabbix 等 📋 类型） | `parse_failed` | `no adapter for source type "zabbix"` | 202（异步失败，源无感知） |
| payload 解析失败（坏 JSON / 结构不符） | `parse_failed` | `normalize: …` | 202（同上） |

> ⚠️ **parse_failed / requeued / 卡住的 received 都是"死"状态**：标记后无人消费——
> 代码注释多处写"恢复后从 RawEvent 回灌"，但**自动回灌/巡检任务未实现（❌）**；
> 也**无 raw_event 查询/重放端点**（全路由核实）。处置只能 DB 直查 + 源侧重发 / 手工重放。

**Asynqmon 死信重放（核实）**：Helm values 有 `asynqmon.enabled`（默认 false）开关，
但 **chart templates 无对应 Deployment**（仅 deployment/pdb/service 三个模板）——开关是空壳；
docker-compose 亦无编排。要用 Asynqmon 需**自行部署**并指向同一 Redis。且注意：上表的 parse_failed
场景任务本身是"正常结束"（失败记在 RawEvent 上），**Asynqmon 里看不到死信**——它只对"任务执行报错"
（如 DB 抖动导致 worker 返回 error）的 retry/archived 队列有用。

#### 排查步骤（可执行）

```
1. 接收层：源侧拿到什么响应码？
   ├─ 401 → token 配错 / Integration 被禁用（B.4：无轮换端点，删除重建后源侧忘了换 token 是常见根因）
   ├─ 429/503 → 限流/背压（B.4）：payload 已落库但没入队 → 按 SQL ③ 查 received 积压
   └─ 202 → 进下一层
2. 归一化层：按 integration 查 RawEvent 状态分布（integration id 从 GET /integrations 拿）→ SQL ①
3. 看 error 定位根因 → 修复：
   ├─ no adapter → Integration.type 选了未实现类型（📋 zabbix/cloud/email/api）；
   │   ⚠️ PATCH 改不了 type（B.4）——需删除重建为 prometheus/grafana/webhook
   ├─ normalize 错误 → 源侧 payload 不符契约（对照 B.4 通用 JSON 契约 / 适配器差异表修源）
   └─ enqueue failed → 查 Redis；恢复后 requeued 的仍需手动处理（无自动回灌）
4. 回灌：优先让源侧重发（最简单）；或从 raw_events.payload 取原文手工 POST 回 webhook
   （⚠️ 注意 5min dedup 窗与 source_event_id 幂等键，重放太快会被去重，见 B.10）
5. 预防：接入完成即做 B.10 验收；对 vigil_alerts_received_total 按 source 配断流告警（D.5）
```

**排查 SQL 样例**：

```sql
-- ① 某接入源的 RawEvent 状态分布（健康 = 几乎全 normalized）
SELECT status, count(*) FROM raw_events
WHERE integration_raw_events = <integration_id>
GROUP BY status;

-- ② 最近失败明细（error 是排查核心；payload 原文可用于对照契约与重放）
SELECT id, status, error, received_at,
       convert_from(payload, 'UTF8') AS payload_text
FROM raw_events
WHERE status IN ('parse_failed', 'requeued')
ORDER BY received_at DESC LIMIT 20;

-- ③ 卡死的 received（正常几秒内变 normalized；限流/背压响应时未入队的会永久停在 received）
SELECT count(*) FROM raw_events
WHERE status = 'received' AND received_at < now() - interval '10 minutes';
```

**与 B.13 的衔接**：本节修完（RawEvent 全 normalized、metrics 恢复增长）后若仍无 Incident，
按 B.10 观察点 4–5 继续往下：unrouted（B.13）→ 噪音（B.13.1）→ `auto_create_incident=false` /
聚合并入旧单（B.10 幂等表）。

> 📋 backlog：raw_event 查询/重放端点、自动回灌巡检任务、Asynqmon 编排落地（helm 开关当前无效）。

---

## 旅程 C：值班工程师告警处置全流程

**主角色**：`responder` / `oncall`（团队 scope）
**目标**：半夜被叫醒 → 在 IM 内**不切系统**完成 ack / 诊断 / 处置 / 解决 / 复盘。
**特点**：IM 首选（Web 兜底）、高频、强时间压力、每个动作都写时间线 + 审计。
**设计哲学**：半夜能用 · 一屏决策 · 降噪优先 · 状态可见。

### C.1 端到端时序（13 步，对应 architecture §4.1）

```
① Prometheus 触发告警
   └─ POST /api/v1/webhook/{token}（token 走路径段，见 B.4）
② Ingress 校验 token → 入队 → 返回 202（秒级，收发解耦）
   └─ raw payload 落 raw_event 表（保底；⚠️ 重放为手工操作，见 B.15）
③ Worker: normalize → Event 入 PostgreSQL（dedup_key 在此生成，见 C.1.1 ①）
④ Worker: 分诊三级（可测行为细则见 C.1.1）
   ├─ dedup（Redis SETNX dedup_key，5min 窗；重复 → Event 标噪落库，不拦截入库）
   ├─ suppression（规则 → is_noise；AI 判噪 📋 未接，见 B.13.1）
   └─ correlation aggregation（同 service+severity、活跃、Incident 建单 5min 内 → 并入）
   └─ 分支：Event.status=resolved → 跳过 dedup/suppression，走自动恢复（C.2.1）
⑤ Worker: route 匹配 Service（⚠️ 仅 labels["service"] 等值匹配 active slug，无 glob，见 B.3 纠偏）
   └─ 命中 → 建单后绑定 Service 的 escalation_policy（schedule/runbook 关联 📋 API 未暴露，B.3）
   └─ 未命中 → unrouted 池（⚠️ 纠偏：critical 兜底通知全员/admin 📋 未实现——
      与普通 unrouted 一样静默落池，无任何通知，见 B.13）
⑥ Incident → status=triggered → 入队升级延迟任务（asynq.ProcessIn）
   └─ ⚠️ 创建本身不写时间线（`incident_created` 类型已预留、全仓无写入点）——新单时间线为空
⑦ 升级引擎到点触发 → 排班引擎实时算 oncall → 通知引擎分发
⑧ IM 卡片送达值班群（带 ack/升级/解决/详情 按钮，按权限渲染，规格见 C.3.1）
⑨ 工程师点 [ack] → IM 层：unionId→User→鉴权→核心服务 ack
⑩ ack 取消该 Incident 所有后续升级 + 通知任务 → status=acked → 时间线
⑪ 处置：展示 Runbook / 诊断执行 / 处置(人确认或外接)
⑫ 标记 resolved →（🟡 当前需手动调 `POST /incidents/:id/postmortem/draft` 起草；📋 设计目标：critical 自动触发）→ 人评审 → 发布
⑬ 闭环：复盘入知识库 → 反哺相似 Incident 检索（下次更快）
```

#### C.1.1 分诊三级的可测输入输出（✅ `triage/engine.go`，写用例直接抄）

**处理顺序**：dedup → suppression → route →（resolved？自动恢复 C.2.1 ：聚合/建单）。
resolved Event **跳过去重与抑制**（收尾信号必达，保证能关单）。

**① dedup_key 生成规则**（归一化阶段，`ingestion/adapters_builtin.go`；构成 = `<source>:<源侧指纹>`）：

| 源 | dedup_key | 指纹（source_event_id）取值 |
|----|-----------|---------------------------|
| prometheus | `prometheus:<指纹>` | `fingerprint`，缺省拼 `alertname:instance` |
| grafana | `grafana:<指纹>` | 同左 |
| webhook 通用 | `generic:<指纹>` | `source_event_id`→`id`→`event_id` 首个非空；都缺 → `generic-<payload字节长度>` ⚠️ 同长度不同告警会互相误判重复（B.4 已提醒：务必带 id） |

**② 去重窗口（5 分钟，`dedupWindow` 硬编码不可配）**：Redis `SETNX vigil:dedup:<dedup_key> EX 5min`。

| 场景 | 预期（可断言） |
|------|------|
| 窗口内同 key 第二条（firing） | RawEvent、**Event 均正常落库**，第二条标 `is_noise=true`、分诊 action=`dedup_skipped`、不挂 Incident、不通知——**Incident 仍只有 1 条**（去重是"标噪不拦截"，不是"Event 只剩 1 条"，见 B.10） |
| 窗口语义 | 首条 SETNX 起 **5 分钟固定 TTL**，重复命中**不续期**（与通知聚合的滑动窗口不同）——第 6 分钟同 key 再来会重新走全流程（大概率被聚合并单，见 ③） |
| Redis 未配置（client 为 nil） | **不去重直接放行**（优雅降级，同 A.5 基线）——重复告警各自成流程 |
| Redis 已配置但故障 | checkDedup 返回 error → 分诊任务失败 → **Asynq 按重试策略重试**（Event 不丢，处理延迟） |

**③ 聚合维度与窗口（`aggregate`）**：命中条件 = 同 Service + 同 severity + 状态 ∈ {triggered, escalated, acked} + **Incident.created_at 在近 5 分钟内**（`aggregateWindow` 硬编码），取最新一条并入（action=`aggregated`，Event 挂 incident_id）。

| 细则 | 说明 |
|------|------|
| 窗口锚点 = **Incident 创建时刻**，不随新 Event 滑动 | 活跃 Incident 建单满 5 分钟后，同 service+severity 的新 Event 会**另起新单**——即使旧单还在 triggered。长风暴会产生多个单（每 5 分钟一个） |
| severity 不同不并单 | 同服务 warning→critical 恶化 = 两个独立 Incident（各走各的升级链） |
| resolved/closed 的旧单不并入 | 已解决后同类告警再来 = 新单（不会复活旧单，复活走 reopen，C.2） |
| 维度/窗口均硬编码 | 按 label 自定义聚合维度、可配窗口 📋 未实现 |

**④ 被 dedup / suppressed 的 Event 去向与可见性**：`is_noise=true` 落库永久保留，
**无产品内明细入口**（无 `GET /events`）——复核走 B.13.1 的 SQL；被抑制的不落命中规则名、
被 dedup 标噪的不挂 Incident（首条挂）。噪音量反映在 `GET /analytics/alerts` 的 NoiseRate。

**⑤ 观测点**：worker 日志 `triage worker: done event_id=… action=<结果>`（action 实际枚举：
`incident_created` / `aggregated` / `dedup_skipped` / `suppressed` / `unrouted` / `resolved`；
⚠️ 常量 `severity_reduced` 已定义但**从不作为最终 action 输出**——降级事件继续走路由/聚合，
最终 action 是 routed 系列之一，降级只体现在 Result 的 SeverityReduced 标志与 Event.severity 新值）；
指标 `vigil_incidents_created_total{severity}`。

### C.2 Incident 状态机（运行时核心对象）

```
                ack in time
   triggered ─────────────────▶ acked ──mark resolved──▶ resolved ──PM done──▶ closed
       │                          ▲                          ▲            （📋 当前不可达）
       │ timeout, no ack          │ ack                      │
       └──────▶ escalated ────────┘──────────────────────────┘
                （resolve 可从 triggered / escalated / acked 任意活跃态直达；
                  告警源自动恢复同样任意活跃态 → resolved，见 C.2.1）

   补边 reopen（✅ incident.reopen）：resolved / closed ──▶ triggered（清 resolved_at，细则见下）
```

| 状态 | 进入 | 退出 | 含义 |
|------|------|------|------|
| `triggered` | Event 提升为 Incident；**reopen 回退**（✅） | 被 ack / 超时升级 / 直接 resolve | 新建（或重开），等待响应 |
| `escalated` | 升级计时器超时 / 手动 escalate | 被 ack / 直接 resolve | 已升级，仍未响应 |
| `acked` | 任意层级 ack | 标记 resolved / 手动 escalate | 有人接手（assignee=操作人） |
| `resolved` | 用户标记 / 告警源自动恢复（C.2.1） | reopen 回 triggered；复盘完成 → closed（📋） | 已解决，等复盘 |
| `closed` | 复盘完成/跳过（📋 **当前不可达**，见下） | reopen 回 triggered | 终态 |

> **closed 不可达（⚠️ 核实纠偏）**：全仓**无任何代码把状态置为 closed**——无 close 端点，
> 复盘 publish/archived（C.6）也不回写 `incident.status`。图上"PM done→closed"是设计目标；
> 当前 resolved 就是事实终态（列表页"已关闭"Tab 恒为空）。复盘闸门（resolved→closed 须复盘完成）📋，细则见 C.6.1。

**铁律**：每次状态变更**必须**产 TimelineItem——人工操作路径 ✅（`incident.Service` 统一记录）；
⚠️ **已知例外**：告警源自动恢复直接 UPDATE，不写时间线（违反铁律，见 C.2.1）。

#### reopen（✅ `POST /incidents/:id/reopen`，`incident/service.go`）

- **前置**：status ∈ {resolved, closed}；权限点 `incident.reopen`（RouteGuard 已登记；内置角色中
  org_admin / team_admin / responder_lead / **responder** 持有，**oncall / subscriber 无**）。
- **效果**：status→`triggered`、**清空 resolved_at**；`acked_at` / `assignee` / `current_level` / `escalated_count` **保留旧值不清**。
- **时间线**：`reopened`「用户 N 重新打开了事件」（✅）。
- **副作用核实（⚠️ 重要）**：发布 `IncidentReopened` 事件 → WS 推送 / IM 卡片刷新 / 出站 webhook ✅；
  但 **escalation 未订阅该事件**（wire.go 只订 Created/Acked/Escalated）——**升级计时器不重启、通知不重发**。
  reopen 后无人处理会**永远静默停在 triggered**；要重新拉起通知需手动 `POST /incidents/:id/escalate`（📋 reopen 自动重启升级链）。
- **IM 侧**：无 reopen 卡片按钮/斜杠命令（仅 Web / API）。

#### 非法转换矩阵（HTTP 映射已核实，可直接做断言）

动作实现于 `incident/service.go`（状态机守卫 `ErrInvalidTransition`）；四个操作端点对**一切业务错误**统一返回
**400** + `{"error":"<详情>","code":"failed_precondition"}`：

| 当前状态 ＼ 动作 | ack | resolve | escalate | reopen |
|------|------|---------|----------|--------|
| triggered | ✅ →acked | ✅ →resolved（**未 ack 直接解决合法**） | ✅ →escalated | ❌ 400 |
| escalated | ✅ →acked | ✅ →resolved | ✅（再升一层） | ❌ 400 |
| acked | ❌ 400 `ack from acked` | ✅ →resolved | ✅ →escalated（ack 后仍可手动拉更高层） | ❌ 400 |
| resolved | ❌ 400 `ack from resolved` | ❌ 400 `resolve from resolved` | ❌ 400 `escalate from resolved` | ✅ →triggered |
| closed | ❌ 400 | ❌ 400 | ❌ 400 | ✅ →triggered |

错误体示例：`{"error":"invalid incident status transition: ack from resolved","code":"failed_precondition"}`。

**其他可断言边界**：

| 场景 | 预期 |
|------|------|
| 重复操作幂等性（连点两次 ack） | **非幂等**：第一次 200，第二次 400 failed_precondition（前端靠按钮 `isPending` 禁用防连点，并发双客户端按此断言）；resolve/reopen 同理 |
| escalate 已在最高层（current_level ≥ levels 数） | **200**，状态/层级不变，但**仍写一条**「手动升级到 level N」时间线（幂等友好设计）——通知不触发 |
| 操作不存在的 incident id（有 org 权限） | ⚠️ **400** failed_precondition `incident not found`（handler 未分流 404，按此现状断言；`GET /incidents/:id` 才是 404） |
| id 非数字 | 400 `{"error":"invalid id","code":"invalid_argument"}` |
| 跨团队操作（team 级用户操作他团队 incident） | **403** `{"error":"forbidden","code":"permission_denied"}`（资源级 scope 反查先于状态机） |
| 手动 escalate 不取消当前层 pending 任务 | 见 B.6：旧任务到点额外一轮通知（escalated 属活跃态） |

#### C.2.1 告警源自动恢复（🟡 简化实现，`triage/engine.go` `handleResolved`）

**场景**：告警源发来 `status:"resolved"` 的 Event（如 Alertmanager 告警恢复）——期望对应 Incident 自动关闭。

**当前行为（✅ 简化版）**：

```
resolved Event
  → 跳过 dedup 与抑制评估（恢复信号必达，C.1.1）
  → 仍需路由命中（labels.service 等值匹配 active slug）
      └─ 未命中 → 分诊 action=unrouted，静默结束（恢复信号被丢弃）
  → 找【同 service】最新的活跃 Incident（status ∈ triggered/escalated/acked，按 created_at 倒序取一条）
      └─ 找不到 → action=unrouted，静默结束
  → 直接 UPDATE status=resolved + resolved_at（绕过 incident.Service）→ action=resolved
```

**与设计的差距（capability 02 §2.7）**：设计要求按 **DedupKey 配对**（resolved 精确关联同 dedup_key 的 firing）；
实现简化为 **service 维度取最新一条**——同 service 多个活跃单并存时可能**解错单**
（恢复的是告警 A，却把最新建的 B 单置为 resolved）⚠️。
「仅提示不自动 resolve」可配模式（更保守，防源侧误报恢复掩盖真故障）📋 未实现（代码注释"生产可配为仅提示"）。

**与手动 resolve 的差异对照（逐项核实代码）**：

| 行为 | 手动 resolve（Web/IM/API → `incident.Service`） | 自动恢复（`handleResolved`） |
|------|------|------|
| 状态推进 | ✅ resolved + resolved_at | ✅ 同 |
| 时间线 | ✅ 记 `resolved`「用户 N 解决了事件」（source=web/im/api，actor=操作人） | ❌ **不写任何 TimelineItem**——违反 C.2 铁律：复盘/时间线上看不到恢复时刻与缘由，只能靠 `resolved_at` 字段与 worker 日志 `action=resolved` |
| 领域事件 | ✅ 发 `IncidentResolved` → WS 推送、IM 卡片刷新、出站 webhook | ❌ **不发事件**——Web 详情页不实时刷新、IM 卡片停留旧状态、webhook 不推送（C.3.6 已知缺口） |
| 升级计时器 | 不主动取消（escalation 不订阅 IncidentResolved），pending 任务到点由状态守卫 no-op（日志 `skip, incident not active`） | 同左 |
| actor 留痕 | 操作人 user id | 无 actor、零留痕（复盘触发方 📋 也无从区分"自愈"与"人工解决"） |
| 复盘自动触发 | 均无（🟡 C.6） | 同左 |

**边界（供测试构造）**：

| 场景 | 预期 |
|------|------|
| resolved 先于 firing 到达 | 无活跃单 → unrouted 静默丢弃（不缓存不配对）；随后的 firing 正常建新单 |
| Incident 已被人 ack | **仍会被自动置 resolved**（acked 在活跃态查询范围内）——处置人手里的单可能"自己消失"，仅 Web 手动刷新可见 |
| Incident 已被手动 resolved | 查询不命中 → unrouted，无副作用（不会重复 resolve、不报错） |
| 同 service 多个活跃单 | 只解**最新**一个（First），旧单保持活跃 ⚠️ |
| resolved Event 本身的去向 | 落库、绑定 service，但**不挂到被解决的 Incident**（详情页 events 关联里看不到恢复事件） |
| 恢复后同告警再 firing | 走正常建单（旧单已 resolved 不并入，C.1.1 ③）——不会自动 reopen |

### C.3 IM 操作详情（核心差异化）

#### C.3.1 交互卡片（M8.1）— ✅ 已实现，内容规格如下

**卡片内容规格**（`im/card.go` `BuildCard`，平台无关结构 → 各平台渲染）：

| 区块 | 内容 | 规则（可断言） |
|------|------|------|
| 标题 | `[CRITICAL] INC-0042 <title>` | severity 大写 + 编号 + 标题 |
| severity 视觉 | 飞书：header 配色 critical=red / warning=orange / info=blue / 其他=turquoise；钉钉：无 header 配色，标题前加 emoji 🔴/🟠/🔵/⚪ | |
| 正文行 | **状态**（恒有，中文：待响应/已升级/已确认/已解决/已关闭） | |
| | 摘要 | summary 非空时 |
| | 当前层级 `Level N` | current_level > 0 时 |
| | 负责人 | 通知场景取首个升级 target 的名字；操作刷新场景取操作人 |
| StatusBadge | 操作后刷新时追加「已确认 操作（by 张三）」 | 仅 IM 内操作触发的刷新带此行 |
| 按钮 | 候选集固定 4 个：✓确认(primary) / ⬆升级 / ✓解决 / 📋详情 | 按**权限**裁剪（见下） |

> ⚠️ 与 ui-ux §5.1 设计卡片的差距：卡片**不含**服务名 / 环境 / 触发时间 / 值班链等字段（📋）；
> **AI 附加信息不在卡片**——`root_cause_hint` / `similar_incident` 只在 Web 详情页的 AI 诊断卡展示（C.4），
> IM 卡片不携带任何 AI 洞察（缺口确认；附录 C 剧本 1 第 4 步已按此表述）。

**按钮显隐矩阵（⚠️ 只按权限裁剪，不按状态）**：

| 接收者（内置角色） | ✓确认 | ⬆升级 | ✓解决 | 📋详情 |
|------|:---:|:---:|:---:|:---:|
| org_admin / team_admin / responder / responder_lead / oncall | ✅ | ✅ | ✅ | ✅ |
| subscriber | — | — | — | ✅（只读干系人只见详情） |
| 通知场景解析不到接收者 user_id | ✅ | ✅ | ✅ | ✅（**宽松渲染全按钮**，回调硬鉴权兜底——QA C5 决策） |

- **不按状态裁剪**：已 acked/resolved 的卡片（若未被刷新）仍显示 [确认] 按钮——点了走状态机报错（C.3.2 负路径）。
- **操作后刷新的卡片移除全部按钮**（`refreshCard` 不再渲染按钮，"按钮区折叠"以此实现）。
- **无拉人按钮**：`add_responder` 不在候选按钮集（`DefaultButtons`），拉人只能 @mention（C.3.4）。

**detail 按钮的 URL 形态（⚠️ 与 `{{.ActionURL}}` 恒空同源问题）**：系统无外部 base URL 配置，无法生成完整 Web 链接——

- 飞书：detail 是普通回调按钮，点击后 Vigil 仅在**回调 HTTP 响应**里返回 `{"detail_url":"/incidents/<id>"}`（相对路径）——飞书端用户**无跳转、无提示** ⚠️（📋 需配置站点 URL 后改为链接按钮）。
- 钉钉：按钮直接是链接 `vigil:///incidents/<id>`（自定义 scheme，普通设备无法打开）⚠️。

**投递目标（私聊 vs 群聊）**：通知主路径固定发到**全局值班群** `VIGIL_IM_ONCALL_CHANNEL`（wire.go；
占位值如 `# 值班群 chat_id...` 会被识别并置空降级为不发送，FIX-H）。channel 带类型前缀：
飞书 `chat_id:oc_xxx`（群）/ `open_id:ou_xxx`（私聊），钉钉 `openConversationId:xxx`（群）/ `userId:xxx`（单聊）——
配置成私聊前缀即"私聊投递"，但**全局只有一个 channel**，"按值班人逐个私聊"📋 未实现。
所有 `Available()` 的平台**冗余各发一份**（飞书+钉钉都配了就两边都发）；`/vigil status` 的回卡发到命令来源 channel。

**动作 → 权限映射**（`PermissionMap`，渲染与回调鉴权共用同一张表）：

| 按钮/动作 | 权限点 | 动作 |
|------|--------|------|
| 确认/ack | `incident.ack` | 取消后续升级，状态→acked |
| 升级/escalate | `incident.escalate` | 立即跳到下一升级层 |
| 解决/resolve | `incident.resolve` | 状态→resolved |
| 拉人/add_responder（仅 @mention） | `incident.add_responder` | 把 @人 加入 responders（见 C.3.4，🟡 不自动授权） |
| 详情/detail | `incident.view` | 返回 Web 详情路径（见上 ⚠️） |

#### C.3.2 IM 鉴权链路（与 Web 完全相同，关键设计）

```
IM 按钮点击
   └─ webhook 回调（POST /api/v1/im/:platform/callback）
       └─ VerifyCallback 签名/解密（平台各异，见下）
           └─ ResolveUser：platform + unionid → User（未绑定拒绝）
               └─ action → permission_code（如 incident.escalate）
                   └─ 查 RoleBindings（incident.team_id scope）
                       └─ 并集权限点 → 判断 code ∈ 集合
                           ├─ 允许 → 执行 → 更新卡片 → 时间线（source=im）
                           └─ 拒绝 → 403 "无权限"（⚠️ 纠偏：当前**不落审计**，见下）
```

> ⚠️ IM **不是**权限后门。IM 操作与 Web 走**同一条**鉴权链（同一 Authorizer + 同一 incident.Service）。
> 未绑 IM 账号的用户被拒并提示去 Web 绑定。

**IM 侧负路径清单（对照 `im/handler.go`，可断言）**：

| 场景 | 行为 |
|------|------|
| 回调签名校验失败 | HTTP **401** `verify failed`，不进任何业务逻辑。飞书：EncryptKey 模式 AES-256-CBC 解密内嵌 SHA256 签名校验（不匹配拒绝防伪造），ParseCallback 再比对 VerificationToken（不匹配 → 400 `parse failed`）；钉钉：aes_key 模式 HMAC-SHA256 校验 `sign` 头 + AES-256-CBC 解密。⚠️ 两个坑：钉钉回调**缺 sign 头时跳过签名校验**（只解密）；两平台未配加密密钥时为**明文模式不校验**——生产务必配置 TOKEN/ENCRYPT_KEY（飞书）与 TOKEN/AES_KEY（钉钉） |
| 未绑定 IM 账号点按钮/发命令 | **403** `im account not bound, please bind in web`（B.9 衔接）。⚠️ 该提示只在**回调 HTTP 响应**里——IM 群内**无机器人回话提示**（replyErr 未实现真正回消息，📋），用户体感是"点了没反应" |
| 已绑定但无权限（如 subscriber 点 ack） | **403** `forbidden: no permission`。⚠️ **纠偏：不落 denied 审计**——AuditLog 实际只有 7 种 action（B.12），IM 越权尝试无审计留痕；链路图的"拒绝→审计"是设计目标 📋 |
| 权限渲染差异 | subscriber 收到的卡片本就只有 [详情]（C.3.1 矩阵）；但通知场景宽松渲染时可能看到全按钮——点击才被回调硬鉴权拒绝（设计如此：卡片侧宽松、回调侧权威） |
| 对已 resolved 的旧卡片再点 [确认] | ⚠️ HTTP **500**（handler 把状态机错误当内部错误 `errs.Internal`，未映射 400）——用户无感知、卡片不变；与 Web 端同操作返回 400 failed_precondition **不一致**（实现缺陷，backlog 候选） |
| 回调缺 incident_id / 非法 | 400 `invalid incident_id` |
| 未注册平台 / wecom 占位平台回调 | 404 `unknown platform`；wecom 已注册但 NoopBot 验签直接失败 → 401 |

**Web 侧负路径清单（同一鉴权链的另一入口）**：

| 场景 | 行为 |
|------|------|
| 无权限用户打开详情页 | ⚠️ **纠偏**：前端按钮**不按权限隐藏/置灰**（只按状态启停，C.3.8）——subscriber 也看到 [确认] 等按钮，点击后收到 403 toast「forbidden」。"无权按钮不显示"当前只在 IM 卡片实现 |
| 绕过 UI 直调 API（无权限） | **403** `{"error":"forbidden","code":"permission_denied"}`（RouteGuard 路由级 + checkAccess 资源级双层）。⚠️ 同样**无 denied 审计** |
| 跨团队访问他团队 Incident | 详情/操作均 403（scope 反查 incident.team，软隔离）；列表被 VisibleTeamIDs 过滤直接看不到（total 也不含） |
| subscriber 直调 `POST /incidents/:id/ack` | 403（RouteGuard 已登记 incident.ack） |
| team_admin 调 `GET /audit-logs` | 403（`admin.audit.view` 仅 org_admin 持有，B.12） |
| responder 想 reassign | ❌ 无端点可调（`incident.reassign` 权限点悬空，见 C.3.8 改派小节） |

**典型负向用例表（写测试直接抄）**：

| # | 用例 | 预期 |
|---|------|------|
| 1 | subscriber 在 IM 点 [详情] | 200（有 incident.view）；回调响应含 detail_url |
| 2 | subscriber 在 Web 点 [确认] | 403 toast；状态不变；时间线无新条目；**审计无记录（现状）** |
| 3 | responder 对他团队的单发 `/vigil ack <id>` | 403 forbidden: no permission（scope=对方团队，无 binding） |
| 4 | oncall 调 `POST /incidents/:id/reopen` | 403（oncall 内置角色无 incident.reopen） |
| 5 | team_admin 看审计 tab | 403（组织级视角，B.12） |

> 与附录 A 的对应：矩阵中的「—」格 = 无该权限点 → 上述 403 路径。reopen / 时间线等行已列入附录 A。

#### C.3.3 平台能力矩阵与降级（M8.4 / §10）— ⚠️ 纠偏重写

> 原文（及 capability 05 §10 的"实现现状"注）称"钉钉卡片更新降级为发新消息标注最新状态"——
> **与代码不符**，实际是 no-op（见矩阵）。按 2026-07-03 代码逐格核实：

| 能力 | 飞书（真实适配器） | 钉钉（真实适配器） | 企微（NoopBot 占位） |
|------|------|------|------|
| 凭证/就绪 | `VIGIL_IM_FEISHU_APP_ID/_SECRET` 配了即 Available ✅ | `VIGIL_IM_DINGTALK_APP_KEY/_SECRET` ✅ | **无适配器**，`Available()` 恒 false 📋 |
| 交互卡片下发 | ✅ 交互卡片（header 配色 + 两列字段 + 按钮） | ✅ ActionCard（emoji 标题 + markdown 正文 + 链接式按钮） | ❌ 不发任何消息（所有操作返回 ErrUnsupported） |
| 卡片按钮回调 | ✅ `card.action.trigger`，value 直带 action+incident_id | ✅ 按钮 actionURL=`vigil://action?act=&inc=`，点击经消息回调解析 | ❌ |
| **卡片状态刷新** | ✅ **原地更新**（PatchInteractiveCard），群内所有人看到一致状态 | ⚠️ **纠偏**：`UpdateCard` 是 **no-op**——代码注释声称"降级为发新消息标注最新状态"，实际**直接 `return nil`，什么都不发**。钉钉用户看到的卡片永远停留在下发时的状态（按钮也还在，点旧按钮走 C.3.2 负路径）📋 降级方案待实现 | ❌ |
| @人拉人（mention → add_responder） | ✅ 解析 `mentions` 列表（C.3.4） | ⚠️ ParseCallback **不解析被 @ 人**（MentionAt 恒空、不产 mention 事件）——钉钉侧 @人拉人**实际不可用** 📋 | ❌ |
| 斜杠命令 `/vigil …` | ✅ 文本消息解析 | ✅ 同左 | ❌ |
| 建群（作战室） | 客户端接口在（CreateChat），但作战室 🚧 不做、无调用方 | 同左 | ❌ |

**可感知行为总结（写用例视角）**：
- 只配飞书：全功能（卡片/刷新/命令/@人）。
- 只配钉钉：能收卡片、能点按钮操作、能发命令；**状态变更后卡片不刷新**（Web 看真状态）、@人拉人无效。
- 只配企微：IM 完全静默——不发卡片不响应回调；通知走 email/webhook 兜底。
- **凭证缺失的整体降级**（呼应 A.5）：`Available()==false` 的平台被通知/刷新逻辑跳过，不报错不阻断；
  `GET /im/platforms` 返回各平台 `{platform, available, impl: real|noop}` 可断言就绪状态。
- 两平台都配：通知卡片**双平台各发一份**（冗余送达，C.3.1）。

#### C.3.4 跨团队拉人（M8.3） — 🟡 部分实现

> **当前实现只到"加入 responders 名单"，不创建临时 RoleBinding**。
> 被拉的人能否实际 ack/操作，取决于他**已有的** RoleBinding —— 恰恰是软隔离边界本身。
> PRD 设计的"事件级临时授权 + 关闭自动失效"机制尚未实现，下文分"当前行为"与"设计目标"两段。

**当前行为（已实现）**：
```
卡片/工作群里 @李四
   └─ 映射 IM id → User
       └─ AddResponder（需 incident.add_responder）
           └─ 把李四加入该 Incident 的 responders 列表 + 写时间线 responder_added
               └─ 李四能否 ack/操作 = 看他已有的 RoleBinding（软隔离不放宽）
```

**设计目标（📋 未实现，backlog）**：
```
... AddResponder 后
   └─ 给李四授"事件级临时 responder 权限"（RoleBinding, scope=incident, expires_at=incident 关闭）
       └─ 李四在 Incident 期间可以 ack/操作
           └─ Incident 关闭时自动撤销临时授权
```

**当前跨团队协作的实际路径**：由 `team_admin` 临时给对方发一个 team-scope 的 `responder` RoleBinding（可设 `expires_at`），事后手动撤销。

#### C.3.5 斜杠命令（M8.5）— 🟡 4/7 已实现，逐命令标注

入口：群内或与机器人会话发送文本 `/vigil <command> <参数>`（飞书 `im.message.receive_v1` / 钉钉机器人消息均可识别；企微 ❌）。
对照 `im/handler.go` `handleCommand` / `commandToAction`：

| 命令 | 状态 | 权限点 | 实际行为（可断言） |
|------|------|--------|------|
| `/vigil ack <id>` | ✅ | `incident.ack` | 同 Web ack（同一 incident.Service）；成功后刷新已发卡片（见下） |
| `/vigil escalate <id>` | ✅ | `incident.escalate` | 同 Web escalate |
| `/vigil resolve <id>` | ✅ | `incident.resolve` | 同 Web resolve |
| `/vigil status <id>` | ✅ | `incident.view` | 向**命令来源 channel** 发一张当前状态卡片（按发起人权限渲染按钮，`sendCardToUser`）并记录 cardID 供后续刷新；⚠️ id 不存在时**静默不发**（仍返回 200 `sent`） |
| `/vigil add @人 <id>` | ❌ | （权限映射存在） | 鉴权通过后落入 default 分支 → 400 `unsupported command: add`——**斜杠形式不支持**；拉人只能走 @机器人 mention（C.3.4，且钉钉侧 mention 解析缺失，见 C.3.3） |
| `/vigil runbook <name> <id>` | 📋 | — | `commandToAction` 返回空 → 权限映射失败 → **403** `no permission mapping for action ""`（注意：不是"unsupported command"——映射失败先于命令分发）。Runbook 执行走 Web / API（C.5） |
| `/vigil oncall` | 📋 | — | 同上 403（查值班走 Web 排班页 / `GET /schedules/:id/oncall`，B.5） |

**参数格式**（`resolveIncidentArg`）：`<id>` 支持**纯数字 ID** 或 **`INC-XXXX` 编号**（按 number 字段精确匹配，大小写敏感）；
只取第一个空白分隔 token，其余忽略（`/vigil ack INC-0042 请尽快` 合法）。

**错误分支**：

| 场景 | 预期 |
|------|------|
| 未绑定 IM 账号 | 403 `im account not bound…`（先于一切，C.3.2） |
| 无权限 | 403 `forbidden: no permission`（无审计，C.3.2） |
| 缺参数 | 400 `incident id required` |
| 编号不存在 | 400 `cannot resolve incident "INC-9999": …` |
| 非法状态转换（如 resolve 已 resolved 的单） | ⚠️ 500（同 C.3.2 卡片路径的映射缺陷） |

**执行后刷新（`refreshCard`）**：命令成功 → 若 CardStore（**进程内存**）记录过该 incident 在该平台的卡片
（通知下发或 status 回卡时记录）则刷新，否则静默跳过；**服务重启后映射丢失**，旧卡片不再可刷新。
刷新后的卡片**不带按钮**、带「XX 操作（by 谁）」徽章；钉钉刷新为 no-op（C.3.3）。

#### C.3.6 状态双向同步（M8.4/§8）— ✅ 已实现（范围见下）

**IM/API → Web：WebSocket 实时推** ✅

- 端点：`GET /api/v1/ws/incidents/:id`（`ws/handler.go`，按单个 incident 订阅）；前端详情页 `useIncidentWS` 自动订阅。
- **触发同步的事件枚举**（wire.go 订阅，与 IM 卡片刷新、出站 webhook 共用同一批）：
  `IncidentAcked` / `IncidentResolved` / `IncidentReopened` / `IncidentEscalated` / `IncidentResponderAdded` 共 5 种
  → 推送 `{"type":"incident_changed","incident_id":N,"action":"ack|resolve|reopen|escalate|add_responder","data":<incident 快照>}`。
- 前端收到后 invalidate React Query 缓存（详情 + 列表 + 时间线），页面即时刷新。
- ⚠️ **不推送的**：`IncidentCreated`（新单无实时提示，列表页要手动刷新）；`timeline_added` 消息类型已定义但**全仓无广播点**
  （时间线刷新靠 incident_changed 顺带触发）；**告警源自动恢复不发事件**（C.2.1）→ WS 不推、页面不动。
- ⚠️ **WS 端点无鉴权**：挂在 public group、无 token 校验——任何可达网络的客户端能订阅任意 incident id 的变更快照
  （含标题/摘要等）。已列入开放问题 10。

**Web/API → IM：卡片刷新** ✅（实际生效范围 = 飞书）

- 同 5 种事件 → `CardRefresher` 遍历各平台已发卡片调 `UpdateCard`。
- 刷新出的卡片：最新状态行、**无按钮、无负责人行**（BuildCard assignee 传空）。
- 前提与限制：CardStore 进程内存（重启失效）；钉钉 UpdateCard no-op、企微无卡片（C.3.3）。

**验收判据（可执行）**：

```
1. 浏览器开 /incidents/:id，在 IM 点 [确认] → 秒级内页面状态徽章变"已确认"、
   时间线出现 ack 条目（无需手动刷新）
2. Web 点 [解决] → 飞书已发卡片状态行变"已解决"、按钮消失（钉钉不变——已知降级缺口）
3. 断开网络 30s 再恢复 → 前端自动重连（指数退避 1s×1.5 上限 30s），重连后 invalidate 拉回全量最新状态
4. 负向：触发告警源自动恢复（C.2.1）→ 页面与卡片都不动（已知缺口，手动刷新可见 resolved）
```

**边界**：多副本部署时 hub 为进程内存，客户端只能收到"连到同一副本"的变更（需 Redis pub/sub，见 D.4）；
服务端读超时 90s / 写超时 30s，慢消费客户端（缓冲 16 条满）消息被丢弃不阻塞广播。

#### C.3.7 Incident 合并 — 📋 未实现（schema 仅预留）

- **人工合并 📋**：`ent/schema/incident.go` 预留 `merged_into`（指向主单）与 `trigger_type=merged` 枚举，
  但**无 merge 端点**（全路由核实无 `/merge` 类路由）、无 Web 入口、全仓无该字段写入点。
- **AI 建议合并 🟡 半程**：AI 分诊可产 `dedup_suggestion`（C.4），但人点"采纳"只把 AIInsight.status 置 accepted
  （`ai/diagnose.go` `ResolveInsight` 单字段 UPDATE），**不执行任何真实合并**（accept 不触发任何应用动作，见 C.4.4）。
- **目标流程（📋 双段式，供后续验收）**：

```
人工：列表多选 → [合并到…] 选主单 → 副单 merged_into=主单 + 状态终止，
      其 events/时间线归并主单展示，通知只走主单
AI：  dedup_suggestion accept → 触发同一合并动作（human-in-the-loop 复用人工链路）
```

- **当前替代**：靠分诊聚合（C.1.1 ③）在建单前归并；已建出的重复单只能各自 resolve（时间线互不相通）。

#### C.3.8 Web 端处置 — ✅ 列表 + 详情 + 四操作（对照 ui-ux 差距如实标注）

> IM 是首选面，Web 是兜底与全局视图。本节按前端实码（`web/src/pages/incidents.tsx` / `incident-detail.tsx`）
> 与后端实际能力写，ui-ux 设计中未实现的逐项标 📋。

**列表页 `/incidents`（✅ `GET /incidents`）**：

| 能力 | 状态 | 说明（可断言） |
|------|------|------|
| 状态筛选 | ✅ | 单选 chips：全部/待响应/已升级/已确认/已解决/已关闭 → `?status=`；⚠️ "已关闭"恒为空（closed 不可达，C.2） |
| 严重度筛选 | ✅ | 全部/严重/警告/信息 → `?severity=` |
| 分页 | ✅ | 前端每页 20；后端 `limit` 默认 50 上限 200 + `offset`；`total` 与筛选一致 |
| 团队数据隔离 | ✅ | SEC-01：team 级用户仅见自己团队的单（无 binding → 空列表；org 级全可见） |
| 列表列 | ✅ | 编号 / 标题 / 严重度 / 状态 / 升级（`L{level} · N次`）/ 创建时间；点行进详情 |
| 搜索、[服务▼]/[团队▼]/[时间▼] 筛选 | 📋 | ui-ux §4.2 设计；后端无对应查询参数、前端无控件 |
| 批量操作（批量确认/解决/分配） | 📋 | 无多选框；后端也无批量端点 |
| 噪音 tab | 📋 | 噪音在 Event 层且无 `GET /events`（B.13.1） |
| **[+ 新建事件]（手动建单）** | 📋 | ui-ux §3 顶栏按钮为**幻影**——核实：**无 `POST /incidents`** 端点、`trigger_type=manual` 仅枚举预留、`incident.create` 权限点全仓无引用。手动拉起处置当前只能靠源侧发一条真实/模拟告警（B.10） |

**详情页 `/incidents/:id`（✅）**：

- **布局**：头部（编号 + severity/status 徽章 + 标题 + 优先级/当前层级/累计升级/创建·解决时间 + 摘要）
  → 右上操作区 → **AI 诊断卡**（[诊断] / [相似事件] / 采纳-拒绝，human-in-the-loop，衔接 C.4）→ 时间线
  （全量列表：内容 + 时间 + actor + 来源；**无筛选控件**——API 支持 `?type=&source=` 但前端未接）。
- ui-ux §4.3 的左栏上下文（关联服务 / 当前值班 / Runbook 一键执行 / 相关事件）📋 未实现
  （Runbook 执行在独立 `/runbooks` 页，C.5；相似事件在 AI 卡内 ✅）。
- **操作按钮按状态启停（⚠️ 非按权限）**：活跃态 → [确认]（acked 时禁用）[升级] [解决]；
  resolved/closed → 仅 [重新打开]。无权限用户同样看到按钮，点击才 403（C.3.2 Web 负路径）。
- [+ 添加备注]（时间线人工备注）📋 未实现（见 C.8）。

**操作流 + 失败分支（以 ack 为例，四操作同构）**：

```
点 [确认] → POST /incidents/:id/ack
  ├─ 200 → toast「已确认事件 INC-0042」+ 详情就地更新（返回值直写缓存）+ 时间线/列表刷新
  ├─ 403 → toast「forbidden」（HTTP 拦截器透出后端 error 字段；无权限与跨团队同文案）
  ├─ 400 failed_precondition → toast「invalid incident status transition: ack from acked」
  │   （连点/多端并发时可见；按钮 isPending 只防单端连点）
  └─ 直调不存在的 id → 400 failed_precondition "incident not found"（⚠️ 非 404，C.2）
跨团队打开详情 → GET 返回 403 → 页面显示「事件不存在」空态 + toast「forbidden」（⚠️ 文案与 404 场景混同）
```

- **WS 实时刷新 ✅**：详情页自动订阅（C.3.6），他人/IM 的操作秒级同步。
- **快捷键 `A`/`E`/`R`/`T`（ui-ux §7.1）**：📋 未实现——前端无任何全局 keydown 处理（仅弹窗 Esc）。
- **危险操作二次确认（ui-ux §7.2：resolve critical 需确认）**：📋 未实现——四操作均单击直发无确认框
  （现有 confirm 仅删团队/删复盘/删排班）。
- **多事件聚合卡片的 [全部确认]（ui-ux §5.3）**：📋——`im/card.go` 无聚合卡片结构；通知聚合（B.7）只合并
  **文本**（"[聚合] 首条标题（含 N 条）"），不逐条列出也无批量按钮；Web 亦无批量确认（见上表）。

**改派 reassign — ❌ 无端点（仅权限点）**：

- `incident.reassign` 权限点已定义（org_admin / team_admin / responder_lead 持有），但**全仓无任何路由/逻辑引用**；
  `assignee` 只在 **ack 时自动置为操作人**，无独立改派入口（Web/IM/API 均无）。附录 A "reassign" 行已按此标注。
- **替代路径**：① 单还在活跃态：手动 escalate 拉起下一层通知 → 新人 ack 即成为新 assignee（状态会走一遍 escalated）；
  ② add_responder 拉人协同（IM @mention，Web 无入口）+ 线下交接（assignee 字段不变）；
  ③ 📋 设计目标：`POST /incidents/:id/reassign`（capability 05/09 提及的指派语义）。

### C.4 AI Copilot（贯穿，human-in-the-loop）— 🟡 仅诊断链可端到端跑通

> 设计上 AI 横向贯穿分诊/诊断/处置/复盘四阶段（capability 07 §B2）；
> **实码核实：全仓只有一处 `AIInsight.Create`（`ai/diagnose.go`，type=`root_cause_hint`）**——
> 分诊/Copilot 阶段的 AI 均无生成代码，复盘草稿走 postmortem 引擎但不落 AIInsight（C.6）。
> 本节按"当前行为 / 设计目标"双段写；**当前唯一可完整测试的 AI 链 = C.4.1 根因诊断**。

#### C.4.0 阶段总览（含通知路径，逐项对照实现）

| 阶段 | AIInsight 类型 | 设计作用 | 实现状态 | 如何通知到人（核实） |
|------|----------------|----------|----------|----------------------|
| 分诊 | `dedup_suggestion` / `severity_adjustment` / 噪声学习 | 建议合并/调级/标噪 | 📋 无生成代码（C.4.2） | —（无产出） |
| 诊断 | `root_cause_hint` | 根因线索 + evidence | ✅ C.4.1 | **仅 Web 详情页 AI 诊断卡，且需人点[诊断]拉取**——同步响应展示，无推送 |
| 诊断 | `similar_incident` | 历史相似事件 | 🟡 检索 ✅ 但**不落 AIInsight**（实时查询即答，C.4.5） | 仅 Web AI 卡内 [相似事件] 按钮，点击才查 |
| Copilot | Runbook 推荐 / `draft_summary` | 推荐处置 + 草拟摘要 | 📋 无代码（C.4.3） | —（无产出） |
| 复盘 | `postmortem_draft` | 草拟复盘各段 | 🟡 草稿生成在 postmortem 引擎（C.6），**不落 AIInsight 表** | 复盘页草稿内容，无推送 |

**「AI 建议不主动打扰」可测断言（✅ 当前行为，也是合理默认）**：AIInsight 全链路**无任何独立推送**——
不进 IM 卡片（C.3.1 已核实卡片字段无 AI 信息）、不进通知模板、不写时间线
（`ai_insight` 时间线类型已预留、**全仓零写入点**，与 `incident_created` 同款）、无 WS 消息。
AI 产出只在用户主动打开 Web 详情页并点按钮时出现。`severity_adjustment` accept 后是否重新通知的问题**不存在**——
该类型无生成代码，且 accept 本身零副作用（C.4.4）。

#### C.4.1 AI 根因诊断链（✅ 端到端操作流，唯一可完整测试的 AI 链）

**前置条件**：
- LLM 已配置（`VIGIL_LLM_API_KEY`，GLM，见 A.5）；未配置也可测降级分支（见下）。
- 操作人对该 incident 有 `incident.view`（资源级 SEC-01 校验；**诊断与采纳/拒绝都只要 view 权限**，无独立 ai 权限点）。

**操作步骤**：

```
1. Web 详情页 /incidents/:id → AI 诊断卡 → 点 [诊断]
   └─ POST /incidents/:id/diagnose（唯一触发入口：无自动诊断、无 IM 入口、无定时任务）
2. 引擎：取 incident + 全量时间线 → 构造 prompt（要求不确定性措辞 + JSON 输出）→ LLM Complete
3. 落库：AIInsight{stage=diagnose, type=root_cause_hint, status=suggested,
         content={root_cause}, confidence, evidence=[全量时间线条目]}
4. 响应 201：{"insight_id":N,"root_cause":"…","confidence":0.85,"evidence":[{timestamp,type,content},…]}
5. 前端展示：置信度徽章（≥0.8 绿 / ≥0.5 黄 / 其余灰）+ 根因文本 + 可折叠"依据（N 条）"
   + [采纳]/[拒绝]（→ C.4.4）
```

**预期结果（可断言）**：

| 场景 | 预期 |
|------|------|
| LLM 正常 | **201** + DiagnoseResult（snake_case 键，json tag 已补）；DB 多一条 status=suggested 的 AIInsight |
| **LLM 未配置** | **200** `{"status":"disabled","message":"AI 诊断暂不可用（未配置或调用失败，已降级）"}`——前端卡片显示降级提示，主流程不受影响（必测用例） |
| **LLM 调用失败**（401/超时/限流/配额） | 同上 **200 disabled**（FIX-C：失败不抛 500，原因只进后端日志 `ai diagnose: llm call failed`）——前端无法区分"未配置"与"临时故障" |
| LLM 返回非 JSON | 不报错：整段输出当根因文本、**置信度固定 0.3**（降级解析）；confidence>1 会被钳到 1 |
| 重复点[诊断] | **每次新建一条 AIInsight**（无幂等/清理）；CostController 对同 prompt 有 1h Redis 缓存（时间线没变则 LLM 不重复计费，但 insight 行照样累加） |
| id 非数字 | 400 `invalid id` |
| 跨团队 | 403（ai_insight/incident → team 反查，`scope.go`） |
| id 不存在 | ⚠️ **500**（engine 错误未分流 404，按现状断言） |

**⚠️ 与设计的三处差距（如实标注）**：

1. **置信度阈值（capability 07 Q2"默认 0.6 不展示"）📋 未实现**——代码无任何过滤，confidence=0.1 也照常落库并展示，前端只用徽章颜色区分。
2. **"evidence 强制：无依据不展示"（§B3）📋 未实现**——evidence 恒等于全量时间线条目的机械拷贝（非 LLM 挑选的依据）；新单时间线为空（C.1 ⑥）时 evidence=[]，照样展示。
3. **洞察不可回看**——AIInsight **无任何读取端点**（`GET /incidents/:id` 不带 ai_insights 边、无 list API），
   诊断结果只存在于前端本地 state：刷新页面即消失，只能重新诊断（新建一条）。历史建议仅 DB 直查可见。

#### C.4.2 分诊阶段 AI（⚠️ 纠偏：当前分诊是纯规则，无 AI 参与）

**当前行为（✅ 规则实现，详见 C.1.1）**：dedup = Redis SETNX 5min 固定窗；降噪 = SuppressionRule 手工规则匹配；
聚合 = 同 service+severity 5min 建单窗。**`triage/engine.go` 全程不调 LLM、不产任何 AIInsight**
（`dedup_suggestion` / `severity_adjustment` 仅是 schema 枚举，全仓无写入点）。

**设计目标（📋，capability 07 §B2）**：AI 分诊建议合并相似单（`dedup_suggestion`，accept 触发真实合并——
合并本身也 📋，见 C.3.7）、基于历史建议调整严重度（`severity_adjustment`）、学习模式识别噪音（B.13.1 已标 📋）。

#### C.4.3 Copilot 处置推荐与 draft_summary（📋 完全无代码）

**核实结论**：`internal/ai/` 与 `internal/runbook/` 均无 Runbook 推荐逻辑（无"按 incident 特征选 runbook"代码）；
`draft_summary` 类型全仓无写入点。当前"该用哪个 Runbook"全靠人在 `/runbooks` 页凭命名自找（C.5.0）。

**设计目标（📋，capability 06 §6 / 07 §B2）**：AI 按相似事件的历史处置推荐 Runbook（"这类故障通常用 runbook X"），
呈现为带 evidence 的 AIInsight（stage=copilot）；**accept 仅高亮/预填推荐项，不代表执行确认**——
真正执行仍走 C.5.1 的写操作审批链（`require_approval` 红线不因"AI 推荐过"而放宽）。

**`draft_summary` 与 `postmortem_draft` 的区别（易混淆，厘清）**：
- `draft_summary`（stage=copilot）：处置**进行中**为 Incident 草拟/更新一段现状摘要（给后来加入的协作者同步上下文）——📋 无代码。
- `postmortem_draft`（stage=postmortem）：事件**解决后**起草结构化复盘（summary/impact/root_cause 各段）——
  🟡 功能在（`postmortem/engine.go GenerateDraft`，C.6），但产出直接进 Postmortem.sections，**不落 AIInsight**，
  故不走 C.4.4 的 accept/reject 反馈链（复盘的"人评审"= 直接编辑正文）。

#### C.4.4 反馈闭环（accept/reject 的真实效果）

**当前行为（✅ 端点在，效果仅一个字段）**：

```
POST /ai-insights/:id/resolve   {"accepted":true|false}     # 权限：incident.view（经 insight→incident→team 反查）
→ 200 {"status":"resolved","accepted":true}
→ 唯一副作用：AIInsight.status = accepted / rejected（单字段 UPDATE，ai/diagnose.go ResolveInsight）
```

| 断言点 | 核实结果 |
|--------|----------|
| 状态流转 | suggested → accepted / rejected；**可反复改判**（UPDATE 无前置状态校验，accepted 可再 POST 改成 rejected）——但仅限 API：Web 卡片 resolve 后即清空结果且无历史入口（C.4.1 差距 3） |
| `applied` 状态 | 📋 **无产生路径**——枚举预留，全仓无 SetStatus(applied)；"accept→应用（合并/填充）"是设计目标，当前 **accept 不触发任何应用动作**（`dedup_suggestion` 场景见 C.3.7 纠偏） |
| 审计/时间线 | ❌ 零留痕——不写 AuditLog（仍仅 7 种 action，B.12）、不写 IncidentAction（全仓零写入，B.12 纠偏）、不写时间线。**谁在何时采纳/拒绝无从追溯**（AIInsight 无 resolver 字段） |
| 拒绝率统计 | 📋 analytics 6 端点无 AI 维度（无 insight 计数/采纳率指标）；"拒绝率高的建议类型调优 prompt"（capability 07 §B6）当前只能 DB 直查 `GROUP BY type,status` |
| 学习闭环 | 📋 无自学习——reject 不影响后续任何建议（下次诊断照常调 LLM，prompt 不含历史反馈）；反馈数据仅沉淀在表里 |
| 不存在的 insight id | ⚠️ 500（未分流 404，同 C.4.1） |

**与知识反哺的关系**：accept/reject **不影响**相似检索（C.4.5 的 embedding 由 incident/postmortem 内容决定，与 insight 状态无关）——
"采纳的诊断权重更高"之类机制 📋 不存在。

#### C.4.5 知识反哺（相似事件 + 相似复盘检索）

**消费侧（两个端点，权限均 `incident.view`）**：

| 端点 | 后端 | 前端 | 行为 |
|------|------|------|------|
| `GET /incidents/:id/similar?limit=`（默认 5） | ✅ | ✅ AI 卡 [相似事件] 按钮（点击才查） | 返回 `{"similar":[Incident…]}`；主路径 pgvector 余弦距离，**降级路径 LIKE 文本匹配**（取标题首词/前两汉字模糊查 title/summary，按创建时间倒序） |
| `GET /incidents/:id/similar-postmortems?limit=`（默认 3） | ✅ | ❌ **无 UI**（`api.ts` 无方法、无组件调用）——只能 curl/API | 返回 `{"similar_postmortems":[Postmortem…]}`（含 incident 边）；⚠️ **无 LIKE 降级**——pgvector/Embed 任一不可用时**静默返回空数组**（与 similar 的降级行为不同，写用例注意区分） |

**生产侧（复盘怎么进检索库）**：

```
复盘 publish（PATCH /postmortems/:id/transition → published，C.6）
  └─ ensurePublishedEmbedding：取 sections 的 summary + root_cause 文本 → LLM Embed → 写 postmortems.embedding
      ├─ embedder 未配置 / Embed 失败 → 静默跳过（发布照常成功，复盘不入检索库；无日志无重试无回填任务）
      └─ sections 两段全空 → 跳过（不算错误）
```

| 细则（可断言） | 核实结果 |
|------|----------|
| 哪些复盘可被检索到 | **仅 `status='published'`**（SQL WHERE 硬编码）——⚠️ **archived 会掉出检索**（embedding 还在但状态不匹配）；draft/in_review 从不可见 |
| embedding 计算时机 | 仅 publish 那一刻（Transition 内）；**发布时 LLM 未配置的存量复盘永远无 embedding**（无补算任务，重新 publish 也不可能——archived 是终态） |
| incident 侧 embedding | **懒计算**：首次调 similar/similar-postmortems 时 Embed（title+summary 截 2000 字节）并回写持久化；回写失败不阻塞本次检索 |
| 排序语义 | pgvector `<=>` 余弦距离升序；无相似度阈值——哪怕毫不相关也凑满 limit 条（结果质量需人判断） |
| 降级链 | pgvector 扩展缺失 / SQLRunner 未注入 / Embed 失败 → similar 走 LIKE；similar-postmortems 返回 `[]`（见上表） |

**端到端验收（发布复盘 A → 新事件 B 命中）**：

```
前置：pgvector 扩展已装 + LLM key 已配（Embed 用 GLM embedding-3，1536 维）
1. 造 Incident A（如"支付网关 5xx"）→ resolve → POST /incidents/:idA/postmortem/draft
2. 编辑 sections（summary/root_cause 写入有语义的文本）→ transition: draft→in_review→published
   （断言：postmortems.embedding 非空——DB 直查，无 API 可见）
3. 再触发一条语义相近的告警建单 B（B.10 的 curl 改标题）
4. GET /incidents/:idB/similar-postmortems → 断言返回含 A 的复盘
5. 负向：停掉 LLM key 重启 → 同一请求返回 {"similar_postmortems":[]}（静默降级，非报错）
```

> 设计愿景"复盘入知识库反哺下次处置"（C.1 ⑬）当前的实际形态就到这里：**检索有了、呈现断链**
> （similar-postmortems 无前端入口 + AI 诊断 prompt 不引用相似复盘内容）。"上次类似故障怎么处理的"
> 一键直达 📋。

### C.5 Runbook 执行（两档安全）

> 创作与配置侧（CRUD/步骤校验/执行器/SSRF/凭据）见 B.8；本节写**处置现场怎么用**。

#### C.5.0 呈现与执行入口（⚠️ 纠偏：自动触发/自动展示均未实现，唯一入口是 Web Runbooks 页）

**四种触发类型的可感知行为（`trigger.type`，capability 06 §3）**：

| trigger.type | 设计的可感知行为 | 实现状态（核实） |
|------|------|------|
| `manual` | 响应者手动点"执行" | ✅ **当前唯一实际生效的方式**（且与 trigger 字段无关——不配 trigger 也能手动执行） |
| `on_incident` | Incident 创建即**展示**关联 runbook 链接（不执行） | 📋 字段可存库、**全仓无求值代码**（M9.2） |
| `on_severity` | 达到某严重度时展示 | 📋 同上 |
| `on_label_match` | 匹配 label（如 service=payment）时展示 | 📋 同上 |

**默认红线（设计基线第 5 条，写用例时作为不变式）**：自动触发的语义是"**展示**给人参考"，**绝不自动执行**；
auto-run 仅允许"步骤全 readonly + admin 显式配置"的组合——判断函数 `IsReadOnly` 已实现但**全仓无调用方**（配套的 auto-run 链路 📋）。

**"告警来了，Runbook 在哪"——三处呈现断链（已逐项核实，均属实）**：

1. **IM 卡片不带 runbook**（C.3.1 卡片字段核实无）；通知模板也无 runbook 链接变量；`/vigil runbook` 命令 403（C.3.5）。
2. **Incident 详情页不展示关联 Runbook**（`incident-detail.tsx` 无 runbook 元素；ui-ux §4.3 左栏"Runbook 一键执行"📋，C.3.8 已标）。
3. **Service↔Runbook 关联 API 未暴露**（B.8）——即使实现了 on_incident 求值也无数据可查。

**当前唯一操作路径（✅ `web/src/pages/runbooks.tsx`）**：

```
Web 侧栏 → Runbooks 页 → 列表（凭命名找，team 隔离见 B.8）→ 点入详情
  → 详情页只渲染 content_markdown（⚠️ "处置步骤"卡片显示的是文档正文，steps JSON 不可视化——
     想核对将执行什么，只能 GET /runbooks/:id 看原始 steps；创建/编辑弹窗也只有 name/type/markdown，
     steps 与 trigger 仅能通过 API 配置）
  → 点 [执行] → 弹窗手填事件 ID（数字，无搜索/下拉，需先去 incidents 页抄 ID）→ [确认执行]
```

- **AI 推荐入口 📋**：见 C.4.3（无代码；设计目标是推荐后仍走本节执行链）。
- **document 型执行行为（衔接 B.8，可断言）**：对 document 型（或 steps 为空的 executable）调
  `POST /runbooks/:id/execute` → **200 空跑**（返回 `Steps` 为空的结果，无报错、不写时间线）——
  document 就是"给人看的"，执行是无害 no-op。
- **用例锚点**：✅ 可测——Runbooks 页手动执行全链路、document 空跑、无 `runbook.execute` 权限者 403（按钮不隐藏，同 C.3.8 风格）；
  📋 不可测——on_* 自动展示、IM 内触发、详情页一键执行。

#### C.5.1 写操作审批的真实交互（✅ 已修复：前端真实传递审批决策 + 阻断按 on_failure 处理 + actor 留痕）

原文"require_approval → IM/Web 弹窗确认，deny → skip/abort"是设计目标（capability 06 §5）。**当前实现**（2026-07-04 安全修复后）：

```
POST /runbooks/:id/execute  {"incident_id":42,"approved":true|false}     # 权限：runbook.execute
执行到某 step（同步逐步执行，一个请求内跑完全部步骤）：
├─ target.readonly==true（诊断）→ 直接跑
└─ target.readonly==false（写操作）
    ├─ approved==true  → 跑（引擎凡写步骤只认 approved，与 require_approval 标志解耦，双保险见 B.8）
    └─ approved==false → 阻断该步（Skipped=true）+ 时间线留痕「未获审批，已阻断」（含 actor）
                          + res.PendingApproval=true，随后按该步 on_failure 处理：
                          continue → 跳过继续（合法"干跑"）；abort → 中止；escalate → 中止并升级
```

| 设计里的审批要素 | 实际情况（逐项核实 `runbook/engine.go` + `handler.go` + `web/src/pages/runbooks.tsx`） |
|------|------|
| 审批交互形态 | **同步请求参数**：仍无独立 pending 状态/审批超时——approved 随 execute 请求一次性给定，缺省 false（引擎的写闸门始终生效；pending/超时审批流 📋） |
| Web 的"确认" | ✅ `/runbooks` 页执行弹窗含**「我确认执行写操作」复选框**（默认不勾选）——按钮据此在「执行（仅干跑）」/「确认并执行写操作」间切换，**真实传递用户决策**（不再恒发 `approved:true`）；⚠️ 弹窗仍不逐条展示哪些步骤是写操作 |
| IM 确认 | ❌ 无入口（`/vigil runbook` 403，C.3.5）——"写操作确认在 IM 完成"（M8）📋 |
| deny 分支 | approved=false 对写步骤 = **阻断并按 on_failure 处理**：continue 跳过继续（合法"干跑"，B.8 操作序列第 2 步）、abort/escalate 则中止（escalate 另触发升级）；只读步骤照常执行 |
| 确认者是谁 | **发起人自证**：唯一权限校验是发起人的 `runbook.execute`，approved 由同一人给出——无独立审批人/双人复核（📋）；数据层强制写步骤 require_approval=true，引擎凡写步骤只认 approved（不因标志放行） |
| 审批留痕 | ✅ 执行/阻断动作均写时间线并**记录 actor**（发起人 user id，source=web）；on_failure=escalate 触发的升级也透传 actor（C.5.3）。⚠️ AuditLog（7 种 action）/ IncidentAction 仍不写（📋） |
| 并发保护 | ❌ 无锁无幂等：同一 runbook+incident 并发两次 execute **都会完整执行**（写操作重复触发！幂等只能靠外部端点自身保证，capability 06 安全约束"由调用方保证"——用例应覆盖连点） |

#### C.5.2 执行结果与失败处理

**响应体 `ExecuteResult`（同步返回全部步骤结果）**：

```
{"RunbookID":7,"IncidentID":42,"Aborted":false,"Reason":"",
 "Steps":[{"StepID":"s1","Name":"查连接池水位","Action":"diagnose","Success":true,
           "Output":"{\"status_code\":200,\"body\":\"…\"}","Error":"","Duration":123456789,"Skipped":false},…]}
```

- ⚠️ **键名 PascalCase**（结构体无 json tag；B.11 analytics 曾同款缺陷，现已补 tag 修复，Runbook 结果结构体仍待修）：前端按 `data.result` 取值恒 undefined →
  **Web 执行后只有一句 toast「执行完成」，看不到任何步骤结果**（成败/输出全被吞）。结果实际只在
  ① API 响应体 ② 时间线 两处可见。执行历史无独立页面/端点——事后只能翻该 incident 的时间线。
- `Output`：http 执行器为 FIX-E 结构化 `{"status_code":N,"body":"…"}`（探活类空 body 也能看到状态码）；
  internal 执行器为 `check_http`/`info` 的 JSON（B.8）。⚠️ **失败步骤 Output 为空**：HTTP ≥400 时执行器虽产出结构化结果，
  但引擎在 err!=nil 分支**丢弃 output 只留 Error**（`"http 500"`）——失败响应体（往往含真正的错误信息）看不到，时间线 detail.output 同样为空。
  `Duration` 仅执行器路径有值（wait/notify/approve 与 skipped 恒 0）。
- `wait`/`notify`/`approve` 三种 action 类型是**占位实现**：wait 不等待直接返回"waited"、notify/approve 返回 no-op——别用它们造审批流。

**时间线（`runbook_executed`，✅ 每步一条，含 actor）**：执行步骤内容「执行 Runbook 步骤 "X"」（失败追加"（失败）"），
detail=`{step,success,output}`；**被阻断的写步骤（approved=false）也记一条**「Runbook 步骤 "X" 为写操作，未获审批，已阻断」，
detail=`{step,blocked:true,reason:"require_approval"}`（安全审计：干跑同样在时间线可见谁尝试跑了未批准的写操作）；
actor=发起人（user id，source=web；系统触发则 kind=system）；`incident_id=0`（不关联事件执行）则全程不记。

**on_failure 三分支（step 失败时，可测矩阵）**：

| on_failure | 引擎行为 | 可见后果（断言点） |
|------------|----------|--------------------|
| `continue`（默认） | 记失败继续下一步 | 响应 Aborted=false；该步时间线带"（失败）"；后续步骤照常有结果 |
| `abort` | 立即中止 | Aborted=true + Reason=`step "X" failed: <err>`；后续步骤**无条目**（不是 skipped，是根本没执行） |
| `escalate` | 中止 + 触发升级 | 同 abort 的中止效果；另调 `incident.Service.Escalate(actorID=<发起人>, source=runbook)` ✅ **已接**（actor 透传，不再恒 0）——事件升一层、发 `IncidentEscalated` → escalation 立即通知下一层（B.6）。⚠️ 时间线上这条升级记录内容仍是「手动升级到 level N」、source=**system**（timeline source 枚举无 runbook，映射进 default）——**看不出是 Runbook 触发的**，只能靠紧邻的失败步骤条目推断 |

> ⚠️ **写步骤未获审批（approved=false）也走 on_failure**（安全修复后）：被阻断的写步骤对 on_failure=abort/escalate 会**中止/升级**（关键处置未完成需人介入），on_failure=continue 才是"跳过继续"的干跑。想纯干跑预览，请用 on_failure=continue 的步骤。

- **escalate 的边界**：incident 已 resolved → Escalate 报错但被 `_=` 吞掉（结果仍 Aborted，无升级也无报错透出）；已在最高层 → 只记时间线不再通知（C.2）。
- **超时 ⚠️ 仅 HTTP client 级**：http 执行器 30s、internal 探活 10s（`ssrf.go newHTTPClient`）——步骤级 `timeout` 配置字段不存在，"每步有超时"（capability 06 安全约束）只对 HTTP 类动作成立且不可配。
- **用例锚点（3 步 runbook：只读→写(on_failure=continue)→只读）**：① 全成功（approved=true）→ 3 条时间线、Aborted=false；
  ② 第 2 步失败 on_failure=continue → 3 条时间线（中间带失败）；③ 第 2 步失败 on_failure=escalate →
  2 条 runbook 时间线 + 1 条"手动升级"+ 下一层收到通知；④ approved=false 干跑 → 响应 3 条 Steps（中间 Skipped）、
  时间线 3 条（含 1 条写步骤「已阻断」）、res.PendingApproval=true；若把写步骤 on_failure 设为 abort/escalate，则干跑在写步骤处中止/升级。

#### C.5.3 审计断言修正（⚠️ 纠偏："每步落 IncidentAction"未实现）

原文/capability 06"所有执行落 IncidentAction（who/when/what/result）"与代码相反——
**IncidentAction 全仓零写入**（见 B.12 纠偏）。当前执行痕迹的真实分工：

| 要素 | 当前落点 | 缺口 |
|------|----------|------|
| what/result | ✅ 时间线 `runbook_executed`（detail 含 step/success/output） | 无 runbook 名称/ID（只有步骤名——重名步骤分不清来自哪个 runbook） |
| when | ✅ 时间线 timestamp | — |
| **who** | ✅ **时间线**——`RecordRunbook`/`RecordRunbookBlocked` 透传发起人 actorID（来自鉴权中间件）：actor=`{kind:user,id:<uid>}`、source=web（系统触发则 kind=system）；on_failure=escalate 的升级也透传 actor | 🟡 仅时间线有 who；AuditLog（仍 7 种 action）/ IncidentAction 未写（📋，见开放问题 12）——但"谁在生产上跑了/被阻断了写操作"已可从时间线追溯 |
| 审批（approved）留痕 | ✅ 执行步骤 detail=`{success,output}`、阻断步骤 detail=`{blocked:true,reason:"require_approval"}` 均含 actor（C.5.1） | 无独立"审批人≠执行人"记录（当前发起人自证，双人复核 📋） |

**设计目标（📋）**：执行落 IncidentAction（type=runbook, via, result）→ B.12 审计页按 action 筛选回放；
RecordRunbook 补 actor 透传。与 B.12"两类留痕的真实分工"交叉引用。

### C.6 复盘闭环（Domain 8） — 🟡 手动链路可跑通（起草→评审→发布→改进项），自动触发/闸门/建单未实现

**当前行为**：草稿生成（`postmortem/engine.go` GenerateDraft：时间线自动填充 + AI 草拟 + LLM 不可用时规则化降级）、
状态机流转、Action Item CRUD、删除均已实现，Web「复盘」页（`web/src/pages/postmortems.tsx`）可走完全流程；
但**自动触发（C.6.1）、复盘闸门（C.6.1）、逐段评审、发布自动建工单（C.6.2）全部 📋**。

```
Incident resolved
   └─ 📋 设计目标（未实现）：按 severity 自动触发起草（critical 强制；warning 可配；info 不强制）→ 见 C.6.1
   └─ 🟡 当前：Web 复盘页点 [从事件起草]（弹窗手填事件 ID）或调 POST /incidents/:id/postmortem/draft
       └─ 草稿 sections：timeline（时间线自动填充）+ summary/impact/root_cause（AI 起草，降级规则文案）
           + what_went_well / what_went_wrong（恒占位）；⚠️ contributing_factors 源设计有、引擎不生成
           └─ 状态: draft
               └─ 人评审（📋 逐段 accept/edit/reject 未实现；⚠️ 连整体编辑 API 都没有，见下）
                   └─ in_review（可打回 draft）
                       └─ published（published_at 仅首次；embedding 入检索库）
                           ├─ Action Items：手动添加 {description, owner_id, tracker_url}（due_date 📋 API 未暴露）
                           │   └─ 📋 发布时自动建 Jira/禅道工单 → 见 C.6.2 纠偏
                           └─ 入知识库 → 反哺相似 Incident 检索（C.4.5）
                               └─ archived（终态；⚠️ 副作用：掉出相似复盘检索，见下）
```

**关键指标**：复盘完成率 ✅（`GET /analytics/postmortems`，口径缩水见 B.11）；Action Item 闭环率 / 超期高亮 📋（due_date API 未暴露，无数据源）。

#### 草稿章节清单与分工（`engine.go` GenerateDraft，可断言）

| sections 键 | 来源 | LLM 不可用/起草失败时降级 |
|------|------|------|
| `timeline` | 时间线**正序全量**机械拷贝 `{time,type,content}`（事实依据，不走 AI） | 同左；时间线为空则空数组 |
| `summary` | AI 起草 | 「事件 INC-xxxx（severity）：title。」 |
| `impact` | AI 起草 | 「持续时间约 Xm0s（待补充影响用户数/损失）。」（未 resolved 则"未知"） |
| `root_cause` | AI 起草 | 「（待人工填写）」 |
| `what_went_well` / `what_went_wrong` | **恒占位** `["（待人工补充）"]`（AI 不参与） | — |
| `action_items` | **恒空数组**（改进项走独立实体，见 C.6.2） | — |
| `contributing_factors` | ❌ **不生成**（能力域 08 §4.2 模板有此章节，engine.go sections 无此键） | — |

> **`generated_by` 语义纠偏**：注入了 LLM（哪怕本次每段都降级）恒 `mixed`，未配置 LLM 恒 `human`——
> 这是**复盘级**粗粒度标记，M12.3 的"每个字段标 AI 起草来源、人一键 accept/edit/reject"📋 未实现。
> ⚠️ 起草**不校验 incident 状态**：对 triggered/acked 的活跃单也能起草成功（无"resolved 后才能复盘"约束）。
> 对不存在的 incident id 起草 → **500**（engine 错误未分流 404，同 C.4 诊断链缺陷款）。

#### 评审与发布操作流（`PATCH /postmortems/:id/transition`，`engine.go` isValidTransition）

请求体 `{"status":"<目标状态>"}`。合法流转矩阵（❌ 均为 **400** `{"error":"invalid transition <from> → <to>"}`）：

| from ＼ to | draft | in_review | published | archived |
|------|:---:|:---:|:---:|:---:|
| draft | — | ✅ 送审 | ❌ 400（不可跳级发布） | ❌ 400 |
| in_review | ✅ **打回** | — | ✅ 发布 | ❌ 400 |
| published | ❌ 400（发布后不可打回） | ❌ 400 | — | ✅ 归档 |
| archived | ❌ 400 | ❌ 400 | ❌ 400（**不可逆**，终态） | — |

**发布（in_review → published）的副作用清单（可断言）**：

1. `published_at` **仅首次**写入（代码守卫 `pm.PublishedAt == nil`，幂等）。
2. 计算 embedding 入检索库（sections 的 summary+root_cause，C.4.5）；embedder 未配置/计算失败**静默跳过不阻塞发布**——
   此时该复盘**永远不进相似检索**（无补算任务，C.4.5 已注）。
3. ❌ **不回写 `incident.status`**（不联动 closed，C.2 闸门 📋）；❌ 不发任何通知/IM 卡片（"复盘发布"无推送）。

**archived 的副作用（⚠️ 讲归档语义必知）**：相似复盘检索 SQL 硬编码 `WHERE status='published'`——
归档 = **掉出知识库检索**（C.4.5）。"归档旧复盘"会让它对新事件不可见，与"沉淀"直觉相反，写用例时按此断言。

**Web 交互**：详情页右上状态下拉直接列全部 4 态，选中即发 transition——选非法目标（如 draft 直选 published）后端 400，前端 toast 报错。

#### 权限映射（RouteGuard `wire.go` + 资源级 `handler.go` + `seed.go` 对照）

| 动作 | 端点 | 权限校验 |
|------|------|----------|
| 列表 | `GET /postmortems` | **无权限点**，仅登录态 + SEC-01 team 可见性过滤 ⚠️（oncall 无 `postmortem.view` 也能看到本团队列表条目，点详情才 403） |
| 详情 | `GET /postmortems/:id` | 资源级 `postmortem.view`（经 incident.team 回溯） |
| 起草 | `POST /incidents/:id/postmortem/draft` | ⚠️ **零校验**——RouteGuard 未登记、handler 无 checkAccess，`postmortem.create` 权限点**悬空**。任何登录用户（含 subscriber、他团队用户）都能对任意事件起草/覆盖复盘 |
| 流转（含送审/打回/发布/归档） | `PATCH /postmortems/:id/transition` | 路由级 `postmortem.publish` + 资源级 `postmortem.view`——**draft→in_review 送审也要 publish 权限**（无独立 update 流转语义） |
| 删除 | `DELETE /postmortems/:id` | 路由级 `postmortem.update` + 资源级 `postmortem.view` |
| 改进项增/改/删 | 见 C.6.2 | 资源级 `postmortem.actionitem.manage`（无路由级） |

内置角色持有（`seed.go`）：org_admin 全部；team_admin 全部（含 actionitem.manage）；
**responder_lead** 有 view/create/update/publish 但**无 actionitem.manage**（能发布复盘却不能管改进项 ⚠️，见 C.6.2）；
responder / subscriber 仅 view；**oncall 无任何 postmortem 权限点**（一线值班人看不了自己处置事件的复盘详情，开放问题）。

#### 人工编辑与重复起草（⚠️ 重大限制，写用例必读）

- **无内容编辑 API**：全路由核实——不存在 `PATCH /postmortems/:id`（sections 更新端点没有），Web 章节卡片**纯只读**。
  "人评审修改草稿"当前**做不到**；唯一"改写"手段 = 重新 POST draft 让引擎整体覆盖后另行口头评审。
- **重复起草直接覆盖 sections**（GenerateDraft 对已存在复盘走 UPDATE，**不看状态**）：对 in_review 甚至 **published**
  的复盘再调一次 draft 端点，sections 与 generated_by 被新生成内容整体覆盖——人工评审结论全丢；
  status / published_at 不变，**embedding 不重算**（检索库内容与正文就此脱节）。
  结合上表"起草零权限校验"：**任何登录用户可无声破坏已发布复盘**（负向用例必测；开放问题候选）。
- 起草端点的 409 分支（"postmortem already exists"）实际是**死代码**——引擎对已存在复盘走覆盖不报错；
  该错误映射只在数据库约束冲突等罕见场景触发。

#### 删除（✅ `DELETE /postmortems/:id`）

- 先删其全部 ActionItem 再删复盘（handler 手动级联 ✅，无孤儿），204；**对 published 同样可删**（无状态守卫）。
- Web 有 confirm 弹窗（「其改进项将一并删除，且不可恢复」）。删除不回写 incident，可再次起草全新草稿。

#### C.6.1 复盘触发与闸门 — 📋 全部未实现

**设计目标（能力域 08 §3 M12.7）vs 当前**：

| severity | 设计 | 当前（全部核实） |
|------|------|------|
| critical | resolved 后**自动**建 draft；强制复盘，未复盘不可 close | 📋 无自动触发——`IncidentResolved` 订阅方仅 IM 卡片刷新 / 出站 webhook / WS（wire.go），无 postmortem 挂钩 |
| warning | 可配（默认建议不强制） | 📋 无任何配置项（config.go 无复盘触发相关配置，"可配"无入口） |
| info | 不强制，可跳过复盘直接 close | ⚠️ 当前**无 close 端点**（C.2：closed 不可达），"跳过复盘"无需任何操作——info 单 resolved 即事实终态 |

- **闸门（"待复盘"状态）📋**：设计要求强制复盘的单 resolved 后停"待复盘"、复盘 published 才可 close。
  当前两头都不存在：无 close 端点（闸门无从拦截），发布也不回写 incident 状态（C.2 状态机注解已标）。
- **"待复盘"积压可见性 🟡**：`GET /incidents?status=resolved` ✅ 可查全部 resolved 单（B.11 的 `resolved` 计数同源）；
  但**无"resolved 且无复盘"组合筛选**——需拿 `GET /postmortems`（响应含 incident 关联）与 resolved 列表**人工比对**；
  Web 无"待复盘"视图/角标/提醒。critical 解决后没人起草，系统不会有任何信号。
- **resolved 后 reopen，已生成草稿如何处理（✅ 核实）**：reopen（C.2）**完全不触碰 postmortem**——
  草稿/已发布复盘原样保留、仍关联该 incident；二次 resolve 后再点起草 = **覆盖旧 sections**
  （timeline 章节会包含两轮处置记录，summary/root_cause 被重写）。无"复盘作废/重开评审"语义。

#### C.6.2 Action Item 跟踪闭环 — 🟡 手动 CRUD 可用（⚠️ 纠偏："发布时自动建工单"未实现）

**纠偏**：能力域 08 §5"复盘发布时自动在 Jira/禅道建改进任务（联动能力域 14）"**无任何集成代码**——
全仓无 Jira/禅道/工单系统 API 调用；`tracker_url` 只是一个**手填字符串**（不校验格式，不回写状态）。
发布（transition→published）的副作用只有 embedding（见上），与工单零关联。

**已实现操作流（`postmortem/handler.go`）**：

| 操作 | 端点 | 字段 | 核实说明 |
|------|------|------|----------|
| 添加 | `POST /postmortems/:id/action-items` | `description`（必填，缺 → 400）/ `owner_id` / `tracker_url` | ⚠️ `owner_id` 是**自由字符串**（非 User 外键，不校验存在性）；**`due_date` schema 有此字段但请求体不收**（📋，B.11 复盘度量"超期"没数据源同因）；Web 表单只有 description 一栏（owner/tracker 仅 API 可填） |
| 推进/改派 | `PATCH /action-items/:id` | `status` / `owner_id` / `tracker_url`（全可选） | status 枚举 open/in_progress/done，**无状态机校验**（done→open 也合法，任意改）；改派 = PATCH owner_id（B.14 交接引用此端点）；Web 仅状态下拉 |
| 删除 | `DELETE /action-items/:id` | — | 204；无确认约束 |

**当前实际工单流（如实）**：复盘发布后，责任人**手动**去 Jira/禅道建单 → 把工单链接 `PATCH` 回 `tracker_url`
（Web 复盘详情里 tracker_url 渲染为外链）。状态推进（open→in_progress→done）全靠人在 Vigil 里手点，与外部工单**互不同步**。

**权限（⚠️ 分布不合理，可断言）**：三个端点资源级校验 `postmortem.actionitem.manage`
（action_item→postmortem→incident→team 三级回溯）；内置角色**仅 org_admin / team_admin 持有**——
**responder_lead 能发布复盘、却不能添加/推进自己复盘的改进项**（403），改进项闭环必须 team_admin 代管。
（附录 A 权限矩阵与开放问题 5 已同步收录。）

**与 B.14 联动**：Action Item owner 被禁用时改派 = `PATCH /action-items/:id {"owner_id":"..."}`；
但 owner_id 是自由字符串（无外键）→ 系统**无法感知** owner 被禁用（无提示、无兜底，B.14 交接清单已列为手动项）。

**设计目标（📋，记入 backlog M14.2）**：发布时自动建外部工单 + 外部状态回写 ActionItem + due_date 超期提醒/高亮。

### C.7 降级分支（当某环节不可用）— ⚠️ 本表按代码核实全面修订

| 故障/异常 | 实际行为（核实） | 状态 |
|------|----------|:---:|
| LLM 未配置 / 调用失败 | diagnose 返回 **200 `{"status":"disabled"}`**（不报错，C.4.1）；复盘草稿规则化降级（C.6）；主流程不受影响 | ✅ |
| LLM 限流命中 / token 配额耗尽 | cost controller 拒绝（`ai/cost.go`）→ 与"未配置"**同一个** 200 disabled 提示，**前端不区分原因**；区分只能看 metrics `vigil_llm_calls_total{result=rate_limited\|quota_exceeded}` | ✅（提示不区分 ⚠️） |
| LLM 相同 prompt 复调 | 1h 内命中 Redis 缓存直接返回（键 = sha256(prompt)，TTL 默认 1h；`vigil_llm_cache_hits_total`） | ✅ |
| Redis 挂 / 未配置 | LLM 缓存/限流/配额**三道闸全放行**（直连真实调用）；接入限流/背压跳过；分诊 dedup 放行不去重（C.1.1）；通知聚合降级为立即逐条发；⚠️ 但 **Asynq 队列不可用——分诊/升级异步链停摆**：接入层仍返回 **202**（RawEvent 落库标 `requeued`，「告警源不必重试」），恢复后**无自动回灌需人工**（B.15） | ⚠️ |
| IM 平台挂 | ❌ **纠偏：无逐通道降级链**。通道集合固定为全局默认（webhook?+im+email）**并联各发一份**（C.9）；IM 发送失败仅 metrics failed + 日志，email/webhook 照常（本来就会发，不是"兜底"）；**电话/短信不会顶上**（通道已注册但不在默认链，永远不被调用，见 C.9） | ⚠️ |
| 卡片无法原地更新 | ❌ **纠偏**：飞书 ✅ 原地刷新；钉钉 `UpdateCard` 为 **no-op（什么都不发）**——"降级发新消息标注最新状态"只存在于注释与设计文档；企微 NoopBot 完全不发（C.3.3） | 📋 |
| 企微未接入 | NoopBot `Available()=false` → IM 通道**静默跳过**该平台（不报错、不计 metrics），通知仍走其余 Available IM 平台 + email 并联 | ✅ |
| 排班空班 | ❌ **纠偏：不告警 team_admin**——空层静默 continue（B.5.2），唯一信号 = 升级时间线「通知 **0** 人」；兜底靠升级策略**末级 target=team**（需策略这么配，非系统内置） | 📋 |
| Event 未路由 | 落 unrouted 池，不建单不通知；❌ **纠偏：critical 兜底通知全员/admin 未实现**（B.13）；池无查看端点，仅 analytics 计数 + DB 直查 | 🟡 |
| 接入源超限（限流/背压） | 429 rate_limited / 503 backpressure，**payload 已落 RawEvent 不丢**（status=received）；恢复后**无自动回灌**，需源侧重发或人工回灌（B.15） | ✅（回灌 📋） |
| 归一化无适配器 / 解析失败 | RawEvent 标 `parse_failed`，不产 Event 不通知，**静默无人消费**（无查询/重放端点，B.15） | 🟡 |
| 告警源自动恢复 | 单被置 resolved，但**不写时间线、不发领域事件**——Web 不实时刷新、IM 卡片停旧态、webhook 不推（C.2.1） | 🟡 |
| reopen 后无人接手 | 升级链**不重启、通知不重发**，静默停在 triggered（C.2）；要拉起通知需手动 escalate | ⚠️ |
| 写 Runbook 步骤失败 | `on_failure: escalate` ✅ 自动升级并立即通知下一层（C.5.2）；⚠️ 时间线归因显示「系统 手动升级到 level N」source=system，看不出是 runbook 触发 | ✅ |
| 通知发送失败（单通道/全通道） | 仅结构化日志 + metrics `result=failed`；**无重试、无兜底告警、无补发**（quiet_hours 静默的通知直接丢弃）——排查路径见 C.9 | ⚠️ |

> **手动重发通知（renotify）📋 无实现**：通知没送到/被静默丢弃后，**没有**"对当前层再发一次"的端点或按钮
> （全仓无 renotify/重发逻辑）。唯一替代是手动 `POST /incidents/:id/escalate`——但**语义不同**：
> escalate 是**推进层级**（状态置 escalated、通知**下一层**），不是原层重发；误用会把事件推向更高层级。

### C.8 时间线协同（能力域 10）— 🟡 查询/备注 API 全，前端呈现与自动捕获有缺口

时间线是协同与复盘的事实基础（设计铁律：全程留痕）。统一写入口 `timeline/recorder.go`，各域共用。

#### 查看（✅ Web 详情页 + API）

- **Web**：事件详情页（`incident-detail.tsx`）「时间线」卡片，**正序展示**（旧→新，`recorder.Query` 恒 `Order(Asc(timestamp))`，
  **无倒序选项**——ui-ux 常见的"最新在上"不成立，写断言按正序）。每条：类型圆点着色 + content + 时间 + actor + 来源。
  - 圆点着色仅 4 类型有专色：`ack`/`resolved`/`escalated`/`incident_created`（其中 incident_created 实际零写入，见矩阵），
    其余（note_added/runbook_executed/…）统一灰色。
  - actor 文案：有 name 显示 name；kind=user →「用户 {id}」；ai →「AI」；integration →「集成」；其余「系统」。
- **实时刷新**：WS `incident_changed` 到达时前端顺带 invalidate 时间线查询 ✅（`use-incident-ws.ts`）；
  `timeline_added` 消息类型前端已处理但**后端无广播点**（C.3.6）——**手动备注不会实时推给其他打开详情页的人**（要等下一次状态变更或手动刷新）。

#### 筛选与分页（🟡 后端全、前端未接）

```
GET /api/v1/incidents/:id/timeline?type=&source=&limit=&offset=     # 权限：incident.view（资源级）
```

- `type` = 时间线类型枚举（见下矩阵）、`source` ∈ web/im/api/system/ai；两者可组合，非法值静默不命中（空结果）。
- `limit` 默认 **100**、上限 **500**（≤0 或 >500 回落 100）；`offset` 正整数生效。
- ⚠️ 响应 `total` 是该事件**全量条目数**（`recorder.Count` 不吃 type/source 筛选条件）——
  带筛选时 total ≠ items 可翻页数，分页器按此实现会翻出空页（与 B.11/incident 列表的 total 口径不同，可断言）。
- **前端未接**：详情页全量展示，无 type/source 筛选控件、无分页（超 100 条只显示前 100，长事件时间线被截断且无提示 ⚠️）。

#### 手动备注（🟡 API ✅ / 前端无入口）

```
POST /api/v1/incidents/:id/timeline
{"content":"扩容后错误率回落，继续观察"}          # content 必填，缺 → 400 "content required"
→ 201 {"status":"recorded"}                        # 类型恒 note_added
```

- 默认 `actor.kind=user`、`source=web`；⚠️ **actor 与 source 均由请求体自报**（服务端不从登录态回填 actor.id，
  也不校验 source 取值）——调用方可冒充任意人/AI/system 留痕，审计价值打折（见开放问题 11）。
- **权限仅 `incident.view`**：subscriber（只读干系人）也能加备注——"只读"角色能写时间线（见开放问题 11）。
- **前端无备注输入框**（详情页只读展示，api.ts 也无对应方法）——"响应者备注"当前只能 curl/API。
- IM 侧无 `/vigil note` 类命令（C.3.5）；作战室消息回写（M10.5）🚧 随作战室推迟（backlog）。

#### 自动捕获验证矩阵（★ 逐行核实的写入点全集，写用例直接抄）

`TimelineItem.type` 枚举 12 种，实际有写入点的只有 **7** 种：

| 动作 | 类型 | content 样例 | actor / source | 状态 |
|------|------|------|------|:---:|
| Incident 创建 | `incident_created` | — | — | ❌ **零写入**（新单时间线为空，C.1 已注） |
| Event 归并进已有单 | `event_attached` | — | — | ❌ **零写入**（聚合不可见，风暴期间时间线无感知） |
| ack（Web/IM/API） | `ack` | 「用户 N 确认了事件」 | user:N / web 或 im | ✅ |
| 手动 resolve | `resolved` | 「用户 N 解决了事件」 | user:N / web 或 im | ✅ |
| **告警源自动恢复** | — | — | — | ❌ **不写**（直接 UPDATE 绕过 Service，C.2.1） |
| reopen | `reopened` | 「用户 N 重新打开了事件」 | user:N / web | ✅ |
| 手动 escalate | `escalated` | 「用户 N 手动升级到 level X」 | user:N / web 或 im | ✅ |
| 升级引擎自动升级 | `escalated` | 「升级到 level X，**通知 N 人**」（detail.notified=N，空班时 N=0 是唯一信号） | system / system | ✅ |
| 拉人 add_responder | `responder_added` | 「用户 N 拉 M 加入响应」 | user:N / web 或 im | ✅ |
| 手动备注 | `note_added` | 请求体 content | 请求体自报 / 默认 web | ✅（仅 API） |
| Runbook 步骤执行 | `runbook_executed` | 「执行 Runbook 步骤 "xxx"（失败）」detail={step,success,output} | **恒 system / system**（执行人不可见，C.5.3）；skipped 步骤不记；**无 runbook 名** | 🟡 |
| AI 诊断/采纳/拒绝 | `ai_insight` | — | — | ❌ **零写入**（AI 建议不进时间线，C.4.1） |
| IM 关键消息捕获 | `im_message` | — | — | ❌ 零写入（M10.5 🚧 随作战室推迟） |
| 状态变更（通用） | `status_changed` | — | — | ❌ 零写入（语义被 ack/resolved 等具体类型覆盖） |

> **source 归因口径（⚠️ 易误解）**：Web 端四操作与直接调 REST API 都记 `source=web`（handler 硬编码 SourceWeb，
> **没有 api 归因**）；`source=api` 只会出现在手动备注自报时。IM 操作 ✅ 正确记 `source=im`。
> B.12 已注：操作留痕唯一载体就是本时间线（IncidentAction 零写入），上表 ❌ 行 = 对应动作**彻底无痕**。

**验收（可执行）**：

```
1. 发测试告警建单（B.10）→ GET timeline：断言空数组（incident_created 不写 ❌ 的现状）
2. ack → resolve → reopen → escalate 各一次 → 断言 4 条、类型/actor/source 正确、正序
3. POST 备注（不带 actor/source）→ 断言 type=note_added、actor.kind=user、source=web
4. GET ?type=note_added → 断言只回备注；同时断言 total 仍为全量 5 条（口径 quirk）
5. subscriber 用户 POST 备注 → 断言 201（当前放行，权限收紧后此用例反转）
```

### C.9 通知没送到怎么办（能力域 7 排查手册）— ⚠️ 送达状态不可查询，先读纠偏

#### 通道决定机制（⚠️ 重大纠偏：没有"兜底链"）

设计（能力域 04）：NotificationRule.channels + EscalationPolicy levels[].notify_channels 逐级决定通道，失败降级下一通道。
**当前实现完全不是**（`wire.go` buildNotifier + `notifier.go` 核实）：

- 每次升级通知的通道集合 = **启动时固定的全局默认链** `[webhook?, im, email]`
  （webhook 仅在配置 `VIGIL_WEBHOOK_OUT_URLS` 时加入）。
- `NotificationRule.channels` **不参与评估**（规则只贡献 quiet_hours 与 template_id，且取"首条 enabled"，B.7）；
  `EscalationLevel.notify_channels` **全仓无引用**（B.6）。
- 通道间是**并联各发一份**，不是"失败才切下一个"——IM 挂了 email 照发（因为本来就发），但**不存在**"IM 失败 → 电话顶上"。
- **电话/短信通道注册了但不在默认链**——`phone`/`sms` 在 registry 里，defaultChans 硬编码不含它们，
  **任何在线路径都不会调用**（即使配好 webhook 中间层）。详见下方"电话/短信占位说明"。
- 单个用户维度：email 通道查 `User.email`（空则该人收不到邮件）；IM 通道发**固定值班群** `VIGIL_IM_ONCALL_CHANNEL`
  （按人私聊 📋，C.3.1）——群没配时 IM 通道**静默返回**（不报错、**连 metrics 都不计**，可观测性盲区 ⚠️）。

#### 送达可观测性（当前仅两处，均无 UI）

| 手段 | 内容 | 状态 |
|------|------|:---:|
| Prometheus `/metrics` | `vigil_notifications_sent_total{channel, result="success"\|"failed"}`（`notifier.go` sendOne 埋点） | ✅ |
| 结构化日志 | 成功 `notification delivered`、失败 `notification failed`（含 incident/channel/target/error 字段；`wire.go` SetResultRecorder 已接线——**只写日志**） | ✅ |
| Notification 实体 / 送达查询端点 / Web 送达面板 | 设计的 pending→sent/failed 状态机（能力域 04）——**ent/schema 无 Notification 实体**，逐条送达记录不落库、不可查询（B.11 夜间打扰指标无数据源同因） | 📋 |
| 全通道失败的兜底告警 | 通知全部失败时告警给管理员——**无任何代码**（失败只有日志+计数，没人会被叫醒） | 📋 |

#### 排查步骤（按信号从粗到细，可执行）

```
1. /metrics：vigil_notifications_sent_total 有无增量？result=failed 在哪个 channel？
   （无任何增量 → 通知根本没触发，回到升级链排查：GET timeline 看有无 escalated 条目）
2. GET /incidents/:id/timeline?type=escalated：
   「升级到 level X，通知 N 人」——N=0 → targets 解析为空（空班 B.5.2 / 值班人被禁用 B.14 / targets 配错 B.6）
   连这条都没有 → 升级任务没跑（Redis/Worker 挂，C.7；或 incident 已 ack 停链）
3. 服务日志 grep "notification failed"：看 channel + target + error（SMTP 拒收 / IM API 报错 / webhook 非 2xx）
4. 通道配置核对：SMTP（VIGIL_NOTIFICATION_SMTP_HOST 空=禁用）；IM 凭据 + VIGIL_IM_ONCALL_CHANNEL
   （占位值自动置空 → IM 静默不发且无日志无计数，B.9/C.3.1）；VIGIL_WEBHOOK_OUT_URLS
5. 静默与聚合：quiet_hours 命中（非 critical 被静默 = 直接丢弃无补发，B.7）；
   非 critical 走 30s 聚合窗（收到的是「[聚合] xxx（含 N 条聚合通知）」且晚 ≤45s，B.7）
6. 收件人资料：User.email 为空 → 邮件无收件人静默跳过；IM 发群不依赖个人绑定（绑定影响的是卡片按钮操作，C.3.2）
```

> **修复后无法补发**：以上任一环节修好，**已丢的通知不会重发**（renotify 📋，见 C.7 注）；
> 只能手动 escalate 触发下一层通知，或等 repeat_times 剩余轮次（B.6 时间轴）。

**验收（对应 C.7「通知失败」行）**：

```
1. 故意配错 IM 凭据（或清空 VIGIL_IM_ONCALL_CHANNEL）→ 发 critical 测试告警（B.10）
2. 断言：Incident 正常创建、升级时间线正常（通知失败不阻塞升级链）
3. 凭据错：metrics im/failed 增量 + 日志 notification failed；
   群未配：im 无任何增量、无日志（静默盲区，按现状断言）
4. email 通道（若配 SMTP）：success 增量——证明"并联"而非"兜底切换"
5. 断言：无任何补发/兜底告警发生（📋 现状）
```

#### 电话/短信通道占位说明（M7.2 现状，一次讲清）

- **形态**：`channels_phone.go` 是**占位转发器**——不接任何云厂商；配置
  `VIGIL_NOTIFICATION_PHONE_WEBHOOK_URL`（SMS 同理 `VIGIL_NOTIFICATION_SMS_WEBHOOK_URL`，可选 `..._FROM`）后，
  通知 POST 到该 URL，由**用户自建中间层**对接阿里云/腾讯云语音 API。
  payload（核实）：`{"channel":"phone","incident":"INC-xxxx","title","summary","level",
  "recipients":[电话号码...],"from"}`。未配置时 `Available()=false` **静默跳过**（不报错）。
- ⚠️ **纠偏（比"占位"更早的断点）**：如上节所述，phone/sms **不在默认通道链**，升级通知**永远不会走到这个通道**——
  当前连"电话叫醒"都不会发生（"电话兜底"整体 📋：既无触发路径，也无云厂商对接）。配了 webhook URL 也只是空等。
- **号码来源（🟡 半截）**：`User.phone` 字段 schema **存在**，`resolvePhones` 按 targets 批量查 ✅；
  但 **无任何 API 能写入 phone**（`PATCH /users/:id` 仅收 name/status/timezone，全仓无 SetPhone 调用）——
  号码只能 DB 直改（重大配置缺口，写入开放问题）。号码为空的用户被静默剔除。
- **接收侧交互（📋）**：IVR「按 1 确认告警」（电话里直接 ack）无实现——设计上电话只负责叫醒，
  ack 仍需回 IM 卡片 / Web / API 完成（附录 C 剧本 2 已按此表述）。

---

## 旅程 D：运维保障（升级 / 迁移 / 备份 / 灾难恢复）

**主角色**：平台运维 / SRE Lead（旅程 A 的延续，但面向**长期运行**而非首次部署）
**目标**：让 Vigil 在生产长期跑下去 —— 安全升级版本、定时备份、出事能恢复。
**特点**：低频高危、命令行驱动、必须先在测试环境验证。

> ⚠️ 与旅程 A 的区别：A 是"从零跑起来"；D 是"已经在跑，要动它"。所有 D 操作**先备份、先在测试环境验证**，再上生产。

### D.1 升级 / 迁移（H1.4）

**前置认知（双轨迁移机制，见 `internal/migrate/migrate.go`）**：
```
vigil migrate 执行顺序：
1. 建 schema_migrations 表（版本追踪）
2. 读已应用版本
3. 跑 pre_*.sql      ← 前置（如装 pgvector 扩展）
4. ent auto-migrate  ← 权威源，建/同步全部 17 实体表
5. 跑 其他 .sql      ← 后置增量（数据回填/索引调优等）
已应用的版本跳过（幂等）。
```

**升级流程**：
```
1. 测试环境验证（必做）：
   git pull && docker compose build vigil
   cp 生产备份到测试环境 → restore.sh 恢复
   docker compose exec vigil vigil migrate   # 跑迁移
   docker compose up -d vigil                # 启动新版本
   冒烟测试：登录 / 发测试告警 / 看 /health /metrics
2. 生产升级：
   a. 先备份（见 D.2）
   b. git pull && docker compose build vigil
   c. docker compose exec vigil vigil migrate   # 关键：migrate 是显式子命令
   d. docker compose up -d vigil                # 滚动重启
   e. 验证：/health、/metrics、发一条测试告警走通 C 的主链路
3. 多副本（Helm）：逐 pod 滚动，确保至少 minAvailable 个就绪
```

**关键限制**：
- ❌ **无 migrate-down / 回滚**（`migrate.go` 只前进不后退）。回滚靠**备份恢复**（见 D.2），这是为什么 step 2a 必做。
- ⚠️ `ent/schema/*.go` 改动后须 `go generate ./ent/...`（开发者责任，见 AGENTS.md）。
- ⚠️ ent auto-migrate 对**删列/改类型有限制**，破坏性变更须 hand-tuned SQL 挂到 `post_*.sql`。
- ✅ 升级期间服务可用性：API 无状态可滚动；Worker 升级时正在处理的任务由 Asynq 重试；升级计时器存 Redis 不受影响。

### D.2 备份（H1.5）

**脚本**：`scripts/backup.sh`（✅ 已实现）
**备份内容**：
- PostgreSQL：`pg_dump` 全量（自定义格式，支持并行恢复）
- Redis：`BGSAVE` 触发 RDB 快照 + 拷贝 `dump.rdb`

**操作**：
```
手动：./scripts/backup.sh                       # 用环境变量
      ./scripts/backup.sh /path/to/backup/dir   # 指定目录
定时（推荐）：
      # crontab -e
      0 2 * * * /path/to/vigil/scripts/backup.sh >> /var/log/vigil-backup.log 2>&1
```

**保留策略**：脚本本身不轮转，建议外部 cron + `find backups/ -mtime +7 -delete`（保留 7 天）。
**验证**：备份产物含 `<timestamp>/vigil_pg.dump` + `redis_dump.rdb.gz`；定期在测试环境 restore 验证可用性（**不验证的备份等于没有**）。

### D.3 恢复 / 灾难恢复

**脚本**：`scripts/restore.sh`（✅ 已实现）
**典型场景**：

| 场景 | 恢复方式 |
|------|----------|
| 误删数据 / 版本升级失败 | stop vigil → `restore.sh <backup_dir>` → start vigil |
| 数据库崩溃 | 新建 PG → restore → 重启 vigil（pgvector 扩展须先装） |
| Redis 丢（升级计时器丢） | **从最近备份恢复 RDB**；恢复前在飞的升级任务需人工核查 Incident 状态 |
| 整机灾难 | 新机器 → 拉镜像 → 起依赖 → restore → 启动 |

**Redis 丢失的特殊风险**（升级计时器存活于 Redis）：
```
Redis 数据丢失 = 正在等待的 Asynq 延迟任务（升级计时器）丢失
后果：未 ack 的 Incident 不会按 EscalationPolicy 升级
处置：
1. 从 RDB 备份恢复 Redis（首选）
2. 若无法恢复，手动核查 status ∈ {triggered, escalated} 的 Incident，
   必要时手动触发升级（incident.escalate 权限）或逐个 ack
```

**恢复流程**：
```
1. docker compose stop vigil
2. ./scripts/restore.sh backups/<timestamp>     # 恢复 PG + Redis
3. docker compose exec postgres pg_isready -U vigil   # 验证 PG
4. docker compose start vigil
5. 验证：/health、登录、发测试告警
```

### D.4 多副本演进（📋 待规划）

当前单实例优先。多副本的关键风险：
- **WebSocket 广播**：多 API 副本需 Redis pub/sub 同步状态推送（影响旅程 C 的"Web 实时刷新"）
- **Worker 队列分片**：多 Worker 副本天然支持（Asynq 设计），但升级任务的幂等性须保证（已用 `incident_id + level` 去重）
- **会话**：JWT 无状态，多副本无问题

详见 [`architecture.md`](./architecture.md) §7 与本文"开放问题"第 3 条。

### D.5 监控 Vigil 自身（谁来盯守夜人）

> `/metrics`（Prometheus 格式）与 `/health` 均在根路径、**无鉴权**（`server.go`）。Prometheus 直接抓 `/metrics` 即可（K8s 下需自配 scrape，chart 无 ServiceMonitor，见 A.3）。

**关键业务指标**（`internal/metrics/metrics.go` 全集）：

| 指标 | 维度 | 含义/用法 |
|------|------|------|
| `vigil_alerts_received_total` | source, severity | 接入量；**归零 = 接入断流**（B.15 的"Events 归零"信号源） |
| `vigil_incidents_created_total` | severity | 建单量 |
| `vigil_escalations_triggered_total` | — | 升级触发次数；**告警在增而它无增量 = 升级链卡死**（Redis/Worker 问题） |
| `vigil_notifications_sent_total` | channel, result | 通知结果（success/failed）；C.9 排查第一信号；含 `channel=webhook_out`（旅程 F 出站） |
| `vigil_incident_resolve_duration_seconds` | histogram | 创建→解决时长（1min~8h 桶） |
| `vigil_timeline_items_recorded_total` | type | 时间线写入量（C.8） |
| `vigil_llm_calls_total` / `_cache_hits_total` / `_rate_limited_total` / `_tokens_total` | stage,result / — / — / provider | AI 链健康与成本（C.4.0 三道闸可观测面） |
| `vigil_http_requests_total` / `_request_duration_seconds` | method, path, status(2xx/4xx/5xx) | 通用 HTTP 面 |

> ⚠️ **没有队列深度指标**——Asynq 队列积压不在 `/metrics` 里，只能自部署 Asynqmon（B.15；chart 的 `asynqmon.enabled` 是空承诺，A.3）或 redis-cli 直查。📋 待补。

**建议告警规则**（写给外部 Prometheus，不是 Vigil 自己的 NotificationRule）：

```promql
rate(vigil_notifications_sent_total{result="failed"}[10m])
  / rate(vigil_notifications_sent_total[10m]) > 0.3        # 通知失败率
rate(vigil_alerts_received_total[15m]) == 0                 # 接入断流（业务时段）
rate(vigil_llm_calls_total{result="error"}[15m]) > 0        # LLM 链持续报错
probe /health != 200                                        # PG/Redis 任一 down（health 返回 503 并带 checks 明细）
```

> ⚠️ **回灌循环风险**：把"Vigil 自身告警"再接回 Vigil（建个 Integration 推给自己）是危险闭环——Vigil 挂了它不会告诉你它挂了（通知链就是坏掉的那部分）。**自监控必须走独立通道**（外部 Alertmanager → 直发 IM/短信，不经 Vigil）。

**演练用例**（可断言）：kill Redis → `/health` 返回 503（`checks.redis: down`）、K8s 探针打挂 pod NotReady、接入 `/webhook/{token}` 仍 202（payload 落库，B.4 降级语义）但分诊不消费；恢复 Redis 后队列续跑。升级计时器随 Redis 数据丢失的处置见 **D.3**（从 RDB 恢复 / 手动核查在飞 Incident）。

---

## 旅程 E：团队 Leader（subscriber）只读订阅视角

**主角色**：业务/研发团队 Leader（`personas.md` 画像 4）——不值班、不处置，但是干系人。
**核心诉求**：出事时**第一时间被告知**（订阅），事后能看团队事件全貌与复盘。
**权限**：内置 `subscriber` 角色（team scope），权限集**只有 3 个**（`seed.go` 核实）：`incident.view` / `event.view` / `postmortem.view`。

### E.1 前提：怎么成为 subscriber

```
1. Leader 的 User 存在（⚠️ 无 POST /users，当前只能 DB 直插，见 B.1.1）
2. org_admin 发 RoleBinding：
   POST /role-bindings {user_id, role_id=<subscriber 角色 id>, scope_level:"team", team_id, (expires_in_hours 可选)}
```

> ⚠️ 纠偏：发绑定需要 `role.assign`，内置角色中**只有 org_admin 持有**（team_admin 只有 `role.view`）——"team_admin 给自己团队 Leader 发订阅"当前做不到，须 org_admin 代发。开放问题候选（团队级授权下放）。

### E.2 「第一时间被告知」的机制核实 — ⚠️ 当前没有"订阅"

**当前行为**（对照代码，逐条如实）：

| 渠道 | subscriber 能否被主动告知 | 依据 |
|------|------|------|
| IM 群卡片 | 🟡 **唯一现实途径 = 身处值班群**。卡片发到全局唯一的 `VIGIL_IM_ONCALL_CHANNEL`（不分团队，`wire.go`）——Leader 在这个群里就能看到全组织告警卡片 | C.3.1 |
| 邮件 | ❌ 默认收不到。邮件目标 = 升级层 targets 解算出的 user 的 `User.email`（`resolveEmails`），subscriber 的 RoleBinding **不参与** targets 解算 | B.6 |
| 邮件（变通） | 🟡 让 team_admin 把 Leader 加为升级某层的显式 `user` 型 target——之后每次触达该层就发邮件。⚠️ 注意 `team` 型 target **不解算成员**（`resolveTargets` 只产 `{Name:"team:N", UserID:0}` 占位），对邮件/电话等按 user_id 解析的通道**等于不发**，别指望"target=team 全team收邮件" | `escalation/engine.go` |
| Web/WS 主动推送 | ❌ 无订阅实体（ent 17 实体无 subscription/watcher）、无定向推送；WS 只有单 Incident 粒度（C.3.6） | schema 全集 |

> 📋 **设计目标**（M7.3 / personas P4"订阅"）：按服务/团队/severity 的订阅关系 + 邮件/IM 定向通知。当前形态是"进群围观"，不是订阅。开放问题候选。

### E.3 Web 只读旅程（登录 → 看全貌 → 看复盘）

```
1. 登录 → Dashboard：⚠️ 看到的是【全组织】数据——analytics 无权限点、无团队 scope
   （B.11 纠偏）。subscriber 看得到别的团队的负载/指标，与"软隔离"基线相悖 → 开放问题。
2. /incidents 列表：✅ 这里有隔离（SEC-01）——只看到自己绑定团队的 Incident。
3. 详情 /incidents/:id：✅ 状态/severity/responders/关联 Event + 时间线（C.8 正序全量）。
   ⚠️ 按钮不按权限隐藏、只按状态启停（C.3.8）——subscriber 看得到 [确认][解决] 等按钮且可点，
   点了后端 403 {"error":"forbidden"}（RouteGuard），前端 toast 报错。体验缺陷 → 开放问题。
4. 复盘：GET /postmortems 列表 + 详情（有 postmortem.view，draft/published 都能看，C.6）。
```

### E.4 IM 侧：卡片与命令

- **群通知卡片**：⚠️ 群卡片按钮是按"首个通知 target（值班人）"的权限渲染的（`notification_channel.go`），不是按看卡片的人——subscriber 在群里看到的卡片**带** [确认][升级][解决] 按钮。点击 → 回调硬鉴权拒绝：HTTP 403 `{"error":"forbidden: no permission"}`，且**群内无任何提示**（拒绝回执只在 HTTP 响应，C.3.2）——Leader 点了没反应，观感是"卡死"。
- **`/vigil status INC-xxxx`**：✅ 返回的卡片按**本人**权限裁剪（`card.go` PermissionMap）——subscriber 只有 `incident.view`，卡片仅剩 [📋 详情] 一个按钮（detail→incident.view）。这是"按权限渲染"的正例。
- 前提：Leader 的 IM 账号已被 org_admin 代绑（B.9），否则任何 IM 交互 403 `im account not bound, please bind in web`。

### E.5 负向用例矩阵（写用例直接抄）

| 动作 | API（直调） | Web | IM |
|------|------|------|------|
| ack / resolve / escalate / reopen | 403 `{"error":"forbidden"}`（RouteGuard，四端点均已登记权限点） | 按钮可见可点 → 403 toast | 点卡片按钮/斜杠命令 → HTTP 403 `forbidden: no permission`，群内静默 |
| 执行 Runbook | 403（`runbook.execute` 未持有，路由已登记） | Runbooks 页执行 → 403 | 无 runbook 命令（C.3.5，本就 403） |
| 发起/发布复盘 | ⚠️ **起草竟然能成**——`POST /incidents/:id/postmortem/draft` 零权限校验（C.6 纠偏），subscriber 能起草甚至覆盖已发布复盘；transition 则 403（postmortem.publish 已登记） | 复盘页起草同样能成 | — |
| 加时间线备注 | ⚠️ **201 成功**——`POST /incidents/:id/timeline` 仅要求 `incident.view`（C.8），"只读角色"能写备注且 actor 可自报 → 开放问题 | 前端无备注入口（C.8） | — |
| 看别的团队 Incident 详情 | 403（资源级 checkAccess 按团队反查，`incident/handler.go` get） | 列表根本不出现 | — |

### E.6 与 responder_lead 的差异对照（`seed.go`）

| 能力 | subscriber | responder_lead |
|------|:---:|:---:|
| 看 Incident/Event/时间线 | ✅ | ✅ |
| ack/resolve/escalate/reopen/reassign/add_responder | — | ✅（reassign 有权限点但无端点，C.3.8） |
| 执行 Runbook | — | ✅ |
| 复盘：看 / 起草+发布 | ✅ / —（按权限设计；实际起草无校验见 E.5） | ✅ / ✅ |
| 管 Action Item | — | —（lead 能发布却管不了改进项，C.6.2 quirk） |
| 通知/抑制规则查看 | — | ✅ |

**quiet_hours 与 Leader**：若按 E.2 变通把 Leader 设为 user 型 target，其通知按 B.7 规则评估——Leader 不是排班解算出的值班人（`isOncall=false`），quiet_hours 窗口内非 critical 通知**直接静默丢弃无补发**（B.7 纠偏）；critical 默认穿透。即"夜里只被 critical 吵醒"这点目前反而是对的。

### E.7 📋 移动端与值班大屏（记录取舍）

`ui-ux.md` §6.1 设计了移动端响应式 + PWA（半夜掏手机场景：首屏摘要+大按钮、暗色、PWA 推送），§6.2 设计了值班大屏（`?display=wall`：在班人+活跃事件+今日指标投屏）。**两者均未实现**——`web/src` 无 PWA manifest/service worker、无 wall 相关代码（全量 grep 核实）；移动端仅靠 Tailwind 默认响应式勉强可看。当前手机上的"第一时间"实际靠 IM 原生 App 的卡片（C.3.1）。📋 未排期（已列入开放问题 15）。

---

## 旅程 F：程序化集成（平台工程师 / API 消费者）

**主角色**：内部平台工程师（`personas.md` 画像 3）——要把 Vigil 作为组件集成进自家平台，不要独立系统。
**关注点**：API 完整性、Webhook 出入、契约稳定。
**契约文档**：`GET /docs`（Swagger UI）+ `GET /openapi.yaml`（✅ 均已实现且**无鉴权公开**，`openapi.go`）。⚠️ 注意 spec 与运行时的已知偏差：Runbook 执行结果键名 PascalCase（C.5.2）以 spec 为准会踩坑；analytics（B.11）已补 json tag 对齐 spec。

### F.1 入向：把告警/事件推进 Vigil

> ⚠️ **纠偏：`POST /api/v1/events` 开放 API 不存在**（全路由核实，附录 B 该行为幻影）。
> 当前程序化接入的**唯一入口**：

```
1. 建 webhook 型 Integration（B.4）→ 拿 token
2. POST /api/v1/webhook/{token}   # 按 B.4 通用 JSON 契约（title/severity/labels.service/...）
   → 202 {"status":"accepted","raw_event_id":N}；幂等靠 dedup_key（C.1.1）；
   限流 429（retry_after=60）/ 背压 503（retry_after=30），payload 已落 RawEvent 不丢（B.4）
```

> 📋 设计目标（capability 10 §A1）：`POST /api/v1/events` 用 **X-Vigil-Key** 鉴权的开放事件 API（区别于 Integration token：Key 挂人/系统账号、可吊销、可审计）。未实现——X-Vigil-Key 目前只用于调**管理/查询类** API（见 F.2），不用于事件投递。

### F.2 API Key 生命周期（✅ 全链路已实现）

**签发**（`POST /api-keys`，权限点 `admin.apikey.manage`，内置仅 org_admin；落 `apikey.create` 审计）：

```jsonc
// 请求（handler_apikey.go apiKeyCreateReq）
{"name": "ci-bot", "scope": ["incident.view"], "expires_in_hours": 720}   // 0/缺省=永久
// 响应 201
{"id": 3, "name": "ci-bot", "prefix": "vgl_1a2b3c4d", "scope": ["incident.view"],
 "status": "active", "expires_at": "...", "created_at": "...",
 "token": "vgl_<32hex>"}   // ★ 明文仅此一次，库内只存 SHA256；丢了只能重建
```

**使用**：任意业务 API 带 `X-Vigil-Key: vgl_...` 头（`resolver.go` 三轨第 2 轨；同时带无效 Bearer 会被优先拒绝，不回退）。**权限 = 归属 User 的角色权限**——`scope` 字段只存储展示、**不做收敛**（`apikey.go` 注释"本期不强制"，📋）。所以给 CI 发 Key 前先想清楚：Key 的能力 = 你这个人的全部能力。

**Web 入口**：✅ 设置页 API Key tab（`web/src/pages/settings/apikey-tab.tsx`：列表/创建（name + expires_in_hours，**无 scope 输入**）/撤销，明文一次性弹窗+复制）。
**巡检**：`GET /api-keys`（仅列自己的 Key，含 `last_used_at`——每次校验成功 best-effort 更新，可用于发现僵尸 Key）。
**吊销**：`DELETE /api-keys/:id` → 204（只能删自己的，删他人 403、不存在 404；落 `apikey.delete` 审计）。⚠️ 吊销 = **硬删**；schema 的 `status=disabled` 枚举无任何 API/代码路径可设置（"停用不删"做不到，📋）。

**负向用例**（`apikey.go` Verify + `middleware.go`，不区分原因防探测）：

| 场景 | 预期 |
|------|------|
| 无效 / 已过期（`expires_at` 过时）/ 已删除的 Key | 401 `{"error":"missing or invalid credentials"}` |
| Key 有效但归属 User 无对应权限 | 403 `{"error":"forbidden"}` |
| ⚠️ **Key 归属 User 被禁用（status=disabled）** | **请求照常成功**——`Verify` 只查 Key 自身的 status/expires，**不查 `User.status`**；Authorizer 也不查。与 refresh token 洞（B.14，JWT 路径）是**两个独立的洞**：禁用用户既能用存量 refresh token 续 30 天，其名下 API Key 更是**永久有效**直到被逐个删除。交接清单（B.14）需加一条"删除/接管其 API Key"。开放问题候选 |

### F.3 出向：Incident 生命周期 Webhook（`webhook/dispatcher.go`）

**配置**：环境变量 `VIGIL_WEBHOOK_OUT_URLS`（逗号分隔多 URL），**改动需重启生效**（启动时读入，无动态订阅表；订阅 CRUD 📋 已记 backlog）。

**实际触发的事件**（`wire.go:201-211` 订阅清单核实，⚠️ 与 capability 10 §A4 的"created/acked/resolved/escalated"不一致）：

| payload `event` 值 | 触发时机 | 备注 |
|------|------|------|
| `incident.ack` / `incident.resolve` / `incident.reopen` / `incident.add_responder` | 对应操作成功后 | reopen **会发**（已核实） |
| `incident.escalate` | **仅手动升级**（含 Runbook on_failure=escalate） | ⚠️ **计时器自动升级不发**——escalation 引擎不发布领域事件（`escalation/` 全包无 bus.Publish） |
| `incident.created` | ❌ **不发** | dispatcher 未订阅 IncidentCreated（纠偏：外部系统**无法靠出站 webhook 感知新告警**，只能轮询 GET /incidents） |

**payload 断言样例**（字段从代码抄，无信封无签名）：

```json
{
  "event": "incident.ack",
  "incident_id": 42,
  "incident": "INC-0042",
  "status": "acked",
  "severity": "critical",
  "title": "支付服务 5xx 错误率 > 5%",
  "summary": "...",
  "timestamp": "2026-07-03T02:15:04Z"
}
```

**可靠性语义**（可断言）：每 URL 独立 goroutine 真异步（不阻塞操作主流程）；单次请求超时 **10s**；**有重试**（⚠️ 纠偏"无重试"疑问）——最多 3 次，网络错误线性退避 1s/2s，**非 2xx 立即重试不退避**；3 次全败仅计 `vigil_notifications_sent_total{channel="webhook_out",result="failed"}`，**无落库、无死信、无补发**。
**鉴权**：⚠️ **出站无签名头**（仅 `Content-Type` + `User-Agent: Vigil-Webhook/1.0`）——接收端**无法校验来源**，任何人可伪造回调打你的接收端点。缓解：URL 带 secret query + 网络 ACL；HMAC 签名 📋（开放问题候选）。

### F.4 WS 订阅（值班大屏/状态墙的现实替代）

`GET /api/v1/ws/incidents/:id`（单 Incident 粒度）。推送消息：`{"type":"incident_changed","incident_id":N,"action":"ack|resolve|reopen|escalate|add_responder","data":<incident 快照>}`；`timeline_added` 类型有定义**无广播点**；`incident.created` 与自动升级同样**不推**（同 F.3 根因）。断线无服务端心跳，客户端自行重连（前端参考实现：指数退避 1s×1.5 封顶 30s，`web/src/lib/ws.ts`）。
⚠️ **无鉴权**——public group 不校验任何凭证，任何可达网络者可订阅任意 incident 的快照（含 title/summary 敏感信息）。C.3.6 已标，开放问题候选。想做"全team大屏"还需逐 incident 建连（无全量流）。

### F.5 排班查询（值班表集成场景）

`GET /api/v1/schedules/{id}/oncall?time=`（✅）——把"现在谁值班"嵌进自家平台/群机器人。响应为 `{schedule_id, schedule_name, layers[]}`（⚠️ 非能力域文档的 `{primary, secondary, overrides}`，B.5 纠偏）；`GET /schedules/{id}/preview?days=` 拉未来排班。写操作（换班 Override）📋 未实现（B.5.1）。

### F.6 幂等 / 限流 / 背压 / 契约位置（速查，勿重复展开）

- 入向幂等与三级分诊可断言行为 → **B.10 / C.1.1**；限流背压参数与 429/503 语义 → **B.4**。
- OpenAPI 契约 → `GET /docs`（Swagger UI）/ `GET /openapi.yaml`；改 handler 注解后 `go generate ./cmd/vigil/...` 再生成（AGENTS.md）。
- 管理面 API 全集 → 附录 B。

---

## 附录 A：权限矩阵（旅程 × 动作 × 权限点）

> 内置角色：`org_admin`(org) / `team_admin`(team) / `responder`(team) / `responder_lead`(team) / `subscriber`(team) / `oncall`(team)。
> ✅=权限点持有 · —=无。权限以 `internal/auth/seed.go` 的 `builtinRoles` 为权威来源（本表对照 2026-07-03 代码）。
>
> ⚠️ **权限点持有 ≠ 端点存在**：📋 标记 = 对应端点/功能未实现（权限点悬空，持有了也无处可用）；
> ⚠️ 标记 = 端点存在但**未挂权限点**（实际放行范围大于本表，详见备注）。

| 动作（权限点） | org_admin | team_admin | responder_lead | responder | oncall | subscriber | 备注 |
|------|:---:|:---:|:---:|:---:|:---:|:---:|------|
| **旅程 A / 账号** | | | | | | | |
| 部署/migrate | —（系统级）| — | — | — | — | — | 终端操作，无业务权限点 |
| 改密码（本人，`POST /auth/change-password`）| ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | 任何登录用户可改**自己的**密码；管理员重置**他人**密码 📋 无端点（B.1.1） |
| **旅程 B** | | | | | | | |
| 建用户 | 📋 | — | — | — | — | — | 无 `POST /users` 端点，只能种子/DB（B.1.1） |
| 禁用用户（`user.disable`）| ✅ | — | — | — | — | — | ⚠️ `PATCH /users/:id` **未登记权限点**——实际任何登录用户可调（开放问题 8） |
| 建/删角色（`role.create/delete`）| ✅ | — | — | — | — | — | 无编辑端点（B.2.1）；team_admin 仅 `role.view` |
| 发 RoleBinding / 临时授权（`role.assign`）| ✅ | — | — | — | — | — | **仅 org_admin**——team_admin 无法给 Leader 发 subscriber 绑定（E.1/剧本 3） |
| 签发/吊销 API Key（`admin.apikey.manage`）| ✅ | — | — | — | — | — | GET 仅列自己的；落审计（F.2） |
| 建团队（`team.create`）| ✅ | — | — | — | — | — | team_admin 仅 `team.update`/`team.view` |
| 加团队成员（`team.member.manage`）| ✅ | ✅ | — | — | — | — | 📋 端点未实现（B.2），权限点悬空 |
| 建 Service / Integration | ✅ | ✅ | — | — | — | — | `service.*` / `integration.*` |
| 建 Schedule / Escalation | ✅ | ✅ | — | — | — | — | `schedule.*` / `escalation.*` |
| 建 Runbook / 通知规则 | ✅ | ✅ | — | — | — | — | `runbook.*` / `notification.rule.*` |
| 换班 Override（`schedule.override`）| ✅ | — | — | — | ✅ | — | 📋 **功能整体未实现**（B.5.1）；且持有分布仅 org_admin/oncall，team_admin/lead 均无 |
| 看报表（analytics）| ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ⚠️ 端点**无权限点、无团队 scope 隔离**——全员可见全组织指标（B.11，开放问题 13） |
| 看审计日志（`admin.audit.view`）| ✅ | — | — | — | — | — | 仅 7 种 action 落审计（B.12） |
| **旅程 C** | | | | | | | |
| 看 Incident/Event（`incident.view`/`event.view`）| ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | 团队软隔离（SEC-01）过滤 |
| ack / resolve | ✅ | ✅ | ✅ | ✅ | ✅ | — | `incident.ack` / `incident.resolve` |
| escalate（手动升级）| ✅ | ✅ | ✅ | ✅ | ✅ | — | `incident.escalate` |
| reopen（`incident.reopen`）| ✅ | ✅ | ✅ | ✅ | — | — | oncall/subscriber 无；升级链不重启（C.2） |
| reassign（`incident.reassign`）| ✅ | ✅ | ✅ | — | — | — | 📋 **端点未实现**，权限点悬空（C.3.8） |
| add_responder（`incident.add_responder`）| ✅ | ✅ | ✅ | ✅ | — | — | oncall **无**该权限点；入口仅 IM @机器人 mention（C.3.4） |
| 执行 Runbook（`runbook.execute`）| ✅ | ✅ | ✅ | ✅ | ✅ | — | 写步骤"审批"=请求体自报（C.5.1） |
| 发起/发布复盘（`postmortem.create/publish`）| ✅ | ✅ | ✅ | — | — | — | ⚠️ 起草端点实际**零权限校验**（`postmortem.create` 悬空，任何登录用户可起草/覆盖，C.6） |
| 查看复盘（`postmortem.view`）| ✅ | ✅ | ✅ | ✅ | — | ✅ | **oncall 无**——列表可见、点详情 403（quirk，见 C.6/开放问题 4） |
| 管理 Action Item（`postmortem.actionitem.manage`）| ✅ | ✅ | — | — | — | — | responder_lead 能发布复盘却**不能**管改进项（C.6.2，开放问题 5） |
| 时间线查看（`incident.view`）| ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | 仅此一道校验（C.8） |
| 时间线加备注（`incident.view`）| ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ⚠️ 仅校验 `incident.view`——**只读的 subscriber 也能备注**，且 actor 请求体自报（C.8，开放问题 11） |

---

## 附录 B：API 端点速查（全量，对照 2026-07-03 路由注册核实）

> 权威源：各 `internal/*/handler*.go` 的 `Register` + `wire.go` `registerSensitiveRoutePerms`。
> 「权限」列为**路由级**登记的权限点；「登录态」= 仅需 JWT/API Key，未挂权限点（可能另有 handler 内资源级校验，标注在用途列）；「公开」= 无需登录。
> 所有业务端点前缀 `/api/v1`（下表省略）。❌📋 = 此前文档写过但**实际不存在**的端点，保留作设计目标对照。

**认证与账号**

| 端点 | 权限 | 用途 / 关键行为 |
|------|------|------|
| `POST /auth/login` | 公开 | 登录拿 JWT（暴破防护：10 次/分限流 + 连续 5 次失败锁 5 分钟） |
| `POST /auth/refresh` | 公开 | 换新 access token；⚠️ 不查 `User.status`（禁用用户仍可换票） |
| `GET /auth/me` | 登录态 | 当前用户信息 |
| `POST /auth/change-password` | 登录态 | 本人改密（首登强制改密唯一途径，Web 无改密页，A.4） |
| `GET /users` · `PATCH /users/:id` | 登录态 ⚠️ | 列用户 / 改 name/status/timezone；**未登记权限点**（开放问题 8） |
| `POST /users/:id/im-accounts` | `user.im.bind` | 绑 IM 账号（org_admin 代绑，幂等；无 DELETE，B.9） |
| `GET /users/:id/im-accounts` | 登录态 | 查绑定 |
| `GET /api-keys` · `POST /api-keys` · `DELETE /api-keys/:id` | `admin.apikey.manage` | API Key 生命周期（GET 仅列自己的；token 仅创建时返回一次，F.2） |

**RBAC 与审计**

| 端点 | 权限 | 用途 / 关键行为 |
|------|------|------|
| `GET /roles` | 登录态 | 列角色（含内置角色权限集，复制角色的替代入口） |
| `POST /roles` · `DELETE /roles/:id` | `role.create` / `role.delete` | 建/删自定义角色（无 PATCH；删内置 403；重名/FK 冲突 500） |
| `GET /role-bindings` | 登录态 | 列绑定 |
| `POST /role-bindings` · `DELETE /role-bindings/:id` | `role.assign` | 授权/撤销（临时授权入参 `expires_in_hours`，到期实时失效） |
| `GET /audit-logs` | `admin.audit.view` | 审计查询（`?actor_user_id=&action=&resource_type=&resource_id=&limit=&offset=`，**无时间参数**；仅 7 种 action 落审计，B.12） |

**组织与目录**

| 端点 | 权限 | 用途 / 关键行为 |
|------|------|------|
| `GET /teams` · `POST /teams` · `PATCH /teams/:id` · `DELETE /teams/:id` | GET 登录态；写 `team.create/update/delete` | 团队 CRUD（成员管理端点 📋 未实现，B.2） |
| `GET /services` (+`/:id`) · `POST` · `PATCH /:id` · `DELETE /:id` | GET 登录态；写 `service.create/update/delete` | 服务目录（路由锚点=slug，B.3） |
| `GET /integrations` (+`/:id`) · `POST` · `PATCH /:id` · `DELETE /:id` | GET 登录态；写 `integration.create/update/delete` | 接入源（PATCH 仅 name/enabled；无 token 轮换，B.4） |

**接入（事件入口）**

| 端点 | 权限 | 用途 / 关键行为 |
|------|------|------|
| `POST /webhook/{token}` | Integration token | 告警 webhook 唯一入口（禁用后 401；限流 429 / 背压 503，B.4） |
| ❌📋 `POST /events` | —— | 开放 API 投递（X-Vigil-Key）**不存在**，设计目标（F.1） |
| ❌📋 `POST /integrations/:id/test` | —— | 集成干跑验证**不存在**，设计目标（勿与 `notification-rules/:id/test` 混淆） |

**排班与升级**

| 端点 | 权限 | 用途 / 关键行为 |
|------|------|------|
| `GET /schedules` (+`/:id`) · `POST` · `PATCH /:id` · `DELETE /:id` | GET 登录态；写 `schedule.create/update/delete` | 排班 CRUD（PATCH 不重建 Rotation——改参与人须删除重建，B.5） |
| `GET /schedules/:id/oncall?time=` | 登录态 | 实时查值班（返回 `{schedule_id,schedule_name,layers[]}`） |
| `GET /schedules/:id/preview?days=` | 登录态 | 预览（days≤90，每日取正午） |
| `GET /escalation-policies` (+`/:id`) · `POST` · `PATCH /:id` · `DELETE /:id` | GET 登录态；写 `escalation.create/update/delete` | 升级策略 CRUD（`repeat_times` 策略级，B.6） |

**通知**

| 端点 | 权限 | 用途 / 关键行为 |
|------|------|------|
| `notification-rules` CRUD（GET/POST/GET:id/PATCH/DELETE） | GET 登录态；写 `notification.rule.*` | 通知规则（⚠️ `condition`/`channels` 当前不参与分发，B.7） |
| `POST /notification-rules/:id/test` | 登录态 | dry-run（incident 不存在返 200+error 字段） |
| `notification-templates` CRUD + `POST /:id/preview` | GET 登录态；写 `notification.template.*` | 模板（内置改/删 403；preview 传 incident_id 渲染） |
| `suppression-rules` CRUD | GET 登录态；写 `suppression.*` | 抑制规则（⚠️ `expires_at` 请求体被忽略，B.7） |

**事件处置**

| 端点 | 权限 | 用途 / 关键行为 |
|------|------|------|
| `GET /incidents` | `incident.view`（资源级）+ SEC-01 团队过滤 | 列表（`?status=&severity=&limit=50(≤200)&offset=`） |
| `GET /incidents/:id` | `incident.view`（资源级） | 详情（含 responders/events 关联——入单 Event 明细唯一入口） |
| `POST /incidents/:id/ack` | `incident.ack` | 确认（取消 pending 升级任务；非法转换 400） |
| `POST /incidents/:id/resolve` | `incident.resolve` | 解决（任意活跃态合法，未 ack 也可） |
| `POST /incidents/:id/escalate` | `incident.escalate` | 手动升级（越界 200 幂等；不取消当前层任务） |
| `POST /incidents/:id/reopen` | `incident.reopen` | 重开（升级链**不**重启，C.2） |
| `GET /incidents/:id/timeline` | `incident.view` | 时间线（`?type=&source=&limit=100(≤500)&offset=`；⚠️ total 不吃筛选） |
| `POST /incidents/:id/timeline` | `incident.view` | 加备注（恒 `note_added`；⚠️ actor/source 请求体自报，C.8） |
| ❌📋 `POST /incidents` | —— | 手动建单**不存在**（`trigger_type=manual` 仅枚举，C.3.8） |

**AI**

| 端点 | 权限 | 用途 / 关键行为 |
|------|------|------|
| `POST /incidents/:id/diagnose` | `incident.view` | AI 根因诊断（LLM 未配置/失败 → 200 `{status:disabled}`；201 结果 snake_case） |
| `GET /incidents/:id/similar` | `incident.view` | 相似事件（pgvector 主路径 + LIKE 降级） |
| `GET /incidents/:id/similar-postmortems` | `incident.view` | 相似复盘（仅 published；**无降级**，不可用静默 `[]`；前端无 UI） |
| `POST /ai-insights/:id/resolve` | `incident.view`（反查） | accept/reject（无状态守卫可反复改判；零留痕，C.4.4） |

**Runbook**

| 端点 | 权限 | 用途 / 关键行为 |
|------|------|------|
| `GET /runbooks` (+`/:id`) · `POST` · `PATCH /:id` · `DELETE /:id` | GET 登录态；写 `runbook.create/update/delete` | Runbook CRUD（写步骤强校验 `require_approval=true` 否则 400） |
| `POST /runbooks/:id/execute` | `runbook.execute` | 执行（写步骤需请求体 `approved:true`，否则 skip；无审批流/无并发保护，C.5） |

**复盘**

| 端点 | 权限 | 用途 / 关键行为 |
|------|------|------|
| `GET /postmortems` | 登录态 ⚠️（无权限点，仅 team 可见性过滤） | 列表 |
| `GET /postmortems/:id` | `postmortem.view`（资源级） | 详情（oncall 403 quirk） |
| `POST /incidents/:id/postmortem/draft` | ⚠️ **零校验**（`postmortem.create` 悬空） | 起草（对已存在复盘**直接覆盖**，含 published，C.6） |
| `PATCH /postmortems/:id/transition` | `postmortem.publish` | 状态流转（draft↔in_review→published→archived；送审也要 publish 权限） |
| `DELETE /postmortems/:id` | `postmortem.update` | 删除（级联删 ActionItem；published 也可删） |
| `POST /postmortems/:id/action-items` | `postmortem.actionitem.manage`（资源级） | 加改进项（`due_date` 不收） |
| `PATCH /action-items/:id` · `DELETE /action-items/:id` | `postmortem.actionitem.manage`（资源级） | 改状态/owner/`tracker_url`（reassign 用这条）· 删除 |

**报表**

| 端点 | 权限 | 用途 / 关键行为 |
|------|------|------|
| `GET /analytics/dashboard` · `/alerts` · `/incidents` · `/team-load` · `/postmortems` · `/trend` | 登录态 ⚠️（无权限点、无团队 scope） | 6 个实时聚合端点（dashboard/trend `?days=`，其余 `?start=&end=` RFC3339；响应键 camelCase 已对齐 spec/前端，B.11） |

**IM / 实时 / 系统**

| 端点 | 权限 | 用途 / 关键行为 |
|------|------|------|
| `POST /im/:platform/callback` | 平台签名验签（public 组） | IM 回调（卡片按钮/命令/mention 统一入口；⚠️ 钉钉缺 sign 头跳过校验） |
| `GET /im/platforms` | 公开 | 平台就绪状态（settings 页用） |
| `GET /ws/incidents/:id` | ⚠️ **无鉴权**（public 组） | WebSocket 订阅单个 incident（推 5 事件快照；无 Created/自动升级，C.3.6） |
| `GET /health` | 公开 | 仅 Redis PING + `SELECT 1`（未 migrate 也全绿，A.3） |
| `GET /metrics` | 公开 | Prometheus 指标（清单见 D.5） |
| `GET /docs` · `GET /openapi.yaml` | 公开 | Swagger UI / OpenAPI spec |
| `POST /__test__/reset` | ⚠️ 无鉴权，仅 `APP_ENV≠production` 挂载 | e2e 重置（TRUNCATE 全库+重 seed）——生产务必确认 `VIGIL_APP_ENV=production`（A.3 🚨） |

---

## 附录 C：典型剧本（端到端串联示例，对照当前实现修订）

> 角色：张三（支付/payment oncall+responder）、李四（用户/user）、王五（订单/order team_admin）。
> 每步括注对应章节；📋 处为原设计情节，按当前实现改写。

**剧本 1：半夜支付告警（happy path，按实况修订）**
1. 02:13 Prometheus 探到 payment-api prod 5xx 错误率 >5% → `POST /api/v1/webhook/{token}`（B.4）
2. 分诊（去重/抑制/聚合）→ 路由命中 payment service → `INC-0042 支付服务 5xx 错误率 > 5%`（critical，triggered）（C.1）
3. 继承升级策略 + 张三所在排班解算 → level[0] 通知
4. 飞书**值班群**卡片送达（标题/状态/摘要/层级/负责人 + ack/escalate/resolve/detail 按钮）。⚠️ 卡片**不带 AI 洞察**——`root_cause_hint`/`similar_incident` 仅在 Web 详情页手动点 [AI 诊断] 后可见（C.3.1/C.4.1；📋 设计目标为通知附 AI 摘要）
5. 张三卡片点 [ack] → pending 升级任务取消 → 卡片原地刷新为 acked（**仅飞书**；钉钉卡片不刷新，C.3.3）
6. 张三到 Web 详情页点 [AI 诊断] → `root_cause_hint`："DB 连接池耗尽"（evidence=时间线）→ 到 Web **/runbooks 页**执行「重启连接池」（弹窗手填事件 ID；写步骤勾确认 = 请求体 `approved:true`，无 IM 确认环节，C.5.0/C.5.1）。Runbook 通过 **http 执行器**调 Jenkins 的 webhook 完成重启（jenkins 专用执行器 📋，B.8）
7. 服务恢复 → 张三 [resolve] →（自动起草 📋 未接，C.6.1）张三手动调 `POST /incidents/:id/postmortem/draft` 起复盘草稿
8. 次日评审：draft → in_review → published（`PATCH /postmortems/:id/transition`；⚠️ 无 sections 编辑 API，评审修改只能重新起草覆盖，C.6）。Action Item「扩容连接池」：**手动**在禅道建工单后把链接 `PATCH` 回 `tracker_url`（自动建单 📋，C.6.2）→ published 复盘进入相似检索（C.4.5）

**剧本 2：oncall 没响应，升级兜底（按实况修订）**
1. INC-0050 triggered → level[0] 通知张三（飞书值班群卡片 + 邮件**并联**，C.9）
2. level[0].delay=5 分钟无 ack → 升级任务触发 → status=escalated → 按策略级 `repeat_times` 重复通知当前层（间隔=该层 delay）+ 排入 level[1]（B.6 时间轴）
3. level[1] target=team → 飞书值班群卡片。⚠️ 两处纠偏：①「电话兜底」📋 **不会发生**——phone 通道不在默认通道链，零触发路径（C.9）；②target=team **不解算成员**，邮件对 team 型 target 实际不发——**群卡片是唯一有效触达**（F.3 纠偏/开放问题 14）
4. 李四在群里看到卡片 → `/vigil ack INC-0050` → pending 升级任务取消 → acked（C.3.5）

**剧本 3：跨团队协作（软隔离） — 🟡 反映当前实现**
1. INC-0060 是订单服务故障，王五是 order team_admin，怀疑波及用户服务
2. 王五在群里 @机器人 `@李四` 把李四加入 INC-0060 的 responders（写 `responder_added` 时间线；仅飞书侧可用，钉钉 mention 解析缺失，C.3.4）—— 但李四**默认无操作权限**（软隔离）
3. ⚠️ 纠偏：`role.assign` **仅 org_admin 持有**（seed.go）——王五发不了绑定，需**org_admin** 给李四发 team=order 的 `responder` RoleBinding，入参 `expires_in_hours`（相对小时数，如 8；**不是** `expires_at` 绝对时间，B.2）
4. 李四现在能查看 order 团队的 Incident/Event → 确认用户服务连带影响 → 协助排查
5. INC-0060 resolve → org_admin `DELETE /role-bindings/:id` 撤销，或等到期自动失效（authz 查询实时过滤 `expires_at`，立即生效）

> 设计目标（📋 未实现）：第 3-5 步由"@人 = 自动事件级临时授权 + 关闭自动失效"一键完成（C.3.4）。

**剧本 4：告警风暴聚合（新增，验证分诊三级 + 通知聚合）**
1. 02:00 Prometheus 规则组抖动，一次性群发 **50 条** `service=payment`、`severity=warning` 告警 → webhook 逐条进入（限流阈值内，B.4）
2. 第 1 条 → 建 `INC-0100`；后续 49 条在 **5 分钟窗口**内（窗口锚 `Incident.created_at`）同 service+severity → 并入该单 `alert_count+1`。⚠️ 归并**不写 `event_attached` 时间线**（零写入点）——风暴期间时间线对聚合无感知，归并规模只能看 `GET /incidents/:id` 的 events 关联数（C.1.1/C.8）
3. 其中同 `dedup_key` 的重复推送 → Event **仍落库**（标 `is_noise=true`，action=`dedup_skipped`），Incident 不重复（C.1.1）
4. 通知侧：只发升级策略 level 触发的几条，且非 critical 通知按 target 进 **30s 聚合窗** → 合并为「[聚合] 首条标题（含 N 条聚合通知）」一条消息（**无按钮**；critical 走旁路**不聚合**逐条发，B.7）。IM 聚合卡片 [全部确认] 📋 未实现（C.3.1）
5. 02:06 风暴仍在 → 5 分钟窗口过期 → **第 2 个 Incident**（⚠️ 窗口锚 created_at 固定不滑动——长风暴每 5 分钟裂一个新单，属已知限制）
6. 值班人处置：ack 首单后按 label 建 SuppressionRule 抑制后续（评估点在去重后路由前，B.7）；复盘时用 `GET /analytics/alerts` 看噪音占比（B.11）

---

## 开放问题（待评审）

> 1–6 为产品/设计层遗留问题；7–15 为本次源码核对（v0.3）新增，**文档层记录，修复另行排期**
> （完整依据见 [`audit/journey-code-audit-2026-07-03.md`](./audit/journey-code-audit-2026-07-03.md)）。

1. **首次部署无向导**：当前靠环境变量 + 种子超管，非技术用户上手陡。是否需要 first-run wizard？（PRD H1.1 当前为"3 容器一键"，未提向导）
2. **IM 平台能力差异**：实测矩阵为——飞书全功能；钉钉卡片刷新 no-op（连"降级发新消息"都没有）且 @人解析缺失；企微完全占位（C.3.3）。降级体验需在旅程 C 明确告知用户。
3. **多副本 WebSocket**：当前单实例优先，多副本需 Redis pub/sub 广播，影响旅程 C 的"Web 实时刷新"（D.4）。
4. **复盘可见性**：默认团队内可见，critical 是否公司全员可见（blameless 文化）未定；另有 quirk——**oncall 角色无 `postmortem.view`**，复盘列表可见、点详情 403（C.6/E.6）。
5. **Action Item 跟踪**：`due_date` schema 有但 API 不收、无超期展示/提醒（C.6.2）；且 **responder_lead 能发布复盘却不能管 Action Item**（`actionitem.manage` 仅 org/team_admin），权责错位。
6. **文档不一致**：README 示例 `VIGIL_AUTH_ENABLED=true`，deployment.md §3.4 示例仍为 `false`；deployment.md §7 称"Ingress 模板已提供"实际无该模板（A.3），需对齐。
7. **🚨 Helm chart 不设 `VIGIL_APP_ENV`（最高优先级）**：K8s 部署默认 development → `__test__/reset` 无鉴权可清库 + `X-Vigil-User-ID` 头鉴权旁路（A.3；已列修复任务）。
8. **user 端点权限点悬空**：`GET /users`、`PATCH /users/:id` 未登记权限点——**任何登录用户可禁用/修改他人**（B.1.1）。
9. **禁用 ≠ 吊销**：refresh token（30 天）与 API Key 均不查 `User.status`，禁用用户仍可换票/调 API；改密后旧 token 也不失效——无任何会话吊销机制（B.14/F.2/A.4）。
10. **WS 端点无鉴权**：任意人可订阅任意 incident 的状态快照（C.3.6/F.4）。
11. **写入身份自报三处同源**：复盘起草端点零权限校验且可覆盖 published；`POST timeline` 的 actor/source 由请求体自报可冒充；只读 subscriber 也能加时间线备注（均只有 `incident.view` 或零校验，C.6/C.8/E.5）。
12. **审计断链**：实际落审计仅 7 种 action；IM 越权拒绝不落审计；Runbook 执行人全链路不留痕（时间线/审计/IncidentAction 皆无 who，且 execute 无并发保护）（B.12/C.3.2/C.5.3）。
13. **团队软隔离缺口**：analytics 6 端点无权限点、无团队 scope（全员可见全组织）；subscriber"订阅"机制缺失，实际=进**全局**值班群围观全组织告警（B.11/E.2）。
14. **静默失败家族**：critical unrouted 无兜底通知；自动恢复不写时间线不发事件；quiet_hours 静默通知直接丢弃无补发；IM 值班群未配时通知无日志无计数地丢弃；escalation target=team 不解算成员（邮件/电话对 team 型 target 实际不发，"末级 target=team 兜底"仅群卡片有效）；通知全败无兜底告警（B.13/C.2.1/C.7/C.9/F.3）。
15. **端侧取舍**：移动端 PWA / 值班大屏暂不做（E.7 已记录）；Web 无改密页（首登改密只能 curl）、无时间线备注输入框（A.4/C.8）。
