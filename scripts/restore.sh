#!/usr/bin/env bash
# Vigil 数据库 + Redis 恢复脚本（docs/deployment.md D2）。
#
# 用法：
#   ./scripts/restore.sh /path/to/backup/20260101_020000
#
# 注意：恢复前先停 vigil 服务（避免恢复期间读写冲突）。
# Redis 恢复会覆盖现有数据，谨慎操作。
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
REDIS_HOSTNAME="${REDIS_HOST%%:*}"
REDIS_PORT="${REDIS_PORT:-${REDIS_HOST##*:}}"
REDIS_PORT="${REDIS_PORT:-6379}"
REDIS_PASSWORD="${VIGIL_REDIS_PASSWORD:-}"

echo "[$(date)] ⚠️  恢复将覆盖 ${PG_HOST}/${PG_NAME} 和 ${REDIS_HOST} 的现有数据"
read -p "确认恢复？输入 YES 继续: " confirm
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

# === Redis 恢复（解压 RDB + FLUSHALL + 通过 redis-cli 还原）===
# 注意：Redis RDB 不能直接通过 redis-cli 导入。生产恢复推荐：
# 1. 停 Redis → 替换 dump.rdb → 启 Redis（需文件系统访问）
# 2. 或用 redis-cli --pipe 批量重放（仅字符串类数据）
# 此脚本提供解压，需人工完成 RDB 替换。
if [[ -f "${BACKUP_PATH}/redis.rdb.gz" ]]; then
  echo "[$(date)] Redis RDB found: ${BACKUP_PATH}/redis.rdb.gz"
  gunzip -kf "${BACKUP_PATH}/redis.rdb.gz"
  echo "[$(date)] Decompressed to ${BACKUP_PATH}/redis.rdb"
  echo "⚠️  Redis RDB 恢复需手动：停 Redis → 替换 dump.rdb → 启 Redis"
else
  echo "[$(date)] No redis.rdb.gz found, skipping Redis restore"
fi

echo "[$(date)] Restore complete from ${BACKUP_PATH}"
