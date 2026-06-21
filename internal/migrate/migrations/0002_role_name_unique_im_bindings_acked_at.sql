-- 0002_role_name_unique_im_bindings_acked_at.sql
-- 增量迁移：
--   1. roles.name 加唯一约束（消除种子竞态 + 业务角色防重名）
--   2. im_account_bindings 表（IM 账号映射独立表，O(1) 索引查询）
--   3. incidents.acked_at 字段（真实 MTTA 计算）
--
-- 注：表结构的完整定义由 ent auto-migrate 创建（migrate.Run 第 5 步）。
-- 本文件处理 auto-migrate 不擅长的「字段约束变更」+ 显式建表保证索引就位。
-- SQLite 测试环境走 enttest.Open 的 auto-migrate，不执行本文件。

-- 1. roles.name 唯一约束（DO 块保证幂等：已存在则跳过）
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'roles_name_key'
    ) THEN
        -- 先清理可能的重复（取每组第一条，其余删除）避免加约束失败
        DELETE FROM roles a USING roles b
        WHERE a.id > b.id AND a.name = b.name;
        ALTER TABLE roles ADD CONSTRAINT roles_name_key UNIQUE (name);
    END IF;
END $$;

-- 2. im_account_bindings 表
CREATE TABLE IF NOT EXISTS im_account_bindings (
    id           SERIAL PRIMARY KEY,
    platform     VARCHAR(255) NOT NULL,   -- feishu | dingtalk | wecom
    account_id   VARCHAR(255) NOT NULL,
    user_im_bindings INTEGER NOT NULL,    -- FK -> users(id)（ent 生成的外键列名）
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    FOREIGN KEY (user_im_bindings) REFERENCES users(id) ON DELETE CASCADE
);

-- (platform, account_id) 全局唯一：同一 IM 账号只能绑一个 User
CREATE UNIQUE INDEX IF NOT EXISTS im_account_bindings_platform_account_id_key
    ON im_account_bindings (platform, account_id);

-- 3. incidents.acked_at（真实 MTTA 计算用）
ALTER TABLE incidents ADD COLUMN IF NOT EXISTS acked_at TIMESTAMPTZ;
