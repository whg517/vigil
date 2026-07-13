# Vigil 运维手册(Operations Runbook)

> 本文是**可执行的操作手册**:部署、升级、备份回滚、故障排查。
> 每项操作背后的"为什么这样设计"见对应 [ADR](./adr/):部署形态 [ADR-0031](./adr/0031-single-binary-compose-helm.md)、迁移与回滚 [ADR-0032](./adr/0032-migration-backup-restore.md)、自监控与鉴权 [ADR-0033](./adr/0033-selfmon-and-auth.md)。
> 环境变量完整清单以 [`.env.example`](../.env.example) 为准(全部 `VIGIL_` 前缀),本文只列关键项。

---

## 1. 前置依赖

| 组件 | 要求 | 说明 |
|------|------|------|
| PostgreSQL | 13+,**必须装 pgvector 扩展** | `Incident.embedding` 列类型 `vector(1536)`,migrate 时扩展缺失会报错。推荐 `pgvector/pgvector:pg16` 镜像(自带扩展) |
| Redis | 6+ | 缓存/队列/锁。生产必须开持久化(AOF/RDB)——升级计时任务在 Redis,宕机即丢。**必须独占,见下方红线** |
| Docker / Compose | 单机部署 | — |
| kubectl + Helm | K8s 部署(可选) | 生产级 |
| pg_dump / redis-cli | 备份恢复(宿主机) | `scripts/backup.sh` 在宿主机执行,需安装与服务端匹配的客户端(compose 默认 PG16 → PostgreSQL 16 客户端;redis-cli 6+) |

> ⚠️ **红线:每套 Vigil 部署必须独占 Redis 实例(或至少独占 DB 编号)。**
> 两套部署(如生产 + 预发,或两个环境的 e2e)共用同一 Redis DB 时,双方的 Asynq worker
> 会互相消费对方队列里的任务——升级计时/通知会被另一环境"偷走"执行,表现为本环境
> 升级不触发、通知丢失,且极难排查(已实证)。隔离手段:独立 Redis 实例,或
> `VIGIL_REDIS_DB` 错开 DB 编号。

## 2. Docker Compose 部署(默认)

```bash
git clone <repo> vigil && cd vigil
cp .env.example .env        # 编辑:改 DB/Redis 默认凭证,按需配 IM/LLM
# 必填:JWT 签名密钥(compose 内 vigil 固定 production 模式,缺失时 up 直接报错)
echo "VIGIL_AUTH_JWT_SECRET=$(openssl rand -hex 32)" >> .env
docker compose run --rm vigil migrate   # 首次:建表 + 启用 pgvector(自动先起 postgres/redis)
docker compose up -d                    # postgres + redis + vigil
open http://localhost:8080          # Web UI
open http://localhost:8080/docs     # Swagger
curl http://localhost:8080/health   # 健康检查
```

说明:

- `.env` 通过 compose 的 `${}` 插值 + vigil 服务的 `env_file` 双路生效:改凭证只改 `.env`
  一处,postgres 容器与 vigil 应用自动同步(勿再手改 docker-compose.yml)。
- postgres/redis 端口仅绑定 `127.0.0.1`(本机调试用),不对外网暴露;容器网络内互通。
- redis 已开 AOF 持久化并挂数据卷;三个服务均 `restart: unless-stopped`。

## 3. 生产 checklist

- [ ] **`VIGIL_APP_ENV=production`(第一优先)**:development 模式是为本地调试放宽的——存在无鉴权的测试端点与请求头身份回退等宽松行为(可被伪造身份/清空数据),**绝不能用于任何可被他人访问的环境**;具体行为与开关以 [README 安全警告](../README.md) 为准。docker compose 部署已在 compose 文件内固定 production;裸二进制/自定义部署必须显式设置。
- [ ] **改默认凭证**:DB / Redis 密码(绝不用 .env.example 默认值);首登改掉种子管理员 `admin/changeme`。
- [ ] **鉴权**:`VIGIL_AUTH_JWT_SECRET`(强随机)+ `VIGIL_AUTH_ENABLED=true`;`X-Vigil-User-ID` 头回退仅限内网,生产禁用(见 [ADR-0033](./adr/0033-selfmon-and-auth.md))。
- [ ] **HTTPS**:前置 nginx/Caddy/Traefik 终止 TLS。
- [ ] **Redis 持久化 + 独占**:AOF/RDB 必开(compose 默认已开 AOF + 数据卷);**独占实例或 DB 编号**(见 §1 红线)。注:当前仅支持单地址直连,Redis Sentinel/Cluster 尚不支持(路线图)。
- [ ] **pgvector 验证**:`SELECT extversion FROM pg_extension WHERE extname='vector';` 有结果。
- [ ] **LLM 成本控制**:配 `VIGIL_LLM_COST_RATE_LIMIT_PER_MIN` / `VIGIL_LLM_COST_TOKEN_QUOTA` 防账单失控。
- [ ] **凭证不入库**:IM AppSecret / LLM Key 仅从环境变量读。
- [ ] **备份**:`scripts/backup.sh` 挂 cron(内含 pg_dump + Redis 快照,保留 7 天;cron 里必须先 `source .env`,见 §5)。**升级前必备份——这是唯一的回滚手段**。
- [ ] **自监控**(可选):先配好独立通道(webhook/email),再开 `VIGIL_SELF_MONITOR_ENABLED=true`(默认关;通道未配就开只会空转,启动日志会 warn)。
- [ ] **暴露 `/metrics` + `/health`** 给外部监控(吃自己狗粮)。
- [ ] **SMTP 入向**(若开启 `VIGIL_INGESTION_SMTP_IN_ENABLED`):端口(默认 2525)必须仅内网可达——无 STARTTLS/SMTP AUTH,公网暴露属错误部署([ADR-0038](./adr/0038-smtp-inbound.md))。

## 4. 升级

```bash
scripts/backup.sh                               # ★ 升级前必备份
git pull && docker compose build vigil
docker compose run --rm vigil migrate status    # (可选)用新镜像看当前版本
docker compose run --rm vigil migrate           # ★ 用新镜像跑迁移(此时旧容器仍在跑旧代码)
docker compose up -d vigil                      # 切换到新镜像
```

> ⚠️ 迁移必须用 `docker compose run --rm vigil migrate`(新镜像的一次性容器),
> **不能** `docker compose exec vigil vigil migrate`——exec 进的是仍在运行的**旧**容器,
> 跑的是旧二进制里的旧迁移集,等于空跑,随后新版本起来会因 schema 落后而异常。

**停机预期**:迁移执行期间旧版本容器仍在服务。新增表/列这类兼容性迁移通常无感;
若某次迁移包含锁表或不兼容变更(发布说明会标注),应在维护窗口内先
`docker compose stop vigil` → migrate → `up -d vigil`,停机时长 ≈ 迁移耗时 + 容器启动(通常秒级~分钟级)。

## 5. 回滚(= 备份恢复)

本项目**不提供 `migrate down`**(理由见 [ADR-0032](./adr/0032-migration-backup-restore.md))。升级/迁移失败:

```bash
docker compose stop vigil
scripts/restore.sh backups/<timestamp>
# 部署回旧版本镜像/代码
docker compose up -d vigil
```

### 5.1 备份挂 cron

`backup.sh` 从环境变量读连接信息且 `VIGIL_DB_PASSWORD` 为必填——cron 环境没有你 shell 里的
变量,**不先 source .env 会直接退出**(且输出只进日志文件,无人值守时是静默失败)。正确写法:

```cron
0 2 * * * . /opt/vigil/.env && /opt/vigil/scripts/backup.sh /opt/vigil/backups >> /var/log/vigil-backup.log 2>&1
```

宿主机需安装与服务端匹配的 `pg_dump`(compose 默认 PG16 → PostgreSQL 16 客户端)与 `redis-cli`(见 §1)。

### 5.2 Redis 恢复(compose 场景)

PG 恢复由 `restore.sh` 全自动完成;Redis 恢复在 compose 场景 `restore.sh` 也会尝试自动执行
(检测到 compose redis 容器时)。若自动步骤失败或非 compose 部署,手工步骤如下——
注意 **AOF 开启时 Redis 只认 AOF、直接替换 dump.rdb 无效**(实测 redis:7):

```bash
docker compose stop vigil redis                     # 1. 停写入方 + redis
# 2. 清掉旧 AOF、放入备份 RDB(redisdata 卷名可用 docker volume ls 确认)
docker run --rm -v vigil_redisdata:/data -v $(pwd)/backups/<timestamp>:/backup alpine \
  sh -c 'rm -rf /data/appendonlydir && cp /backup/redis.rdb /data/dump.rdb'
# 3. 临时以 appendonly no 启动(否则 RDB 不会被加载),再开 AOF 令其从已载入数据集重写
docker run --rm -d --name vigil-redis-restore -v vigil_redisdata:/data redis:7-alpine \
  redis-server --appendonly no
docker exec vigil-redis-restore redis-cli config set appendonly yes
# 等 aof_rewrite_in_progress:0 且 aof_last_bgrewrite_status:ok
docker exec vigil-redis-restore redis-cli info persistence | grep -E 'aof_rewrite|bgrewrite'
docker stop vigil-redis-restore
docker compose up -d redis vigil                    # 4. 恢复正常拓扑
```

### 5.3 恢复演练清单

备份没经过恢复演练 = 没有备份。建议每季度(或重大升级前)在**隔离环境**演练一次:

- [ ] 用最近一次备份在干净环境执行 `restore.sh`,PG/Redis 均无报错;
- [ ] `vigil migrate status` 版本与备份时点一致;
- [ ] 登录 Web,抽查若干 Incident/时间线/排班数据完整;
- [ ] 演练环境使用**独立 Redis**(§1 红线:切勿连生产 Redis,会互抢队列任务);
- [ ] 记录恢复耗时(= 真实事故时的 RTO 参考)。

## 6. Kubernetes(Helm)

```bash
kubectl create secret generic vigil-secrets ...   # DB 密码/JWT Secret/Redis 密码走 existingSecret
helm install vigil ./deploy/helm
# ★ 首次安装后必须手动执行一次迁移,否则 readiness 探测(依赖 schema)持续 503:
kubectl exec deploy/vigil -- vigil migrate        # pod 未 ready 也可 exec
```

- **当前 chart 为单 Deployment(API + worker 同进程)、仅验证单副本部署**。
  vigil-api / vigil-worker 分离扩缩为路线图(二进制暂无角色 flag);
  Redis Sentinel/Cluster 亦为路线图(客户端仅支持单地址直连)。
- `replicaCount > 1` 时 WebSocket 广播依赖 Redis pub/sub(代码已实现),但多副本形态
  未经端到端验证,生产暂不建议。
- Chart 位于 [`deploy/helm/`](../deploy/helm/)(Chart/values/Deployment/Service/Ingress 可选/PDB)。

## 7. 故障排查

| 现象 | 排查 |
|------|------|
| `migrate` 报 `extension "vector" does not exist` | PG 未装 pgvector:换 `pgvector/pgvector:pg16` 镜像或手动装扩展 |
| IM 通知不发送 | 查 `VIGIL_IM_*` 凭证 + `VIGIL_IM_ONCALL_CHANNEL`;`/health` 看 redis;IM 群未配时会记 metric + warn(不静默) |
| AI 不工作 | `VIGIL_LLM_API_KEY` 未配 → 自动降级(复盘规则草稿、诊断跳过),不影响告警主链路 |
| 相似检索退化为文本匹配 | embed 维度与 `vector(1536)` 不符(如 Ollama nomic-embed-text 是 768 维),见 [ADR-0023](./adr/0023-llm-provider-cost-control.md) |
| 升级不触发/通知丢失(多环境共用 Redis) | §1 红线:另一环境的 worker 在消费本环境队列。隔离 Redis 实例/DB 编号后重启 |
| 升级任务丢失(Redis 数据丢过) | 预防:开 AOF/RDB(compose 默认已开)。已丢失:计时任务不会自动重建(暂无对账机制),人工兜底——筛出仍未 ack/resolve 的 Incident,在 Web/IM 上手动升级或再通知/重新指派 |
| 告警接入 401 | webhook token 不匹配 Integration.token(接入详情页可查看/轮换) |
| 队列积压 | Asynqmon 看队列深度/死信;接入层积压超阈会返回 429/503(payload 仍落库,恢复后可重放) |
