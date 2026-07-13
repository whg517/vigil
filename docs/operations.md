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

## 6. 密钥与凭证轮换

Vigil 涉及四类密钥/凭证,轮换能力差异很大,动手前先分清(能力现状以代码为准,勿凭直觉操作):

| 密钥/凭证 | 载体 | 影响面 | 轮换支持 |
|------|------|------|------|
| 凭据加密密钥 | `VIGIL_CREDENTIAL_ENCRYPTION_KEY` | Runbook 执行器凭据、工单集成凭据/回调密钥、Webhook 订阅签名密钥(**同一把钥**) | ⚠️ 无 keyring,换钥后旧密文全部不可解,须逐条重录(§6.1) |
| JWT 签名密钥 | `VIGIL_AUTH_JWT_SECRET` | 全部登录态(Web/IM 同一鉴权链路) | 支持但一刀切:换钥 = 全员立即下线(§6.2) |
| API Key | 库内 SHA256 哈希 | 程序化接入方(开放 API/CI) | 新建 + 切换 + 删旧,可平滑过渡(§6.3) |
| 告警源 webhook token | `Integration.token` | 单个告警接入点 | ✅ 内置一键轮换(§6.3) |

### 6.1 凭据加密密钥(VIGIL_CREDENTIAL_ENCRYPTION_KEY)

**先读限制**:当前实现为单密钥 AES-256-GCM,密文不带密钥版本号,**无 keyring/新旧双钥并存机制**——进程只认当前一把钥。换钥后所有旧密文一律解密失败;且 Vigil 从不回显任何明文/密文(安全红线,接口与页面均只出元数据),**无法从 Vigil 导出旧值再自动重加密**。因此轮换 = 换钥 + 逐条人工重录,重录所需明文必须来自外部系统侧的源头(Jenkins/Ansible token、工单系统凭据、与 webhook 接收方约定的签名密钥)。

同一把钥加密了三类数据,一换全换,解密失败的表现**不一样**:

| 数据 | 换钥后的表现 | 重录入口 |
|------|------|------|
| Runbook 执行器凭据(Credential) | 显式失败:引用该凭据的 Runbook step 执行报解密错误(错误已脱敏) | Web「凭据」页,或 `PATCH /api/v1/credentials/{id}` 传 `secret` |
| 工单集成凭据/回调密钥(TicketIntegration) | ★ **静默失败**:解密失败时按"历史明文"透传,即拿密文串去调外部工单系统 → 对端认证失败 | Web「工单集成」页,或 `PATCH /api/v1/ticket-integrations/{id}` 传 `credential`/`callback_secret` |
| Webhook 订阅签名密钥(WebhookSubscription) | ★ **静默失败**:用密文串计算出站签名,接收端验签不过 | Web「Webhook 订阅」页,或 `PATCH /api/v1/webhook-subscriptions/{id}` 传 `signing_secret` |

> ⚠️ 后两类的静默透传是为兼容加密功能启用前落库的明文数据,换钥后**不会报任何错**,只会"签名/认证悄悄不对"。轮换必须以"逐条重录 + 端到端验证"为完成标准,不能以"没报错"为准。

**例行轮换操作序列**:

1. **盘点**:导出三类清单(均只含元数据):`GET /api/v1/credentials`、`GET /api/v1/ticket-integrations`、`GET /api/v1/webhook-subscriptions`,或在对应 Web 页面逐条记录。
2. **备好明文源值**:从外部系统侧取得每条的当前值;拿不到源值的(如当初随手生成没留档),趁此机会在外部系统重签一个新值。
3. **换钥**:`openssl rand -base64 32` 生成新钥 → 更新 `.env`(compose)或 vigil-secrets(Helm)中的 `VIGIL_CREDENTIAL_ENCRYPTION_KEY` → 重启 vigil(配置仅启动时读取)。
4. **逐条重录**:对盘点清单里的每一条,经 Web 页或 PATCH 接口重新提交明文(提交即用新钥重加密落库)。
5. **验证**:执行一个引用凭据的 Runbook step 确认无解密错误;触发一次建单确认工单系统认证通过;触发一次订阅事件并在接收端确认验签通过。

**疑似泄露 vs 例行轮换**:

- **疑似泄露**(密钥可能与库同时外泄):库内所有密文应视为已泄露。**先在外部系统侧吊销/重签全部受托管凭据**,再走上述序列录入新值——顺序必须是"先废外部凭据,再换 Vigil 密钥",否则窗口期内旧凭据仍可被持有者滥用。
- **例行轮换**:凭据本体不必换,仅换加密钥并重录同值;可分批推进,期间未重录条目按上表"表现"降级(注意静默失败的两类要优先)。

### 6.2 JWT 签名密钥(VIGIL_AUTH_JWT_SECRET)

- **影响**:HS256 单密钥签发/校验,换钥重启后所有已签发 token(access 默认 15m、refresh 默认 30d)立即失效——**全员强制下线**,重新登录即恢复,无数据影响。API Key 不受影响(独立哈希校验);告警接入 token 不受影响。
- **建议时机**:例行轮换放低峰/维护窗口,提前在 IM 周知"需重新登录"。**疑似泄露立即换,不等窗口**——泄露的 secret 可伪造任意用户的 token,比全员下线严重得多。
- **操作序列**:
  1. `openssl rand -hex 32` 生成新值;
  2. 更新 `.env`(compose)或 vigil-secrets(Helm)中的 `VIGIL_AUTH_JWT_SECRET`;
  3. `docker compose up -d vigil`(或 `kubectl rollout restart deploy/vigil`);
  4. 验证:持旧 token 调任意 API 返回 401,重新登录成功。
- 补充:若只需强制**单个用户**下线(如单账号被盗),不必动全局密钥——该用户改密即令其全部旧 token 失效(token_version 吊销机制)。

### 6.3 API Key 与告警源 webhook token

- **API Key**:无原地轮换,按"新建(`POST /api/v1/api-keys`,明文仅此一次返回)→ 切换调用方 → 删旧(`DELETE /api/v1/api-keys/{id}`)"三步平滑过渡,新旧并存期间无中断。
- **告警源 webhook token**:接入详情页一键轮换,或 `POST /api/v1/integrations/{id}/rotate-token`;**旧 token 立即失效**,轮换后须同步更新告警源侧的推送地址,否则接入 401(见「故障排查」表)。

## 7. Kubernetes(Helm)

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

## 8. 故障排查

| 现象 | 排查 |
|------|------|
| `migrate` 报 `extension "vector" does not exist` | PG 未装 pgvector:换 `pgvector/pgvector:pg16` 镜像或手动装扩展 |
| IM 通知不发送 | 查 `VIGIL_IM_*` 凭证 + `VIGIL_IM_ONCALL_CHANNEL`;`/health` 看 redis;IM 群未配时会记 metric + warn(不静默) |
| AI 不工作 | `VIGIL_LLM_API_KEY` 未配 → 自动降级(复盘规则草稿、诊断跳过),不影响告警主链路 |
| 相似检索退化为文本匹配 | embed 维度与 `vector(1536)` 不符(如 Ollama nomic-embed-text 是 768 维),见 [ADR-0023](./adr/0023-llm-provider-cost-control.md) |
| 升级不触发/通知丢失(多环境共用 Redis) | §1 红线:另一环境的 worker 在消费本环境队列。隔离 Redis 实例/DB 编号后重启 |
| 升级任务丢失(Redis 数据丢过) | 预防:开 AOF/RDB(compose 默认已开)。已丢失:升级对账 sweeper 会周期核对并重排缺失的升级任务(默认 2 分钟,`VIGIL_ESCALATION_SWEEP_INTERVAL`,见 ADR-0016);等不及时人工兜底——在 Web/IM 上手动升级或再通知 |
| 告警接入 401 | webhook token 不匹配 Integration.token(接入详情页可查看/轮换) |
| 队列积压 | Asynqmon 看队列深度/死信;接入层积压超阈会返回 429/503(payload 仍落库,恢复后可重放) |

## 9. 外部监控接入(谁来监控守夜人)

Vigil 的自监控(selfmon)运行在 Vigil 进程内:进程崩了、Redis 挂了、宿主机断电时,
selfmon 与故障**同生共死**,不可能发出告警。所以生产部署必须有一个**独立于 Vigil
故障域**的外部监控兜底——这是自监控闭环的最后一环,不是可选项。

### 8.1 部署建议

- **外部 Prometheus(或兼容抓取端)必须部署在独立故障域**:不同宿主机/集群/可用区,
  绝不与 Vigil 跑在同一台 compose 主机上(主机挂 = 监控与被监控者一起消失)。
  已有公司级监控平台的,把 Vigil 作为普通抓取目标纳入即可。
- 抓取 `/metrics`(Prometheus 文本格式)+ 探测 `/health`(blackbox exporter 或等价探针)。
- selfmon(进程内,秒级发现队列积压/通知失败)与外部监控(进程外,兜底进程/Redis 整体
  故障)互补,**两者都要**:只开 selfmon 有共死盲区,只有外部监控则丢失内部信号语义。

### 8.2 必抓指标清单

| 指标 | 含义 | 建议告警条件 |
|------|------|------|
| `up{job="vigil"}` | 抓取目标存活(Prometheus 内置) | `== 0` 持续 1m —— 进程/网络故障,selfmon 已共死,只有这里能发现 |
| `/health` 探测 | 健康检查(含 DB/Redis 依赖) | 非 200 持续 1m |
| `vigil_queue_tasks{state="archived"}` | **死信**:重试耗尽的最终失败任务(升级/通知彻底丢失) | `> 0` —— 出现即须人工介入(Asynqmon 里检查后重放或删除) |
| `vigil_queue_tasks{state="pending"}` | 队列积压(待消费) | 按容量定阈,如 `> 5000` 持续 5m |
| `vigil_queue_stats_collect_errors_total` | 队列指标采集失败(此时 queue gauge 是陈旧值) | `increase(...[5m]) > 0` —— 同时也是 Redis 故障信号 |
| `vigil_self_monitor_alerts_total` | selfmon 触发过自告警(按 kind: queue_depth/notif_failure/queue_probe_failure) | `increase(...[10m]) > 0` —— 交叉验证:selfmon 独立通道若没送达,外部监控还能看见触发本身 |
| `vigil_notifications_sent_total{result="failed"}` | 通知失败量(按 channel) | 失败占比 > 30% 持续 10m |

### 8.3 Prometheus 告警规则示例(可直接粘贴)

```yaml
groups:
  - name: vigil-watchdog
    rules:
      - alert: VigilDown
        expr: up{job="vigil"} == 0
        for: 1m
        labels: {severity: critical}
        annotations:
          summary: "Vigil 进程失联(自监控已随之失效,须立即处理)"
      - alert: VigilDeadLetterTasks
        expr: sum by (queue) (vigil_queue_tasks{state="archived"}) > 0
        for: 1m
        labels: {severity: critical}
        annotations:
          summary: "队列 {{ $labels.queue }} 出现死信任务(升级/通知已最终失败),到 Asynqmon 检查并重放"
      - alert: VigilQueueBacklog
        expr: sum by (queue) (vigil_queue_tasks{state="pending"}) > 5000
        for: 5m
        labels: {severity: warning}
        annotations:
          summary: "队列 {{ $labels.queue }} 积压 {{ $value }},消费可能跟不上生产"
      - alert: VigilQueueStatsStale
        expr: increase(vigil_queue_stats_collect_errors_total[5m]) > 0
        for: 5m
        labels: {severity: warning}
        annotations:
          summary: "队列指标采集持续失败(gauge 已陈旧),大概率 Redis 故障"
      - alert: VigilSelfMonitorFired
        expr: increase(vigil_self_monitor_alerts_total[10m]) > 0
        labels: {severity: warning}
        annotations:
          summary: "Vigil 自监控触发过 {{ $labels.kind }} 自告警——确认独立通道(webhook/email)是否送达"
      - alert: VigilNotificationFailureRatio
        expr: |
          sum(rate(vigil_notifications_sent_total{result="failed"}[10m]))
            / clamp_min(sum(rate(vigil_notifications_sent_total[10m])), 1e-9) > 0.3
        for: 10m
        labels: {severity: warning}
        annotations:
          summary: "通知失败率超 30%,检查通知通道配置与外部依赖"
```

说明:

- 队列指标由进程内采集器每 15s 用 asynq Inspector 刷新一次(非抓取时实时查 Redis),
  Redis 故障期间 gauge 保留最后一次成功值——判断其新鲜度靠 `vigil_queue_stats_collect_errors_total`。
- selfmon 侧的对应能力:队列探测**连续 N 次失败**(默认 3,`VIGIL_SELF_MONITOR_QUEUE_PROBE_FAILURE_THRESHOLD`)
  会触发 `queue_probe_failure` 自告警(区别于「积压」——那是链路慢了,这是链路探不到了);
  单次失败只记 warn 防抖动。该告警仍依赖 Vigil 进程与独立通道存活,故外部 `up`/`/health` 兜底不可省。
