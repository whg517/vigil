-- 0002_pgvector.sql
-- 启用 pgvector 扩展（能力域 11 M11.4 相似事件检索依赖）。
-- Incident.embedding 列类型为 vector(1536)，由 ent auto-migrate 创建（migrate.Run 第 5 步），
-- 但 vector 类型本身需要先安装 pgvector 扩展，否则建列报错。
--
-- 部署前置：Postgres 镜像须含 pgvector（推荐 pgvector/pgvector:pg16，见 docs/operations.md）。
-- 手动安装扩展包后本语句才能成功；CREATE EXTENSION 需超级用户或已授予创建权限。
CREATE EXTENSION IF NOT EXISTS vector;
