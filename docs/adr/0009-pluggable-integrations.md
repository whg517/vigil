# ADR-0009: 可插拔集成 —— 5 类扩展点接口抽象

| 字段 | 内容 |
|------|------|
| **状态** | Accepted |
| **日期** | 2026-07-09 |
| **相关** | [`0011-ingestion-decoupled-idempotent.md`](./0011-ingestion-decoupled-idempotent.md)、[`../architecture.md`](../architecture.md) |

## 背景

Vigil 面向异构环境:告警源(Prometheus/Zabbix/Grafana/云监控/邮件)、IM 平台(钉钉/飞书/企微)、LLM 供应商(云端/本地)、执行环境各不相同。若把任一具体实现写死进核心,使用者或贡献者接入新平台就要改核心代码,违背开源可扩展定位。

## 决策

5 个扩展点均以接口抽象,统一注册进插件注册表、由配置驱动启用:

1. **告警源 Adapter** —— `Adapter.Normalize(raw) → []Event`。覆盖 Prometheus/Grafana/邮件/通用 webhook。
2. **通知通道 Channel** —— `Channel.Send(target, msg) → ack`。覆盖 IM / 邮件 / Webhook。
3. **执行器 Executor** —— `Executor.Run(step) → result`。内置 HTTP / 诊断,可扩展 Ansible / Jenkins / 内部平台。
4. **LLM Provider** —— `LLMProvider.Complete / Embed`。云端 GLM/OpenAI/通义,本地 Ollama。
5. **IM Bot** —— `IMBot.*`(收发消息 / 卡片)。钉钉 / 飞书。

## 理由

- 不被任何单一 IM、LLM、监控源绑死;适配面沉在边界,核心只依赖接口。
- 使用者与贡献者扩展新平台无需改核心,只需实现接口并注册,契合开源协作。

## 备选方案

- 为单一主流平台硬编码实现:开发快但绑死生态,与"IM 原生、多源可扩展"定位冲突,否决。

## 影响 / 权衡

- 接口抽象带来一层间接性,早期实现少量平台时略显"过度设计",但为长期可扩展性所必需。
- 各扩展点需配套注册表与配置驱动的启用/降级机制(如某平台不可用时的兜底),详见各能力域相关 ADR。
