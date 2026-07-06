# Backlog（暂不做 / 待规划）

> 本文件是 Vigil 的**单一待办信源**：记录**已评估、明确不在当前迭代做**的需求项。
> 每条注明出处（PRD 需求 ID / 能力域），🚧 项另附推迟依据与重启前置条件。
>
> - 与各文档「开放问题」的区别：开放问题是**待讨论的决定**；本文件是**已决定推迟/待规划**的事项。
> - 与 `roadmap-completeness.md` 的区别：roadmap 是**已排期/已完成**任务的执行视图；本文件是**未排期**事项。
>
> **状态约定**：🚧 暂不做（明确推迟，有推迟依据）· 📋 待规划（已确认要做、未排期）
>
> **维护约定**：正文只列**未做**的事项。项一旦完成，从下方表格移除，并在文末「附录·已完成移出」追加一行索引（供追溯），保持正文精简。

---

## 一、🚧 暂不做（明确推迟）

### 1.1 作战室（War Room）—— M8.2 / M8.9 / M10.5

**影响范围**：能力域 8（IM 协同）的 M8.2 / M8.9，及能力域 10（时间线）依赖作战室的 M10.5。

| PRD 需求 ID | 描述 |
|-------------|------|
| **M8.2** | 一键作战室：Incident 触发时自动建临时 IM 群、拉相关人、置顶事件信息；升级到新层级自动把新 oncall 拉入群 |
| **M8.9** | 作战室归档：事件关闭后保留作战室聊天记录，关联到复盘（需 `war_room` 实体落库） |
| **M10.5** | IM 消息回写时间线：捕获含关键词 / @机器人的作战室关键消息写入时间线 |

- **推迟原因**：① 自动建群 / 邀人 / 升级联动入群 / 关闭归档是跨 IM 平台与业务事件的整套编排，落地成本高，当前用「工作群 + 交互卡片（M8.1）+ 卡片实时刷新（M8.4）」已满足协同诉求；② 飞书/钉钉/企微在建群、群成员 API、消息回写上能力参差，需先做 PoC 定边界（PRD §4.8 风险点）。
- **现状（代码保留物）**：飞书/钉钉 `CreateChat` 建群原语已实现（`internal/im/{feishu,dingtalk}/adapter.go`），企微为 `NoopBot` 占位；live path 未接（Incident 事件链不调 `CreateWarRoom`）；`Incident.war_room` 字段无写入。
- **替代方案**：M8.1 交互卡片发到工作群 + M8.4 状态实时刷新。
- **重启前置**：IM 建群/群成员/消息回写 API 的 PoC 完成；M8.2 编排设计评审通过；与复盘（域 12）的 M8.9 归档、M10.5 回写联动方案确定。

### 1.2 IaC / Terraform 声明式资源管理

**影响范围**：平台化（personas P1-3 平台工程师）。已评估并明确推迟（2026-07-06）。

| 项 | 出处 |
|----|------|
| **IaC / Terraform Provider** | personas P1-3 |

- **推迟原因**：平台工程师用 Terraform/IaC 声明式管理 Vigil 资源（Integration/Service/Schedule 等）需实现完整 Terraform Provider（资源 CRUD 映射 + state 协调 + 幂等 + import），体量大、需长期维护契约稳定；当前 REST API + Web 已能全量管理资源，IaC 属"锦上添花"的规模化运维能力，ROI 不足以进当前迭代。
- **现状（代码保留物）**：所有资源均有稳定 REST API（`internal/server/`），Terraform Provider 可在其上构建，无需后端改造即可后续接入。
- **替代方案**：REST API 脚本化（curl/SDK）+ Web 手动管理；配置模板（`config-template`）辅助批量接入。
- **重启前置**：稳定 API 版本化契约（避免 Provider 频繁破坏性升级）；资源 import/state 映射方案；目标用户规模验证 IaC 需求真实存在。

### 1.3 首次部署向导 / 企微完整 bot / 工单 SDK

**影响范围**：H1（部署 onboarding）、能力域 8（企微适配器）、能力域 14（Jira/禅道 SDK）。三项已评估并明确推迟（2026-07-06）。

| 项 | 出处 |
|----|------|
| **首次部署向导（first-run wizard）** | 待讨论 / H1 |
| **企微完整 bot 适配器** | 能力域 8 / PRD §4.8 |
| **Jira / 禅道具体 SDK 适配器** | 能力域 14 M14.2 |

- **推迟原因**：① 首次部署向导——env + 种子超管 `admin/changeme` + 强制首登改密已够初始化，分步 web 向导是纯 onboarding UX 大件、与 first-run 状态判定耦合，需产品评审；② 企微 bot——飞书/钉钉已完整支持，企微能力/授权模型差异大，需应用注册 + 能力 PoC（同作战室 PoC 风险）；③ Jira/禅道 SDK——通用 webhook 工单已覆盖主要场景，完整 REST SDK（认证/字段映射/双向回写）体量大、需真实实例联调，ROI 不足。
- **现状（代码保留物）**：① `SeedBuiltinRoles`/`SeedDefaultAdmin` 幂等种子 + `must_change_password` 链路完整，无向导页；② 企微 `NoopBot` 占位，`Available()==false` 被通知链排除、**不静默丢告警**（走邮件/电话/短信兜底），adapter 接口已留；③ `internal/ticket/adapter.go` 有 `Adapter` 接口 + 通用 webhook 适配器 + `NewJiraAdapter`/`NewZentaoAdapter` 占位（返回 `ErrAdapterNotImplemented`），替换占位即接入、触发链路不变。
- **替代方案**：① env + 种子超管 + Web 手动建组织结构；② 邮件/电话/短信/webhook 兜底 + 飞书/钉钉卡片；③ 通用 webhook 工单（可配 URL、SSRF 防护）+ 手填/回写 tracker_url。
- **重启前置**：① onboarding UX 评审 + first-run 状态判定方案；② 企微应用注册 + 卡片/群 API PoC；③ 目标工单实例可联调 + 认证/字段映射方案。

---

## 二、📋 待规划（已确认要做、未排期）

> 按能力域归组。含 2026-06-22 `TODO.md` 合并入的长期增强项（非阻塞生产）。

### 2.1 接入与归一化（能力域 1）

| 项 | 出处 | 说明 |
|----|------|------|
| Zabbix 适配器 | M1.2 | 解析 Zabbix action script payload（trigger/priority/eventid）。当前 `config-template` 列了 zabbix 类型但无适配器，推送落 `parse_failed` |
| 云监控适配器 | M1.2 | 阿里云 / 腾讯云 / AWS SNS 各自消息结构适配 |
| 邮件接入 SMTP→Event | M1.3 | SMTP 收信地址收告警，主题解析 severity、正文解析 detail。**注**：与「邮件**通知**通道」（已实现，对外发邮件）是两件事——前者入向告警源、后者出向通知 |
| 严重度映射表可配置 | M2.3 | `mapPromSeverity`（`ingestion/adapters_builtin.go`）当前硬编码，待支持 `Integration.config` 覆盖映射表 |

### 2.2 通知（能力域 7）

| 项 | 出处 | 说明 |
|----|------|------|
| 电话 / SMS 真实云厂商语音 API 对接 | M7.2 | 当前 PhoneChannel/SMSChannel 为抽象层 + webhook 占位转发（已纳入降级链、可触发），待对接阿里云/腾讯云语音 API 做真实呼叫/短信 |

### 2.3 处置执行（能力域 9）

| 项 | 出处 | 说明 |
|----|------|------|
| InternalExecutor 只读诊断扩展 | 域 9 | 当前支持 check_http 探活 + info，待加 query_metrics（查 Prometheus）/ query_logs（查 Loki）等更多只读诊断类型 |

### 2.4 AI 智能（能力域 11）

| 项 | 出处 | 说明 |
|----|------|------|
| 智能降噪**自动学习 / 回训** | M11.5 | 「AI 建议 → 规则沉淀」已落地（N1.4：`noise_suggestion` accept → SuppressionRule）；**无监督自学习 / 模型回训**明确不做——与设计基线第 4 条 human-in-the-loop 冲突（自动改判绕过人确认），详见 `docs/capabilities/07-timeline-ai.md` Q5 决策。此处保留仅作记录，非待排期项 |

### 2.5 报表与看板（能力域 15）

> 本域当前无未排期项（audit-logs 导出已完成，见附录）。

### 2.6 平台化 / 运维

| 项 | 出处 | 说明 |
|----|------|------|
| i18n 国际化 | NFR | **框架已落地**（i18next + react-i18next，zh/en 双语，侧边栏语言切换 + localStorage 持久化，`en.ts` 类型约束保证 key 结构与 zh 一致）+ **核心流程已完整双语**（导航/登录/改密/仪表盘/事件列表/设置 tab/严重度·状态 Badge）。**其余管理页仍为存量中文，待增量迁移**（逐页替换 `t()` 即可，框架无需再改）。覆盖清单见 `web/README.md`「国际化（i18n）」。 |

### 2.7 旅程 / UX 缺口

| 项 | 出处 | 说明 |
|----|------|------|
| 前端管理页 UX 目视复核 | Phase 2/3 | 已做一轮 Playwright 浏览器评审（14 页巡检 + 维护窗口/审计导出/语言切换重点走查，0 白屏 0 真错误）；**换班时区、quiet_hours 默认、通知模板 name 引用**等具体交互细节仍待产品目视复核 |

---

## 附录 · 已完成、从 backlog 移出（可追溯）

> 下列项曾列入待办，现已实现并合入 main。仅保留一行索引供追溯，不占正文。

| 项 | 完成 | 项 | 完成 |
|----|------|----|------|
| 未路由事件重路由端点 | T2.4 `91143d5` | 复盘 resolve 自动起草 | T4.1 `9f49f77` |
| 报表 CSV 导出 + 定时聚合 | T6.1 `2fbea67` | 跨团队 @人事件级临时授权 | N1.2 `bde581c` |
| 多副本 WebSocket pub/sub | T6.4 `014a0d0` | IM 斜杠命令全量（runbook/oncall） | N2.1 `5849755` |
| Action Item 自动建工单 | T4.3 `466a01f` | 用户禁用自动交接提示 | N2.3 `b4749e9` |
| 工单侧反向回写 | N1.3 `e96c966` | AI 噪声建议沉淀为抑制规则 | N1.4 `e96c966` |
| analytics 团队 scope 隔离 | T0.7 `4e0ba13` | 集成向导 UI（4 步分步接入） | P3.1 `5ae441a` |
| oncall 解算跳过禁用用户 | T2.3 / T2.4 | follow_the_sun 完整跨时区接力 | P3.2 `ddad166` |
| webhook 出站动态订阅 CRUD | N2.2 `5849755` | Incident 人工合并端点 | N1.1 `efdad8d` |
| 通知送达持久化（Notification 实体） | T2.2 `3907aac` | NotificationRule 精确匹配 | T2.2 `3907aac` |
| 执行器凭据加密托管 | T6.3 `aad6a9c` | 出站签名 + 死信 + 重放 | T5.2 `14b663c` |
| 值班大屏 / PWA / WS 实时看板 | P4·B `4c10e38` | migrate status/down 版本化回滚 | P4·C `564ef26` |
| AI Ollama 本地 Provider | `76505bb` | AI 置信度阈值配置化 | `76505bb` |
| audit-logs CSV 导出端点 | `4979072` | 自监控闭环（队列/失败率超阈自告警） | `247e93d` |
| 维护窗口独立操作流（kind + 专属页） | `a1466be`/`0e09402` | git 钩子 + commitlint CI | `142d460`/`7989f2c` |
