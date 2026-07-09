# ADR-0004: Web 框架选 Echo

| 字段 | 内容 |
|------|------|
| **状态** | Accepted |
| **日期** | 2026-07-09 |
| **相关** | [ADR-0003](0003-backend-language-go.md)、[ADR-0005](0005-data-access-ent-atlas.md)、[ADR-0007](0007-async-tasks-asynq.md)、[ADR-0018](0018-im-same-rbac-as-web.md)、[`../architecture.md`](../architecture.md) |

## 背景

在选定 Go(见 [ADR-0003](0003-backend-language-go.md))后,需要一个 HTTP 框架承接高频 webhook 接入、统一鉴权中间件、实时推送等需求。Vigil 已用 ent 做数据访问、Asynq 做异步任务,框架不应强绑架项目结构或重复这些职责。

## 决策

Web 框架采用 **Echo**。

## 理由

- **低开销路由与中间件**:适合 webhook 高频接入场景。
- **中间件链清晰**:契合统一鉴权中间件的组织方式(IM 与 Web 复用同一鉴权链路,见 [ADR-0018](0018-im-same-rbac-as-web.md))。
- **轻量不绑架**:不绑架项目结构、不强制自带 ORM 或 DI,与 ent、Asynq 各司其职。
- **原生 WebSocket 支持**:满足实时推送需求。

## 备选方案

事实文件未记录被否决的具体框架;选型核心在于「轻量、不绑架、中间件链清晰」这一取舍标准,重型全家桶式框架因会与 ent/Asynq 职责重叠、绑架结构而不符。

## 影响 / 权衡

- 框架轻量意味着项目结构、依赖注入、数据访问需自行组织,但这正是与 ent/Asynq 分工清晰所要求的。
- 中间件链成为统一鉴权的核心承载点,鉴权逻辑集中在此,便于 IM/Web 复用。

出处:tech-stack §二/§3.2。
