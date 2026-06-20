# Vigil 端到端集成验证记录

| 字段 | 内容 |
|------|------|
| **日期** | 2026-06-20 |
| **验证范围** | 全链路：webhook → 归一化 → 分诊 → Incident → 升级 → 时间线 |
| **环境** | 真实 PostgreSQL 16 + Redis 7（Docker Compose）+ Asynq |

## 一、验证目标

用真实 PG/Redis 跑通完整链路，暴露单测（sqlite 内存库）发现不了的集成问题：
任务串联、Asynq 真实触发、ent 在真实 PG 的行为、各域装配正确性。

## 二、验证环境搭建

```bash
docker compose up -d                      # 起 PG + Redis
go run ./cmd/vigil/ migrate               # 应用 ent schema 到 PG
VIGIL_DB_*=vigil go run ./cmd/vigil/      # 起服务
```

## 三、验证结果：✅ 全链路跑通

触发一条模拟 Prometheus critical 告警（labels.service=payment），观察各阶段产物：

| 阶段 | 产物 | 结果 |
|------|------|------|
| ① webhook 接入 | raw_event_id=1, HTTP 202 | ✅ |
| ② 归一化（Asynq） | RawEvent.status=normalized | ✅ |
| ③ Event 生成 | source=prometheus, severity=critical, 正确摘要 | ✅ |
| ④ 路由 + Incident | INC-0001（label 匹配 payment 服务） | ✅ |
| ⑤ 升级触发（Asynq） | status=escalated, count=1, level=1 | ✅ |
| ⑥ 时间线 | "升级到 level 1，通知 1 人" | ✅ |

**结论**：异步流水线（ingestion→triage→escalation→timeline）在真实 PG+Redis+Asynq 下完整工作。

## 四、发现并修复的集成 bug

这些 bug 都是**单测发现不了的**——单测用 sqlite + ent API，绕过了驱动注册、配置默认值、PG 列名等真实环境差异。

| # | bug | 根因 | 修复 |
|---|-----|------|------|
| 1 | `sql: unknown driver "postgres"` | 生产代码未 import postgres 驱动（单测用 sqlite 不触发） | main.go blank import `lib/pq` |
| 2 | `unknown driver "pgx"` | store.go ping 用 pgx，main 用 lib/pq，两套驱动混用 | store 统一用 ent API ping（去掉 pgx 依赖） |
| 3 | config 嵌套字段 default 不生效 | envconfig 对嵌套 struct + default 的已知限制 | 部署需显式设 `VIGIL_DB_*` 环境变量（文档记录） |

## 五、注意事项

- **部署必须显式设数据库环境变量**（bug #3）：`VIGIL_DB_USER/PASSWORD/NAME`，不能依赖 default。
- **ent edge 列名非 `<entity>_id`**：如 `service_escalation_policy`、`team_services`。raw SQL 操作要注意，用 ent API 则无此问题。
- **本验证未覆盖**：通知实际送达（webhook URL 需配置）、resolved 事件、升级多级、IM 通道——这些待对应能力域完善。

## 六、复跑方式

```bash
cd <repo>
docker compose up -d
VIGIL_DB_USER=vigil VIGIL_DB_PASSWORD=vigil VIGIL_DB_NAME=vigil go run ./cmd/vigil/ migrate
VIGIL_DB_USER=vigil VIGIL_DB_PASSWORD=vigil VIGIL_DB_NAME=vigil go run ./cmd/vigil/ &
# 灌数据 + 触发 webhook（见 docs/e2e-verification.md 验证过程）
```
