# ADR-0020: 拉人即事件级临时授权

| 字段 | 内容 |
|------|------|
| **状态** | Accepted |
| **日期** | 2026-07-09 |
| **相关** | [`0028-single-org-soft-isolation.md`](./0028-single-org-soft-isolation.md)、[`0027-rbac-permissions-roles.md`](./0027-rbac-permissions-roles.md)、`internal/incident/temp_grant.go` |

## 背景

Vigil 采用单组织多团队软隔离:资源归属团队,权限按 scope 判定,团队之间默认互不可处置。但真实故障常需临时拉外团队的人来帮忙处置。若为此放宽软隔离,协作口子一开就收不回;若临时授权不精确回收,又会造成权限泄漏。

## 决策

@人 / `add_responder` 时,由 `ResponderGranter` 发放**事件级临时授权**。

- **按需发放**:仅当被拉人对该 incident 所属 team **无处置权限**时,才发放内置 `responder` 角色绑定;发放 **team scope**(非 org)。
- **绑定元信息**:带 `expires_at`(默认 24h)+ `source_incident_id`。
- **自动撤销**:incident 收口(Closed / Resolved / Merged)时按 `source_incident_id` 自动删除该绑定;若漏删,则由 `expires_at` 过期兜底。
- **审计与实时性**:发放 / 撤销均落审计;authz 实时查库,撤销即失效。
- 实现见 `internal/incident/temp_grant.go`。

## 理由

- 在软隔离下提供**受控**的跨团队协作方式:不放宽软隔离本身,只在需要处协作时精确开一个口子。
- 双重回收(收口删除 + `expires_at` 兜底)确保临时权限不残留,避免权限泄漏。
- 授权与该 incident 强绑定(`source_incident_id`),回收边界清晰。

## 备选方案

- **放宽软隔离 / 授予 org scope**:口子过大,协作变越权——否决。
- **手工事后回收临时权限**:必然漏删、残留,形成权限泄漏——改为按 `source_incident_id` 自动撤销 + 过期兜底。

## 影响 / 权衡

- 仅对"无权者"发放,已有权限的人不重复授权,避免污染其正常绑定。
- 双重回收使临时权限最长存活约 24h,即便收口逻辑漏删也会过期失效。
- authz 实时查库,撤销即时生效,不依赖缓存失效窗口。
