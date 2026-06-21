# Vigil 需求缺口与技术债务分析

| 字段 | 内容 |
|------|------|
| **文档版本** | v0.1（讨论草案） |
| **创建日期** | 2026-06-21 |
| **状态** | Draft —— 用于制定后续实现任务，待讨论收敛 |
| **关联** | [`PRD.md`](./PRD.md)（15+2 能力域）、各 [`capabilities/`](./capabilities/) |
| **评估范围** | `internal/` + `web/` + `ent/schema/` 对照 PRD 全量需求 |

> 本文档逐域核对 PRD/能力域设计文档与实际代码（含 TODO 标记、占位实现、实体缺失），
> 产出**未实现功能缺口**与**非功能性需求缺口**，作为后续制定实现任务的依据。
> 代码中部分"降级/占位"是设计内的优雅降级（如无 Redis 跳过缓存），不列为本报告的硬缺口。

---

## 0. 总览结论

主链路（接入→分诊→路由→升级→通知→IM 协同→复盘）已跑通，但**六块明显欠账**：

1. **安全**（鉴权可伪造、无 API Key/审计/加密）
2. **告警源广度**（适配器只做 2/6）
3. **通知通道广度**（邮件/电话/SMS 缺位或占位）
4. **治理审计**（API Key、AuditLog、知识沉淀实体缺失）
5. **可观测实时性**（无 WebSocket，吃自己狗粮闭环未闭）
6. **生产部署**（无 Helm Chart、无备份脚本）

---

## 一、功能性需求缺口（按能力域）

### 🟠 域 1 接入：告警源适配器 1/2（范围已收敛）

**澄清（v0.2 修正）**：Integration/RawEvent 实体**已存在**（`ent/schema/service.go` Integration、`event.go` RawEvent），
且 `Integration.type` 枚举已预留 `grafana/zabbix/cloud/email/api`，token 标 `.Sensitive()`。
所以接入层**数据模型已就位**，只差适配器实现 + 接入管理前端。

**范围收敛**：云消息告警源（阿里云/腾讯云/AWS）+ 邮件接入（SMTP）**移入 `TODO.md` 未来实现**，
本期只补 **Grafana 适配器**（PRD M1.2）。

`internal/ingestion/adapter.go:57`：
```go
r.Register(&PrometheusAdapter{})
r.Register(&GenericJSONAdapter{})
// TODO: Zabbix / Grafana / 云监控 / 邮件  → 本期只做 Grafana，其余进 TODO.md
```

| 缺失 | PRD | 本期范围 |
|------|-----|---------|
| **Grafana 适配器** | M1.2 | ✅ 做 |
| 多条 alert 拆分 | —— | ✅ 做（`adapters_builtin.go:25` Prometheus 一次 webhook 多 alert 只取首条，**会丢告警**） |
| 严重度映射表可配置 | M2.3 | ⚠️ `mapPromSeverity` 写死，本期可选 |
| Zabbix / 云监控 / 邮件 | M1.2/M1.3 | ❌ 进 `TODO.md` |

### 🔴 域 7 通知：通道 2/4，且邮件是占位

| 通道 | PRD | 实现 |
|------|-----|------|
| IM（飞书/钉钉） | M7.1 | ✅ 真实 |
| Webhook | M7.4 | ✅ 真实 |
| **邮件（SMTP）** | M7.3 | ❌ `channels_builtin.go:91` `// TODO: 接入 SMTP`，只模拟返回 success，**没真发** |
| **电话/SMS（云语音）** | M7.2 | ❌ 完全无通道实现，仅在 channel.go 留字符串 `"phone"|"sms"` 占位 |
| 送达确认持久化 | M7.6 | ❌ 无 Notification 实体（`main.go:168`「后续加表」），送达结果只打日志 |
| NotificationRule 精确匹配 | M7.5 | ⚠️ `main.go:189/215`「本期简化：取首条 enabled 规则」，条件匹配未实现 |

### 🔴 域 13 管理治理：API Key / 审计 / 加密全缺

| 需求 | PRD | 状态 |
|------|-----|------|
| API Key 管理（org_admin，scoped） | M13.7 | ❌ 无 apikey 实体，开放 API `/api/v1/events` 端点不存在 |
| 审计日志（角色变更/token/删除留痕） | M13.5 | ✅ 已实现（feat-audit-log：AuditLog 实体 + AuditRecorder + 角色变更/API Key/登录埋点 + GET /audit-logs 查询 + 前端 Tab） |
| 操作审计查询/导出 | M13.6 | ⚠️ 有 timeline_action 实体，无独立查询/导出 API |
| 凭证加密（非功能-安全） | NFR | ❌ IM AppSecret/LLM Key 明文环境变量，无加密存储 |

### 🟠 域 5/6 排班与升级：编排层有断点

- `internal/triage/worker.go:49-51` 过期 TODO：`// TODO: Incident 创建后，触发排班(5)/升级(6)/通知(7)`。实际 `OnIncidentCreated` 回调已在 main.go 接通升级，**但注释未清理，需确认首轮通知是否真发**。
- 排班 Redis 缓存：`engine.go:112` `// TODO: Redis 缓存` 未做，每次实时算无缓存。
- Override/换班 API（M5.3）：oncall handler 只读，无 override CRUD。

### 🟠 域 8 IM：企微 + 作战室归档未做

| 需求 | PRD | 状态 |
|------|-----|------|
| 企微适配器 | M8.8 | ❌ `im.NewNoopBot("wecom")` 占位（doc 自承"待 PoC"） |
| 作战室归档 | M8.9 | ❌ 无 war_room 实体，聊天记录不落库 |
| IM 消息回写时间线 | M10.5 | ❌ `handler.go:149`「本期不回写，留后续」 |

### 🟠 域 9/11/12 处置·AI·复盘：闭环断了

| 需求 | PRD | 状态 |
|------|-----|------|
| 智能降噪（AI 学模式） | M11.5 | ❌ 仅规则式 |
| 知识沉淀（published 复盘进知识库，反哺相似检索） | M12.6 | ❌ 无 knowledge_base 实体，相似事件检索只查 incident 不查复盘 |
| Runbook on_failure=escalate | M9.7 | ❌ `engine.go:89` `// TODO: 触发升级`，处置失败不升级 |
| InternalExecutor 真实诊断（查指标/日志/拓扑） | M9.4 | ❌ `executor.go:94` `// TODO`，返回模拟结果 |

---

## 二、前端缺口（对照后端能力域）

### 后端有、前端完全没入口的整块功能

| 前端缺失 | 对应能力域 |
|----------|-----------|
| Integration / Webhook 接入配置页 | 域 1（无路由） |
| 升级策略管理页（配置 levels/delay/targets） | 域 6（无路由） |
| 用户管理 / 团队管理页 | 域 13（无 `/users` `/teams` 路由） |
| AI 诊断 / 相似事件展示 | 域 11（types.ts 定义了 ai_insight 但详情页不渲染） |

### 后端 client 已就绪、UI 只接了 list+delete（补 UI 成本低）

- 通知模板/规则/抑制规则的**创建编辑表单**（域 7/3）—— `hooks/settings.ts` 全套 mutation 写好，`settings.tsx` 没 import
- 排班 CRUD + 日历编辑 + Override（域 5）—— `api.ts` 有 create/update，`oncall.tsx` 纯只读
- RBAC 角色/绑定的**创建表单**（域 13）

---

## 三、非功能性需求缺口（PRD §5，**最严重部分**）

### 🔴 安全（README 自带红色警告）

README 第 44-50 行：
> 当前业务 API 鉴权**默认关闭**（`VIGIL_AUTH_ENABLED=false`），仅解析 `X-Vigil-User-ID` 头，**无任何登录态校验**。`X-Vigil-User-ID` 可被任意伪造。

| 项 | PRD | 现状 |
|----|-----|------|
| 登录态鉴权 | NFR-安全 | ✅ JWT 自管已实现（feat-auth-jwt：login/refresh/me + bcrypt + 前端登录页 + 路由守卫；`X-Vigil-User-ID` 保留为 AUTH_ENABLED=false 降级兼容） |
| API Key（scoped） | M13.7 | ✅ 已实现（feat-apikey：APIKey 实体 + SHA256 哈希 + IdentityResolver 三轨统一 + CRUD + 前端 Tab；明文仅创建时返回一次；鉴权继承归属 User 角色） |
| 凭证加密 | H1.3 | ❌ 明文环境变量 |
| HTTPS 默认 | H1.6 | ⚠️ 靠用户前置 nginx，应用层无强制 |
| 接入鉴权审计 | capabilities §3.2 | ❌ 无审计表 |

### 🔴 可用性 / 可靠性

| 项 | 现状 |
|----|------|
| 限流与背压（M1.7，429/503） | ✅ 已实现（feat-ratelimit：Redis 滑动窗口按 Integration 限流 429 + 队列积压背压 503，payload 均仍落 RawEvent 不丢告警） |
| 熔断 | ❌ 无 |
| 死信重放 UI | ⚠️ doc 说"Asynqmon 可视化"，Asynqmon 未部署（compose 无此服务） |

### 🟠 性能（1000 events/min、通知 P95<5s）

- 无压测验证（PRD 自承"目标，实现阶段校准"）
- 排班无 Redis 缓存，高并发重复计算
- 送达记录只打日志不入库，无法事后统计 P95

### 🟠 国际化

- 前端无 i18n 框架，文案硬编码中文（结构未预留）

### 🟠 横切 H1 部署（deployment.md D1/D2 自承）

| 需求 | 状态 |
|------|------|
| H1.1 Docker Compose | ✅ |
| **H1.2 Kubernetes Helm Chart** | ❌ `deploy/helm/` 不存在，doc §7「后续补全」 |
| **H1.5 备份恢复** | ❌ 无脚本，doc D2「后续」 |
| H1.3 配置加密 | ❌ 见安全 |
| H1.4 版本化迁移 | ✅ |

### 🟠 横切 H2 自身可观测性

| 项 | 现状 |
|----|------|
| H2.1 Metrics 端点 | ✅ 完整 |
| H2.2 Health 端点 | ✅ |
| H2.3 结构化日志 | ✅ zap |
| **H2.4 吃自己狗粮** | ⚠️ 队列积压/通知失败率超阈值未对接自身告警，闭环没闭 |
| **实时推送（WebSocket）** | ❌ 前端无 WS/SSE/轮询，后端无 WS handler，IM↔Web 双向同步未落地 |

---

## 四、测试覆盖盲点

23 个 internal 包中 **5 个零测试**：`logger`、`migrate`、`queue`、`server`、`store`。
其中 `server`（接入层/health/openapi）和 `migrate`（版本迁移）是生产关键路径，缺测试有风险。

---

## 五、优先级建议（按"能否上生产"排序）

### P0（阻塞生产，必须做）
1. **登录态鉴权**替换 `X-Vigil-User-ID` 头伪造（安全红线）✅
2. **限流/背压**（M1.7，否则单源拖垮系统）✅ 已完成（feat-ratelimit）
3. **邮件通道真实 SMTP**（M7.3 占位会假性"发送成功"）
4. **审计日志实体 + API Key**（M13.5/M13.7）✅ 已完成（feat-auth-jwt + feat-apikey + feat-audit-log）

### P1（核心差异化受损）
5. **电话/SMS 通道**（M7.2，升级兜底链断一截）
6. **知识沉淀闭环**（M12.6，AI 差异化"越用越聪明"没闭）
7. **前端 Integration / 升级策略 / 用户团队管理页**
8. **WebSocket 实时同步**（架构承诺的 IM↔Web 双向同步）

### P2（广度补全）
9. Zabbix/Grafana/云监控适配器（M1.2）
10. Helm Chart + 备份脚本（H1.2/H1.5）
11. 企微真实适配（M8.8）

---

## 六、待讨论的开放问题（制定任务前需收敛）

| # | 问题 | 讨论点 |
|---|------|--------|
| D1 | **首批攻坚方向**：先补 P0 安全闭环，还是先补通知通道广度，还是先补前端管理页？ | 取决于"先能上生产"还是"先补差异化" |
| D2 | **交付节奏**：一次做一个完整闭环（含前端+后端+测试+文档），还是按层批量铺开？ | 项目 development.md 强调特性闭环原子性，倾向前者 |
| D3 | **鉴权方案选型**：JWT（自管）vs Session vs OIDC（接企业 SSO）？ | ✅ 已定：JWT 自管（feat-auth-jwt 已落地） |
| D4 | **告警源/通知通道的接入策略**：自己实现 vs 定义 Adapter 接口等社区贡献？ | 影响 P2 优先级 |
| D5 | **AI 知识沉淀**：新建 knowledge_base 实体，还是复用 postmortem + pgvector 直接检索？ | 影响 M12.6 实现成本 |
