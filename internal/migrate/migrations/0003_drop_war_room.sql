-- 0003_drop_war_room.sql
-- 移除作战室(War Room)残留列(ADR-0036)。
--
-- war_room 自创建起无任何写入路径,列内容恒为 NULL,删除无数据损失。
-- ent auto-migrate 不删列,须显式迁移;IF EXISTS 保证新装库(baseline 已不含该列)幂等。
ALTER TABLE incidents DROP COLUMN IF EXISTS war_room;
