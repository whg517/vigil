# 安全策略（Security Policy）

Vigil 是自托管的告警处置平台，会接触生产环境的告警数据、值班人员联系方式与执行器凭据。我们严肃对待每一份安全报告。

## 支持版本

项目处于 0.x 阶段，安全修复只覆盖**最新的 minor 版本**：

| 版本 | 是否接收安全修复 |
|------|------------------|
| 最新 0.x minor（含其 patch） | ✅ |
| 更早版本 | ❌ 请先升级到最新版本再确认问题是否仍存在 |

版本号见 git tag（`git tag -l 'v*'`）。

## 如何申报漏洞

**请勿通过公开 issue、PR 或讨论区披露安全漏洞。**

请使用 GitHub **Private Vulnerability Reporting**（私密漏洞报告）：

1. 打开本仓库的 **Security** 标签页；
2. 选择 **Report a vulnerability**（报告漏洞）；
3. 按表单描述影响版本、复现步骤、影响面与可能的修复建议。

报告将只对维护者与你本人可见（GitHub Security Advisories 机制）。我们会尽力在 **7 天内确认**报告并给出初步评估，修复发布后与你协商公开披露时间与致谢方式。本项目为开源项目，响应时效为尽力而为，不构成正式 SLA。若 7 天内 GitHub Private Vulnerability Reporting 仍无响应，可发邮件到 `security@<维护者域名>` 作为备用渠道（仅在主渠道失效时使用）。

## 代码层安全机制

除了部署基线，Vigil 在代码层也实现了若干安全机制（设计决策见对应 ADR）：

- **RBAC 可自配置权限点**：权限点系统枚举，角色自由组合 —— [ADR-0027](docs/adr/0027-rbac-permissions-roles.md)
- **双轨审计（无静默截断）**：API/IM 同链路留痕，导出超 5 万行带 `X-Vigil-Truncated` 头 —— [ADR-0029](docs/adr/0029-dual-audit-no-silent-truncation.md)
- **凭据 AES-256 加密托管**：Runbook 执行器的外部凭据密文落库、运行时解密注入 —— [ADR-0030](docs/adr/0030-integrations-encrypted-openapi.md)
- **Runbook 两档 + 人工审批**：诊断只读、处置写操作须人确认 —— [ADR-0021](docs/adr/0021-runbook-two-tier.md)
- **自监控与鉴权危险开关**：默认关、production 强制关 —— [ADR-0033](docs/adr/0033-selfmon-and-auth.md)

## 报告范围提示

优先关注（不限于）：

- 鉴权/RBAC 绕过（Web / API / IM 操作链路）；
- 跨团队数据隔离失效（越权读写他队 incident/配置）；
- Runbook 执行链路的注入或审批闸门绕过（含 SSRF）；
- 凭据托管（AES-256-GCM）相关的明文泄露路径；
- 入向 Webhook/SMTP 解析导致的拒绝服务或代码执行。

部署配置不当（如自行关闭鉴权后暴露公网）不属于产品漏洞，但文档误导导致的不安全默认值属于——欢迎报告。

## 自托管安全基线

部署前请务必完成：

- [`README.md`](README.md) 「安全警告（自托管必读）」——JWT 密钥、默认管理员改密、危险开关（`AUTH_ENABLED` / header 回退 / 测试端点）的行为与约束；
- [`docs/operations.md`](docs/operations.md) 「生产 checklist」——`VIGIL_APP_ENV=production` 第一优先，及网络暴露面、备份等逐项核对。

简要底线：生产必须 `VIGIL_APP_ENV=production`、设置强随机 `VIGIL_AUTH_JWT_SECRET`、立即修改默认管理员密码、不把 API 直接暴露到不受信网络。
