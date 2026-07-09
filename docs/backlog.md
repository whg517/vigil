# Backlog(暂不做 / 待规划)

> 本文件是 Vigil 的**单一待办信源**:记录已评估、明确不在当前迭代做的事项。
> 与 [ADR](./adr/) 的分工:ADR 记录"已定型的架构决策";本文件记录"推迟/未排期"的事项及其**重启前置条件**。
>
> 状态约定:🚧 暂不做(明确推迟,有依据)· 📋 待规划(确认要做、未排期)
> 维护约定:项一旦完成即从本文移除(git 历史可追溯),保持正文精简。

---

## 一、🚧 暂不做(明确推迟)

### 1.1 作战室(War Room)live path

一键作战室(Incident 触发自动建群/拉人/升级联动入群)、作战室归档、IM 消息回写时间线。

- **现状**:飞书/钉钉 `CreateWarRoom` 建群**原语已实现**(`internal/im/{feishu,dingtalk}/adapter.go`),但 **live path 未接**——Incident 事件链不调用它,`Incident.war_room` 字段无写入路径。
- **推迟原因**:① 建群/邀人/升级联动/归档是跨 IM 平台与业务事件的整套编排,成本高;当前「工作群 + 交互卡片 + 实时刷新」已满足协同诉求;② 各平台建群/群成员/消息回写 API 能力参差,需先 PoC 定边界。
- **重启前置**:IM 建群/群成员/消息回写 API PoC 完成;编排设计评审通过(建议先落 `docs/design/`);与复盘归档、时间线回写的联动方案确定。

### 1.2 IaC / Terraform Provider

- **推迟原因**:完整 Provider(CRUD 映射 + state 协调 + import)体量大、需长期契约稳定;REST API + Web 已能全量管理资源,ROI 不足。
- **替代**:REST API 脚本化 + 配置模板(config-template)批量接入。
- **重启前置**:API 版本化契约稳定;资源 import/state 映射方案;IaC 需求规模验证。

### 1.3 首次部署向导 / 企微完整 bot / Jira·禅道 SDK

- **首次部署向导**:env + 种子超管 + 首登强制改密已够;分步 web 向导属 onboarding UX 大件,待产品评审。
- **企微 bot**:`NoopBot` 占位,`Available()==false` 被通知链排除但**不静默丢告警**(走邮件/电话/短信兜底,见 [ADR-0019](./adr/0019-imbot-pluggable-degradation.md));重启前置:企微应用注册 + 卡片/群 API PoC。
- **Jira/禅道 SDK**:通用 webhook 工单已覆盖主要场景;`internal/ticket/adapter.go` 留有 `NewJiraAdapter`/`NewZentaoAdapter` 占位,替换占位即接入;重启前置:目标实例可联调 + 认证/字段映射方案。

### 1.4 AI 无监督自学习 / 回训

**明确不做**(非推迟),与 human-in-the-loop 基线冲突,裁决见 [ADR-0025](./adr/0025-no-auto-retrain.md)。

## 二、📋 待规划(确认要做、未排期)

| 能力域 | 项 | 说明 |
|--------|-----|------|
| 接入 | Zabbix 适配器 | config-template 已列类型但无适配器,推送现落 `parse_failed` |
| 接入 | 云监控适配器 | 阿里云 / 腾讯云 / AWS SNS 消息结构适配 |
| 接入 | SMTP 入向(邮件→Event) | 与已实现的「邮件**通知**通道」(出向)是两件事 |
| 接入 | 严重度映射表可配置 | `mapPromSeverity` 当前硬编码,待支持 `Integration.config` 覆盖 |
| 通知 | 电话/SMS 真实云厂商 API | PhoneChannel/SMSChannel 现为 webhook 占位转发(已纳入降级链),待接阿里云/腾讯云语音 |
| 处置 | InternalExecutor 只读诊断扩展 | 现支持 check_http/info,待加 query_metrics(Prometheus)/ query_logs(Loki) |
| UX | 前端管理页产品目视复核 | Playwright 巡检已过(14 页 0 白屏 0 真错误);换班时区、quiet_hours 默认、模板 name 引用等交互细节待产品复核 |

> 已完成项的历史追溯:见 git 历史(本文件在 `2b40e53` 前的版本含完整「已完成移出」附录)。
