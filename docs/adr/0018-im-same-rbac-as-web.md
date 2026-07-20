# ADR-0018： IM 走与 Web 相同 RBAC 链路

| 字段 | 内容 |
|------|------|
| **状态** | Accepted |
| **日期** | 2026-07-09 |
| **相关** | [`0027-rbac-permissions-roles.md`](./0027-rbac-permissions-roles.md)、[`0020-responder-temp-grant.md`](./0020-responder-temp-grant.md)、`internal/incident/service.go`、[`../architecture.md`](../architecture.md) |

## 背景

Vigil 是 IM-first 平台,ack / resolve / escalate / 拉人等处置操作大量在 IM 内完成。若 IM 侧另起一套鉴权(或干脆"在 IM 里就放行"),IM 就成了绕过 RBAC 的权限后门,软隔离与团队边界形同虚设。

## 决策

IM 操作复用与 Web **完全相同**的鉴权链路,不因"在 IM 里"而放行。

- **回调鉴权流程**:IM 回调(`im_platform + im_unionid + action`)→ 查 `User.im_accounts[platform]` 映射到 User → 把 action 解析成权限点 → 查 `incident.team_id` scope 下的 RoleBinding → 判定。无权则拒绝并记审计;**未绑定 IM 账号的用户在 IM 的操作被拒**。
- **复用层**:IM 与 Web 共用 `internal/incident/service.go`(`Ack / Resolve / Escalate / AddResponder`),鉴权只有这一处真相。
- **权限感知卡片**:卡片按当前用户权限渲染,无权的按钮不显示(ack → `incident.ack`,escalate → `incident.escalate`)。实现见 `card.go` 的 `Renderer.WithPermittedButtons`。

## 理由

- IM 不因身处 IM 而放行,鉴权单一来源,IM 不成为权限后门。
- 复用同一 service 层,避免两套鉴权逻辑分叉、漂移。
- 卡片按权限渲染,把"无权"前移到界面,减少无效操作与拒绝噪音。

## 备选方案

- **IM 侧独立鉴权 / 简化放行**:两套逻辑必然漂移,且"在 IM 里就放行"直接开后门——否决。

## 影响 / 权衡

- 未绑定 IM 账号的用户在 IM 内无法操作,须先完成账号映射——这是刻意的安全约束。
- 卡片渲染需实时解析当前用户权限,略增渲染成本,换取界面即权限边界。
- 跨团队协作不能靠"IM 里放行"实现,须走事件级临时授权(见 ADR-0020)。
