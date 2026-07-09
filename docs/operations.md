# Vigil 运维手册(Operations Runbook)

> 本文是**可执行的操作手册**:部署、升级、备份回滚、故障排查。
> 每项操作背后的"为什么这样设计"见对应 [ADR](./adr/):部署形态 [ADR-0031](./adr/0031-single-binary-compose-helm.md)、迁移与回滚 [ADR-0032](./adr/0032-migration-backup-restore.md)、自监控与鉴权 [ADR-0033](./adr/0033-selfmon-and-auth.md)。
> 环境变量完整清单以 [`.env.example`](../.env.example) 为准(全部 `VIGIL_` 前缀),本文只列关键项。

---

## 1. 前置依赖

| 组件 | 要求 | 说明 |
|------|------|------|
| PostgreSQL | 13+,**必须装 pgvector 扩展** | `Incident.embedding` 列类型 `vector(1536)`,migrate 时扩展缺失会报错。推荐 `pgvector/pgvector:pg16` 镜像(自带扩展) |
| Redis | 6+ | 缓存/队列/锁。生产必须开持久化(AOF/RDB)——升级计时任务在 Redis,宕机即丢 |
| Docker / Compose | 单机部署 | — |
| kubectl + Helm | K8s 部署(可选) | 生产级 |

## 2. Docker Compose 部署(默认)

```bash
git clone <repo> vigil && cd vigil
cp .env.example .env        # 编辑:改 DB/Redis 默认凭证,按需配 IM/LLM
docker compose up -d        # postgres + redis + vigil
docker compose exec vigil vigil migrate   # 首次:建表 + 启用 pgvector
open http://localhost:8080          # Web UI
open http://localhost:8080/docs     # Swagger
curl http://localhost:8080/health   # 健康检查
```

## 3. 生产 checklist

- [ ] **改默认凭证**:DB / Redis 密码(绝不用 .env.example 默认值);首登改掉种子管理员 `admin/changeme`。
- [ ] **鉴权**:`VIGIL_AUTH_JWT_SECRET`(强随机)+ `VIGIL_AUTH_ENABLED=true`;`X-Vigil-User-ID` 头回退仅限内网,生产禁用(见 [ADR-0033](./adr/0033-selfmon-and-auth.md))。
- [ ] **HTTPS**:前置 nginx/Caddy/Traefik 终止 TLS;`VIGIL_APP_ENV=production`。
- [ ] **Redis 持久化 + 高可用**:AOF/RDB 必开;生产建议哨兵/集群。
- [ ] **pgvector 验证**:`SELECT extversion FROM pg_extension WHERE extname='vector';` 有结果。
- [ ] **LLM 成本控制**:配 `VIGIL_LLM_COST_RATE_LIMIT_PER_MIN` / `VIGIL_LLM_COST_TOKEN_QUOTA` 防账单失控。
- [ ] **凭证不入库**:IM AppSecret / LLM Key 仅从环境变量读。
- [ ] **备份**:`scripts/backup.sh` 挂 cron(内含 pg_dump + Redis 快照,保留 7 天)。**升级前必备份——这是唯一的回滚手段**。
- [ ] **自监控**(可选):先配好独立通道(webhook/email),再开 `VIGIL_SELF_MONITOR_ENABLED=true`(默认关;通道未配就开只会空转,启动日志会 warn)。
- [ ] **暴露 `/metrics` + `/health`** 给外部监控(吃自己狗粮)。

## 4. 升级

```bash
scripts/backup.sh                                # ★ 升级前必备份
git pull && docker compose build vigil
docker compose exec vigil vigil migrate status   # (可选)看当前版本
docker compose exec vigil vigil migrate
docker compose up -d vigil
```

## 5. 回滚(= 备份恢复)

本项目**不提供 `migrate down`**(理由见 [ADR-0032](./adr/0032-migration-backup-restore.md))。升级/迁移失败:

```bash
docker compose stop vigil
scripts/restore.sh backups/<timestamp>
# 部署回旧版本镜像/代码
docker compose up -d vigil
```

## 6. Kubernetes(Helm,生产)

```bash
kubectl create secret generic vigil-secrets ...   # DB 密码/JWT Secret/Redis 密码走 existingSecret
helm install vigil ./deploy/helm
```

- vigil-api(无状态多副本)与 vigil-worker(按队列深度扩缩)独立扩缩;状态全在 Redis。
- 多实例 WebSocket 广播依赖 Redis pub/sub(已实现)。
- Chart 位于 [`deploy/helm/`](../deploy/helm/)(Chart/values/Deployment/Service/PDB)。

## 7. 故障排查

| 现象 | 排查 |
|------|------|
| `migrate` 报 `extension "vector" does not exist` | PG 未装 pgvector:换 `pgvector/pgvector:pg16` 镜像或手动装扩展 |
| IM 通知不发送 | 查 `VIGIL_IM_*` 凭证 + `VIGIL_IM_ONCALL_CHANNEL`;`/health` 看 redis;IM 群未配时会记 metric + warn(不静默) |
| AI 不工作 | `VIGIL_LLM_API_KEY` 未配 → 自动降级(复盘规则草稿、诊断跳过),不影响告警主链路 |
| 相似检索退化为文本匹配 | embed 维度与 `vector(1536)` 不符(如 Ollama nomic-embed-text 是 768 维),见 [ADR-0023](./adr/0023-llm-provider-cost-control.md) |
| 升级任务丢失 | Redis 未持久化,开 AOF/RDB |
| 告警接入 401 | webhook token 不匹配 Integration.token(接入详情页可查看/轮换) |
| 队列积压 | Asynqmon 看队列深度/死信;接入层积压超阈会返回 429/503(payload 仍落库,恢复后可重放) |
