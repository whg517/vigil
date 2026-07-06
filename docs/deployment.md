# Vigil 部署指南

| 字段 | 内容 |
|------|------|
| **文档版本** | v0.1 |
| **创建日期** | 2026-06-21 |
| **适用** | 自托管部署（Docker Compose 单机 / Kubernetes） |

---

## 1. 前置依赖

| 组件 | 要求 | 说明 |
|------|------|------|
| **PostgreSQL** | 13+，**必须装 pgvector 扩展** | 相似事件检索（能力域 11 M11.4）依赖 `vector` 类型。推荐 `pgvector/pgvector:pg16` 镜像（自带扩展） |
| **Redis** | 6+ | 缓存/队列/锁。生产建议开启持久化（AOF/RDB）+ 高可用 |
| Docker / Docker Compose | 单机部署用 | —— |
| kubectl + Helm（可选） | K8s 部署用 | 生产级 |

> ⚠️ **pgvector 是硬前置**：`Incident.embedding` 列类型为 `vector(1536)`，migrate 时若扩展未安装会报错。`pgvector/pgvector:pg16` 镜像开箱即用。

## 2. Docker Compose 一键部署（默认）

```bash
# 1. 克隆 + 配置
git clone <repo> vigil && cd vigil
cp .env.example .env
# 编辑 .env：务必改 DB/Redis 默认凭证，按需配 IM/LLM

# 2. 起服务（postgres + redis + vigil）
docker compose up -d

# 3. 首次：建表 + 启用 pgvector 扩展
docker compose exec vigil vigil migrate
# 输出 "migrate: schema applied" 即成功

# 4. 访问
open http://localhost:8080          # Web UI
open http://localhost:8080/docs     # Swagger API 文档
curl http://localhost:8080/health   # 健康检查
```

`docker-compose.yml` 的 vigil 服务默认前台跑应用；首次需手动跑一次 `migrate`（也可临时 `command: ["migrate"]` 后重启）。

## 3. 配置（环境变量）

完整变量见 [`.env.example`](../.env.example)，所有前缀 `VIGIL_`。关键分组：

### 3.1 数据库 / Redis（必填，改默认凭证）

```bash
VIGIL_DB_HOST=postgres
VIGIL_DB_USER=vigil
VIGIL_DB_PASSWORD=<强密码>     # ⚠️ 生产必改
VIGIL_DB_NAME=vigil
VIGIL_REDIS_ADDR=redis:6379
VIGIL_REDIS_PASSWORD=<强密码>  # ⚠️ 生产必改
```

### 3.2 IM 协同（能力域 8，按需配平台）

```bash
# 飞书
VIGIL_IM_FEISHU_APP_ID=
VIGIL_IM_FEISHU_APP_SECRET=
VIGIL_IM_FEISHU_VERIFICATION_TOKEN=    # 事件订阅校验
VIGIL_IM_FEISHU_ENCRYPT_KEY=           # 事件订阅加密（可选）

# 钉钉
VIGIL_IM_DINGTALK_APP_KEY=
VIGIL_IM_DINGTALK_APP_SECRET=
VIGIL_IM_DINGTALK_TOKEN=               # 事件订阅校验
VIGIL_IM_DINGTALK_AES_KEY=             # 事件订阅加密

# 值班群（告警卡片发送目标；飞书 chat_id 或钉钉 openConversationId）
VIGIL_IM_ONCALL_CHANNEL=
```

未配的平台自动降级为不发送（设计基线第 7 条）。

### 3.3 LLM / AI（能力域 11，按需配）

```bash
VIGIL_LLM_PROVIDER=glm                 # 提供方：glm（云端，默认）| ollama（本地，数据不出境）
# --- 云端智谱 GLM（Provider=glm 时生效）---
VIGIL_LLM_API_KEY=                     # 智谱 API Key，空则 AI 降级
VIGIL_LLM_MODEL=glm-4-flash
# --- 本地 Ollama（Provider=ollama 时生效，隐私场景数据不出境）---
VIGIL_LLM_OLLAMA_BASE_URL=http://localhost:11434
VIGIL_LLM_OLLAMA_MODEL=llama3
VIGIL_LLM_OLLAMA_EMBED_MODEL=nomic-embed-text  # ⚠️ 768 维；pgvector 列为 1536 维，维度不符则相似检索降级为 LIKE 文本匹配
# --- AI 建议置信度门槛 ---
VIGIL_LLM_CONFIDENCE_THRESHOLD=0.6     # 低于此值的 AI 建议不产出（防低置信度打扰）
# 成本控制（缓存/限流/配额，两种 Provider 通用）
VIGIL_LLM_COST_CACHE_TTL_SECONDS=3600
VIGIL_LLM_COST_RATE_LIMIT_PER_MIN=0    # 0=不限
VIGIL_LLM_COST_TOKEN_QUOTA=0           # 0=不限
```

### 3.4 鉴权 / Webhook 出口

```bash
VIGIL_AUTH_ENABLED=false               # true 强制业务 API 鉴权（X-Vigil-User-ID）
VIGIL_WEBHOOK_OUT_URLS=                # incident 生命周期事件外推，逗号分隔
```

### 3.5 自监控闭环（平台化 H2.4，按需开）

Vigil 自身也会出故障（队列积压、通知发不出去）。开启后周期巡检关键内部信号，超阈值时经
**独立通道**自告警 org_admin（默认关，需显式开）。

```bash
VIGIL_SELF_MONITOR_ENABLED=false                             # 默认关（未配独立通道时贸然开只会空转/误告）
VIGIL_SELF_MONITOR_CHECK_INTERVAL=1m                         # 巡检间隔
VIGIL_SELF_MONITOR_QUEUE_DEPTH_THRESHOLD=10000               # 队列 pending+active 之和超此值触发
VIGIL_SELF_MONITOR_NOTIFICATION_FAILURE_RATE_THRESHOLD=0.5   # 失败率阈值（0~1）
VIGIL_SELF_MONITOR_FAILURE_RATE_WINDOW=15m                   # 失败率统计窗口
VIGIL_SELF_MONITOR_FAILURE_RATE_MIN_SAMPLE=20                # 窗口内样本不足不判（防小样本误报）
VIGIL_SELF_MONITOR_COOLDOWN=30m                              # 同类告警冷却（防刷屏）
VIGIL_SELF_MONITOR_ALERT_CHANNELS=webhook,email             # 独立通道（★ 刻意排除 im）
```

> **★ 三条设计红线**（诚实 + 防自触发循环）：
> 1. 自告警**绕开 escalation 流水线**，直发独立通道（刻意排除 im）——被监控的正是这条链路，
>    若自告警也走它，「链路坏了→告警也走坏链路→告警也失败」等于没告警。
> 2. 失败率统计**只算业务通知**（关联 Incident 的送达），排除自告警自身（unrouted）——
>    否则「自告警失败→抬高失败率→再次触发→循环」。
> 3. 开启但独立通道（webhook 出口 URL / SMTP Host）**未真实配置**时，启动会 `log warn`
>    明说「告警可能无法送达」——不假装闭环一定成功。
>
> 触发次数暴露为 metric `vigil_self_monitor_alerts_total{kind}`（kind=queue_depth|notif_failure）。

## 4. 迁移机制

Vigil 用**版本化 SQL 迁移 + ent auto-migrate** 双轨，执行分三个阶段：

1. **pre-migrate**（`pre_` 前缀文件）：在 ent auto-migrate 之前执行。当前仅 `pre_0001_pgvector.sql`（`CREATE EXTENSION IF NOT EXISTS vector`，**需 pgvector 已安装**）。
2. **ent auto-migrate**：根据 `ent/schema/*.go` 自动创建/更新所有表结构。
3. **post-migrate**（其余 `.sql` 文件）：在 ent auto-migrate 之后执行。当前仅 `0002_baseline.sql`（占位）。

> 职责划分：ent schema 负责"建表 + 基本约束"，SQL 迁移仅处理 ent 不擅长的操作（PG 扩展安装、数据迁移等）。
> 新增迁移文件时，如需在 ent auto-migrate 之前执行（如安装扩展），文件名加 `pre_` 前缀。

幂等：`schema_migrations` 表追踪已应用版本，重复执行安全。

```bash
# 容器内
docker compose exec vigil vigil migrate

# 或本地源码（开发模式 godotenv 自动加载 .env）
go run ./cmd/vigil migrate
```

更多本地开发说明见 [`docs/local-dev.md`](local-dev.md)。

### 4.1 查看状态 / 回滚（status / down）

```bash
vigil migrate status               # 只读：当前版本 / 已知版本（已应用●待应用○ + 是否可逆 + 时间）/ 孤儿记录
vigil migrate down                 # 逆向【最近应用的一个】版本化 SQL 迁移
vigil migrate down --to <version>  # 逆向所有晚于 <version> 的版本（保留该版本及更早）
vigil migrate down --dry-run       # 只打印将执行什么，不落库
vigil migrate down --force         # 跳过破坏性步骤（如 DROP EXTENSION）的交互确认
```

**★ 回滚边界（诚实第一，勿被命令名误导）**：

- `migrate down` **只逆向「版本化 SQL 迁移」**——即提供了 `<version>.down.sql` 逆向脚本的 pre/post SQL 步骤（倒序执行、每步一事务、失败即停）。
- **ent auto-migrate 的实体结构变更（建表/加列/改类型）无法被逆向**：ent 是声明式 diff，不能安全自动 down。**回退实体结构只能靠备份恢复**（`scripts/restore.sh`）。命令**每次都会先打印此边界警告**。
- **无 `.down.sql` 的版本会被显式拒绝**（报错停止，不静默跳过）。例如 `0002_baseline`（代表全部 ent 表已建好的锚点）无 down 脚本，尝试逆向它会被拒——这是刻意防呆。
- **down 脚本约定**：与 up 脚本同目录（`internal/migrate/migrations/`），命名 `<version>.down.sql`（up=`pre_0001_pgvector.sql` → down=`pre_0001_pgvector.down.sql`）。脚本首部含注释 `-- vigil:destructive` 标记破坏性（删数据/结构），执行时要求交互输入 `yes` 或 `--force`。

## 5. 生产 checklist

- [ ] **改默认凭证**：DB / Redis 密码（绝不使用 .env.example 默认值）。
- [ ] **HTTPS**：前置 nginx/Caddy/Traefik 终止 TLS，后端走 `VIGIL_APP_ENV=production`。
- [ ] **Redis 持久化**：开启 AOF 或 RDB（任务队列/升级计时持久化在 Redis，宕机不丢）。
- [ ] **Redis 高可用**：哨兵/集群（生产）。
- [ ] **pgvector 扩展**：确认 `SELECT extversion FROM pg_extension WHERE extname='vector';` 有结果。
- [ ] **LLM 成本控制**：配 `VIGIL_LLM_COST_RATE_LIMIT_PER_MIN` / `TOKEN_QUOTA` 防账单失控。
- [ ] **凭证不入库**：IM AppSecret / LLM Key 仅从环境变量读，绝不硬编码/提交 git。
- [ ] **备份**：定期 `pg_dump` + Redis RDB 快照。（✅ 已提供 `scripts/backup.sh` + `scripts/restore.sh`，支持 cron 定时 + 保留 7 天）
- [ ] **监控自身**：`/metrics`（Prometheus）+ `/health`；Vigil 可接入自身监控（吃自己狗粮）。
- [ ] **自监控闭环**（H2.4，可选）：配好独立通道（webhook/email）后开 `VIGIL_SELF_MONITOR_ENABLED=true`，让队列积压/通知失败率超阈时自告警 org_admin（详见 §3.5）。

## 6. 升级

```bash
git pull
docker compose build vigil
docker compose exec vigil vigil migrate status  # （可选）升级前先看当前版本
docker compose exec vigil vigil migrate         # 应用新版本迁移
docker compose up -d vigil                       # 滚动重启
```

> 回滚：先备份（§5）。`migrate down` 只能逆向有 `.down.sql` 的版本化 SQL 迁移，
> **ent 实体结构变更（表/列）不可逆，回退结构必须用 `scripts/restore.sh` 从备份恢复**（见 §4.1）。

## 7. Kubernetes（Helm，生产）

集群部署拓扑（对应 architecture.md §7.2）：

```
vigil-api（Deployment，多副本，无状态）──┐
vigil-worker（Deployment，多副本）     ├──► 共享 postgres（含高可用 + pgvector）
                                       ├──► 共享 redis（含高可用）
                                       └──► 前端静态资源由 CDN/Ingress 提供
```

- API 与 worker 可独立扩缩（API 水平扩展接流量，worker 按队列深度扩缩）。
- 无状态设计：会话/队列状态全在 Redis，副本间对等。
- Helm Chart 详见 `deploy/helm/`（✅ 已提供：Chart.yaml/values.yaml/Deployment/Service/Ingress 模板）。
  - 生产部署：`kubectl create secret generic vigil-secrets ...` 后 `helm install vigil ./deploy/helm`
  - 敏感值（DB密码/JWT Secret/Redis密码）通过 existingSecret 引用，不硬编码
  - 多实例时需 Redis pub/sub 支持 WebSocket 广播（架构 §6.4 预留）

## 8. 故障排查

| 现象 | 排查 |
|------|------|
| `migrate` 报 `extension "vector" does not exist` | PG 未装 pgvector。换 `pgvector/pgvector:pg16` 镜像，或在 PG 上手动安装扩展 |
| IM 通知不发送 | 检查 `VIGIL_IM_*` 凭证是否齐备 + `VIGIL_IM_ONCALL_CHANNEL` 是否配；`/health` 看 redis 状态 |
| AI 不工作 | `VIGIL_LLM_API_KEY` 未配 → 自动降级（复盘走规则草稿、诊断跳过） |
| 升级任务丢失 | Redis 未持久化。开启 AOF/RDB |
| 告警接入 401 | webhook token 不匹配 Integration.token |

## 9. 开放问题

| # | 问题 | 状态 |
|---|------|------|
| D1 | Helm Chart 完整化（values/secrets/ingress） | ✅ 已完成（feat-deploy-artifacts） |
| D2 | 备份恢复的自动化脚本 | ✅ 已完成（feat-deploy-artifacts：backup.sh + restore.sh） |
| D3 | 多副本 worker 的队列分片细节 | 初期单实例，扩展时细化 |
