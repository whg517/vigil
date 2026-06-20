# Vigil 竞品分析

> 本文档沉淀立项前期的市场调研成果，作为产品定位与功能取舍的依据。
> 调研时间：2026-06。

---

## 一、问题界定：告警的"下一步"问题

传统监控/告警系统（Prometheus、Zabbix、Datadog 等）解决的是"**发现问题**"——把指标越界转成一条告警。但告警产生之后到问题被**真正解决**之间，还有一大段没人管的地带：

> 告警进来 → 谁来响应？怎么通知到他？多人怎么协同？查什么？
> 按什么步骤处置？谁升级？解决后怎么复盘？

这段"告警之后"的链条就是 oncall 产品要解决的问题。它对应完整的 **Incident Response Lifecycle（事件响应生命周期）**，而非简单的发短信通知。

---

## 二、市场分代

| 代际 | 代表产品 | 核心定位 | 模式 |
|------|---------|---------|------|
| **1.0 Paging 通知** | PagerDuty、Opsgenie、OnPage | 把告警可靠地"摇醒"对的人，靠升级策略兜底 | 商业 SaaS |
| **2.0 响应协同** | incident.io、Rootly、FireHydrant、Squadcast | 在 Slack/Teams 里把整个响应流程做出来（建群、拉人、跑 runbook、复盘） | 商业 SaaS |
| **2.5 开源自托管** | Grafana OnCall、GoAlert、OneUptime、Cabot | 对标 1.0，少量 2.0 能力，可私有部署 | 开源 |
| **3.0 AI 智能化** | PagerDuty AIOps、BigPanda、Datadog Event Mgmt | 告警降噪、相关性聚合、自动分诊/路由 | 商业（贵） |

### 关键观察

- 1.0 已高度同质化且贵（PagerDuty AIOps 起价 ~$699/月）。
- 2.0 是当前创新热点（Slack-native 工作流）。
- 2.5 开源方案质量参差：**Grafana OnCall 的 OSS 版已被移到 `cold-storage`，基本停更**——纯做 paging 的开源项目商业化困难。
- 3.0 是壁垒所在，但被大厂垄断。

---

## 三、核心功能矩阵

| 能力维度 | PagerDuty | Opsgenie | incident.io | Rootly | FireHydrant | Squadcast | Grafana OnCall(OSS) | GoAlert |
|---------|-----------|----------|-------------|--------|-------------|-----------|---------------------|---------|
| 排班/升级策略 | ★★★★★ | ★★★★ | ★★★ | ★★★ | ★★★ | ★★★★ | ★★★ | ★★★ |
| 多通道通知(电话/SMS/Push) | ★★★★★ | ★★★★ | 依赖集成 | 依赖集成 | 依赖集成 | ★★★★ | ★★★ | ★★ |
| Slack/Teams 原生工作流 | ★★★ | ★★★ | ★★★★★ | ★★★★★ | ★★★★ | ★★★ | ★★ | ★ |
| Runbook 自动化 | ★★★(独立SKU) | ★★ | ★★★ | ★★★★ | ★★★★★ | ★★★★ | ★ | ★ |
| 告警降噪/AI 相关性 | ★★★★(AIOps) | ★★ | ★★★ | ★★★ | ★★★ | ★★★ | ★ | ★ |
| 自动复盘/postmortem | ★★★ | ★★ | ★★★★★ | ★★★★★ | ★★★★ | ★★★ | ★ | ★ |
| 自托管/开源 | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✓ | ✓ |
| 中文/本地化支持 | 弱 | 弱 | 无 | 无 | 无 | 弱 | 无 | 无 |

### 市场空白（对立项决策至关重要）

1. **没有产品同时满足"自托管/开源"+"完整 2.0 响应协同"+"中文本土化"**。Grafana OnCall OSS 是最接近的，但它停更了，且只有 1.0 能力。
2. **Slack-native 工作流是 2.0 代核心壁垒**，但中国团队用的是钉钉/飞书/企业微信，这块完全空白。
3. **AI 能力被拆成高价 SKU 单卖**（PagerDuty AIOps），中小团队用不起。

---

## 四、2.0 代三流派设计哲学（对 Vigil 设计最有参考价值）

### 流派 A：incident.io —— "约定俗成派"

- **理念**：流程不该靠配置堆出来，而应内建一套最佳实践，开箱即用。
- **做法**：Slack 里 `/incident` 一条命令拉起一切——自动建作战室、拉 responders、开录屏、抓时间线、事后自动生成 postmortem。
- **优点**：上手快、配置少、体验顺滑。
- **代价**：灵活性低，复杂场景要迁就它的流程。

### 流派 B：Rootly —— "自动化引擎派"

- **理念**：给一个带条件判断的自动化引擎，让用户自己拼工作流。
- **做法**：`if 告警 severity=P1 且 service=支付 then 建群+拉DBA+开Zoom+建Jira`。
- **优点**：灵活，适配任意组织流程。
- **代价**：配置重，需要专人维护规则。

### 流派 C：FireHydrant —— "runbook 全生命周期派"

- **理念**：围绕可执行 runbook 组织一切，API-first。
- **做法**：runbook 按服务/严重度自动触发，可手动可自动；把"文档式 runbook"变成"可执行的 runbook"。
- **优点**：处置动作真正落地，不只是通知。
- **代价**：runbook 建设成本高，冷启动难。

> **对 Vigil 的启示**：三种哲学不是互斥的，而是 MVP 的不同切入点。立项时必须选一个作为切入点，而不是三个都要。**Vigil 的 MVP 切入点 = 流派 A（约定俗成）+ 本土 IM 原生**，因为冷启动最快、最容易做出差异化。

---

## 五、技术架构参考（开源方案）

### Grafana OnCall 架构（最值得研究的开源样本，虽已停更）

- **核心组件**：Django 后端 + React 前端 + Celery（异步任务/定时升级）+ RabbitMQ/Redis（消息队列）+ Telegram/Slack bot
- **关键设计**：
  - **Schedule Engine**：基于 iCal 的排班日历引擎，算"此刻谁在班"
  - **Escalation Engine**：Celery 定时任务驱动，按策略链逐级通知
  - **Integration Layer**：用 webhook 接入 Grafana/Prometheus/Zabbix 等告警源
  - **Notification Layer**：抽象出 provider，电话走 Twilio/Vonage，Push 走 mobile app
- **它的教训**：Grafana 把它移到 cold-storage，说明纯做 paging 的开源项目商业化困难——这恰恰印证了"下一步问题"才是更有价值的产品空间。

### GoAlert 架构（Target 出品，更精简）

- Go 单体 + Postgres，强调轻量、易部署
- 排班/升级/通知三件套，无 IM 工作流

> **架构启示**：oncall 产品的核心是三个引擎——**排班引擎（谁）+ 升级引擎（何时找下一个）+ 通知引擎（怎么找到）**。这三个是 1.0 的地基，必须先做扎实。在此之上，工作流引擎 + IM 集成 + AI 才是 2.0/3.0 的差异化。

---

## 六、AI 化趋势（3.0 代，潜在壁垒）

2025 年数据：**78% 的企业 NOC 团队报告严重告警疲劳**。AI 在"下一步问题"上能做的事：

| AI 能力 | 解决的痛点 | 商业化现状 |
|--------|----------|-----------|
| **告警去重/相关性聚合** | 同一故障触发几十条告警，淹没响应者 | BigPanda/Datadog/PagerDuty AIOps 已成熟，但贵 |
| **智能分诊/路由** | 告警不知道该派给哪个团队 | 主流产品都有，规则+ML |
| **自动诊断/根因建议** | 响应者不知道从哪查起 | 2025 新热点，LLM 驱动 |
| **自动处置（auto-remediation）** | 已知故障自动跑 runbook 修复 | 前沿，落地少 |
| **自动复盘生成** | 复盘文档耗时（社区数据：3-4h→30min） | incident.io/Rootly 已落地 |

> **机会**：AI 降噪和自动诊断是当前最有壁垒、最难被开源复制的方向，且大厂卖得很贵。Vigil 若做一个开源 + 本土化 + LLM 原生的方案，差异化会很清晰。

---

## 七、结论：Vigil 的定位空位

🎯 **开源、IM 原生、AI 原生的告警处置平台**

- **部署**：开源 + 可自托管（对标 Grafana OnCall，但补齐它的 2.0 短板）
- **协同**：钉钉/飞书/企微原生工作流（本土空白，对标 incident.io）
- **处置**：内建 runbook + 工作流引擎（对标 FireHydrant / Rootly）
- **智能**：LLM 原生降噪/诊断/复盘（对标 BigPanda / PagerDuty AIOps，但便宜）
- **语言**：中文优先（无人做）

### 需要避开的坑

1. **别只做 paging**——纯通知已被做透且商业化困难（Grafana OnCall OSS 停更是警示）。
2. **别一上来做全功能**——三流派选一个切入，Vigil 选"约定俗成 + IM 原生"。
3. **IM 渠道通知别自己造**——电话/SMS 用云厂商 API，别碰通信基础设施。
4. **AI 是双刃剑**——先做容错率高的场景（降噪聚合 + 复盘生成），最后再做高风险的自动处置。

---

## 参考来源

### 商业化产品
- [PagerDuty - Escalation Policies](https://support.pagerduty.com/main/docs/escalation-policies) | [Incident Workflows 深度解析](https://www.pagerduty.com/blog/incident-management-response/incident-workflows-deep-dive/) | [AIOps 用例](https://www.pagerduty.com/resources/aiops/learn/aiops-use-cases-incident-resolution/)
- [Squadcast - Workflows](https://dev.to/squadcast/how-squadcasts-workflows-enhance-incident-management-automation-1mjo) | [Runbook 自动化](https://www.squadcast.com/blog/automated-runbooks-faster-recovery)
- [incident.io vs Rootly 设计哲学对比](https://incident.io/blog/incident-io-vs-rootly) | [incident.io vs FireHydrant](https://incident.io/blog/incident-io-vs-firehydrant)
- [Rootly vs incident.io（企业自动化）](https://rootly.com/sre/rootly-vs-incident-io-which-automation-wins-for-enterprise)
- [4 家全功能对比（OpsBrief）](https://opsbrief.io/compare/incident-management-tools)

### 开源自托管方案
- [Grafana OnCall OSS 官方](https://grafana.com/oss/oncall/) | [开源发布公告](https://grafana.com/blog/introducing-grafana-oncall-oss-open-source/) | [GitHub（已归档）](https://github.com/grafana-cold-storage/oncall)
- [开源 PagerDuty 替代品盘点（incident.io）](https://incident.io/blog/best-open-source-pagerduty-alternatives-2026)

### AI / AIOps 趋势
- [告警疲劳与 AI 相关性（Ennetix，含 2025 数据）](https://ennetix.com/alert-fatigue-in-noc-and-soc-teams-how-ai-correlation-ends-the-noise-in-2026/)
- [Datadog Event Management](https://www.datadoghq.com/blog/datadog-event-management/) | [BigPanda 事件相关性](https://www.bigpanda.io/blog/event-correlation/)

### 生命周期 / ChatOps
- [Slack 官方：Slack 用于事件管理](https://slack.com/resources/using-slack/slack-for-incident-management)
- [自动复盘实战（r/sre）](https://www.reddit.com/r/sre/comments/1ntxc8j/spent_4_hours_yesterday_writing_an_incident/)
