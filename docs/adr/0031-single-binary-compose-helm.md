# ADR-0031： 单二进制 embed + Compose 默认 / Helm 生产

| 字段 | 内容 |
|------|------|
| **状态** | Accepted |
| **日期** | 2026-07-09 |
| **相关** | [`0007-async-tasks-asynq.md`](./0007-async-tasks-asynq.md)、[`0032-migration-backup-restore.md`](./0032-migration-backup-restore.md)、`deploy/helm/` |

## 背景

Vigil 的核心诉求是自托管、开箱即用——数据不出企业网,部署越简单越好。组件越多、进程越多,运维负担越重,与"中小团队一键起来就能用"的目标背离。但同时也要给成长后的用户留出生产级弹性伸缩的路径,不能把简单和可扩展对立起来。

## 决策

- **单二进制 embed**:前端静态资源与 OpenAPI spec 在编译期 embed 进单个 Go 二进制;同一二进制内承载 API、worker、前端静态资源多角色。
- **Compose 默认交付**:Docker Compose 单机 **3 容器**——`vigil`(API+worker+前端静态资源,单二进制多角色)、`postgres`、`redis`;首次部署手动执行 `vigil migrate` 完成迁移。
- **Helm 生产路径**:生产走 Kubernetes/Helm,`deploy/helm/` 提供 Chart、values、Deployment、Service、PDB。`vigil-api`(无状态多副本)与 `vigil-worker`(按队列深度扩缩)可独立扩缩;状态全在 Redis(见 [`0007`](./0007-async-tasks-asynq.md)),多实例 WebSocket 广播经 Redis pub/sub。
- **可选组件**:本地 LLM(Ollama,数据不出境)、外部执行器(Ansible/Jenkins),按需启用。
- **配置**:全部 `VIGIL_` 前缀环境变量 + YAML(12-Factor,敏感项走环境变量)。
- **构建与 CI**:容器基于 alpine/distroless 多阶段构建;CI 用 GitHub Actions(lint + ent 生成 + test + build + 镜像发布)。

迁移与回滚策略见 [`0032`](./0032-migration-backup-restore.md)。

## 理由

- 自托管/开箱即用是核心诉求,组件越少、进程越少越好,单二进制把前端与 spec 都装进去,交付物就是"一个镜像 + 两个依赖"。
- 无状态计算 + Redis 承载状态,使 API 与 worker 都能水平扩展,生产弹性与开发简单可以并存。
- `VIGIL_` 前缀 + 12-Factor 让同一制品在 Compose 与 K8s 下用不同配置运行,无需改代码。

## 备选方案

- **前后端分离多服务**:前端独立部署、API/worker 各自成服务,组件与进程数暴涨,违背"组件越少越好"。
- **纯 K8s-only**:直接要求 Kubernetes,对中小团队/单机试用门槛过高,丢掉开箱即用。

## 影响 / 权衡

- 单二进制多角色意味着前端改动也要重新构建整个 Go 二进制;前端与后端的发布节奏被绑定。
- 状态全放 Redis:Redis 成为关键依赖,其可用性直接决定系统可用性,多实例广播也依赖其 pub/sub。
- Compose 3 容器方案不含内建高可用,生产高可用需迁移到 Helm 路径,两条路径的配置差异需文档明确。
