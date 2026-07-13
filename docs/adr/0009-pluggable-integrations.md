# ADR-0009: 可插拔集成 —— 5 类扩展点接口抽象(编译期注册)

| 字段 | 内容 |
|------|------|
| **状态** | Accepted(2026-07-14 修订措辞:如实表述为"接口 + 编译期注册",非运行时插件) |
| **日期** | 2026-07-09 |
| **相关** | [`0011-ingestion-decoupled-idempotent.md`](./0011-ingestion-decoupled-idempotent.md)、[`../extending.md`](../extending.md)、[`../architecture.md`](../architecture.md) |

## 背景

Vigil 面向异构环境:告警源(Prometheus/Grafana/邮件,其余经通用 webhook)、IM 平台(钉钉/飞书)、LLM 供应商(云端/本地)、执行环境各不相同。若把任一具体实现写死进核心业务逻辑,接入新平台就要改动核心流程代码,违背开源可扩展定位。

## 决策

5 个扩展点均以 **Go 接口抽象 + 编译期注册表**组织,启停与参数由配置驱动:

1. **告警源 Adapter** —— `Adapter.Normalize(raw) → []Event`。内置 Prometheus / Grafana / 邮件(SMTP 入向) / 通用 webhook。
2. **通知通道 Channel** —— `Channel.Send(target, msg) → ack`。内置 IM / 邮件 / Webhook。
3. **执行器 Executor** —— `Executor.Execute(step) → result`。内置 HTTP / 内部诊断,可扩展 Ansible / Jenkins / 内部平台。
4. **LLM Provider** —— `LLMProvider.Complete / Embed`。云端 GLM,本地 Ollama。
5. **IM Bot** —— `IMBot.*`(收发卡片 / 回调校验解析)。钉钉 / 飞书。

**扩展模型如实说明**:这是「接口隔离 + 编译期注册」,**不是**运行时插件系统。新增一个实现需要修改并重新编译 Vigil:实现接口、在注册点登记;部分扩展点还有配套触点(schema 枚举、配置模板、前端选项、i18n 等)。"核心业务流程只依赖接口、不感知具体平台"成立;"扩展新平台完全不改仓库代码"不成立。逐扩展点的完整代码触点清单见 [`docs/extending.md`](../extending.md)。

## 理由

- 不被任何单一 IM、LLM、监控源绑死;适配面沉在边界,核心流程只依赖接口。
- 编译期注册换来简单性:无插件加载器、无 ABI/版本兼容矩阵、无进程外通信,单二进制自托管形态([ADR-0031](./0031-single-binary-compose-helm.md))不被破坏。
- 各扩展点耦合面不同:Runbook Executor 的「自由字符串 kind + 注册表」触点最少(不涉及 schema 枚举),是推荐范式;告警源 Adapter 受 `Integration.type` 枚举约束,触点最多。

## 备选方案

- **为单一主流平台硬编码实现**:开发快但绑死生态,与"IM 原生、多源可扩展"定位冲突,否决。
- **运行时插件(Go plugin / 进程外 gRPC)**:加载、分发与版本兼容复杂度高,与单二进制形态冲突,当前阶段否决;社区扩展需求增长后可另立 ADR 再议。

## 影响 / 权衡

- 接口抽象带来一层间接性,早期实现少量平台时略显"过度设计",但为长期可扩展性所必需。
- 编译期注册意味着第三方扩展以 fork/PR 进主仓库的方式落地,这是简单性与开放性之间的当前取舍。
- 各扩展点需配套注册表与配置驱动的启用/降级机制(如某平台不可用时的兜底),详见各能力域相关 ADR;逐类扩展步骤见 [`docs/extending.md`](../extending.md)。
