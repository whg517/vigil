#!/usr/bin/env bash
# Vigil 数据库 + Redis 备份脚本（docs/deployment.md D2）。
#
# 备份内容：
#   - PostgreSQL：pg_dump 全量（自定义格式，支持并行恢复）
#   - Redis：BGSAVE 触发 RDB 快照 + 拷贝 dump.rdb
#
# 用法：
#   ./scripts/backup.sh                          # 用环境变量（VIGIL_DB_HOST 等）
#   ./scripts/backup.sh /path/to/backup/dir      # 指定备份目录
#   CRON: 0 2 * * * /path/to/vigil/scripts/backup.sh >> /var/log/vigil-backup.log 2>&1
#
# 依赖：pg_dump（PostgreSQL 客户端）、redis-cli、gzip
# 恢复：参见 scripts/restore.sh
set -euo pipefail

BACKUP_DIR="${1:-./backups}"
TIMESTAMP=$(date +"%Y%m%d_%H%M%S")
DATE_DIR="${BACKUP_DIR}/${TIMESTAMP}"

# 从环境变量读连接信息（与 vigil 应用配置一致）
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

mkdir -p "${DATE_DIR}"
echo "[$(date)] Vigil backup → ${DATE_DIR}"

# === PostgreSQL 备份（pg_dump 自定义格式，支持并行恢复 + 压缩）===
echo "[$(date)] Backing up PostgreSQL ${PG_HOST}:${PG_PORT}/${PG_NAME}..."
export PGPASSWORD="${PG_PASSWORD}"
pg_dump -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_USER}" -d "${PG_NAME}" \
  --format=custom --compress=9 \
  -f "${DATE_DIR}/postgres.dump"
echo "[$(date)] PostgreSQL backup done: ${DATE_DIR}/postgres.dump ($(du -h "${DATE_DIR}/postgres.dump" | cut -f1))"

# === Redis 备份（BGSAVE 触发 RDB 快照，通过 redis-cli --rdb 拉取）===
echo "[$(date)] Backing up Redis ${REDIS_HOSTNAME}:${REDIS_PORT}..."
REDIS_ARGS=(-h "${REDIS_HOSTNAME}" -p "${REDIS_PORT}")
if [[ -n "${REDIS_PASSWORD}" ]]; then
  REDIS_ARGS+=(-a "${REDIS_PASSWORD}")
fi
redis-cli "${REDIS_ARGS[@]}" --rdb "${DATE_DIR}/redis.rdb"
gzip "${DATE_DIR}/redis.rdb"
echo "[$(date)] Redis backup done: ${DATE_DIR}/redis.rdb.gz ($(du -h "${DATE_DIR}/redis.rdb.gz" | cut -f1))"

# === 清理旧备份（保留最近 7 天）===
find "${BACKUP_DIR}" -maxdepth 1 -type d -mtime +7 -name "20*" -exec rm -rf {} \; 2>/dev/null || true
echo "[$(date)] Cleaned backups older than 7 days"

echo "[$(date)] Backup complete: ${DATE_DIR}"
