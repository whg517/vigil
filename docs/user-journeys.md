# 用户旅程与操作流程

> 本文档基于 `PRD.md`（15+2 能力域）、`architecture.md`（6 大引擎）、`data-model.md`（RBAC + 实体）、
> `capabilities/`（能力域详设）、`deployment.md` / `local-dev.md` / `personas.md` 综合整理，
> 描述 Vigil 在三类典型角色下的端到端操作流程。
>
> **阅读对象**：产品/设计/测试/QA，用于评审交互流程、编写测试用例、对照验收标准。
>
> **状态**：Draft v0.2（2026-07-03）。文档为 PRD 的"动起来"投影，需求变更以 PRD 为准。

---

## ⚠️ 实现状态约定（务必先读）

本文档描述的是**目标产品行为**（来自 PRD/架构/能力域设计），**不等于当前代码已全部实现**。
阅读时请注意每节的内联标记，以及下方的功能状态总表：

- ✅ 已实现：代码已落地，可据此编写验收/测试用例。
- 🟡 部分实现：核心逻辑在，但某个编排/触发环节缺失；用例需标注前置条件。
- 🚧 暂不做：设计保留，当前版本明确不做（见下表"备注"）。
- 📋 未实现：PRD 设计目标，未排期，作为 backlog。

### 功能状态总表（对照源码核对，2026-07-03）

| 功能 | 状态 | 说明 / 备注 |
|------|------|-------------|
| 接入 + 归一化流水线 | ✅ | webhook/SMTP/API + Adapter Normalize |
| 分诊三级（dedup/suppression/aggregation） | ✅ | |
| 路由匹配 Service | ✅ | |
| Incident 状态机（5 态） | ✅ | `ent/schema/incident.go` |
| 排班实时 oncall 计算 | ✅ | `schedule/engine.go`，**无 Redis 缓存**（实时算） |
| 升级引擎（Asynq ProcessIn） | ✅ | `escalation/engine.go`，ack 取消 pending 任务已实现 |
| 通知引擎（IM/邮件/电话/webhook） | ✅ | |
| **IM 交互卡片（按权限渲染按钮）** | ✅ | `im/card.go`，含 ack/escalate/resolve/add_responder/detail |
| **IM 操作同链鉴权（非后门）** | ✅ | `im/handler.go` 走同一 Authorizer |
| AI 诊断/分诊/复盘 Copilot（human-in-the-loop） | ✅ | LLM 不可用自动降级 |
| Runbook 两档（readonly 自动 / 写操作人确认） | ✅ | |
| 复盘草稿生成（`GenerateDraft`） | ✅ | 时间线 + AI 填充 + 规则化降级 |
| **复盘 resolve 自动触发起草** | 🟡 | 草稿生成在，**但未接 `IncidentResolved` 事件**，当前需手动调 `POST /incidents/:id/postmortem/draft` |
| **作战室 War Room（M8.2/M8.9/M10.5）** | 🚧 **暂不做** | 现阶段不做，已记录至 [`backlog.md`](./backlog.md)。协同改走 IM 交互卡片 + 状态实时刷新 |
| **跨团队 @人 → 事件级临时授权** | 🟡 | `AddResponder` 把人加入 responders，但**不创建临时 RoleBinding**；被 @人能否操作取决于其已有权限。详见 C.3.4 |
| IM 斜杠命令 | 📋 | 部分命令在，全量待补 |
| 数据报表 / 分析（6 端点） | ✅ | dashboard/alerts/incidents/team-load/postmortems/trend；**无 export 端点** |
| 审计日志查询 | ✅ | `GET /audit-logs`（`admin.audit.view`，仅 org_admin）；无导出 |
| 未路由事件池查看 | ✅ | `event.view_unrouted`；🟡 **无重路由端点**，只能修 Service labels |
| 用户禁用 | ✅ | `user.disable`；🟡 **无自动交接提示**，须手动处理排班/Action Item |
| 备份脚本（`scripts/backup.sh`） | ✅ | PG pg_dump + Redis BGSAVE；脚本本身不轮转 |
| 恢复脚本（`scripts/restore.sh`） | ✅ | 含 Redis 丢失场景的升级计时器处置 |
| migrate-down / 回滚 | ❌ | 无；回滚靠备份恢复 |

> 用作验收依据前，🚧/🟡/📋 项需在用例中显式标注前置条件或排除范围。

---

## 0. 角色与全景

### 0.1 三类角色（旅程主线）

| 旅程 | 主角色 | 角色定位 | 主要权限点 | 主战场 |
|------|--------|----------|------------|--------|
| **A 首次部署** | 平台运维 / SRE Lead | 把系统跑起来、初始化超管 | （系统级，无业务权限点） | 宿主机 / K8s / 终端 |
| **B 管理员配置** | `org_admin` + `team_admin` | 把组织结构、服务、排班、升级、通知、Runbook 配好 + 报表/审计/分诊/交接 | `team.*` / `service.*` / `schedule.*` / `escalation.*` / `runbook.create` / `integration.*` / `role.*` / `admin.audit.view` | Web 控制台 |
| **C 告警处置** | `responder` / `oncall` | 半夜被叫醒 → 在 IM 内 ack / 诊断 / 处置 / 解决 / 复盘 | `incident.*` / `event.view` / `runbook.execute` / `postmortem.*` | **IM（首选）** + Web（补充） |
| **D 运维保障** | 平台运维 / SRE Lead | 长期运行：升级/迁移/备份/灾难恢复 | （系统级，无业务权限点） | 宿主机 / K8s / 终端 |

> 旁路角色：`subscriber`（团队 Leader，只读订阅）、`responder_lead`（可 reassign + 发起复盘）。
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

### A.3 K8s + Helm（生产路径）

```
1. 准备 Secret（不放明文进 chart）：
   kubectl create secret generic vigil-secrets \
       --from-literal=db-password=<...> \
       --from-literal=redis-password=<...> \
       --from-literal=jwt-secret=<...>
2. helm install vigil ./deploy/helm -f values-prod.yaml
3. （DB/Redis 用外部托管实例，或 chart 内 subchart 仅用于 dev/test）
4. 配 Ingress + HTTPS 终止（nginx/Caddy/Traefik）
```

**生产安全加固（chart 已内置，SEC-05）**：`runAsNonRoot`、UID/GID 65532、`readOnlyRootFilesystem: true`、drop 所有 capabilities、seccomp `RuntimeDefault`、仅 `/tmp` 可写。
**多副本**：API 按 QPS 横扩、Worker 按队列深度横扩；多副本 WebSocket 广播需 Redis pub/sub（当前单实例优先）。

### A.4 初始化超管（自动种子，无向导）

启动时 `internal/server/wire.go` 自动执行（幂等）：

```
1. SeedBuiltinRoles           # 种内置角色：org_admin / team_admin / responder /
                              #   responder_lead / subscriber / oncall
2. 初始化 JWT 签名器          # 读 VIGIL_AUTH_JWT_SECRET；未设 → 登录被禁用 + 告警日志
3. auth.SeedDefaultAdmin      # 仅当 JWT 可用：
                              #   username=admin / password=changeme /
                              #   email=admin@vigil.local
                              #   绑 org_admin 角色（org scope，FIX-A）
                              #   must_change_password=true
```

**首次登录强制改密**：种子超管 `must_change_password=true`，在改密前 `RequireUser` 中间件拦截所有业务 API，杜绝 `admin/changeme` 长期可用（审计项 H1.6 / C8）。

### A.5 接入外部依赖（可选，可后补）

| 依赖类 | 环境变量 | 不配的后果 |
|--------|----------|------------|
| LLM（智谱 GLM） | `VIGIL_LLM_API_KEY` / `_MODEL` / `_BASE_URL` + cost 控制 `_COST_*` | AI 降级为规则化草稿，诊断跳过；告警主流程不受影响 |
| IM（飞书） | `VIGIL_IM_FEISHU_APP_ID/_SECRET/_TOKEN/_ENCRYPT_KEY` | 该平台不发卡片；通知走兜底通道 |
| IM（钉钉） | `VIGIL_IM_DINGTALK_APP_KEY/_SECRET/_ROBOT_CODE/_TOKEN/_AES_KEY` | 同上 |
| IM 目标群 | `VIGIL_IM_ONCALL_CHANNEL` | 告警卡片无处投递 |
| 邮件 | `VIGIL_NOTIFICATION_SMTP_HOST/_PORT/_USER/_PASS/_FROM` | 邮件通道禁用 |
| 电话/短信 | `VIGIL_NOTIFICATION_PHONE_WEBHOOK_URL` / `_SMS_` | 升级兜底不可用 |
| Webhook 出站 | `VIGIL_WEBHOOK_OUT_URLS` | 不向外部系统推送 Incident 生命周期 |
| 限流/背压 | `VIGIL_INGESTION_RATE_LIMIT_PER_MIN` / `_BACKPRESSURE_DEPTH` | 无 Redis 时退化为放行 |

> 设计原则：**所有外部依赖都"优雅降级"** —— 不配 LLM 不会让告警断流，不配 IM 会走电话/邮件兜底。

### A.6 验收清单（部署完成判据）

- [ ] `curl /health` 返回 200
- [ ] `SELECT extversion FROM pg_extension WHERE extname='vector';` 有结果（pgvector 装好）
- [ ] 浏览器能登录 admin/changeme 并被强制改密
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

| 步骤 | 操作 | 权限点 | 产出 |
|------|------|--------|------|
| 1 | 建用户（或对接 SSO/LDAP，若已集成） | `user.create` | `User` 实体 |
| 2 | 建自定义 Role（按需组合权限点，或拷贝内置） | `role.create` | `Role`（scope_level=org/team） |
| 3 | 创建 API Key（供外部系统调用） | `admin.apikey.manage` | `APIKey`（明文仅显示一次） |
| 4 | 配全局 Integration 凭据（如多个团队共用一套 Prometheus token 池） | `admin.global_integration` | 全局集成配置 |

### B.2 团队与权限（team_admin）

> 触发权限点：`team.*` / `role.assign` / `team.member.manage`

```
1. 建 Team（可设 parent_team_id，但仅展示用，权限不继承）
2. 加成员（Member 关联 User ↔ Team）
3. 建/选 Role（team scope）
4. 发 RoleBinding：
   - level=team, team_id=<X>
   - 可选 expires_at（临时授权，如临时顶替 team_admin）
5. 鉴权链路（运行时）：action→permission_code → resource→scope
   → 查 User 的 org + team RoleBinding → 并集权限点 → 判断 code ∈ 集合
```

### B.3 服务目录（team_admin，★ 配置枢纽）

> 触发权限点：`service.create` / `service.update`

| 步骤 | 操作 | 关键字段 |
|------|------|----------|
| 1 | 建 Service，归属某 Team | `team_id`、`name` |
| 2 | 设 `labels`（路由匹配锚点，如 `service=payment, env=prod`） | `labels`（精确 + glob） |
| 3 | 绑 `escalation_policy_id` | 关联升级策略 |
| 4 | 关联 `schedule_ids` | 关联排班（可多个） |
| 5 | 绑 `runbook_ids` | 关联诊断/处置手册 |
| 6 | 设 `auto_create_incident`（critical 默认自动建） | 是否自动提升为 Incident |

> ⚠️ labels 是路由命脉：配错 → Event 落 `unrouted` 池（需 `event.view_unrouted` 才能看到），critical 落 unrouted 会兜底通知全员/admin。

### B.4 接入源 Integration（team_admin / org_admin）

> 触发权限点：`integration.create` / `integration.update`

```
1. 选类型（Prometheus / Zabbix / Grafana / 云厂商 / Email / Generic JSON）
2. 设 token（webhook URL 鉴权用：路径段或 Authorization 头）
3. 设 rate_limit（防某源刷屏）
4. 绑默认 service_id（跳过标签匹配，直达）
5. 配 severity 映射覆盖（按需）
6. POST /:id/test 干跑一次验证
```

**鉴权模型**：
- Webhook：per-Integration token
- Email：地址 + 发件白名单（可选 DKIM/SPF）
- 开放 API：API Key（`X-Vigil-Key`），归 `org_admin` 管

### B.5 排班 Schedule（team_admin）

> 触发权限点：`schedule.create` / `schedule.update`

| 步骤 | 操作 | 字段 |
|------|------|------|
| 1 | 建 Schedule，选类型 | `calendar` / `rotation` / `follow_the_sun` |
| 2 | 设时区（每个 Schedule 独立时区，跨时区团队各自算） | `timezone` |
| 3 | 配层（primary / secondary） | layers |
| 4 | 配 Rotation 规则 | `participants`、`shift_length`(如 24h)、`handoff_time`(如 09:00)、`rotation_type`、`start_date`、`end_date` |
| 5 | 预览未来 N 天 | `GET /schedules/{id}/preview?days=14` |

**实时查询值班**：`GET /schedules/{id}/oncall?time=<iso8601>` → `{primary, secondary, overrides}`。
> 注：代码 `schedule/engine.go` 的 `OncallNow` 当前**无 Redis 缓存**（`// TODO: Redis 缓存`），每次实时计算。这与"永远实时算"一致；设计目标的"分钟级缓存"未实现。

**换班 Override**（`oncall` 用户自助，权限 `schedule.override`）：`{user_id, start, end, reason}`，覆盖窗口内完全顶替 Rotation。

**空班兜底**：检测到空班 → 告警 `team_admin`。

### B.6 升级策略 EscalationPolicy（team_admin）

> 触发权限点：`escalation.create` / `escalation.update`

```
按 ordered levels 配置，每层：
- delay_minutes     # 本层等待 ack 的时长
- targets           # schedule / user / team（可多类，并集去重）
- notify_channels   # IM / 电话 / 短信 / 邮件 / webhook
- repeat_times      # 本层重复通知次数
末层通常是"全团队 + 多通道"兜底，保证一定有人响应。
```

**与排班联动**：target.type=schedule 时，每次升级实时解算值班人 —— 排班变了，下一次升级立刻跟上。

### B.7 通知规则与模板（team_admin）

> 触发权限点：`notification.rule.update`

| 项 | 说明 |
|----|------|
| NotificationRule | `{name, condition(severity/team/service), channels, template_id, quiet_hours}` |
| quiet_hours | 如 22:00–07:00 抑制非 critical；critical 透传（`bypass_for:[critical]`） |
| Template | Go 模板，变量 `{{.Severity}}`/`{{.Service.Name}}`/`{{.ActionURL}}`；启动时幂等种 3 个内置模板 |
| SuppressionRule | 维护窗/已知问题 → `suppress`（标 noise）或 `reduce_severity` |
| 聚合 | 30s 窗口同目标合并，防轰炸；critical 透传 |

### B.8 Runbook（team_admin）

> 触发权限点：`runbook.create` / `runbook.update`

| 类型 | 触发 | 执行 | 安全 |
|------|------|------|------|
| `document` | 展示给人看 | 不执行 | 无风险 |
| `executable`（diagnose） | manual/on_incident/on_severity/on_label_match | readonly → Vigil 直接跑 | 默认安全 |
| `executable`（remediation） | 同上 | 写操作 → 生成指令 + `require_approval:true`（人确认） | **绝不**直接动生产 |

**步骤结构**：`steps[].action.type`（diagnose/execute/notify/wait/approve）、`target.kind`（http/ansible/jenkins/internal）、`readonly`、`require_approval`、`on_failure`（continue/abort/escalate）。

**Executor 凭据**（Ansible/Jenkins token）由管理员加密托管。

### B.9 用户绑 IM 账号（每个 oncall 用户自助）

> 触发权限点：`user.im.bind`

**这是旅程 C 能在 IM 操作的前提**：每个 User 绑定 `im_accounts[platform].account_id`（IM unionId）。未绑定用户在 IM 点按钮会被拒（提示去 Web 绑定）。

### B.10 验收：发一条测试告警走通全链路

```
1. 在已配 Integration 用 curl 模拟一条 critical 告警（带 service labels）
2. 观察：
   - raw_event 落库
   - normalize 出 Event
   - dedup/suppression/aggregation 出 Incident（status=triggered）
   - route 命中 Service
   - 排班引擎算出当前 oncall
   - 升级计时器启动（Asynq ProcessIn）
   - 通知引擎投递 IM 卡片到 oncall
3. oncall 在 IM 点 ack → 状态 acked，升级任务全部取消，卡片实时刷新
4. 标记 resolved →（🟡 自动起草未接）手动调 `POST /incidents/:id/postmortem/draft` 起复盘草稿
```

这条链路通了，旅程 B 才算交付完成。

### B.11 数据报表与分析（能力域 15）

> 触发权限点：登录即可访问（无独立报表权限点；数据按团队 scope 隔离 —— 只看到自己团队）

**角色**：`team_admin` / `responder_lead` / `subscriber`（团队 Leader 看团队全貌）
**端点**（均 ✅ 已实现，`internal/analytics/handler.go`）：

| 端点 | 内容 | 关键指标 |
|------|------|----------|
| `GET /analytics/dashboard?days=7` | 仪表盘汇总 | 综合概览 |
| `GET /analytics/alerts` | 告警度量 | 告警量、降噪率、unrouted 数 |
| `GET /analytics/incidents` | 事件度量 | MTTA、MTTR、severity 分布 |
| `GET /analytics/team-load` | 团队负载 | oncall 次数、**夜间打扰次数**、人均事件数 |
| `GET /analytics/postmortems` | 复盘度量 | 完成率、Action Item 闭环率 |
| `GET /analytics/trend?days=7` | 趋势 | 时间序列（变好/变差） |

**操作流程**：
```
1. 进报表页 → 选时间窗（默认 7 天）+ 团队
2. 看仪表盘汇总 → 下钻具体维度
3. 重点看：
   - 降噪率 = 1 − (已通知 Event 数 / 原始 Event 数) —— 验证分诊效果
   - MTTA / MTTR —— 验证响应速度
   - 夜间打扰次数（quiet_hours 内通知到 oncall 的次数）—— 验证"少打扰"目标
   - 复盘完成率 + Action Item 超期数 —— 验证闭环质量
4. 📋 导出：当前**无 export 端点**（backlog），需导出时走数据库直查或后续补端点
```

> 关键定义：MTTA = created → first ack；MTTR = created → resolved；夜间打扰 = quiet_hours 内通知到 oncall 的次数。

### B.12 审计调查（能力域 13 M13.5）

> 触发权限点：`admin.audit.view`（**仅 `org_admin`**，见附录 A）

**角色**：`org_admin`（合规/追责场景）
**端点**（✅ `GET /audit-logs`，`internal/auth/handler_audit.go`）：

```
GET /audit-logs?actor=<user_id>&action=<type>&object=<type>&from=<ts>&to=<ts>
```

支持按 **操作者 / 操作类型 / 对象类型 / 时间** 筛选，分页返回。

**两类审计**：
| 类 | 内容 | 字段 |
|----|------|------|
| 管理审计 | 角色变更、Integration token、用户禁用、配置变更 | actor / action / target / time |
| 操作审计 | 每个 IncidentAction | actor / **via**(web/im/api/automation) / action / time |

**典型调查流程**：
```
1. 起因：某 Incident 被误 resolve / 某权限被不当授予
2. 进审计页 → 按时间窗 + 对象类型筛选
3. 定位条目 → 看 actor（谁）+ via（从哪里操作的）
4. via 字段的价值：统计"多少操作发生在 IM 内" —— 验证 IM-first 落地效果
5. 📋 导出：当前无导出端点（backlog），截图或数据库直查
```

> 注意：`team_admin` **看不了审计日志**（无 `admin.audit.view`）。团队内合规问题须上报 org_admin。

### B.13 未路由事件分诊（能力域 4 M4.3） — 🟡 部分实现

> 触发权限点：`event.view_unrouted`

**背景**：Event 的 labels 匹配不到任何 Service → 落 `unrouted` 池（`triage/engine.go` 标记 `ActionUnrouted`，Event.service_id 留空）。**critical 落 unrouted 会兜底通知全员/admin**，是顶级运维痛点。

**操作流程**：
```
1. team_admin 进 unrouted 池（需 event.view_unrouted 权限）
2. 检查每条未路由 Event：
   ├─ 看原始 labels（Event.detail）
   ├─ 判断属于哪个 Service
   └─ 找根因：Service labels 配错？新服务未登记？源系统 label 不全？
3. 🟡 当前实现：无"重路由/改派"端点 —— 处置方式是：
   ├─ 修 Service labels（补匹配规则）→ 后续相同 Event 会命中
   └─ 当前这批 unrouted Event 不会被回溯路由，等新 Event 进来或人工建 Incident 关联
4. 若是噪声 → 加 SuppressionRule（见 B.7）防再次打扰
```

**预防**：新接入 Integration 时，先用 `POST /integrations/:id/test`（见 B.4）干跑验证 labels 命中，避免上线后才发现 unrouted。

> 📋 backlog：补"unrouted Event 重路由"操作端点（标 service_id 或合并到现有 Incident）。

### B.14 用户禁用与交接（能力域 13 M13.1） — 🟡 部分实现

> 触发权限点：`user.disable`

**角色**：`team_admin` / `org_admin`（员工离职/转岗场景）

**操作流程**：
```
1. 禁用用户：PATCH 用户 status=disabled
   ├─ ✅ 已实现：用户不能登录，历史保留
   └─ 🟡 当前实现：仅置标志，无自动交接
2. 手动交接（🟡 全靠管理员手动，系统不提示）：
   ├─ 排班：从所有 Rotation.participants 移除，或建 Override 覆盖其班次
   │   ⚠️ 不移除会留空班 → 空班检测会告警 team_admin
   ├─ Action Item：该用户 owner 的 postmortem action_item 须 reassign
   │   （复盘发布前的 open item，不交接会超期高亮）
   ├─ 角色：其临时 RoleBinding（expires_at 未到）手动撤销
   └─ IM 绑定：im_accounts 不自动清，建议手动解绑
3. 验证：进 B.11 报表看团队负载，确认无遗漏班次；进审计看交接记录
```

> ⚠️ 设计目标（📋 backlog）：禁用用户时**提示**待交接项（capability 09-admin-rbac.md L33 已写），当前不提示，管理员须自查。
> **建议**：离职场景前先做 step 2 再禁用，避免留空班/超期 item。

---

## 旅程 C：值班工程师告警处置全流程

**主角色**：`responder` / `oncall`（团队 scope）
**目标**：半夜被叫醒 → 在 IM 内**不切系统**完成 ack / 诊断 / 处置 / 解决 / 复盘。
**特点**：IM 首选（Web 兜底）、高频、强时间压力、每个动作都写时间线 + 审计。
**设计哲学**：半夜能用 · 一屏决策 · 降噪优先 · 状态可见。

### C.1 端到端时序（13 步，对应 architecture §4.1）

```
① Prometheus 触发告警
   └─ POST /webhook/prometheus/{token}
② Ingress 校验 token → 入队 → 返回 202（秒级，收发解耦）
   └─ raw payload 落 raw_event 表（保底，可重放）
③ Worker: normalize → Event 入 PostgreSQL
④ Worker: 分诊三级
   ├─ dedup（Redis dedup_key，5min 窗）
   ├─ suppression（规则或 AI → is_noise）
   └─ correlation aggregation（service+severity 窗口内 → 一个 Incident）
⑤ Worker: route 匹配 Service（labels 精确+glob）
   └─ 命中 → Incident 继承 escalation_policy + schedule + runbooks
   └─ 未命中 → unrouted 池（critical 兜底通知全员）
⑥ Incident → status=triggered → 入队升级延迟任务（asynq.ProcessIn）
⑦ 升级引擎到点触发 → 排班引擎实时算 oncall → 通知引擎分发
⑧ IM 卡片送达 oncall（带 ack/升级/详情 按钮，按权限渲染）
⑨ 工程师点 [ack] → IM 层：unionId→User→鉴权→核心服务 ack
⑩ ack 取消该 Incident 所有后续升级 + 通知任务 → status=acked → 时间线
⑪ 处置：展示 Runbook / 诊断执行 / 处置(人确认或外接)
⑫ 标记 resolved →（🟡 当前需手动调 `POST /incidents/:id/postmortem/draft` 起草；📋 设计目标：critical 自动触发）→ 人评审 → 发布
⑬ 闭环：复盘入知识库 → 反哺相似 Incident 检索（下次更快）
```

### C.2 Incident 状态机（运行时核心对象）

```
                ack in time
   triggered ─────────────────▶ acked ──mark resolved──▶ resolved ──PM done──▶ closed
       │                                                       ▲
       │ timeout, no ack                                       │
       └──────────────▶ escalated ─────ack─────────────────────┘
```

| 状态 | 进入 | 退出 | 含义 |
|------|------|------|------|
| `triggered` | Event 提升为 Incident | 被 ack / 超时 | 新建，等待响应 |
| `escalated` | 升级计时器超时 | 被 ack | 已升级，仍未响应 |
| `acked` | 任意层级 ack | 标记 resolved | 有人接手 |
| `resolved` | 用户标记 | 复盘完成 | 已解决，等复盘 |
| `closed` | 复盘完成/跳过 | — | 终态 |

**铁律**：每次状态变更**必须**产 TimelineItem。

### C.3 IM 操作详情（核心差异化）

#### C.3.1 交互卡片（M8.1）

告警通知渲染为卡片，按钮**按权限渲染**（无权限不显示）：

| 按钮 | 权限点 | 动作 |
|------|--------|------|
| 确认/ack | `incident.ack` | 取消后续升级，状态→acked |
| 升级/escalate | `incident.escalate` | 立即跳到下一升级层 |
| 解决/resolve | `incident.resolve` | 状态→resolved |
| 拉人/add_responder | `incident.add_responder` | 把 @人 加入 responders（见 C.3.4，🟡 不自动授权） |
| 详情/detail | `incident.view` | 跳 Web 详情 |

#### C.3.2 IM 鉴权链路（与 Web 完全相同，关键设计）

```
IM 按钮点击
   └─ webhook 回调（platform + unionid + action）
       └─ 查 User.im_accounts[platform].account_id
           └─ 映射到 User
               └─ action → permission_code（如 incident.escalate）
                   └─ 查 RoleBindings（incident.team_id scope）
                       └─ 并集权限点 → 判断 code ∈ 集合
                           ├─ 允许 → 执行 → 更新卡片 → 时间线
                           └─ 拒绝 → 返回"无权限" + 审计
```

> ⚠️ IM **不是**权限后门。IM 操作与 Web 走**同一条**鉴权链。未绑 IM 账号的用户被拒并提示去 Web 绑定。

#### C.3.3 卡片实时刷新（M8.4）

状态变更 → 通过 IM 平台 card-update API 原地刷新卡片，群内所有人看到一致状态。
- 飞书：完全支持
- 钉钉：部分支持
- 企微：不支持 → 降级为"发新消息标注最新状态"

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

#### C.3.5 斜杠命令（M8.5）

```
/vigil ack <id>
/vigil escalate <id>
/vigil resolve <id>
/vigil add @人 <id>
/vigil runbook <name> <id>
/vigil status <id>
/vigil oncall
```
每个命令都受对应权限点约束。

### C.4 AI Copilot（贯穿，human-in-the-loop）

| 阶段 | AIInsight 类型 | 作用 | 人确认 |
|------|----------------|------|--------|
| 分诊 | `dedup_suggestion` / `severity_adjustment` / 噪声学习 | 建议合并/降级/标噪 | accept→merge/降级；reject→记录用于改 AI |
| 诊断 | `root_cause_hint`（引日志/指标/变更）/ `similar_incident` | 给根因线索 + 历史相似 | accept→采纳；evidence 必备，无 evidence 不展示 |
| Copilot | 建议用哪个 Runbook / `draft_summary` | 推荐处置 + 草拟摘要 | accept→应用 |
| 复盘 | `postmortem_draft` | 草拟复盘各段 | accept→填入 |

**降级**：LLM 挂了 → AI 自动 off，告警主流程不中断；CostController 三道闸（Redis 缓存 → 限流 → 配额），无 Redis 时全透明放行。

### C.5 Runbook 执行（两档安全）

```
执行到某 step：
├─ readonly==true → Executor 直接跑（HTTP/internal diagnose）
│                   → 成功写时间线 runbook_executed
│
└─ readonly==false（写操作）
    ├─ require_approval==true（默认）→ 必须人确认（IM/Web 弹窗）
    │                                 ├─ confirm → 跑 → 时间线
    │                                 └─ deny    → skip/abort
    └─ require_approval==false（仅高度可信，admin 显式配）→ 直接跑

失败 → on_failure: continue / abort / escalate
每步都作为 IncidentAction 审计
```

### C.6 复盘闭环（Domain 8） — 🟡 草稿生成在，自动触发未接

**当前行为**：`GenerateDraft`（时间线自动填充 + AI 草拟 summary/impact/root_cause + LLM 不可用时规则化降级）已实现，但**只暴露为手动端点** `POST /incidents/:id/postmortem/draft`，未接 `IncidentResolved` 事件。

```
Incident resolved
   └─ 📋 设计目标（未实现）：按 severity 自动触发起草（critical 强制；warning 可配；info 不强制）
   └─ 🟡 当前：用户在 Web 手动点"起草"或调 API
       └─ 起草稿：时间线（自动）+ AI postmortem_draft（填 summary/impact/root_cause）
           └─ 状态: draft
               └─ 人评审（每段标"AI 草拟"，可 accept/edit/reject）
                   └─ in_review
                       └─ published
                           ├─ Action Items: {owner, due_date, status, tracker_url}
                           │   └─ 发布时自动建 Jira/禅道工单（Domain 14）
                           └─ 入知识库 → 反哺相似 Incident 检索
                               └─ archived
```

**关键指标**：复盘完成率、Action Item 闭环率、超期高亮。

### C.7 降级分支（当某环节不可用）

| 故障 | 降级路径 |
|------|----------|
| LLM 挂 | AI off，主流程继续；复盘用规则化草稿 |
| IM 平台挂 | 通知引擎走电话/邮件/webhook 兜底 |
| 卡片无法原地更新 | 降级发新消息标注最新状态（企微路径） |
| Redis 挂 | 限流/cost 控制放行；缓存失效但不阻断 |
| 排班空班 | 告警 team_admin；升级末层兜底全员 |
| Event 未路由 | 落 unrouted 池；critical 兜底通知全员/admin |
| 写 Runbook 失败 | `on_failure: escalate` → 自动升级 |

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

---

## 附录 A：权限矩阵（旅程 × 动作 × 权限点）

> 内置角色：`org_admin`(org) / `team_admin`(team) / `responder`(team) / `responder_lead`(team) / `subscriber`(team) / `oncall`(team)。
> ✅=有 · —=无。权限以 `internal/auth/seed.go` 的 `builtinRoles` 为权威来源（本表对照 2026-07-03 代码）。

| 动作 | org_admin | team_admin | responder_lead | responder | oncall | subscriber |
|------|:---:|:---:|:---:|:---:|:---:|:---:|
| **旅程 A** | | | | | | |
| 部署/migrate | —（系统级）| — | — | — | — | — |
| 改超管密码 | ✅ | — | — | — | — | — |
| **旅程 B** | | | | | | |
| 建用户/角色/APIKey | ✅ | — | — | — | — | — |
| 建团队/加成员 | ✅ | ✅ | — | — | — | — |
| 建 Service / Integration | ✅ | ✅ | — | — | — | — |
| 建 Schedule / Escalation | ✅ | ✅ | — | — | — | — |
| 建 Runbook / 通知规则 | ✅ | ✅ | — | — | — | — |
| 看报表（analytics）| ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| 看审计日志（`admin.audit.view`）| ✅ | — | — | — | — | — |
| 禁用用户（`user.disable`）| ✅ | — | — | — | — | — |
| 自己换班 Override | ✅ | ✅ | ✅ | — | ✅(仅自己) | — |
| **旅程 C** | | | | | | |
| 看 Incident/Event | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| ack / resolve | ✅ | ✅ | ✅ | ✅ | ✅ | — |
| escalate（手动升级）| ✅ | ✅ | ✅ | ✅ | ✅ | — |
| reassign | ✅ | ✅ | ✅ | — | — | — |
| add_responder（拉人入 responders）| ✅ | ✅ | ✅ | ✅ | ✅ | — |
| 执行 Runbook | ✅ | ✅ | ✅ | ✅ | ✅ | — |
| 发起/发布复盘 | ✅ | ✅ | ✅ | — | — | — |
| 看审计日志（`admin.audit.view`）| ✅ | — | — | — | — | — |

---

## 附录 B：关键 API 端点速查

| 域 | 端点 | 用途 |
|----|------|------|
| 接入 | `POST /api/v1/webhook/{integration_id}` | 告警 webhook |
| 接入 | `POST /api/v1/events` | 开放 API 投递（X-Vigil-Key） |
| 排班 | `GET /api/v1/schedules/{id}/oncall?time=` | 实时查值班 |
| 排班 | `GET /api/v1/schedules/{id}/preview?days=14` | 预览排班 |
| 鉴权 | `POST /api/v1/auth/login` | 登录拿 JWT |
| 鉴权 | `POST /api/v1/auth/change-password` | 首次改密 |
| 集成测试 | `POST /api/v1/integrations/:id/test` | 干跑验证 |
| 模板 | `GET/POST /api/v1/notification-templates` | 通知模板 CRUD |
| 模板 | `POST /api/v1/notification-templates/:id/preview` | 模板预览 |
| 角色 | `POST /api/v1/roles` / `POST /api/v1/role-bindings` | RBAC 自配置 |
| 实时 | `WS /api/v1/...` | 状态/时间线/通知实时推 |
| 健康 | `GET /health` · `GET /metrics` | 健康检查 + Prometheus |

---

## 附录 C：典型剧本（端到端串联示例）

> 角色：张三（支付/payment oncall）、李四（用户/user）、王五（订单/order team_admin）。

**剧本 1：半夜支付告警（happy path）**
1. 02:13 Prometheus 探到 payment-api prod 5xx 错误率 >5% → webhook
2. 分诊聚合 → `INC-0042 支付服务 5xx 错误率 > 5%`（critical，triggered）
3. 路由命中 payment service → 继承升级策略 + 张三所在排班
4. 升级 level[0] 通知 → 飞书卡片送达张三，附 AI `root_cause_hint`："DB 连接池耗尽 78%"（引慢查询日志），`similar_incident`：INC-0035
5. 张三卡片点 [ack] → 升级任务取消 → 卡片实时刷新为 acked 状态，群内可见
6. 张三 `/vigil runbook restart-pool INC-0042` → 诊断步骤 readonly 自动跑；处置步骤弹确认 → 张三确认 → Jenkins 重启连接池
7. 服务恢复 → 张三 [resolve] →（🟡 自动起草未接）张三手动调 `POST /incidents/INC-0042/postmortem/draft` 起复盘草稿
8. 次日张三评审 AI 草稿 → in_review → published → Action Item "扩容连接池"建禅道工单 → 入知识库

**剧本 2：oncall 没响应，升级兜底**
1. INC-0050 triggered → level[0] 通知张三（飞书卡片）
2. 5 分钟无 ack → level[0] 计时器超时 → status=escalated → 重复通知 + 启动 level[1]
3. level[1] target=team → 通知全 payment 团队 + 电话兜底
4. 李四（backup）电话接 → 在 IM `/vigil ack INC-0050` → 升级任务全取消 → acked

**剧本 3：跨团队协作（软隔离） — 🟡 反映当前实现**
1. INC-0060 是订单服务故障，王五是 order team_admin，怀疑波及用户服务
2. 王五 `@李四` 把李四加入 INC-0060 的 responders（写时间线）—— 但李四**默认无操作权限**（软隔离）
3. 王五（有 `role.assign`）临时给李四发 team=order 的 `responder` RoleBinding（设 `expires_at`=当晚 23:59）
4. 李四现在能查看 order 团队的 Incident/Event → 确认用户服务连带影响 → 协助排查
5. INC-0060 resolve → 王五（或李四自己）撤销临时 RoleBinding，或等 `expires_at` 自动失效

> 设计目标（📋 未实现）：第 3-5 步由"@人 = 自动事件级临时授权 + 关闭自动失效"一键完成。

---

## 开放问题（待评审）

1. **首次部署无向导**：当前靠环境变量 + 种子超管，非技术用户上手陡。是否需要 first-run wizard？（PRD H1.1 当前为"3 容器一键"，未提向导）
2. **IM 平台能力差异**：飞书卡片可原地更新，企微不行。降级体验需在旅程 C 明确告知用户。
3. **多副本 WebSocket**：当前单实例优先，多副本需 Redis pub/sub 广播，影响旅程 C 的"Web 实时刷新"。
4. **复盘可见性**：默认团队内可见，critical 是否公司全员可见（blameless 文化）未定。
5. **Action Item 超期**：当前仅报告高亮，是否自动提醒 owner/team_admin 未定。
6. **文档不一致**：README 示例 `VIGIL_AUTH_ENABLED=true`，deployment.md §3.4 示例仍为 `false`，需对齐。
