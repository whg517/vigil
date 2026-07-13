#!/usr/bin/env bash
# Vigil 数据库 + Redis 恢复脚本（docs/operations.md §5）。
#
# 用法：
#   ./scripts/restore.sh /path/to/backup/20260101_020000
#
# 注意：恢复前先停 vigil 服务（避免恢复期间读写冲突）：docker compose stop vigil
# Redis 恢复会覆盖现有数据，谨慎操作。
# Redis 部分：compose 场景自动完成（识别 compose 起的 redis 容器并替换其数据）；
# 非 compose 部署回退为手工步骤提示。可用 VIGIL_REDIS_CONTAINER=<容器名/ID> 显式指定容器。
set -euo pipefail

BACKUP_PATH="${1:?Usage: restore.sh <backup_dir>}"
if [[ ! -d "${BACKUP_PATH}" ]]; then
  echo "Error: backup dir ${BACKUP_PATH} not found" >&2
  exit 1
fi

PG_HOST="${VIGIL_DB_HOST:-localhost}"
PG_PORT="${VIGIL_DB_PORT:-5432}"
PG_NAME="${VIGIL_DB_NAME:-vigil}"
PG_USER="${VIGIL_DB_USER:-vigil}"
PG_PASSWORD="${VIGIL_DB_PASSWORD:?VIGIL_DB_PASSWORD required}"

REDIS_HOST="${VIGIL_REDIS_ADDR:-localhost:6379}"

echo "[$(date)] ⚠️  恢复将覆盖 ${PG_HOST}/${PG_NAME} 和 ${REDIS_HOST} 的现有数据"
read -r -p "确认恢复？输入 YES 继续: " confirm
if [[ "${confirm}" != "YES" ]]; then
  echo "已取消"
  exit 0
fi

# === PostgreSQL 恢复（pg_restore 自定义格式）===
if [[ -f "${BACKUP_PATH}/postgres.dump" ]]; then
  echo "[$(date)] Restoring PostgreSQL from ${BACKUP_PATH}/postgres.dump..."
  export PGPASSWORD="${PG_PASSWORD}"
  pg_restore -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_USER}" -d "${PG_NAME}" \
    --clean --if-exists --no-owner --no-privileges \
    "${BACKUP_PATH}/postgres.dump"
  echo "[$(date)] PostgreSQL restore done"
else
  echo "[$(date)] No postgres.dump found, skipping PG restore"
fi

# === Redis 恢复 ===
# 注意：RDB 不能经 redis-cli 导入；且 AOF 开启时 Redis 启动只认 AOF、直接替换 dump.rdb
# 无效（实测 redis:7，含「删掉 AOF 目录只留 dump.rdb」的情况——会得到空库）。
# 正确序列：停 redis → 清 AOF + 放入备份 RDB → 临时以 appendonly no 启动（加载 RDB）
# → CONFIG SET appendonly yes（从已载入数据集重写 AOF）→ 停临时实例 → 起正式 redis。
# compose 场景下面自动执行该序列；非 compose 场景打印手工步骤。
if [[ -f "${BACKUP_PATH}/redis.rdb.gz" ]]; then
  echo "[$(date)] Redis RDB found: ${BACKUP_PATH}/redis.rdb.gz"
  gunzip -kf "${BACKUP_PATH}/redis.rdb.gz"
  echo "[$(date)] Decompressed to ${BACKUP_PATH}/redis.rdb"

  # 定位 compose 的 redis 容器（可用 VIGIL_REDIS_CONTAINER 覆盖；找不到则走手工提示）。
  REDIS_CONTAINER="${VIGIL_REDIS_CONTAINER:-$(docker compose ps -aq redis 2>/dev/null || true)}"
  REDIS_VOLUME=""
  REDIS_IMAGE=""
  if [[ -n "${REDIS_CONTAINER}" ]]; then
    # 取挂载在 /data 的命名卷与镜像（bind mount 或无卷时回退手工路径）
    REDIS_VOLUME="$(docker inspect "${REDIS_CONTAINER}" \
      --format '{{ range .Mounts }}{{ if eq .Destination "/data" }}{{ .Name }}{{ end }}{{ end }}' 2>/dev/null || true)"
    REDIS_IMAGE="$(docker inspect "${REDIS_CONTAINER}" --format '{{ .Config.Image }}' 2>/dev/null || true)"
  fi

  if [[ -n "${REDIS_CONTAINER}" && -n "${REDIS_VOLUME}" && -n "${REDIS_IMAGE}" ]]; then
    BACKUP_ABS="$(cd "${BACKUP_PATH}" && pwd)"
    RESTORE_NAME="vigil-redis-restore-$$"
    echo "[$(date)] Restoring Redis via docker (container=${REDIS_CONTAINER}, volume=${REDIS_VOLUME})..."
    docker stop "${REDIS_CONTAINER}" > /dev/null

    # 清旧 AOF + 放入备份 RDB
    docker run --rm -v "${REDIS_VOLUME}:/data" -v "${BACKUP_ABS}:/backup:ro" alpine \
      sh -c 'rm -rf /data/appendonlydir && cp /backup/redis.rdb /data/dump.rdb'

    # 临时以 appendonly no 启动以加载 RDB，再开 AOF 重写
    docker run --rm -d --name "${RESTORE_NAME}" -v "${REDIS_VOLUME}:/data" \
      "${REDIS_IMAGE}" redis-server --appendonly no > /dev/null
    for _ in $(seq 1 30); do
      docker exec "${RESTORE_NAME}" redis-cli ping > /dev/null 2>&1 && break
      sleep 1
    done
    docker exec "${RESTORE_NAME}" redis-cli config set appendonly yes > /dev/null
    # 等 AOF 重写完成再停，避免半写状态
    for _ in $(seq 1 60); do
      docker exec "${RESTORE_NAME}" redis-cli info persistence 2>/dev/null \
        | grep -q 'aof_rewrite_in_progress:0' && break
      sleep 1
    done
    docker stop "${RESTORE_NAME}" > /dev/null

    docker start "${REDIS_CONTAINER}" > /dev/null
    echo "[$(date)] Redis restore done（数据已载入，AOF 已重建）"
  else
    echo "⚠️  未识别到 compose redis 容器（或其 /data 非命名卷），Redis 恢复需手工执行："
    echo "    1. 停 Redis（及所有写入方）"
    echo "    2. 清空数据目录里的 appendonlydir/，将 ${BACKUP_PATH}/redis.rdb 复制为数据目录 dump.rdb"
    echo "    3. 以 appendonly no 启动 Redis（AOF 开启时不会加载 dump.rdb）"
    echo "    4. redis-cli config set appendonly yes（重建 AOF），等重写完成"
    echo "    5. 恢复原配置（appendonly yes）重启 Redis"
    echo "    详见 docs/operations.md §5.2"
  fi
else
  echo "[$(date)] No redis.rdb.gz found, skipping Redis restore"
fi

echo "[$(date)] Restore complete from ${BACKUP_PATH}"
