# 用户故事:架构师视角

| 字段 | 内容 |
|------|------|
| **文档类型** | 用户故事(角色:企业基础架构师) |
| **编号空间** | US-ARC-01 ~ US-ARC-10;关联需求编号见 [`../requirements.md`](../requirements.md) |
| **日期** | 2026-07-15 |
| **相关** | [`../requirements.md`](../requirements.md)、[`../architecture.md`](../architecture.md)、[`../operations.md`](../operations.md)、[`../extending.md`](../extending.md)、[ADR 索引](../adr/README.md) |

> **单一信源声明**:本文只描述用户视角的场景与验收,不复制架构决策的理由——"为什么这么定"见对应 [ADR](../adr/README.md);实体与字段以 `ent/schema/` 为准,权限点以 `internal/auth/permission.go` 为准,文中出现的权限点名称仅为举例引用。
> 除明确标注「**规划中**」的条目外,所有故事均基于当前代码已实现的能力撰写。

---

## 角色画像:王芳,企业基础架构师

**背景**:王芳在一家约 1200 人的证券信息技术子公司做了六年基础架构,负责公司级技术选型评估与平台集成落地。公司监控体系是典型的"多年沉积"形态:Prometheus + Grafana 管 K8s 和主机层,一套自研巡检平台管交易链路,数据库团队还留着一批发邮件的老告警脚本。办公协同全员用钉钉。合规红线明确:生产数据不出内网,任何 SaaS 化的告警平台在评估第一轮就会被否掉。

**目标**:

- 为公司选定一个可自托管的告警处置平台,把三套监控源的告警收敛到一处,打通钉钉协同;
- 平台要能被她的团队"接得住":部署拓扑清晰、故障模式可预判、容量可规划、升级路径可演练;
- 集成和扩展要有明确的边界与文档——她的团队会做二次开发(自研巡检平台接入、Jenkins 处置作业对接)。

**痛点**(来自选型访谈记录):

- "上一个开源方案我们试了两周,文档说支持高可用,真拆开看是单点 + 一句'建议自行部署多实例',故障模式全靠猜。"
- "AI 功能听起来都好,但我第一个问题永远是:调的谁的模型?告警内容出不出网?"
- "钉钉集成十个产品九个只做通知推送,点进去还是要开电脑连 VPN。半夜值班同事最恨这个。"
- "升级最怕'不可逆迁移 + 没有回滚剧本'。我要的不是承诺,是能照着演练一遍的手册。"

---

## US-ARC-01 内网 PoC:Compose 一键部署,数据不出网

**故事**:作为企业基础架构师,我想要在隔离内网环境用 Docker Compose 一次性拉起完整系统,以便在不接触外网、不提交任何数据的前提下完成选型 PoC。

**场景叙事**:周二上午,王芳在评估环境(一台 8C16G 内网虚机,只有内部镜像仓库可达)开始 Vigil 的 PoC。她先让同事把 `vigil`、`pgvector/pgvector:pg16`、`redis` 三个镜像同步进内部仓库——只有三个,这让她有点意外,上一个候选产品要九个。照着 [`operations.md`](../operations.md) §2,她改了 compose 文件里的镜像地址、填上 JWT 密钥,先 `docker compose run --rm vigil migrate` 完成建表,再 `docker compose up -d`,前端页面就直接可用了——前端静态资源是编译期打进二进制的,不需要单独的 Nginx 或 CDN。她特意抓了半小时容器出网流量确认无外联,然后在 PoC 报告里写下第一条结论:"部署面 = 一个镜像 + 两个依赖,符合数据不出网要求;注意 PostgreSQL 需带 pgvector 扩展,是相似检索的前置,内部仓库需固定同步 pgvector 镜像。"

**验收标准**:

- Given 一个仅能访问内部镜像仓库的隔离网络环境,When 按 [`operations.md`](../operations.md) §2 先执行 `vigil migrate` 建表、再部署 Docker Compose 三容器(vigil / postgres / redis),Then 系统完整可用(接入、处置、前端界面),全程无外网依赖。
- Given 未配置任何 LLM 的 API Key,When 系统运行,Then 告警接入 → 分诊 → 通知的主流程不受影响,AI 功能自动降级不报致命错误。
- Given 部署使用不含 pgvector 扩展的 PostgreSQL,When 使用相似事件检索,Then 系统降级为 LIKE 文本匹配而非报错中断。
- Given 部署完成,When 巡查容器网络连接,Then 除部署方自行配置的出向集成(如 LLM、webhook)外,系统自身不产生任何外联请求。

**关联**:NFR-DEP、NFR-SEC | [ADR-0031](../adr/0031-single-binary-compose-helm.md)、[ADR-0006](../adr/0006-primary-store-postgresql.md)、[ADR-0002](../adr/0002-product-positioning.md) | **P0**

---

## US-ARC-02 存量监控接入:Prometheus / Grafana / 自研平台 / 邮件老脚本

**故事**:作为企业基础架构师,我想要把公司现有的 Prometheus、Grafana、自研巡检平台和邮件告警脚本全部接入同一个平台,以便告警收敛到一处处置,而不是逼各团队改造监控端。

**场景叙事**:PoC 第三天,王芳约了监控组的小陈做接入验证。Prometheus 和 Grafana 走内置适配器,在接入向导里创建接入点、把生成的 `POST /api/v1/webhook/{token}` 地址填进 Alertmanager 的 receiver,用向导第 4 步的样例 payload 干跑测试通过。自研巡检平台没有现成适配器,小陈本以为要写代码,结果用通用 webhook 类型直接接入,平台侧只改了一行推送 URL。最麻烦的是数据库组那批只会发邮件的老脚本——Vigil 有内置 SMTP 入向(默认关闭),开启后把告警邮件的收件地址设成 `{token}@vigil.internal` 就能收进来,从邮件主题解析严重度。王芳在报告里记下两条:接入解耦设计(先落库、秒级返回 202)意味着下游处理慢也不会拖垮 Alertmanager 的推送;单接入点默认限流 600/min,超出会返回 429,需按源站告警量在接入点配置里逐个覆盖。

**验收标准**:

- Given 一个 Prometheus Alertmanager,When 将其 webhook receiver 指向 Vigil 接入点 URL 并触发告警,Then 告警被归一化为 Event(severity 归一为 critical/warning/info),原始 payload 完整保留可查。
- Given 一个无内置适配器的自研监控系统,When 用通用 webhook 类型接入点接收其 JSON 推送,Then 告警正常进入分诊管线,无需修改 Vigil 代码。
- Given SMTP 入向已开启(`VIGIL_INGESTION_SMTP_IN_ENABLED`,默认关闭,监听 `:2525`)且存在 `type=email` 的接入点,When 向"收件地址 local part = 接入点 token"的地址发送告警邮件,Then 邮件走与 webhook 相同的接入链路生成 Event;token 不匹配的收件人在 RCPT 阶段被拒收。
- Given 接入并发超过接入点限流阈值,When 上游继续推送,Then 系统返回 429 而非静默丢弃;队列积压时返回 503 但 payload 已先落库,恢复后可回灌。
- Given 一条告警的 token 鉴权失败,When 请求到达,Then 返回 401 且不落库,但留有审计记录(防探测)。

**关联**:FR-ING、FR-INT | [ADR-0011](../adr/0011-ingestion-decoupled-idempotent.md)、[ADR-0038](../adr/0038-smtp-inbound.md)、[ADR-0009](../adr/0009-pluggable-integrations.md) | **P0**

---

## US-ARC-03 钉钉落地:IM 是协同工作面,且与 Web 同一套 RBAC

**故事**:作为企业基础架构师,我想要值班同事在钉钉里直接认领、处置告警,并且 IM 侧操作走与 Web 完全相同的权限校验,以便把钉钉从"又一个通知渠道"变成真正的处置工作面,而不留下权限后门。

**场景叙事**:接入验证通过后,安全部的老李加入评审,他只问一个问题:"钉钉里点按钮,鉴权走哪里?"王芳带他做了组实验:先用一个未绑定钉钉账号的测试用户在群里点"认领",被拒绝;绑定账号后再点,操作成功且时间线记录 `via=im`;再用一个只有查看权限的账号看同一张群卡片——群卡片全群共享一张,按钮按代表接收者(当班值班人)的权限裁剪一次,只读账号看到的仍是这张带按钮的卡片,但他点"认领"在回调侧被权限校验拒绝,审计里留下一条拒绝记录。老李起初对"看得见按钮"皱了眉,追问后确认了真正的安全边界:按钮显不显示只是体验层裁剪,权威判定在回调侧硬鉴权——即便绕过卡片直接构造回调,也一样被拒并留痕。他翻了 [ADR-0018](../adr/0018-im-same-rbac-as-web.md) 后在评审意见里写:"IM 回调经账号映射到平台用户,复用 Web 同一鉴权链路,无旁路,通过。"王芳补充了一条平台差异备注:钉钉无法原地刷新卡片,状态变更靠重发带状态徽章的新消息,群里会多几条消息,这是钉钉开放平台的限制,不是缺陷;若未来公司换飞书,同一套机制支持原地更新。

**验收标准**:

- Given 一个未绑定 IM 账号的用户,When 其在钉钉群卡片上执行任何处置操作,Then 操作被拒绝,不产生任何状态变更。
- Given 事件卡片发往值班群,When 渲染卡片按钮,Then 按代表接收者(首个可解析 user_id 的通知目标,即当班值班人)的权限裁剪一次,全群成员看到同一张卡片,不做逐观看者裁剪(权威口径见 [`../requirements.md`](../requirements.md) FR-IM-4)。
- Given 一个已绑定但对该事件所属团队无处置权限的用户,When 点击群卡片上的操作按钮(或构造回调强行触发),Then 操作由回调侧硬鉴权拒绝并记审计,不产生任何状态变更。
- Given 一个有权限的用户在钉钉里认领事件,When 操作完成,Then 事件状态变为 acked、升级任务被取消、时间线新增 `via=im` 的操作记录,Web 端实时可见同一状态。
- Given 平台为钉钉,When 事件状态变更,Then 以重发带状态徽章的新消息呈现最新状态(平台降级矩阵行为),告警不因平台能力缺失而静默丢失。

**关联**:FR-IM、FR-SEC | [ADR-0018](../adr/0018-im-same-rbac-as-web.md)、[ADR-0019](../adr/0019-imbot-pluggable-degradation.md)、[ADR-0037](../adr/0037-trim-deferred-features.md) | **P0**

---

## US-ARC-04 故障模式演练:Redis 宕机、worker 崩溃时不丢告警

**故事**:作为企业基础架构师,我想要在上线前演练平台自身的关键故障模式(Redis 宕机、worker 崩溃、通知通道故障),以便确认"告警平台自己出问题时不丢告警",并明确外部监控如何兜底。

**场景叙事**:上线评审前一周,王芳组织了半天的故障演练。第一项直接 `docker stop` Redis:接入请求仍被接收并先落 PostgreSQL,Redis 恢复后积压的原始告警被回灌处理;演练记录里她特别标注——升级引擎有对账巡检(默认 2 分钟一轮,`VIGIL_ESCALATION_SWEEP_INTERVAL`),Redis 里丢失的延迟升级任务会按数据库里的"应然"状态自动重排,不会出现"没人被叫"的静默失效。第二项 kill 掉进程再拉起:队列任务持久化在 Redis,重启后继续消费,at-least-once 语义下靠幂等键不重复升级、不重复通知。第三项拔掉邮件通道:通知按有序降级链逐通道尝试,整链失败会落 `failed` 并兜底告警管理员,不静默。最后是她最在意的问题:"谁来监控守夜人?"结论写进部署方案——自监控(selfmon)默认关闭且与进程共生死,必须按 [`operations.md`](../operations.md) §9 用公司 Prometheus 抓 `/metrics` 做外部兜底;半夜的电话强提醒则经出向 webhook 对接公司自建语音网关(平台刻意不内置电话/短信通道)。

**验收标准**:

- Given Redis 完全不可用,When 告警持续推送进来,Then 接入层仍接收并将 payload 先落 PostgreSQL,Redis 恢复后自动回灌处理,期间无告警被内存丢弃。
- Given Redis 数据丢失(如故障后空库重启),When 存在未 ack 的活跃事件,Then 升级对账巡检在一个周期内(默认 2 分钟)从数据库状态重排升级任务,升级链不断链。
- Given vigil 进程(worker)崩溃重启,When 队列中存在待处理任务,Then 任务从 Redis 恢复继续执行,且升级/通知因幂等键不重复动作。
- Given 某通知通道故障,When 触发通知,Then 按降级链尝试下一通道;整链失败时状态落 `failed` 并向管理员发出兜底告警(走非 IM 通道),失败可查,重试耗尽的任务进 archived 死信可重放(suppressed 的补发端点属规划中,口径见 [`../requirements.md`](../requirements.md) FR-NTF-2)。
- Given 外部 Prometheus 抓取 `/metrics`,When 队列积压或通知失败率异常,Then 可依据 [`operations.md`](../operations.md) §9 的告警规则在 Vigil 自身故障时收到外部告警。

**关联**:NFR-RELY、NFR-OBS | [ADR-0007](../adr/0007-async-tasks-asynq.md)、[ADR-0016](../adr/0016-escalation-asynq-delayed.md)、[ADR-0017](../adr/0017-notification-fallback-chain.md)、[ADR-0033](../adr/0033-selfmon-and-auth.md) | **P0**

---

## US-ARC-05 生产拓扑:从 Compose 迁到 Helm,容量按实测规划

**故事**:作为企业基础架构师,我想要一条从单机 Compose 到 Kubernetes/Helm 的明确迁移路径,以及可复现的容量基线,以便按公司告警量规划生产部署并给出扩容依据。

**场景叙事**:PoC 通过后进入生产方案设计。王芳统计了全公司峰值告警量:约 400 events/min,大促演练时冲到 700。对照 [`operations.md`](../operations.md) §10 的实测基线——1000 events/min 达标、余量约 2 倍——单实例够用,但她还是按公司规范上了 K8s。`deploy/helm/` 的 Chart 让她省了不少事:数据库迁移做成了 pre-install/pre-upgrade hook Job,helm upgrade 时先跑迁移、失败则新版本不滚动上线(fail-fast),不需要人肉 kubectl exec。方案评审时开发负责人问"能不能直接上三副本",王芳翻出文档如实回答:"Helm 路径当前验证的是单进程单副本;api/worker 拆分、多副本 WebSocket 广播的代码已实现但未做端到端验证,属路线图。我们一期单副本 + PDB + 外部监控,吞吐有 2 倍余量;二期跟社区多副本验证进度。"这种"现状与路线图分得清"的文档风格,反而是她给这个项目加分的地方。

**验收标准**:

- Given `deploy/helm/` 的 Chart 与公司 K8s 集群,When 执行 `helm install/upgrade`,Then 数据库迁移由 pre-install/pre-upgrade hook Job 自动执行,迁移失败时新版本不上线。
- Given [`operations.md`](../operations.md) §10 的 k6 压测方法,When 在等价环境复现压测(注意单接入点默认限流 600/min 需临时调高),Then 得到 ≥1000 events/min 的可复现吞吐基线用于容量规划。
- Given 生产峰值告警量低于实测基线的 1/2,When 采用 Helm 单副本部署 + PDB + 探针,Then 满足容量要求且部署形态在官方已验证范围内。
- Given 架构评审需要判断多副本方案,When 查阅 [`../architecture.md`](../architecture.md) 部署章节,Then 能明确区分"已验证形态"(Compose 三容器、Helm 单副本)与"路线图"(api/worker 拆分、多副本),不存在混写。

**关联**:NFR-DEP、NFR-PERF | [ADR-0031](../adr/0031-single-binary-compose-helm.md)、[`operations.md`](../operations.md) §7/§10 | **P1**

---

## US-ARC-06 权限治理:权限点固定、角色自配置,审计可导出对接合规

**故事**:作为企业基础架构师,我想要用系统内置的权限点自由组合出贴合公司组织的角色,并把管理审计定期导出归档,以便通过公司安全部的权限治理与等保审计要求。

**场景叙事**:安全部对权限模型的要求向来苛刻:角色必须能对齐公司自己的岗位体系,不接受"产品预设的三个角色凑合用"。王芳发现 Vigil 的模型正好是"权限点是系统枚举、角色由使用者组合":她照着安全部的岗位矩阵新建了"一线值班""值班长""只读稽核"三个自定义角色——内置角色不可删、不可改,定制的方式是参照内置角色的权限集新建角色、自行勾选权限点组合,其中"只读稽核"只勾了各资源的 view 类权限点(权限点清单以 `internal/auth/permission.go` 为准,界面上可全量勾选)。团队维度上,交易线和基础设施线各自建团队,数据软隔离,跨团队处置靠拉人时的事件级临时授权,事件收口自动回收——她在评审材料里特意强调"临时授权精确到单个事件,且双重回收,不是放宽隔离"。审计方面,每季度合规检查需要留档:管理审计(角色变更、集成配置等)从 `GET /api/v1/audit-logs/export` 导出 CSV,她在对接脚本里按文档要求检查了 `X-Vigil-Truncated` 响应头——单次导出上限 5 万行,超限不静默截断,按时间窗分段拉取即可。

**验收标准**:

- Given 内置角色(`builtin:true`),When 管理员尝试删除或修改,Then 均被拒绝;When 新建角色并勾选与内置角色相同的权限点组合后调整,Then 得到可自由编辑的自定义角色。
- Given 一个仅含 view 类权限点的自定义角色,When 将其绑定给稽核用户(team 或 org scope),Then 该用户可查看对应资源但所有写操作(含 IM 侧)被拒绝并记审计。
- Given 用户同时持有 org 与 team 两个 scope 的角色绑定,When 鉴权判定,Then 取两者权限并集;撤权时需清理所有相关绑定方可生效。
- Given 团队树存在父子关系,When 父团队管理员访问子团队资源,Then 权限不沿树继承,需显式 org scope 授权才可跨团队管理。
- Given 审计日志超过 50000 行的导出请求,When 调用导出接口,Then 响应携带 `X-Vigil-Truncated: true` 且服务端记 warn 日志,调用方可缩小时间窗分段重导。

**关联**:FR-SEC、FR-ADM | [ADR-0027](../adr/0027-rbac-permissions-roles.md)、[ADR-0028](../adr/0028-single-org-soft-isolation.md)、[ADR-0020](../adr/0020-responder-temp-grant.md)、[ADR-0029](../adr/0029-dual-audit-no-silent-truncation.md) | **P1**

---

## US-ARC-07 数据不出境:LLM Provider 切换本地 Ollama

**故事**:作为企业基础架构师,我想要把 AI 能力的 LLM 后端从云端切换为内网 Ollama,以便在启用 AI 分诊、诊断、复盘起草的同时满足"告警内容不出网"的合规红线。

**场景叙事**:AI 能力评审会上,王芳的开场白就是老问题:"告警里有主机名、内网 IP、业务指标,这些内容发给谁?"Vigil 的答案让评审顺利了很多:LLM 经 Provider 抽象对接,`VIGIL_LLM_PROVIDER=ollama` 即切到本地推理,数据不出境;不配任何 Provider 时 AI 功能整体降级,告警主流程零影响——"AI 是增强不是依赖",这正是她要的架构姿态。她让算法组在内网 GPU 机器上起了 Ollama 做验证,过程中踩到一个文档里明确预警过的坑:`Incident.embedding` 列是 1536 维(对齐云端 GLM embedding-3),而 Ollama 默认 embedding 模型是 768 维,切换后要么改列维度、要么接受相似检索降级为 LIKE 文本匹配。她把这条写进了切换 SOP。另外她确认了成本护栏对本地部署同样有意义:缓存、限流、配额三道闸能防止 AI 调用打爆内网 GPU;所有 AI 产出必须带 evidence 且需人工 accept 才生效,不存在"AI 自动改配置"的路径,安全部对此无异议。

**验收标准**:

- Given `VIGIL_LLM_PROVIDER=ollama` 且内网 Ollama 服务可达,When 触发 AI 诊断/复盘起草,Then LLM 请求仅指向内网 Ollama 地址,无任何云端 LLM 外联。
- Given 未配置任何可用的 LLM Provider,When 告警接入与事件处置正常进行,Then AI 功能降级(不产出建议),主流程无报错、无阻塞。
- Given 从 GLM 切换到 Ollama 且未调整 embedding 列维度,When 使用相似事件检索,Then 系统降级为 LIKE 文本匹配而非返回错误。
- Given AI 产出一条处置建议,When 无人 accept,Then 该建议不产生任何实际变更;无 evidence 的产出不展示。
- Given 设置了 LLM 限流与配额,When AI 调用量超限,Then 调用被拒绝或降级,`/metrics` 中可观测 LLM 调用量与 token 成本。

**关联**:FR-AI、NFR-SEC | [ADR-0023](../adr/0023-llm-provider-cost-control.md)、[ADR-0024](../adr/0024-similar-incident-pgvector.md)、[ADR-0022](../adr/0022-aiinsight-hitl-evidence.md) | **P1**

---

## US-ARC-08 扩展落地:自研 Jenkins 处置作业接成 Runbook 执行器

**故事**:作为企业基础架构师,我想要按官方扩展指南把公司 Jenkins 上的标准处置作业接成 Runbook 执行器,以便值班同事在事件页一键触发(写操作仍须人工审批),而不是切到 Jenkins 控制台手工找 job。

**场景叙事**:公司积累了几十个标准处置 Jenkins 作业(重启服务、切流、清缓存)。王芳评估扩展方式时,首先确认了文档的如实性:Vigil 的扩展模型是"Go 接口 + 编译期注册",不是运行时插件——新增执行器要改代码、重新编译,以内部 fork 分支维护(或走 PR 回馈社区)。她把 [`extending.md`](../extending.md) 交给平台组的小周,执行器恰好是文档标注的"触点最少的推荐范式":实现 `Executor` 接口、在注册表注册一个自由字符串 kind,不涉及 schema 枚举变更。两天后内部构建的镜像就带上了 `jenkins` 执行器。凭据管理让她省心:Jenkins 的 API token 录入凭据托管,AES-256-GCM 加密落库、明文永不回显,执行时才解密注入。上线前她逐条核对了红线:所有写操作步骤保持 `require_approval:true`,须人在 Web 确认后才真正触发——IM 不承载审批,IM 内触发的执行,写步骤恒阻断在待审批状态;每次执行落操作审计——"平台可以帮忙按按钮,但按不按永远是人说了算"。

**验收标准**:

- Given 按 [`extending.md`](../extending.md) 第四节实现并注册了自定义执行器(自由字符串 kind),When 重新编译部署,Then 可在 executable 型 Runbook 步骤中引用该执行器,核心业务代码无需改动。
- Given 一个包含写操作步骤(`readonly:false`)的 Runbook,When 值班人员触发执行(Web 或 IM),Then 写操作步骤停在待审批状态,须人在 Web 确认后才执行(IM 不承载审批,IM 内写步骤恒阻断待审);默认配置下不存在写操作自动执行路径。
- Given 执行器所需的 Jenkins API token 已录入凭据托管,When 查看凭据管理页或调用查询接口,Then 明文永不回显;执行时由系统解密注入。
- Given Runbook 步骤执行失败,When 按步骤的 `on_failure` 配置处理,Then 行为为 continue/abort/escalate 之一,且每次执行(成功或失败)均落操作审计可追溯。

**关联**:FR-RBK、FR-INT | [ADR-0009](../adr/0009-pluggable-integrations.md)、[ADR-0021](../adr/0021-runbook-two-tier.md)、[ADR-0030](../adr/0030-integrations-encrypted-openapi.md)、[`extending.md`](../extending.md) | **P1**

---

## US-ARC-09 版本升级演练:备份即回滚,没有侥幸路径

**故事**:作为企业基础架构师,我想要在预生产环境完整演练一次"升级失败 → 回滚"流程,以便确认升级路径可控,并把回滚 SOP 固化进公司变更管理流程。

**场景叙事**:任何平台进公司生产环境前,王芳都要求交付一份"能照着做的回滚剧本"。Vigil 的回滚哲学很直白,她第一次读 [ADR-0032](../adr/0032-migration-backup-restore.md) 时甚至愣了一下:**不提供逆向迁移**,回滚 = 备份恢复,处置序列固定为 stop → 恢复备份 → 部署回旧版本 → start。想明白之后她反而认可——声明式迁移的自动逆向本来就不可能安全,与其给一个假的 `migrate down`,不如把"升级前必先备份"写成硬前提。她在预生产做了完整演练:cron 挂上 `scripts/backup.sh`(保留最近 7 天),升级前手动做一次全量备份,升级后故意模拟失败,照 [`operations.md`](../operations.md) §5 走 `restore.sh` 恢复、部署回旧镜像,全程 40 分钟,数据回到备份点。她在变更管理模板里加粗了一行:"回滚粒度是整库恢复,备份点之后的数据会丢失——所以升级窗口选在告警低峰,且升级前备份是不可跳过的门禁。"

**验收标准**:

- Given 升级前已按 [`operations.md`](../operations.md) 完成数据库备份,When 升级失败执行"stop → restore → 部署旧版本 → start"序列,Then 系统恢复到备份点状态并正常提供服务。
- Given `scripts/backup.sh` 已挂 cron,When 查看备份目录,Then 存在按计划生成的备份且保留最近 7 天。
- Given 版本升级(Compose 路径),When 新版本启动前执行 `vigil migrate`,Then 迁移按 `schema_migrations` 幂等追踪,重复执行不产生副作用;Helm 路径下同一迁移由 hook Job 自动完成。
- Given 回滚已完成,When 核对数据,Then 数据状态等于备份点,备份点之后的写入丢失——该限制在变更方案中被如实告知而非隐藏。

**关联**:NFR-COMP、NFR-RELY | [ADR-0032](../adr/0032-migration-backup-restore.md)、[ADR-0031](../adr/0031-single-binary-compose-helm.md) | **P0**

---

## US-ARC-10 「规划中」审计归档与 Event 分区:三年规模的数据生命周期预案

**故事**:作为企业基础架构师,我想要平台对审计类数据提供"先归档后删"的保留策略、对 Event 大表提供分区扩容预案,以便按公司三年数据规模与等保审计要求(审计日志留存 ≥ 1 年)做长期容量与合规规划。

> **注意:本条为规划中能力**,依据 [ADR-0039](../adr/0039-data-lifecycle.md)(状态 Proposed)撰写。当前已实现的部分:Event/RawEvent 保留清理(默认 90/30 天,可配,`<=0` 关闭),且未关闭的活跃事件所引用的 Event 超期也保留(证据保护)。以下验收标准针对**待实现**部分,落地前 Notification、审计类数据的无界增长是文档中显式声明接受的债。

**场景叙事**:做三年容量规划时,王芳按公司告警量估算:Event 表年增约 2 亿行,审计类数据受等保约束必须留存一年以上。她在 [ADR-0039](../adr/0039-data-lifecycle.md) 里找到了平台的立场:Event/RawEvent 的自动清理已实现且带活跃事件证据保护;审计日志与操作审计计划按 365 天"先归档(CSV 导出)后删"处理——她注意到文档特意提醒"保留默认值是产品立场而非合规承诺",于是在部署基线里把审计类保留期上调到公司要求的 3 年;Event 按月分区则是有明确触发条件的预案(行数超 5000 万或清理跟不上写入),走原生 SQL 迁移、对上层无感。她把这两项列入采用计划的"跟踪项",每季度对照版本发布说明复核落地进度。

**验收标准**(规划中,以 [ADR-0039](../adr/0039-data-lifecycle.md) 落地为准):

- Given AuditLog/IncidentAction 达到配置的保留期(默认 365 天,可按合规要求上调),When 清理任务运行,Then 数据先完成归档导出、后删除,不存在未归档即删除的路径。
- Given Notification/WebhookDelivery 达到各自保留期,When 清理任务运行,Then 按策略直删,清理进度可观测。
- Given Event 表行数超过分区预案触发条件,When 实施按月分区迁移,Then 迁移经原生 SQL 脚本完成,应用层(ent)无感,清理由分区裁剪加速。
- Given 任一保留策略生效,When 存在未关闭事件引用的超期数据,Then 证据保护规则优先——处置证据不因保留期到期被删除。

**关联**:FR-ADM、NFR-COMP | [ADR-0039](../adr/0039-data-lifecycle.md)、[ADR-0029](../adr/0029-dual-audit-no-silent-truncation.md) | **P2**

---

## 附:故事与需求域映射总览

| 编号 | 标题 | 需求域 | 优先级 | 状态 |
|------|------|--------|--------|------|
| US-ARC-01 | 内网 PoC:Compose 一键部署,数据不出网 | NFR-DEP、NFR-SEC | P0 | 已实现 |
| US-ARC-02 | 存量监控接入:Prometheus / Grafana / 自研平台 / 邮件 | FR-ING、FR-INT | P0 | 已实现 |
| US-ARC-03 | 钉钉落地:IM 协同与 Web 同一 RBAC | FR-IM、FR-SEC | P0 | 已实现 |
| US-ARC-04 | 故障模式演练:Redis 宕机不丢告警 | NFR-RELY、NFR-OBS | P0 | 已实现 |
| US-ARC-05 | 生产拓扑:Helm 上 K8s 与容量规划 | NFR-DEP、NFR-PERF | P1 | 已实现(多副本为路线图) |
| US-ARC-06 | 权限治理:自配置 RBAC 与审计导出 | FR-SEC、FR-ADM | P1 | 已实现 |
| US-ARC-07 | 数据不出境:LLM Provider 切换本地 Ollama | FR-AI、NFR-SEC | P1 | 已实现 |
| US-ARC-08 | 扩展落地:自研 Jenkins 执行器 | FR-RBK、FR-INT | P1 | 已实现(需二次开发) |
| US-ARC-09 | 版本升级演练:备份即回滚 | NFR-COMP、NFR-RELY | P0 | 已实现 |
| US-ARC-10 | 审计归档与 Event 分区预案 | FR-ADM、NFR-COMP | P2 | **规划中** |
