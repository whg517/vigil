# ADR-0003: 后端语言选 Go

| 字段 | 内容 |
|------|------|
| **状态** | Accepted |
| **日期** | 2026-07-09 |
| **相关** | [ADR-0002](0002-product-positioning.md)、[ADR-0004](0004-web-framework-echo.md)、[ADR-0005](0005-data-access-ent-atlas.md)、[ADR-0007](0007-async-tasks-asynq.md)、[`../architecture.md`](../architecture.md) |

## 背景

Vigil 以自托管、开箱即用为核心诉求(见 [ADR-0002](0002-product-positioning.md)),部署形态、并发模型、内存占用直接影响能否「一键部署、低占用运行」。同时它是事件驱动系统,需承接高频 webhook 接入与大量异步任务。后端语言的选择须对齐这些关键指标。

## 决策

后端采用 **Go 1.25 + Go Modules**。

## 理由

从五个维度对比后 Go 全面占优:

- **部署形态**:编译为单二进制,无需运行时/解释器,契合自托管一键部署。
- **并发模型**:goroutine / channel 契合事件驱动的高并发接入与异步处理。
- **内存占用**:低占用,适合自托管环境资源受限场景。
- **自托管友好度**:单二进制 + 低占用 + 强并发三项关键指标全面占优。
- **领域先例**:oncall 领域有 GoAlert 等 Go 先例;IM/云厂商 SDK 生态成熟。

## 备选方案

- **Python**:需解释器 + 依赖分发,GIL 限制并发;虽有 Grafana OnCall 先例,但部署形态与并发模型不及 Go。
- **Java**:需 JVM,线程模型重、内存占用高,且无对口领域先例。

## 影响 / 权衡

- 全栈以 Go 为中心,Web 框架([ADR-0004](0004-web-framework-echo.md))、数据访问([ADR-0005](0005-data-access-ent-atlas.md))、异步任务([ADR-0007](0007-async-tasks-asynq.md))均在 Go 生态内选型,各司其职。
- Go 生态相对 Python 在部分 AI/数据科学库上较薄,但 Vigil 的 LLM 能力通过 Provider 抽象对接外部服务,不依赖本地重型库,影响可控。

出处:tech-stack §二/§3.1。
