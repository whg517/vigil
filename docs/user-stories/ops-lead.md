# 用户故事:运维主管(Ops Lead)

| 字段 | 内容 |
|------|------|
| **角色** | 运维主管 / SRE 团队负责人 |
| **编号前缀** | US-OPS |
| **日期** | 2026-07-15 |
| **状态** | 现行(与需求文档 [`../requirements.md`](../requirements.md) 同步维护) |
| **相关** | [`../requirements.md`](../requirements.md)、[`../architecture.md`](../architecture.md)、[`../adr/README.md`](../adr/README.md) |

> **单一信源声明**:本文只讲「这个角色要什么、怎么验收」。功能需求条目见 [`../requirements.md`](../requirements.md)(编号 FR-xx/NFR-xx 与本文互相追溯);决策理由见各 ADR 链接,不在此复制;实体字段以 [`ent/schema/`](../../ent/schema/) 为准,权限点以 [`internal/auth/permission.go`](../../internal/auth/permission.go) 为准,本文出现的字段与权限点名仅为举例。
>
> 明确标注**「规划中」**的故事尚未实现,不构成现有能力承诺;落地前须按 [ADR-0001](../adr/0001-record-architecture-decisions.md) 的治理规则先立 ADR。

---

## 角色画像:张磊

**背景**。张磊,35 岁,某中型互联网公司基础架构部 SRE 主管。带 8 名值班工程师,支撑交易、商品、物流 3 条业务线共 20 多个后端服务。监控体系是现成的:Prometheus + Grafana 看板 + 若干自建脚本,「发现问题」不缺工具,缺的是发现之后那一段——告警发到钉钉群里就没有然后了。团队用钉钉办公,晋升述职和月度经营例会都要拿运维数据说话。

**目标**。让告警「有人接、接得住、有下文」:每条 critical 都能追到具体责任人;值班轮换公平透明,不再靠 Excel 和口头约定;每月例会能拿出 MTTA/MTTR、降噪率这类硬数据;审计检查时拿得出「谁在什么时候做了什么」的底账。平台本身不能变成第 21 个要值班伺候的服务。

**痛点**(访谈原话摘录):

| 痛点 | 张磊的原话 |
|------|-----------|
| 告警轰炸 | 「一次数据库主从切换,群里 40 分钟刷了 400 条,后来大家把群设了免打扰——等于监控白做。」 |
| 值班不公平 | 「排班在 Excel 里,有人连着两个周末被排上,来找我拍桌子;临时换班口头说好,出事了升级还打给换走的那个人。」 |
| 升级靠吼 | 「值班的睡死了,告警在群里躺 40 分钟,最后是业务方打我手机。所谓升级机制就是我半夜在群里@所有人。」 |
| 复盘走过场 | 「复盘就是开个会,文档没人写,改进项没人跟,下季度同一个坑再摔一次。」 |
| 汇报没数据 | 「老板问『告警比上月降了多少、响应快了多少』,我只能拍脑袋说个大概。」 |
| 平台维护成本 | 「不敢再引入一套要专人伺候的系统,数据也不能出公司网。」 |

## 故事索引

| 编号 | 标题 | FR 域 | 优先级 | 状态 |
|------|------|-------|--------|------|
| [US-OPS-01](#us-ops-01) | 告警风暴归拢与维护窗口降噪 | FR-TRI | P0 | 已实现 |
| [US-OPS-02](#us-ops-02) | 升级链自动接力,不靠人肉在群里吼 | FR-ESC | P0 | 已实现 |
| [US-OPS-03](#us-ops-03) | 排班透明、换班自助、空班有告警 | FR-ONC | P0 | 已实现 |
| [US-OPS-04](#us-ops-04) | 在钉钉里处置,权限与 Web 完全一致 | FR-IM / FR-SEC | P0 | 已实现 |
| [US-OPS-05](#us-ops-05) | 月度例会拿得出 MTTA/MTTR 与降噪率 | FR-RPT | P1 | 已实现 |
| [US-OPS-06](#us-ops-06) | critical 复盘闸门,复盘不再走过场 | FR-PMR / FR-AI | P1 | 已实现 |
| [US-OPS-07](#us-ops-07) | 审计有底账,导出不静默截断 | FR-SEC | P1 | 已实现 |
| [US-OPS-08](#us-ops-08) | 成员离职交接预检,不留幽灵值班人 | FR-ADM | P2 | 已实现 |
| [US-OPS-09](#us-ops-09) | 平台自身低成本可托管、坏了自己会喊 | NFR-DEP / FR-OBS | P1 | 已实现 |
| [US-OPS-10](#us-ops-10) | 个人级值班负载与公平度报表 | FR-RPT / FR-ONC | P2 | **规划中** |

---

<a id="us-ops-01"></a>
## US-OPS-01 告警风暴归拢与维护窗口降噪

**故事**:作为运维主管,我想要重复告警自动去重、同源告警聚合成一个处理单元、计划内维护自动静音,以便团队从「群消息轰炸」回到「一次故障只处理一件事」,不再对告警麻木。

**场景叙事**:周三凌晨 2:40,交易库主从切换。换成 Vigil 之前,这种场景是 Prometheus 和业务脚本一起开火,钉钉群 40 分钟刷 400 条,值班的小陈直接把手机扣在桌上。现在:同指纹的重复告警在去重窗口内只算一次;主从切换牵连的十几条同服务同级别告警,在聚合窗口内并进同一个 Incident——小陈手机上只响了一次,卡片上写着「已聚合 17 条事件」。再往前一周,机房网络割接,张磊提前在维护窗口页面建了一条 maintenance 抑制规则,填好起止时间,割接期间物流线的 warning 全部静音、到点自动失效;他特意确认了 `preserve_critical` 开着——真出大事,critical 照样穿透进来。第二天他在事件列表里还能翻到被抑制的原始 Event,一条都没丢。

**验收标准**:

- Given 同一告警源在去重窗口(默认 5 分钟,`VIGIL_TRIAGE_DEDUP_WINDOW` 可调)内重复发送同指纹告警,When 告警接入,Then 只触发一次后续处理,不重复通知。
- Given 同 service + severity 已存在活跃 Incident,When 新 Event 在聚合窗口(5 分钟)内到达,Then 并入既有 Incident 而非新建。
- Given 已配置 kind=maintenance 的抑制规则(带起止时间窗),When 窗口内匹配的非 critical 告警到达,Then 被抑制,且规则到期自动失效。
- Given `preserve_critical` 保持默认开启,When critical 告警命中抑制规则,Then 不被抑制,照常进入处理流程。
- Given Event 被去重或抑制,When 在事件列表查看,Then 原始 Event 仍可查询(不静默丢弃)。

**关联**:FR-TRI(去重 / 抑制 / 聚合);[ADR-0012](../adr/0012-triage-three-stage-pipeline.md)、[ADR-0010](../adr/0010-event-incident-separation.md);优先级 **P0**。

---

<a id="us-ops-02"></a>
## US-OPS-02 升级链自动接力,不靠人肉在群里吼

**故事**:作为运维主管,我想要为每个关键服务配置多层升级策略,值班人超时未确认就自动升到下一层直至全团队,以便「没人响应」这件事永远不会以业务方打我手机的方式暴露。

**场景叙事**:张磊给支付服务绑了三层策略:L1 当班人,5 分钟未 ack 按 `repeat_times` 再催一轮;仍无响应升 L2 资深值班;再不行 L3 通知全团队。上线后第一次真实触发是个周六凌晨:小陈手机静音,5 分钟后系统自己催了第二遍,又过 5 分钟 L2 的老周被叫醒,点了 ack——升级链就地停住,后面没有任何人被打扰。周一站会张磊看时间线:每一跳都有记录,而他本人整晚没接到一个电话。他现在只关心一件事:这条链本身够不够可靠——演练时他手动清过一次 Redis,对账巡检两分钟内把丢失的升级计时器重排了回来。

**验收标准**:

- Given Service 显式绑定了升级策略(策略不从父服务继承),When Incident 创建后当前层在 `delay_minutes` 内无人 ack,Then 按 `repeat_times` 在本层重复通知,用尽后自动推进到下一层。
- Given 任一层的目标在 Web 或 IM 侧 ack,When ack 成功,Then 后续待触发的升级任务被取消,不再打扰其他层级。
- Given ack 与升级触发发生竞态,When 升级任务被误触发,Then 状态守卫保证已 ack/resolved 的 Incident 不产生升级动作。
- Given Redis 数据丢失(如未开持久化的重启),When 升级对账巡检(默认 2 分钟,`VIGIL_ESCALATION_SWEEP_INTERVAL` 可调)运行,Then 活跃 Incident 缺失的升级任务被自动重排,不静默断链。
- Given 末级目标配置为 team,When 升级推进到末级,Then 全团队被通知,保证最终有人响应。
- Given 升级链每次触发与取消,Then 时间线(TimelineItem)留有可追溯记录。

**关联**:FR-ESC(升级)、FR-ONC(升级目标解析排班);[ADR-0016](../adr/0016-escalation-asynq-delayed.md)、[ADR-0015](../adr/0015-schedule-realtime-no-snapshot.md);优先级 **P0**。

---

<a id="us-ops-03"></a>
## US-OPS-03 排班透明、换班自助、空班有告警

**故事**:作为运维主管,我想要排班表对全员可见、顶班由顶班人自助登记并立即生效、解算不出在班人时系统立即告警,以便值班公平有据可查,升级永远找得到「此刻真正在班的人」。

**场景叙事**:以前排班是张磊每月底在 Excel 里手排,错漏和抱怨都冲他来。现在三条业务线各一份 Schedule,轮换规则(rotation)配好后系统自己转;任何人打开值班页能看到未来 30 天的日历预览,谁的班一目了然。上周五小陈家里有事,和老周私下说好换班——以前这种口头约定是事故温床,现在老周自己在值班页上登记了一条 Override:「这个时段我来顶班」。顶班人是他本人,凭值班角色自带的 `schedule.override` 权限即可提交,立即生效:当晚真来了告警,升级引擎实时算出来的在班人就是老周,一秒都没找错人。小陈也试过反过来直接把班「指」给别人,系统拒绝了——把他人设为顶班人是管理级动作,须由持 `schedule.update` 的团队管理员或值班长操作,自助的边界画在「顶班人是不是本人」。月初有一次调整轮换参数后,某个周日凌晨没人在班——当晚一条事件升级找人时解算不出任何在班人,空班告警当场触发,张磊半夜被叫起来把班补上。他清楚这套检测的边界:告警在实际发生在班解算(升级找人、发送通知、值班查询)时触发,不会提前巡检未来的排班空档——想提前发现,目前靠日历预览人工核对(未来空档主动巡检属**规划中**,落地前须先立 ADR)。

**验收标准**:

- Given Schedule 配置了轮换规则,When 查询 `GET /schedules/:id/preview`,Then 按天返回代表时刻的在班人(普通轮换取当天正午解算;follow_the_sun 按每 4 小时采样合并当天接力各层),可供日历展示;一天多班次的班次级预览暂不提供。
- Given 工程师 A 与 B 协商换班,When B 以本人为顶班人创建 Override(顶班人 == 操作者本人,仅需 `schedule.override` 权限),Then Override 立即生效。
- Given Override 已生效,When 该时段有 Incident 触发升级或通知,Then 目标解析为 Override 后的在班人(实时计算,无快照漂移)。
- Given 仅持有 `schedule.override` 权限的用户,When 创建 Override 时把他人设为顶班人,Then 被拒绝(代他人登记顶班须叠加 `schedule.update`,团队管理员/值班长具备)。
- Given 某时段所有层都算不出在班人,When 该时段实际发生在班解算(升级找人、发送通知或值班查询),Then 触发空班告警,而非静默跳过;日历预览不触发告警,对未来空档的周期性主动巡检属**规划中**。

**关联**:FR-ONC(排班);[ADR-0015](../adr/0015-schedule-realtime-no-snapshot.md);优先级 **P0**。

---

<a id="us-ops-04"></a>
## US-OPS-04 在钉钉里处置,权限与 Web 完全一致

**故事**:作为运维主管,我想要团队直接在钉钉群里认领和处置告警,且 IM 里的每个操作走与 Web 完全相同的权限校验,以便处置效率提上去的同时,IM 不会变成绕过权限管控的后门。

**场景叙事**:告警卡片现在直接推进值班群:标题、级别、影响服务,下面一排操作按钮。小陈在地铁上用手机点「认领」,卡片状态当场刷新(钉钉平台不支持原地改卡片,系统重发一条带状态徽章的新消息,这点张磊验收时专门确认过是平台限制而非 bug)。让张磊真正放心的是权限这层:发进值班群的是同一张卡片,按钮按代表接收者(当班值班人)的权限渲染——新来的实习生在群里同样看得到带按钮的卡片,但他真点了「解决」,操作在回调侧被权限校验拒绝,审计日志里留了一条拒绝记录;有一次外包同事想替忙不过来的值班「顺手」关个单——账号没绑定 IM,同样被拒并留痕。张磊验收时想明白了一层:卡片上按钮显不显示只是体验层的裁剪,真正的红线是回调侧的硬鉴权——「在群里」不等于「有权限」,这让他敢把处置面全面搬进 IM。

**验收标准**:

- Given 用户已绑定 IM 账号且在事件所属团队持有 `incident.ack` 权限,When 在 IM 卡片点认领,Then 走与 Web 相同的鉴权链路放行,产生状态变更、TimelineItem 与 `via=im` 的 IncidentAction 记录。
- Given 用户未绑定 IM 账号,When 在 IM 发起任何处置操作,Then 操作被拒绝。
- Given 告警卡片发往群聊,Then 按钮按代表接收者(首个通知目标,即当班值班人)的权限裁剪一次,全群成员看到同一张卡片(不做逐观看者裁剪);权限硬校验在回调侧——无对应权限的用户点击按钮(或伪造回调),操作被拒绝并记入审计。
- Given 事件状态在 Web 侧变更,Then IM 侧卡片同步刷新(飞书原地更新;钉钉以重发带状态徽章的新消息呈现)。
- Given 支持的 IM 平台,Then 为飞书与钉钉两个平台(企业微信不在支持范围,见 [ADR-0037](../adr/0037-trim-deferred-features.md))。

**关联**:FR-IM(IM 协同)、FR-SEC(RBAC);[ADR-0018](../adr/0018-im-same-rbac-as-web.md)、[ADR-0019](../adr/0019-imbot-pluggable-degradation.md)、[ADR-0027](../adr/0027-rbac-permissions-roles.md);优先级 **P0**。

---

<a id="us-ops-05"></a>
## US-OPS-05 月度例会拿得出 MTTA/MTTR 与降噪率

**故事**:作为运维主管,我想要平台内置按时间窗统计的 MTTA、MTTR、降噪率、团队负载与趋势报表并支持 CSV 导出,以便月度经营例会上用数据回答「运维这个月做得怎么样」,而不是拍脑袋。

**场景叙事**:月底例会前一晚,张磊打开报表仪表盘,把时间窗调到近 30 天:MTTA 4 分 12 秒(上月 11 分钟),MTTR 38 分钟,告警总量 1.2 万条、实际通知不到 3 千——降噪率 76%。他把告警度量和团队负载各导了一份 CSV 贴进 PPT。会上交易线的 TL 质疑「你们是不是把我们的告警压掉了」,张磊当场把 team 级视图切给他看——对方的账号本来也只看得到自己团队的数据,这是权限模型保证的,不是口头承诺。散会后老板只说了一句:「下个月继续用数据说话。」

**验收标准**:

- Given 持有 `analytics.view` 权限,When 访问 `/analytics/dashboard`(可带时间窗参数),Then 返回窗口期内事件量、severity 分布、MTTA(acked_at − created_at 均值)、MTTR(resolved_at − created_at 均值)等汇总指标。
- Given 告警经过分诊管线,When 查看 `/analytics/alerts`,Then 可见降噪率(1 − 实际通知量 / 告警总量)。
- Given 需要按团队看负载,When 查看 `/analytics/team-load`,Then 返回各团队时间窗内的 Incident 数。
- Given 需要离线汇报,When 调用 `/analytics/{alerts,incidents,team-load,postmortems}/export`,Then 得到对应 CSV 文件。
- Given 用户仅有 team 级角色绑定,Then 报表只呈现其所属团队数据;org 级绑定可见全局。

**关联**:FR-RPT(报表分析;降噪率属 [`../requirements.md`](../requirements.md) §四成功指标,由 FR-RPT-4 承载运行期度量,基线见 [ADR-0002](../adr/0002-product-positioning.md));[ADR-0029](../adr/0029-dual-audit-no-silent-truncation.md)(处置审计 `via` 字段逐条落库;内置渠道占比报表属**规划中**,现状与局限见 [US-OPS-07](#us-ops-07));优先级 **P1**。

---

<a id="us-ops-06"></a>
## US-OPS-06 critical 复盘闸门,复盘不再走过场

**故事**:作为运维主管,我想要 critical 事件解决后强制走复盘流程才能关单、AI 起草初稿降低写作负担、改进项推到工单系统被跟踪,以便复盘从「开个会就散」变成有产出、有闭环的制度。

**场景叙事**:支付超时那次 critical 处理完,小陈想顺手把单子关了——系统拒绝:「critical 事件须先完成复盘(发布)或显式跳过复盘后才能关闭。」他点开系统自动建好的复盘草稿:时间线已经自动填充,摘要和根因分析是 AI 起草的,每个字段都标着「AI 起草」并附引用的时间线证据。小陈逐字段过了一遍,改掉两处不准的表述,提交评审;老周评审通过后发布。三条改进项经工单集成推到了公司的工单系统,回写了工单链接——下次季度回顾,张磊按链接就能查每条做没做。有一次确实是演练误报,值班长用「跳过复盘」放行了关单——跳过复盘是治理动作,须持 `postmortem.update` 权限(内置角色中 responder_lead/team_admin 具备,普通值班不具备),动作本身也留了痕。张磊每周一还会用「待复盘」过滤器扫一遍积压,谁的复盘拖着,站会上直接点名。

**验收标准**:

- Given critical Incident 被 resolved,Then 系统自动创建 draft 复盘,AI 起草字段标注来源并附 evidence;无 evidence 的 AI 内容不展示。
- Given critical Incident 的复盘未 published/archived 且未显式跳过,When 尝试 close,Then 被拒绝(failed_precondition)并给出明确提示。
- Given 确认为误报或演练,When 持 `postmortem.update` 权限者显式调用 skip-postmortem(复盘治理决策,非仅 `incident.close`,口径与 [`project-manager.md`](./project-manager.md) US-PM-07 一致),Then 放行关单,且跳过动作可追溯。
- Given AI 起草的字段,When 复盘人逐字段 accept/edit/reject,Then 未经人确认的 AI 内容不生效(HITL)。
- Given 复盘产出 ActionItem 且已配置工单集成,When 推送外部工单,Then 回写 tracker_url 可跳转跟踪。
- Given 想盯复盘积压,When 以 `pending_postmortem=true` 过滤事件列表,Then 可见「resolved 后停在待复盘」的 critical 单。

**关联**:FR-PMR(复盘)、FR-AI(AI 起草)、FR-INT(工单外接);[ADR-0026](../adr/0026-postmortem-ai-draft.md)、[ADR-0022](../adr/0022-aiinsight-hitl-evidence.md);优先级 **P1**。

---

<a id="us-ops-07"></a>
## US-OPS-07 审计有底账,导出不静默截断

**故事**:作为运维主管,我想要管理操作与处置操作双轨留痕、支持按条件导出 CSV 且超限时明确告知,以便安全合规检查时拿得出完整、可信的操作底账。

**场景叙事**:年度安全审计,审计员开出清单:「近一年谁修改过升级策略?有多少处置操作发生在 IM 里?权限拒绝有没有记录?」放在两年前这些问题只能靠翻群聊记录。现在张磊分两路取数:管理侧,审计日志里角色变更、集成 token 轮换、策略改动一条条都在;处置侧,每个 Incident 的操作记录带 `via` 字段,抽查任何一单都能回答「这步操作来自 IM 还是 Web」。审计员还想要一个年度汇总——「IM 操作占比多少」,张磊在数据库里按 `via` 聚合出 83%,并在报告里如实注明口径:`via` 逐条落库、数据可信,但平台暂无内置的渠道占比报表,也无 IncidentAction 批量导出,汇总须经逐事件接口遍历或数据库统计得出。导全年管理审计数据时第一次拉了个大时间窗,响应头带回 `X-Vigil-Truncated: true`,他按季度分四段重导,拼出完整年度底账——系统宁可明确告诉他「截断了」,也不给一份看起来完整实际缺数据的文件。

**验收标准**:

- Given 持有 `admin.audit.view`(org 级授予),When 按条件查询 audit-logs,Then 角色变更、集成配置、token 轮换等管理操作可查。
- Given IM 内发生处置操作,Then IncidentAction 记录 `via=im`;渠道维度汇总现阶段经逐事件接口(`GET /incidents/:id/actions`)或数据库统计得出——内置渠道占比报表与 IncidentAction 批量导出属**规划中**(落地前须先立 ADR)。
- Given 无权限的 Web/IM 操作被拒绝,Then 拒绝事件本身记入审计。
- Given 导出行数达到 50000 上限,When 调用 `GET /audit-logs/export`,Then 响应头置 `X-Vigil-Truncated: true` 并记 warn 日志,绝不静默截断。
- Given 需要超过单次上限的数据,When 按时间窗分段多次导出,Then 可拼合出完整数据集。

**关联**:FR-SEC(审计);[ADR-0029](../adr/0029-dual-audit-no-silent-truncation.md)、[ADR-0027](../adr/0027-rbac-permissions-roles.md);优先级 **P1**。

---

<a id="us-ops-08"></a>
## US-OPS-08 成员离职交接预检,不留幽灵值班人

**故事**:作为运维主管,我想要在禁用离职成员账号前一键看到其所有待交接项(排班、改进项、角色、IM 绑定),以便人走之后不会出现「升级打给已离职的人」这种事故。

**场景叙事**:老王提了离职。上一次有人离职,排班表里的名字忘了摘,两周后一条凌晨告警按排班升级到了他——电话自然没人接,链路白白多空转了十分钟。这次张磊在用户管理里点开老王的交接预检:他还在物流线的主值班表里、名下挂着两条没关闭的复盘改进项、一个 team_admin 角色绑定、一个钉钉账号绑定,四类清单一目了然。张磊按单子逐项处理:排班参与人替换成新人、改进项转给老周、角色回收,最后禁用账号——清单清空,系统不再提示。整个过程十分钟,没有靠任何人的记性。

**验收标准**:

- Given 成员即将离职,When 查询 `GET /users/:id/handover-preview`,Then 返回其参与的排班、未完成 ActionItem、角色绑定、IM 绑定四类清单及 `has_items` 汇总标志。
- Given 存在待交接项(`has_items=true`),When 管理员在界面上发起禁用,Then 先看到交接提示而非直接禁用。
- Given 四类清单均为空,Then `has_items=false`,可径直禁用账号。

**关联**:FR-ADM(用户与团队管理)、FR-ONC(排班交接);[ADR-0027](../adr/0027-rbac-permissions-roles.md)、[ADR-0015](../adr/0015-schedule-realtime-no-snapshot.md);优先级 **P2**。

---

<a id="us-ops-09"></a>
## US-OPS-09 平台自身低成本可托管、坏了自己会喊

**故事**:作为运维主管,我想要告警平台本身部署轻、可观测、升级可回滚、故障时能通过独立通道自告警,以便它不会成为团队要额外伺候的又一个「服务」,数据也不出公司网。

**场景叙事**:选型时张磊把丑话说在前面:「谁引入的系统谁伺候,我们没有多余的人。」POC 那天他在一台闲置 Docker 主机上按运维手册起了 Compose——vigil、postgres、redis 三个容器,半小时接入了第一条 Prometheus 告警,数据全在内网。上线后他把 `/metrics` 接进了自家 Prometheus,队列深度、通知成败一目了然;又开了自监控并按文档配了独立通知通道——他注意到一个细节:自告警刻意不走 IM,「IM 挂了的时候用 IM 告诉你 IM 挂了」这种循环设计上就被排除了。第一次升级版本前,他按手册先跑了备份脚本再动手——手册把话讲得很直白:不提供逆向迁移,升级前的备份就是回滚的唯一前提。他反而觉得踏实:边界清楚,好过虚假承诺。

**验收标准**:

- Given 一台安装 Docker 的主机,When 按 [`../operations.md`](../operations.md) 执行 Compose 部署,Then vigil + postgres(pgvector)+ redis 三容器启动,平台可用。
- Given 平台运行中,Then `/metrics` 暴露 HTTP、队列(含死信)、接入、通知等指标可被外部 Prometheus 抓取,`/health` 可作存活探针。
- Given 已开启自监控(selfmon,默认关闭)且配置了独立通知通道,When 队列积压或通知失败率超阈,Then 经排除 IM 的独立通道自告警 org_admin。
- Given 开启 selfmon 但独立通道未配置,Then 启动即记 warn 日志,不假装闭环(平台与进程共生死,须外部监控兜底)。
- Given 升级前已执行备份,When 升级失败,Then 按固定处置序列(stop → 恢复备份 → 部署回旧版本 → start)回滚到备份点;系统不提供逆向迁移,备份点之后的数据随回滚丢失属已知边界。

**关联**:NFR-DEP(部署门槛)、FR-OBS(自监控)、NFR-RELY(可靠性);[ADR-0031](../adr/0031-single-binary-compose-helm.md)、[ADR-0032](../adr/0032-migration-backup-restore.md)、[ADR-0033](../adr/0033-selfmon-and-auth.md);优先级 **P1**。

---

<a id="us-ops-10"></a>
## US-OPS-10 个人级值班负载与公平度报表(规划中)

> **规划中**:本故事描述的是未实现的期望能力。当前报表的团队负载仅到团队粒度(时间窗内各团队 Incident 数,见 `/analytics/team-load`),**不存在**个人粒度的值班时长、被叫次数统计。落地前须按需求变更机制先立 ADR,再回写 [`../requirements.md`](../requirements.md) 与本文。

**故事**:作为运维主管,我想要按人统计值班时长、被通知次数、夜间被叫次数,以便用数据回应「排班不公平」的抱怨,并在一对一沟通里识别谁快被值班拖垮了。

**场景叙事**(期望):季度一对一,小陈欲言又止地说「感觉我总是被叫得最多」。张磊希望那时他能打开报表下钻到人:过去 90 天每人的值班小时数、被通知次数、其中落在夜间静默时段的次数——如果数据证实小陈的夜间被叫次数是团队均值的两倍,他就调整轮换参数,并把这页报表贴在下次团队会上;如果数据不支持,也能坦诚地把数字摊开谈。公平不公平,让数据说话,而不是比谁嗓门大。

**验收标准**(以规划中口径,落地时须细化):

- Given 该能力落地,When 查看团队负载报表,Then 可下钻到个人:时间窗内值班时长、被通知次数、夜间(静默时段)被叫次数。
- Given 需要离线分析,Then 个人级负载数据支持 CSV 导出,权限继承报表现有的 team/org 两级 scope 隔离。
- Given 该需求推进,Then 先新增 ADR 记录设计取舍(如「夜间」的时区口径、与排班蓝图实时计算的关系),不得绕过需求变更机制。

**关联**:FR-RPT(报表分析)、FR-ONC(排班公平);暂无对应 ADR(落地前须新增);优先级 **P2**。

---

## 需求追溯

| 故事 | 关联 FR/NFR 域 | 主要 ADR |
|------|----------------|----------|
| US-OPS-01 | FR-TRI | [0012](../adr/0012-triage-three-stage-pipeline.md)、[0010](../adr/0010-event-incident-separation.md) |
| US-OPS-02 | FR-ESC、FR-ONC | [0016](../adr/0016-escalation-asynq-delayed.md)、[0015](../adr/0015-schedule-realtime-no-snapshot.md) |
| US-OPS-03 | FR-ONC | [0015](../adr/0015-schedule-realtime-no-snapshot.md) |
| US-OPS-04 | FR-IM、FR-SEC | [0018](../adr/0018-im-same-rbac-as-web.md)、[0019](../adr/0019-imbot-pluggable-degradation.md)、[0027](../adr/0027-rbac-permissions-roles.md) |
| US-OPS-05 | FR-RPT(降噪率等成功指标由 FR-RPT-4 承载) | [0002](../adr/0002-product-positioning.md)、[0029](../adr/0029-dual-audit-no-silent-truncation.md) |
| US-OPS-06 | FR-PMR、FR-AI、FR-INT | [0026](../adr/0026-postmortem-ai-draft.md)、[0022](../adr/0022-aiinsight-hitl-evidence.md) |
| US-OPS-07 | FR-SEC | [0029](../adr/0029-dual-audit-no-silent-truncation.md)、[0027](../adr/0027-rbac-permissions-roles.md) |
| US-OPS-08 | FR-ADM、FR-ONC | [0027](../adr/0027-rbac-permissions-roles.md)、[0015](../adr/0015-schedule-realtime-no-snapshot.md) |
| US-OPS-09 | NFR-DEP、FR-OBS、NFR-RELY | [0031](../adr/0031-single-binary-compose-helm.md)、[0032](../adr/0032-migration-backup-restore.md)、[0033](../adr/0033-selfmon-and-auth.md) |
| US-OPS-10(规划中) | FR-RPT、FR-ONC | 暂无(落地前须新增) |
