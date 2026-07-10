-- 0004_trim_deferred_types.sql
-- ADR-0037 收敛延期功能:Integration/TicketIntegration 类型枚举收窄前,先把存量行归一。
--
--   - integrations.type ∈ (zabbix, cloud) → webhook:这两类从未有适配器,推送本就走
--     通用归一化(失败落 parse_failed),webhook 即其实际语义。
--   - ticket_integrations.type ∈ (jira, zentao) → webhook:占位适配器从未建单成功,
--     归一到唯一真实类型。
--
-- 幂等:WHERE 命中即改,重复执行无副作用。post-migrate 阶段执行(表已存在)。
UPDATE "integrations" SET "type" = 'webhook' WHERE "type" IN ('zabbix', 'cloud');
UPDATE "ticket_integrations" SET "type" = 'webhook' WHERE "type" IN ('jira', 'zentao');
-- 通知模板与 IM 绑定的存量清理:
--   - phone/sms 通道的模板已无消费方,直接删除(内置默认模板 seed 只建 im/email/webhook)。
--   - wecom 平台的账号绑定已无对应适配器,删除(重绑 feishu/dingtalk 即可)。
DELETE FROM "notification_templates" WHERE "channel" IN ('phone', 'sms');
DELETE FROM "im_account_bindings" WHERE "platform" = 'wecom';
