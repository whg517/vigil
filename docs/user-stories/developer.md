# 用户故事:开发人员(oncall 轮值)视角

| 项 | 内容 |
|----|------|
| 文档定位 | 以「开发人员/轮值 oncall」角色第一视角描述 Vigil 的使用场景与验收标准,是需求文档的场景化补充 |
| 角色 | 陈晨,后端开发工程师(轮值 oncall) |
| 故事编号 | US-DEV-01 ~ US-DEV-09 |
| 需求追溯 | 每条故事标注 FR/NFR 需求域前缀,编号定义见 [`../requirements.md`](../requirements.md) |
| 事实基准 | 实体字段以 [`ent/schema/`](../../ent/schema/) 为准,权限点以 [`internal/auth/permission.go`](../../internal/auth/permission.go) 为准;本文只引用不复制。功能状态以代码现状为据,**规划中**内容已显式标注 |
| 日期 | 2026-07-15 |

---

## 角色画像:陈晨

**背景**。29 岁,某中型互联网公司交易平台组后端开发工程师,主要负责支付网关与对账服务(Go 微服务,K8s 部署)。团队 8 人,每人每 6 周轮值一周 7×24 oncall。公司日常协同用飞书,值班群也在飞书里。写代码是主业,值班是"兼职"——但值班周的体验直接决定他对整套告警体系的信任度。

**目标**。

- 值班周也能保住睡眠和白天的开发产出,半夜被叫醒时 30 秒内判断"要不要起床"。
- 处置时少切系统、少凭记忆:上下文、历史、手册都在告警旁边。
- 误报能当场处理掉,而不是忍一周再提工单。
- 临时有事能放心换班,值班结束能放心把现场交出去。

**痛点**(访谈整理,针对迁移前旧告警体系)。

| # | 原话摘录 | 对应诉求 |
|---|---------|---------|
| 1 | "以前的告警只有一行标题,我得连 VPN、开 Grafana、翻 wiki 才知道发生了什么——人在被窝里,心态已经崩了。" | 告警上下文充分,IM 内一键处置 |
| 2 | "一次磁盘告警每 5 分钟重发一遍,凌晨轰炸 40 条,真正的 critical 混在里面差点漏掉。" | 去重、聚合、静默,降噪但不误杀 |
| 3 | "演练环境的误报报到我头上,想屏蔽还得找运维改 Alertmanager 配置,一等就是两天。" | 误报快速抑制 + 反馈闭环 |
| 4 | "临时有事想换班,得在群里喊一嗓子,然后祈祷升级通知别再找我。" | 换班即时生效,升级找对人 |
| 5 | "处置全靠脑子里的肌肉记忆,新人值班就是灾难。" | Runbook 一键诊断、AI 辅助、相似历史参考 |

## 故事总览

| 编号 | 标题 | 需求域 | 优先级 | 状态 |
|------|------|--------|--------|------|
| [US-DEV-01](#us-dev-01) | 凌晨的 critical:IM 卡片一键确认,升级计时应声而停 | FR-IM、FR-ESC、FR-NTF | P0 | 已实现 |
| [US-DEV-02](#us-dev-02) | 一屏看全上下文:从卡片到详情页不再开五个系统 | FR-INC、FR-TRI | P0 | 已实现 |
| [US-DEV-03](#us-dev-03) | Runbook 一键诊断:只读自动跑,写操作过审批 | FR-RBK | P0 | 已实现 |
| [US-DEV-04](#us-dev-04) | AI 辅助定位与相似历史事件参考 | FR-AI | P1 | 已实现 |
| [US-DEV-05](#us-dev-05) | 误报的快速抑制与噪声反馈闭环 | FR-TRI、FR-AI | P1 | 已实现 |
| [US-DEV-06](#us-dev-06) | 少被无意义打扰:聚合降噪、夜间静默与值班必达 | FR-NTF | P1 | 已实现 |
| [US-DEV-07](#us-dev-07) | 临时换班:Override 即时生效,升级找对人 | FR-ONC、FR-ESC | P1 | 已实现 |
| [US-DEV-08](#us-dev-08) | 把自己服务的告警接进来:向导式接入,不丢告警 | FR-ING、FR-RTE、FR-INT | P1 | 已实现 |
| [US-DEV-09](#us-dev-09) | 值班交接摘要(**规划中**) | FR-ONC、FR-INC | P2 | **规划中** |

---

<a id="us-dev-01"></a>
## US-DEV-01 凌晨的 critical:IM 卡片一键确认,升级计时应声而停

**故事**:作为轮值 oncall 的后端开发工程师,我想要在 IM 里收到信息完整的告警卡片并一键确认,以便半夜不用打开电脑就能接下响应责任、停止升级计时,让链路上的其他人知道"有人管了"。

**场景叙事**:周三凌晨 2:47,陈晨的手机震了一下——不是电话轰炸,是飞书值班群里弹出一张深色底的告警卡片:`[CRITICAL] INC-0217 支付网关 5xx 比例超阈`,正文几行写着所属服务、环境、触发时间和当前值班人(他自己)。卡片下面三个按钮:「✓ 确认」「升级」「详情」。他眯着眼点了「确认」,卡片原地刷新成「已确认 by 陈晨」——同一秒,原本 10 分钟后要叫醒二线同事老张的升级计时被取消了。他坐起来开电脑时心里有底:该他响应,且只有他被吵醒。第二天老张在群里说:"昨晚那单我压根没被叫,舒服。"这正是陈晨想要的:确认这个动作本身,就是对全链路的一次广播。

**验收标准**:

- Given 陈晨是当前值班人且已绑定飞书账号,When 一条 critical Incident 触发,Then 通知按降级链送达,飞书卡片含标题/严重度/服务等正文行与操作按钮,且按钮按陈晨实际权限点(如 `incident.ack`)裁剪——无权按钮不显示(见 [ADR-0018](../adr/0018-im-same-rbac-as-web.md))。
- When 陈晨点击「确认」,Then Incident 状态从 `triggered/escalated` 流转为 `acked`,升级延迟任务被取消(后续层级不再触发),时间线新增状态变更条目,操作审计记录 `via=im`。
- Then 飞书卡片原地更新状态徽章;钉钉平台以重发带状态徽章的新消息模拟(平台能力降级矩阵,见 [ADR-0019](../adr/0019-imbot-pluggable-degradation.md))。
- Given 点击者的 IM 账号未绑定任何 Vigil 用户,When 点击操作按钮,Then 操作被拒绝并记审计,Incident 状态不变(IM 不是权限后门)。
- Given Incident 已被他人先行 ack,When 某个升级任务恰好到期触发,Then 状态守卫使该次升级不产生任何通知动作。

**关联**:FR-IM、FR-ESC、FR-NTF · [ADR-0016](../adr/0016-escalation-asynq-delayed.md)、[ADR-0018](../adr/0018-im-same-rbac-as-web.md)、[ADR-0019](../adr/0019-imbot-pluggable-degradation.md)、[ADR-0034](../adr/0034-uiux-oncall-principles.md) · **P0**

---

<a id="us-dev-02"></a>
## US-DEV-02 一屏看全上下文:从卡片到详情页不再开五个系统

**故事**:作为处置中的值班工程师,我想要在一个页面看全事件的状态、原始信号、时间线与他人动作,以便不用在监控、wiki、群聊天记录之间来回拼线索。

**场景叙事**:确认告警后,陈晨点开卡片上的「详情」进入 Web 详情页。左侧是 Incident 的状态、严重度、所属服务与团队;中间的时间线从"事件创建"开始逐条追加:触发、通知送达、他自己的 ack。这个 Incident 下面挂着 6 条 Event——分诊层已经把 5 分钟内同一服务同一严重度的告警聚合成了一个处理单元,他不用面对 6 条各自为战的消息。点开任意一条 Event 能看到监控系统推来的原始 payload,label 里 `pod`、`instance` 一个不少。他在页面上停留期间,组长在 IM 里补了一句备注,详情页的时间线不用刷新就多了一条——两个人看到的是同一个现场。严重度标签是"颜色 + 文字"双编码,他从来不用猜"这个红是哪种红"。

**验收标准**:

- Given 聚合窗口内同一 `service + severity` 的多条 Event 到达,When 陈晨打开 Incident 详情,Then 只存在一个 Incident,其下关联全部 Event,且每条 Event 的原始 payload 完整可查(归一化不丢原始信息,见 [ADR-0010](../adr/0010-event-incident-separation.md)、[ADR-0012](../adr/0012-triage-three-stage-pipeline.md))。
- When 打开详情页,Then 一屏可见状态/严重度/所属服务团队、只追加的时间线与操作审计记录,任何状态变更都对应一条时间线条目;通知送达记录可经详情关联接口查询(`GET /incidents/:id/notifications`),在详情页内直接呈现为**规划中**。
- Given 他人经 Web 或 IM 对该 Incident 操作,When 陈晨停留在详情页,Then 页面经 WebSocket 实时更新,无需手动刷新。
- Then 严重度以颜色 + 文字双编码呈现(见 [ADR-0034](../adr/0034-uiux-oncall-principles.md))。
- **规划中**:核心响应页暗色模式(夜间 22:00–07:00 首访强引导)——[ADR-0034](../adr/0034-uiux-oncall-principles.md) 已裁决但尚未实现;当前仅 `/wall` 告警大屏为固定深色样式。

**关联**:FR-INC、FR-TRI · [ADR-0010](../adr/0010-event-incident-separation.md)、[ADR-0012](../adr/0012-triage-three-stage-pipeline.md)、[ADR-0022](../adr/0022-aiinsight-hitl-evidence.md)、[ADR-0034](../adr/0034-uiux-oncall-principles.md) · **P0**

---

<a id="us-dev-03"></a>
## US-DEV-03 Runbook 一键诊断:只读自动跑,写操作过审批

**故事**:作为不想半夜手敲命令的值班工程师,我想要一键执行团队沉淀的诊断 Runbook,并且确信任何写操作在我明确批准前绝不会碰生产,以便又快又安全地收集现场信息。

**场景叙事**:INC-0217 的详情页里,陈晨看到团队为支付网关配好的 Runbook《5xx 突增排查》。他点了执行:三个只读诊断步骤(查最近部署记录、拉错误日志摘要、查上游依赖健康)自动跑完,输出逐条落进时间线——不用登跳板机,证据已经在眼前:错误集中在 15 分钟前的一次配置下发之后。Runbook 的第四步是"回滚配置",这一步卡住了:界面明确提示这是写操作,需要他确认。他看完前三步的输出,点了批准,回滚才真正执行。事后他在时间线里能看到"谁在几点批准执行了哪一步"。有一次他手抖连点了两下执行,第二次直接被 409 挡了回来——同一事件上同一 Runbook 不会被并发重复触发。他也试过在飞书里用 `/vigil runbook` 触发:只读步骤照常跑,写步骤在 IM 里永远不放行,批准必须回到 Web 页面完成。

**验收标准**:

- Given 一个 executable Runbook 的步骤全部 `readonly=true`,且陈晨持有 `runbook.execute` 权限,When 触发执行,Then 各只读步骤直接执行,输出与执行人记入时间线。
- Given Runbook 含 `readonly=false` 的写步骤且本次执行未获批准,When 执行到该步,Then 该步被阻断(绝不触碰写操作),按步骤 `on_failure` 语义(continue/abort/escalate)处理,并留痕"谁在何时尝试执行未获批的写操作"(见 [ADR-0021](../adr/0021-runbook-two-tier.md))。
- When 在 IM 里以 `/vigil runbook <name> <incident_id>` 触发,Then 复用与 Web 相同的执行引擎与 `runbook.execute` 权限点,且写步骤在 IM 侧恒不放行(审批不在 IM 内完成)。
- Given 同一 (Runbook, Incident) 已有一次已批准执行在进行,When 再次触发,Then 请求被并发保护拒绝(409),防止连点重复执行不可逆写操作。
- Given 步骤依赖外部系统凭据,Then 凭据在执行时解密注入,任何界面与日志不回显明文(见 [ADR-0030](../adr/0030-integrations-encrypted-openapi.md))。

**关联**:FR-RBK、FR-SEC · [ADR-0021](../adr/0021-runbook-two-tier.md)、[ADR-0030](../adr/0030-integrations-encrypted-openapi.md) · **P0**

---

<a id="us-dev-04"></a>
## US-DEV-04 AI 辅助定位与相似历史事件参考

**故事**:作为对某些服务不熟的值班工程师,我想要 AI 基于事件与时间线给出带证据的根因线索,并看到相似历史事件及其复盘,以便站在团队已有经验之上定位问题,而不是从零猜起。

**场景叙事**:又一个值班夜,这次报警的是陈晨不熟的对账服务。他在详情页点了「AI 诊断」:几秒后返回一条根因线索——"错误模式与批处理任务堆积一致",下面列着支撑这个判断的证据条目(具体的事件字段、时间线片段),每条都可以点开核对。他又点开「相似历史事件」:三个月前有一单几乎一样的 INC-0089 排在最前。他转到复盘页翻出 INC-0089 已发布的复盘——当时的根因是上游文件延迟到达,处理方式写得清清楚楚。他照着复盘里的改进项检查,十分钟定位。AI 的建议他核实后点了「接受」,这个判断连同他的确认一起留在了记录里;有一次 AI 猜错了,他点「拒绝」,也仅此而已——AI 从不替他做任何动作。还有一个凌晨公司的 LLM 服务恰好挂了,诊断按钮暂时没结果,但通知、升级、处置一切照旧:AI 是增强,不是依赖。

**验收标准**:

- When 陈晨触发 AI 诊断,Then 产出根因线索类 AIInsight(状态 `suggested`),每条建议附 evidence 且可溯源;无 evidence 的建议不展示(见 [ADR-0022](../adr/0022-aiinsight-hitl-evidence.md))。
- When 查看相似历史事件,Then 按向量相似度返回相似 Incident 列表(详情页呈现其严重度/标题/状态/时间);pgvector 或 Embedding 不可用时降级为文本匹配,功能不报错(见 [ADR-0024](../adr/0024-similar-incident-pgvector.md))。
- 相似的已发布复盘经独立接口(`GET /incidents/:id/similar-postmortems`)按向量相似度检索;相似事件列表中连同复盘链接一并呈现为**规划中**,现状经复盘页按事件查找。
- When 陈晨对某条建议 accept/reject,Then AIInsight 状态相应流转并记审计;任何建议在人工 accept 之前不产生自动动作(HITL 硬约束)。
- Given LLM Provider 不可用或产出置信度低于阈值,Then AI 功能降级或不产出,告警主流程(接入/通知/升级)不受任何影响(见 [ADR-0023](../adr/0023-llm-provider-cost-control.md))。

**关联**:FR-AI · [ADR-0022](../adr/0022-aiinsight-hitl-evidence.md)、[ADR-0023](../adr/0023-llm-provider-cost-control.md)、[ADR-0024](../adr/0024-similar-incident-pgvector.md)、[ADR-0026](../adr/0026-postmortem-ai-draft.md) · **P1**

---

<a id="us-dev-05"></a>
## US-DEV-05 误报的快速抑制与噪声反馈闭环

**故事**:作为被误报骚扰的值班工程师,我想要当场把确认的噪声沉淀为抑制规则(自己动手或采纳 AI 建议),以便下一个同类告警不再打扰任何人——同时确信真正的 critical 永远不会被这套降噪机制误杀。

**场景叙事**:周四上午,演练环境的一条 warning 告警又一次找上陈晨——这是本周第三次,内容都是"周期演练,无需响应"。这次他没有忍:详情页里分诊 AI 已经给出一条降噪建议,标着匹配标签(`env=drill`)和证据(近期同标签告警的重复模式)。他核对无误后点了「接受」,系统据此生成了一条来源标记为 AI 的抑制规则——从此同类 Event 进来即被抑制,不再生成 Incident、不再通知任何人。他也可以不等 AI:在维护窗口/抑制规则页面手动建一条 adhoc 规则,是一分钟的事,不用求任何人改监控侧配置。他最初担心的"降噪降过头"没有发生:critical 天然不会被任何抑制规则拦截,被抑制的 Event 也全部落库可查——哪天规则错了,证据都在。让他放心的还有一点:这套系统绝不会"自作聪明"——没有他的确认,任何规则都不会自动出现或自动改变。

**验收标准**:

- Given 一条非 critical 的疑似噪声 Incident,When 触发分诊 AI,Then 可产出降噪建议(带匹配标签与 evidence);critical 不产出降噪建议。
- When 陈晨接受该建议,Then 沉淀一条 `source=ai` 的抑制规则,后续命中同标签的 Event 被抑制;重复接受幂等(不重复建规则);拒绝则不生成规则(见 [ADR-0025](../adr/0025-no-auto-retrain.md))。
- Given 任何抑制规则(adhoc 或 maintenance),When 命中 critical Event,Then `preserve_critical` 守卫使 critical 不被抑制(降噪不误杀,见 [ADR-0012](../adr/0012-triage-three-stage-pipeline.md))。
- When 陈晨手动创建 adhoc 抑制规则或维护窗口,Then 规则即刻对后续 Event 生效;被抑制的 Event 仍落库可查,不静默丢失。
- Then 不存在任何无人确认的规则自动生成或自动调整(明确否决自动回训,见 [ADR-0025](../adr/0025-no-auto-retrain.md))。

**关联**:FR-TRI、FR-AI · [ADR-0012](../adr/0012-triage-three-stage-pipeline.md)、[ADR-0025](../adr/0025-no-auto-retrain.md) · **P1**

---

<a id="us-dev-06"></a>
## US-DEV-06 少被无意义打扰:聚合降噪、夜间静默与值班必达

**故事**:作为要在值班周保住睡眠的工程师,我想要短时间的通知洪峰被合并、我作为订阅者收到的非紧急通知在夜间被静默,同时值班告警始终必达、critical 永远穿透静默,以便"被打扰"只发生在该我负责且真正值得的时候。

**场景叙事**:陈晨最怕的不是 critical——真出大事被叫醒他认——而是凌晨三点的 warning:磁盘用到 82%、某个非核心任务重试了一次。迁移到 Vigil 后他先弄清了一件事:**静默时段保护的不是值班人**。他在班时,升级链解算出来要找的就是他,通知必须送达——"值班人始终被通知"是升级链不断链的刻意设计(见 [ADR-0017](../adr/0017-notification-fallback-chain.md));值班周想少被吵,正路是把误报当场抑制掉(见 [US-DEV-05](#us-dev-05))和依靠聚合降噪,而不是把值班通知静默掉。真正受益于静默的是"非当班的他":他订阅了自己负责但本周不值班的服务,团队配了 23:00–07:00 的静默时段、穿透名单里只有 critical——夜里这些订阅通知不再震手机,而是标记为"已静默"记在送达账本里,第二天打开通知记录,静默的每一条都在,没有任何东西被偷偷扔掉。白天的一次网络抖动曾在半分钟内产生 4 条同目标通知,他收到的是合并后的一条,而不是四连震。那个值班周结束,他的手机夜间响过的每一次都确实该他起床;而交完班之后的夜晚,非 critical 再没吵过他。

**验收标准**:

- Given 配置了静默时段(如 23:00–07:00)且穿透名单含 critical,且**通知目标不是当班值班人**(如订阅者等非当班干系人),When 夜间产生 warning 通知,Then 送达状态记为 `suppressed` 并在送达记录中可查询(补发能力**规划中**);critical 通知穿透静默正常送达(见 [ADR-0017](../adr/0017-notification-fallback-chain.md))。
- Given 通知目标是升级链按排班解算出的**当班值班人**,Then 静默时段对其不生效,夜间 warning 也照常送达——值班人始终被通知,升级链不因静默断链(见 [ADR-0017](../adr/0017-notification-fallback-chain.md))。
- Given 聚合窗口(默认 30 秒)内同一接收人产生多条通知,Then 合并为一条聚合通知发送;critical 不聚合、立即单发。
- Given 静默时段跨午夜(起始晚于结束,如 23:00–07:00),Then 按静默规则配置的 IANA 时区(`NotificationRule.quiet_hours.timezone`)计算生效区间,支持跨午夜窗口。
- Given 陈晨订阅了非本人值班服务的事件变更,Then 订阅通知按"非值班人"口径处理,遵守静默时段(夜间非 critical 不打扰)。

**关联**:FR-NTF · [ADR-0017](../adr/0017-notification-fallback-chain.md)、[ADR-0034](../adr/0034-uiux-oncall-principles.md) · **P1**

---

<a id="us-dev-07"></a>
## US-DEV-07 临时换班:Override 即时生效,升级找对人

**故事**:作为临时有事的值班工程师,我想要和同事约定顶班后由顶班人自助登记换班、且换班即刻对升级与通知生效,以便不用"在群里喊一嗓子然后祈祷",也不用走管理员审批流。

**场景叙事**:周五下午,陈晨想起晚上要去机场接人,正好撞上自己的夜班。他在群里跟组里的小吴说定顶班,小吴打开值班日历,给该班次 20:00–24:00 建了一条 Override,把**自己**登记为顶班人——把自己顶上去只需 `schedule.override` 权限,不需要任何管理员审批。权限判定看的是"顶班人是否为操作者本人":若由陈晨代小吴登记(顶班人不是操作者自己),则须管理级的 `schedule.update`,通常由 team_admin 出手。保存即生效:值班表没有"缓存快照"要等,升级引擎每次触发都实时计算"此刻谁在班"。当晚 21:15 一条告警升级到值班层,通知直接发给了小吴;陈晨在机场用飞书敲了句 `/vigil oncall payment-gateway`,机器人回的当前值班人是小吴——他确认自己真的"下班了"。接完人回家,他删掉这条 Override(删除同样只需 `schedule.override`),后半夜的班照常回到自己头上。全程没有一条通知找错人。

**验收标准**:

- Given 小吴持有 `schedule.override` 权限,When 他创建 Override 并把**自己**登记为顶班人,Then 创建成功——仅需 `schedule.override`;When 操作者把**他人**登记为顶班人(顶班人 ≠ 操作者本人,无论班次原属谁),Then 须叠加管理级 `schedule.update`,否则拒绝(403)(见 [ADR-0015](../adr/0015-schedule-realtime-no-snapshot.md))。
- When Override 保存成功,Then 值班判定立即生效(实时计算、不存快照):该时段新触发事件的升级通知发给顶班人而非陈晨。
- When 在 IM 里发送 `/vigil oncall <service|team>`,Then 返回实时计算的当前值班人(含 Override 结果),权限点为只读的 `schedule.view`。
- When 删除 Override,Then 原排班立即恢复生效。

**关联**:FR-ONC、FR-ESC · [ADR-0015](../adr/0015-schedule-realtime-no-snapshot.md)、[ADR-0016](../adr/0016-escalation-asynq-delayed.md) · **P1**

---

<a id="us-dev-08"></a>
## US-DEV-08 把自己服务的告警接进来:向导式接入,不丢告警

**故事**:作为新服务的负责人,我想要按向导自己完成告警源接入并当场验证连通,以便服务上线当天告警就有人接,而且确信接入链路不会丢掉任何一条告警。

**场景叙事**:陈晨的新对账服务要上线了,告警接入这件事他没有提工单——在 Vigil 的接入向导里选了 Prometheus 类型,系统生成一个带 token 的 webhook 地址,并给出可以直接粘贴的 Alertmanager 配置样例。他把配置贴进 Alertmanager,点了向导里的"发送测试",几秒后测试事件出现在事件列表里,接入完成。上线首日流量高峰,Alertmanager 一口气推了几十条:接收端只做校验落库入队、秒级返回,监控侧从没超时;有一条 payload 因为模板写错解析失败,也没有消失——它以 `parse_failed` 状态躺在原始事件列表里,他修好模板后一键重放,一条不少。告警的 label 匹配到了他配好的 Service,升级策略照团队的走;有一条 label 打错了没匹配上,进了未路由池等人认领,而不是无声蒸发。后来一次安全巡检怀疑 token 外泄,他点了"轮换 token",旧地址即刻失效,换掉配置即可——全程不需要找平台管理员。

**验收标准**:

- When 陈晨经接入向导创建 Prometheus 类型 Integration,Then 获得带 token 的 webhook 接入地址与配置模板/接线指引,并可发送测试事件验证连通(见 [ADR-0011](../adr/0011-ingestion-decoupled-idempotent.md))。
- When 告警源推送事件,Then 接收端只校验 token、落原始 payload、入队,秒级返回 202;归一化异步完成,严重度统一归一为 critical/warning/info。
- Given payload 格式错误,Then 原始事件落库并标记 `parse_failed`,修正后可重放,不丢失。
- Given Event 的 labels 匹配到 Service,Then 按确定性路由规则归属该 Service 并沿用其升级策略;匹配失败进入 unrouted 池(查看需 `event.view_unrouted` 权限)而非静默丢弃(见 [ADR-0013](../adr/0013-deterministic-routing.md))。
- When 轮换 Integration token,Then 旧 token 即刻失效,轮换操作需 `integration.update` 权限并记审计。

**关联**:FR-ING、FR-RTE、FR-INT · [ADR-0011](../adr/0011-ingestion-decoupled-idempotent.md)、[ADR-0013](../adr/0013-deterministic-routing.md)、[ADR-0038](../adr/0038-smtp-inbound.md) · **P1**

---

<a id="us-dev-09"></a>
## US-DEV-09 值班交接摘要(规划中)

> **状态:规划中。** 本条是合理的未来期望,当前版本未实现,不构成对现有能力的描述。现状下的替代路径:事件列表按状态筛选未收口事件 + 值班日历确认下一班 + 各事件时间线阅读处置进展。

**故事**:作为值班周结束的工程师,我想要一键生成本周期的交接摘要(未关闭事件、待跟进项、生效中的临时抑制规则),以便下一位值班人不靠口头转述就能接住现场。

**场景叙事**(期望形态):周一早上 9:00,陈晨的值班周结束。他点开"交接摘要":两条未 resolved 的事件带着各自的时间线要点、一条他临时建的演练抑制规则(还有 3 天过期)、一个等外部厂商回复的跟进项,整整齐齐列在一页里。下一班的小吴在飞书里收到这份摘要,回了个"收到"——过去那种"上一班的坑靠下一班踩出来"的交接,不再发生。

**验收标准**(期望,随规划落地时细化):

- Given 陈晨的值班周期结束,When 生成交接摘要,Then 汇总该周期内未关闭的 Incident、时间线关键节点与生效中的临时抑制规则。
- Then 摘要可推送给下一班值班人(复用现有通知/IM 通道)。
- Then 摘要的事件范围遵守团队软隔离与 RBAC(不因交接放大可见范围)。

**关联**:FR-ONC、FR-INC · [ADR-0015](../adr/0015-schedule-realtime-no-snapshot.md)、[ADR-0028](../adr/0028-single-org-soft-isolation.md) · **P2(规划中)**

---

## 附:本文与其他文档的关系

| 要找什么 | 去哪里 |
|---------|--------|
| FR/NFR 需求条目定义 | [`../requirements.md`](../requirements.md) |
| 系统如何运转(引擎/数据模型) | [`../architecture.md`](../architecture.md) |
| 每项设计"为什么这么定" | [`../adr/`](../adr/README.md) |
| 实体字段权威定义 | [`ent/schema/`](../../ent/schema/) |
| 权限点权威清单 | [`internal/auth/permission.go`](../../internal/auth/permission.go) |
| 其他角色视角的用户故事 | 本目录(`docs/user-stories/`)下同系列文档 |
