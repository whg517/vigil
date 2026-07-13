# ADR-0033: 自监控三红线 + 鉴权 Bearer JWT

| 字段 | 内容 |
|------|------|
| **状态** | Accepted |
| **日期** | 2026-07-09 |
| **相关** | [`0031-single-binary-compose-helm.md`](./0031-single-binary-compose-helm.md)、[`0017-notification-fallback-chain.md`](./0017-notification-fallback-chain.md) |

## 背景

Vigil 自身就是告警处置平台,若它自己出问题(队列积压、通知发不出去),谁来告警?这带来一个特殊约束:**被监控的对象正是通知链路本身**,自监控不能天真复用业务告警路径。另一方面,业务 API 需要明确、可撤销、可对外声明的鉴权方案,同时保留本地开发的便利与 webhook/IM 各自的接入机制。

## 决策

### 自监控(selfmon,默认关闭)

三条红线:

1. **自告警绕开 escalation,直发独立通道**,且**刻意排除 IM**——自告警不走升级引擎,直接投递到运维预先配置的独立通道。
2. **失败率只算业务通知**——统计通知失败率时**排除自告警的 unrouted**,避免自监控自身的投递计入业务健康度。
3. **独立通道未真实配置时,启动即 log warn** 明确告知"自告警可能送不达",绝不假装闭环成功。

触发条件:队列积压 / 通知失败率超阈值时自触发。

### 鉴权

- 业务 API 唯一声明的鉴权方案为 **HTTP Bearer JWT**,经 `POST /auth/login` 换取,令牌可撤销。
- `X-Vigil-User-ID` 头作本地开发 / 回退身份使用,**不在 OpenAPI securitySchemes 中声明**,且**默认禁用**——头部身份可伪造,须经 `VIGIL_AUTH_HEADER_FALLBACK=true` 显式开启,仅在受信网络内作便利手段;生产环境无条件强制禁用(见下方修订记录)。
- **webhook token** 与 **IM 签名**各自有独立校验机制,**不走 RBAC**。

## 理由

- 自告警若走与业务相同的通知链,一旦通知链本身故障,自告警同样发不出去——等于没有告警。排除 IM 是因为 IM 恰是主交互面,故障面重叠最大。
- 失败率排除自告警 unrouted,是为避免"自告警失败 → 抬高失败率 → 再次触发自告警"的自激循环。
- 未配独立通道时启动 warn,坚持"不假装闭环成功"——宁可明说可能送不达,也不给运维虚假的安全感。
- Bearer JWT 是可撤销、可标准化声明的方案;头部身份便利但可伪造,故限定开发/受信网络并对生产禁用;webhook 与 IM 有各自的签名/token 机制,强套 RBAC 反而不匹配其调用模型。

## 备选方案

- **自告警走与业务相同的升级/通知链**:否决——通知链故障时自告警同样失效,自监控形同虚设。
- **自监控默认开启**:否决——未配置独立通道时默认开启会产生送不达的无效告警,默认关闭保证"无配置不回归"。
- **仅用 `X-Vigil-User-ID` 头做鉴权**:否决——头部可伪造,不能作为生产鉴权。

## 影响 / 权衡

- 正面:自监控不会因通知链故障而失效;失败率指标不被自身污染;鉴权方案清晰可撤销。
- 负面/限制:自监控依赖运维**额外配置一条独立通道**,否则只 warn 不告警;自告警刻意排除 IM,意味着自监控通知不出现在主交互面,运维需关注独立通道。
- 默认关闭意味着新部署需显式开启并配置独立通道后,自监控才真正生效。

## 修订记录

### 2026-07-14: 头回退与测试端点改独立显式开关(默认关闭)

**问题**:本 ADR 原文称 `X-Vigil-User-ID` 头"生产环境禁用",但实现将其门控在 `!IsProduction()` 上——而 `VIGIL_APP_ENV` 默认值是 `development`,即**默认配置下伪造头直接生效**(任何客户端带头即可冒充任意用户)。同理,无鉴权的 `/api/v1/__test__/reset`(TRUNCATE 全部业务表)也隐式跟随 development 注册,二进制直跑的用户在完全不知情下暴露了"一个 POST 清空全库"的端点。README 曾误称头回退"仅 `AUTH_ENABLED=false` 时生效",与实现(由 APP_ENV 门控,与 AUTH_ENABLED 无关)不符。

**修订**:两个危险行为从"隐式跟随 APP_ENV"改为**独立显式开关,默认关闭**:

- `VIGIL_AUTH_HEADER_FALLBACK`(默认 `false`):控制 X-Vigil-User-ID 头回退。`VIGIL_APP_ENV=production` 时无条件强制 `false`(双保险,`config.Auth.EffectiveHeaderFallback`)。
- `VIGIL_TEST_ENDPOINTS_ENABLED`(默认 `false`):控制 `__test__` 路由注册。production 同样强制 `false`(`config.TestEndpoints.EffectiveEnabled`)。
- 任一开关开启时,启动日志打印醒目 SECURITY WARN(说明风险与适用场景)。
- `APP_ENV` 默认值保持 `development` 不变(不破坏本地开发链路,如 JWT secret 自动填充)。

**原则**:危险行为必须显式开启,不能搭默认环境的便车;"生产禁用"只是兜底,不是安全默认。e2e 依赖方(docker-compose.e2e.yml 的 Playwright reset)已显式声明开关。
