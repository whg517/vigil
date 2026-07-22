# 扩展指南:新增告警源 / 通知通道 / IM 平台 / Runbook 执行器 / LLM Provider

> 本文是 [ADR-0009](./adr/0009-pluggable-integrations.md)「接口 + 编译期注册」模型的**操作手册**:
> 逐扩展点列出全部代码触点。Vigil 不是运行时插件系统——每类扩展都需要改代码、重新编译,
> 但核心业务流程只依赖接口,你要动的只有下面列出的边界文件。
>
> 改完后走 [`AGENTS.md`](../AGENTS.md)「开发约定」的 worktree + 三道门禁流程提交。

## 通用心智模型

| 扩展点 | 接口定义 | 注册点 | 标识约束 | 触点数量 |
|--------|---------|--------|---------|---------|
| 告警源 Adapter | `internal/ingestion/adapter.go` | 同文件 `RegisterBuiltins()` | ent 枚举(`Integration.type`) | 最多(含 schema/前端) |
| 通知通道 Channel | `internal/notification/channel.go` | `internal/server/wire.go` `buildNotifier()` | 自由字符串 | 中 |
| IM 平台 Bot | `internal/im/bot.go` | `internal/server/wire.go` `buildIMRegistry()` | 自由字符串(回调路由 `:platform` 参数) | 中 |
| LLM Provider | `internal/ai/provider.go` | `internal/server/wire.go` `buildLLMProvider()` | 配置项 `VIGIL_LLM_PROVIDER` 枚举(glm / ollama) | 少 |
| Runbook 执行器 | `internal/runbook/executor.go` | 同文件 `NewRegistry()` | 自由字符串 kind | 最少(**推荐范式**) |

「自由字符串 + 注册表」(Runbook Executor 的模式)是耦合面最小的范式:标识不进 ent 枚举,
新增实现只需注册,存量数据里的未知标识按"查不到即跳过/报错"降级。告警源 Adapter 因
`Integration.type` 是 ent 枚举暂做不到这一点(放宽枚举留待后续迭代)。

---

## 一、新增告警源(Alert Source Adapter)

以新增 `datadog` 类型为例,触点如下(按依赖顺序):

1. **实现适配器** —— `internal/ingestion/adapters_builtin.go`(或新建文件)实现 `Adapter` 接口
   (`Type() string` + `Normalize(ctx, raw, integ, rawEvent) ([]*NormalizedEvent, error)`)。
   参考 `PrometheusAdapter`;严重度归一可用 `severity_map` 覆盖机制(Integration.config)。
2. **注册** —— `internal/ingestion/adapter.go` 的 `RegisterBuiltins()` 加一行 `r.Register(&DatadogAdapter{})`。
3. **schema 枚举** —— `ent/schema/service.go` 的 `Integration` `field.Enum("type").Values(...)`
   加入新值,然后两步:**`go generate ./ent/...`**(生成代码一起提交)+
   **`atlas migrate diff <name> --env local`**(生成 `internal/schema/migrations/*.sql` 与 `atlas.sum`,一起提交)。
   运行时 `vigil migrate` 只 apply 版本化迁移文件——漏了第二步,生产库不会有该枚举变更;
   收窄/改名等破坏性变更的注意事项见 [ADR-0005](./adr/0005-data-access-ent-atlas.md)。
4. **OpenAPI + 前端类型** —— `go generate ./cmd/vigil/...` 重生成 spec,再
   `pnpm --dir web gen:types` 重生成 `types.gen.ts`(前端 `IntegrationType` 由此派生)。
5. **配置模板(向导后端)** —— `internal/integration/config_template.go` 的 `configTemplates`
   map 加条目:字段说明、示例、上游接线指引(SetupHint)。向导第 1/2 步靠它渲染。
6. **前端选项** —— `web/src/pages/integrations.tsx` 的 `TYPE_OPTIONS` 加值;
   `web/src/pages/integration-wizard.tsx` 的 `SAMPLE_PAYLOADS` 加该源的样例 payload(第 4 步干跑测试用)。
7. **i18n** —— 若新增了前端文案,`web/src/lib/i18n.ts` 补中英词条(类型的中文展示名走
   config_template 的 `DisplayName`,不必进 i18n)。
8. **测试** —— `internal/ingestion/` 补归一化单测(字段映射/严重度/去重键/resolved 归一);
   涉及接入主链路,建议本地 `make test-e2e` 复验。

## 二、新增通知通道(Notification Channel)

1. **实现通道** —— `internal/notification/` 下新建文件,实现 `Channel` 接口
   (`Name() string` + `Send(ctx, msg) ([]SendResult, error)`)。参考 `EmailChannel`:
   未配置时应自我降级(发送直接失败/跳过),不阻塞降级链。
2. **配置** —— 需要凭据/地址的,`internal/config/config.go` 的 `Notification` 段加字段,
   `.env.example` 补样例与注释。
3. **注册** —— `internal/server/wire.go` `buildNotifier()` 里 `reg.Register(...)`;
   若应参与默认降级链,同函数的 `defaultChans` 决定顺序(链是有序降级,非并联,
   见 [ADR-0017](./adr/0017-notification-fallback-chain.md))。
4. **规则/前端选项** —— `NotificationRule.channels` 存自由字符串,注册表查不到的名字自动跳过;
   要让用户可选,更新 `web/src/pages/settings/notification-tab.tsx` 的 `channelOptions`、
   `web/src/pages/escalation-policies.tsx` 与 `web/src/pages/settings/subscription-tab.tsx` 的 `CHANNELS`,
   并在 `web/src/lib/i18n.ts` 补词条。
5. **测试** —— 通道单测 + 降级链行为(失败继续下一通道)用例。

## 三、新增 IM 平台(IMBot)

1. **实现适配器** —— `internal/im/<platform>/` 新建包,实现 `IMBot` 接口
   (`Platform() / Available() / SendCard / UpdateCard / VerifyCallback / ParseCallback`,
   见 `internal/im/bot.go`)。平台能力缺失的方法返回 `im.ErrUnsupported`,走降级矩阵
   ([ADR-0019](./adr/0019-imbot-pluggable-degradation.md));参考 `internal/im/feishu/`、`internal/im/dingtalk/`。
2. **配置** —— `internal/config/config.go` 的 `IM` 段加平台凭据字段,`.env.example` 补样例。
3. **注册** —— `internal/server/wire.go` `buildIMRegistry()` 构造并 `reg.Register(...)`,
   `logIMStatus()` 补就绪日志。回调路由是通用的 `POST /api/v1/im/:platform/callback`
   (`internal/im/handler.go`),按 `Platform()` 查注册表分发,**无需新增路由**。
4. **账号绑定与前端** —— 绑定链路按 `IMEvent.UnionID` 映射 User,平台无关;
   前端设置页若列出平台选项需同步,并补 `web/src/lib/i18n.ts` 词条。
5. **文档** —— [ADR-0019](./adr/0019-imbot-pluggable-degradation.md) 的平台能力降级矩阵补一列。
6. **红线** —— IM 操作必须走与 Web 相同的 RBAC 链路([ADR-0018](./adr/0018-im-same-rbac-as-web.md)),
   适配器只做协议转换,不做任何鉴权判断。

## 四、新增 Runbook 执行器(Executor)★ 推荐范式

1. **实现执行器** —— `internal/runbook/` 下实现 `Executor` 接口
   (`Kind() string` + `Execute(ctx, target, params) (output, err)`)。kind 是自由字符串
   (如 `ansible`/`jenkins`),不进任何 schema 枚举;步骤里 `StepTarget.Kind` 查不到执行器
   按失败处理。需要凭据的实现 `SetCredentialResolver`(托管凭据注入,明文不落步骤/日志)。
2. **注册** —— `internal/runbook/executor.go` 的 `NewRegistry()` 加 `r.Register(...)`
   (需配置驱动的,在 `internal/server/wire.go` 构造 registry 处注册)。
3. **两档红线** —— 诊断只读可直接执行;处置写操作必须 `require_approval` 人工确认
   ([ADR-0021](./adr/0021-runbook-two-tier.md)),新执行器不得绕过 `Readonly`/审批闸门。
4. **测试** —— 执行器单测 + `on_failure`(continue/abort/escalate)行为用例。

## 五、新增 LLM Provider(附)

`internal/ai/provider.go` 实现 `Provider` 接口(`Complete/Embed/Available`),在
`internal/server/wire.go` `buildLLMProvider()` 的 switch 中按 `cfg.LLM.Provider` 接入,
外层自动套成本控制包装([ADR-0023](./adr/0023-llm-provider-cost-control.md));
`.env.example` 补配置样例。LLM 不可用时 AI 功能整体降级,不影响告警主流程。
