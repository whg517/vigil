# Vigil 技术选型说明

| 字段 | 内容 |
|------|------|
| **文档版本** | v0.1 |
| **创建日期** | 2026-06-20 |
| **状态** | Draft |
| **关联文档** | [`PRD.md`](./PRD.md)、[`data-model.md`](./data-model.md)、[`architecture.md`](./architecture.md) |

---

## 一、选型原则

技术选型服务于 Vigil 的产品特性与约束，遵循以下优先级（冲突时上位优先）：

1. **可自托管、轻量部署**：用户能 Docker Compose 一键拉起（PRD H1.1 是硬指标）。避免重型中间件依赖。
2. **契合核心负载特征**：Vigil 的核心是**事件驱动 + 异步任务 + 定时调度**（告警接入、升级计时、排班计算、通知重试），技术栈要天然适合这类负载。
3. **IM/LLM 集成的生态**：本土 IM（钉钉/飞书/企微）和 LLM 的 SDK/社区支持要成熟。
4. **运维简单、单二进制优先**：自托管场景下，组件越少、进程越少越好。
5. **开源成熟、社区活跃**：作为 MIT 开源项目，所选技术本身也应是主流开源，降低贡献门槛。

---

## 二、总体技术栈

| 层 | 选型 | 关键理由 |
|----|------|---------|
| **后端语言** | **Go 1.25** | 并发原语（goroutine/channel）天然契合事件驱动；编译成单二进制，部署运维简单；oncall 领域有 GoAlert 等先例；IM/云 SDK 生态成熟 |
| **Web 框架** | **Echo** | 高性能、轻量、中间件链清晰；契合接入层（REST + WebSocket + Webhook）的灵活组装需求；与 ent/可插拔中间件（鉴权、限流、日志）协作顺畅 |
| **ORM / 数据访问** | **ent**（Facebook Ent） | 基于 schema 的代码生成，类型安全；图式（graph）建模契合 Vigil 的强关系型实体（User-Team-Service-Incident 等）；自动生成类型化查询 API，减少手写 SQL 与运行时错误；迁移工具 `atlas`（ent 官方配套）一并采用 |
| **主存储** | **PostgreSQL** | 关系型主库承载实体；JSONB 灵活存放 Event.detail 等半结构化字段；窗口函数支持聚合分诊（能力域 3） |
| **缓存/队列** | **Redis** | 缓存 + 轻量消息队列 + 分布式锁（升级计时、去重窗口）；单组件多用途，减少依赖 |
| **异步任务** | **Asynq（Go + Redis）** | 成熟可靠，原生支持延迟/定时/重试/死信/优先级队列，并自带 Asynqmon 监控面板；契合 oncall 的升级计时、通知重试等关键场景；无需从 ZSET 起步自研 |
| **前端框架** | **React + TypeScript** | 生态最广、贡献者最易招募；TS 保证中大型前端可维护 |
| **前端构建** | **Vite** | 极快的冷启动与 HMR；原生 ESM；配置简单；React + TS 项目开箱即用 |
| **前端 UI** | **shadcn/ui + Tailwind CSS** | shadcn/ui 是"复制到项目里的组件"而非黑盒依赖，可完全定制；Tailwind 原子化 CSS 保证一致的设计系统与体积可控；契合 Vigil 需要深度定制交互（IM 卡片预览、排班日历、时间线）的诉求 |
| **IM 集成** | **适配器模式（可插拔）** | 钉钉/飞书/企微各自实现，统一抽象接口；详见架构文档 §集成层 |
| **LLM 集成** | **Provider 抽象（可插拔）** | 支持云端（OpenAI/智谱/通义）与本地模型（Ollama）；统一接口 |
| **API 风格** | **REST + WebSocket** | REST 供管理/集成；WebSocket 供前端实时推送（事件状态、时间线） |
| **部署** | **Docker Compose + Helm** | Compose 满足单机一键；Helm 满足 K8s 生产部署 |
| **配置** | **环境变量 + YAML** | 12-Factor 风格；敏感信息走环境变量/密钥管理 |
| **可观测性** | **Prometheus metrics + 结构化日志（zap）** | 暴露 `/metrics` 供自身被监控；吃自己狗粮（PRD H2） |

---

## 三、关键选型论证

### 3.1 为什么是 Go 而非 Python/Java

| 维度 | Go | Python | Java |
|------|:--:|:------:|:----:|
| 部署形态 | 单二进制 ✅ | 需解释器+依赖 | 需 JVM |
| 并发模型 | goroutine 天然契合事件驱动 ✅ | GIL 限制 | 线程模型偏重 |
| 内存占用 | 低 ✅ | 中 | 高 |
| 自托管友好度 | 极高 ✅ | 中 | 低 |
| oncall 领域先例 | GoAlert | Grafana OnCall | — |

Go 在"单二进制 + 低占用 + 强并发"这三项自托管场景的关键指标上全面占优。采用 **Go 1.25**，享受最新的运行时与标准库改进。

### 3.2 Web 框架用 Echo

Echo 是高性能、极简的 Go Web 框架，契合 Vigil 接入层的诉求：

- **高性能**：基于 `fasthttp` 风格优化，路由与中间件开销低，适合 webhook 高频接入。
- **中间件链清晰**：鉴权（RBAC）、限流、日志、恢复、CORS 等以可组合中间件挂载，与统一鉴权中间件（架构 §6.1）天然契合。
- **足够轻量**：不绑架项目结构，不强制 ORM/依赖注入，与 ent、Asynq 各司其职。
- **WebSocket 支持**：原生支持，承载前端实时推送（事件状态、时间线）。

### 3.3 数据访问用 ent + Atlas

- **ent**：基于 schema 代码生成的类型安全 ORM。Vigil 的实体是**强关系型图结构**（User-Team-Service-Incident-Schedule-EscalationPolicy 之间多对多），ent 的 graph schema 建模比传统 tag-based ORM 更直观，且生成强类型查询 API，避免手写 SQL 的运行时错误。
- **JSONB 原生支持**：Event.detail 等半结构化字段用 ent 的 JSON scalar 即可，类型安全地存取。
- **Atlas（ent 官方迁移工具）**：自动生成 schema 迁移，解决 PRD H1.4 的版本化迁移需求——无需再单独引入 `golang-migrate`/`goose`。
- 仍以 **PostgreSQL** 为底座（关系完整性 + JSONB + 窗口函数 + `pg_trgm`/`pgvector`），原则不变：能在一个 Postgres 里解决的不拆组件。

### 3.4 异步任务用 Asynq

Vigil 的异步任务可归为五类，覆盖 oncall 的关键场景：

- **事件流水线任务**：告警接入后串行执行 归一化→分诊→路由→创建 Incident，解耦"接收"与"处理"，保证告警源秒级 ACK、绝不丢失。
- **延迟任务**（★ 升级引擎核心）：`delay_minutes` 后触发下一级通知/升级——海量、每事件独立、必须可靠。
- **定时任务**：排班换班交接、报表聚合、健康巡检、死信清理。
- **通知重试任务**：失败后指数退避重试，最终失败升级到下一通道。
- **长耗时/AI 任务**：LLM 分诊/诊断/复盘草稿生成，可降级，需限流。

这些任务的共同硬要求是：可靠投递（at-least-once）、幂等、延迟精度、崩溃恢复、可观测、优先级。

选用 **Asynq**（Go + Redis）：

- **开箱覆盖关键能力**：延迟任务（`asynq.ProcessIn`）、定时任务（`asynq.PeriodicTask`）、重试与死信、优先级队列——上述五类的基础设施均现成，尤其升级计时和通知重试这两类 oncall 命脉场景，自研风险高、收益低。
- **自带 Asynqmon 监控面板**：队列深度、任务状态、失败重试可视化，契合"吃自己狗粮"的可观测诉求。
- **基于 Redis**：与已选定的缓存/队列组件同源，不增加新中间件。
- **自研范围收窄**：基于 Asynq 之上的业务任务定义（任务 schema、handler）+ 幂等保证（业务幂等键兜底 at-least-once 的重复投递），而非从 ZSET 造轮子。

> 注：Asynq 的 at-least-once 语义要求 handler 必须幂等。Vigil 的幂等键设计：升级任务以 `incident_id + level`、通知任务以 `notification_id`、流水线任务以 `source_event_id` 去重。

### 3.5 前端用 Vite + shadcn/ui + Tailwind

- **Vite**：极快的冷启动与 HMR 让前端开发体验流畅；原生 ESM、配置极简，React + TS 项目开箱即用。
- **shadcn/ui**：与传统的"黑盒组件库"（如 Ant Design）不同，shadcn/ui 把组件源码**直接复制进项目**，可完全掌控与定制。Vigil 需要大量深度定制交互（IM 卡片预览、排班日历、事件时间线、作战室视图），黑盒库的定制成本反而更高；shadcn/ui 让这些深度定制成为常态。
- **Tailwind CSS**：原子化 CSS，保证设计系统一致、体积可控，与 shadcn/ui 天生搭配。
- 不选 Ant Design 的原因：体积偏大、定制需覆盖大量默认样式；Vigil 偏"工具型可定制前端"而非"标准中后台表单"，shadcn/Tailwind 更契合。

---

## 四、可插拔扩展点

以下能力设计为接口抽象，支持使用者/贡献者扩展，无需改核心：

| 扩展点 | 接口 | 内置实现 | 扩展方向 |
|--------|------|---------|---------|
| **告警源适配器** | `Adapter.Normalize(raw) → Event` | Prometheus/Zabbix/Grafana/云监控/邮件 | 自研监控源 |
| **通知通道** | `Channel.Send(target, msg) → ack` | IM（钉钉/飞书/企微）、电话/SMS、邮件、Webhook | 内部 IM、自定义通道 |
| **执行器** | `Executor.Run(step) → result` | HTTP、内置诊断 | Ansible/Jenkins/内部平台 |
| **LLM Provider** | `LLM.Complete(prompt) → text` | 云端（OpenAI/智谱/通义）、本地（Ollama） | 私有模型 |
| **IM 平台** | `IMBot.*`（收发消息/卡片/建群） | 钉钉/飞书/企微 | 其他 IM |

> 所有扩展点统一注册到插件注册表，配置驱动启用。这是 Vigil"不被任何单一 IM/LLM/监控源绑死"的关键。

---

## 五、依赖组件总览（自托管部署所需）

最小部署（Docker Compose）只需 **3 个容器**：

| 容器 | 角色 | 必需 |
|------|------|:--:|
| `vigil` | 后端 API + worker（单二进制内多角色）+ 前端静态资源 | ✅ |
| `postgres` | 主存储 | ✅ |
| `redis` | 缓存/队列/锁 | ✅ |

可选（按需）：

| 组件 | 何时需要 |
|------|---------|
| 本地 LLM（Ollama） | 启用 AI 且要求数据不出境时 |
| 外部执行器（Ansible/Jenkins） | 启用处置类 Runbook 自动执行时 |
| 云语音（阿里云/腾讯云） | 启用电话/SMS 通知时 |

---

## 六、版本与构建

| 项 | 选择 |
|----|------|
| Go 版本 | **1.25** |
| 包管理 | Go Modules |
| Web 框架 | Echo |
| ORM | ent + Atlas（迁移） |
| 异步任务 | Asynq（+ Asynqmon 监控） |
| 前端构建 | Vite |
| 前端 UI | shadcn/ui + Tailwind CSS |
| 容器基镜像 | `alpine` / `distroless`（小体积） |
| 多阶段构建 | 是（构建镜像与运行镜像分离） |
| CI | GitHub Actions（lint + ent 生成 + test + build + 镜像发布） |
| API 版本 | URL 前缀 `/api/v1`，契约用 OpenAPI 描述 |

---

## 七、开放问题

| # | 问题 | 状态 |
|---|------|------|
| T1 | 前端是否需要 SSR | 倾向不需要（Vite SPA 足够），架构设计阶段定 |
| T2 | 多 worker 实例的水平扩展方案（队列分片） | 初期单实例够用，扩展需求出现时再设计 |
| T3 | LLM 调用的成本控制与配额 | 设计阶段定（限流 + 缓存 + 配额） |
