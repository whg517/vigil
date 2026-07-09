# ADR-0027: RBAC 权限点枚举 + 自配置角色

| 字段 | 内容 |
|------|------|
| **状态** | Accepted |
| **日期** | 2026-07-09 |
| **相关** | [`0028-single-org-soft-isolation.md`](./0028-single-org-soft-isolation.md)、[`0020-responder-temp-grant.md`](./0020-responder-temp-grant.md)、`internal/auth/permission.go` |

## 背景

Vigil 面向"单组织多团队"的自托管场景,不同使用者对角色的划分习惯差异巨大:有的团队只需 admin/值班两档,有的需要区分值班长、只读干系人、oncall 参与者。若把角色档位写死在系统里,任何组织结构差异都要改代码,与"使用者自由配置"的诉求冲突。同时,权限判定必须有单一、稳定的真相来源,不能因入口(Web/IM/API)不同而分叉。

## 决策

采用三元组模型 `User──(RoleBinding, scope)──> Role ──> Permission`:

- **Permission**:系统内置的细粒度权限点枚举,形如 `<resource>.<action>`,代表系统能力边界(固定,不可由使用者增删)。清单以 `internal/auth/permission.go` 为权威源,覆盖 `incident.* / event.* / service.* / schedule.* / escalation.* / runbook.* / integration.* / postmortem.* / team.* / user.* / role.* / notification.* / admin.*`。
- **Role**:由使用者自定义,自由组合权限点。内置角色 `builtin:true`,可复制不可删——`org_admin`(org 全部)、`team_admin`(team 范围)、`responder`(team 一线值班)、`responder_lead`(值班长)、`subscriber`(只读干系人)、`oncall`(responder + 对自己 `schedule.override`)。
- **RoleBinding**:把 Role 授予 User,并带 scope(`org` 或 `team`)。

鉴权流程:解析 action → `permission_code`,解析 resource → scope(资源归属即作用域,如操作 Incident 取其 `incident.team_id`);查该 User 在 `org` 与 `team` 两个 scope 下的 RoleBinding,**取并集**(任一授予即生效);拒绝时记审计。平台级保留权限(管理角色定义、全局集成、apikey)**仅限 org scope 授予**,避免团队管理员越权。

## 理由

- 权限点是系统固定的能力边界(枚举),角色是使用者的组织策略(自由)——两者分离,既保证判定稳定又保证配置灵活。
- 内置角色开箱即用,复制即可派生自定义角色,降低上手成本。
- org+team 并集使跨团队管理员与本团队值班可叠加授权,契合软隔离下的协作需求。

## 备选方案

- **固定 admin/responder/subscriber 三档**:不灵活,组织结构稍有差异就得改代码。
- **ABAC(属性策略引擎)**:表达力过剩,规则编写与调试成本高,对本场景过重。
- **团队私有角色**:每团队各维护一套角色定义,徒增管理负担;团队差异已由 RoleBinding 的 scope 承载,无需私有角色。

## 影响 / 权衡

- 权限点是代码即真相,新增系统能力须同步维护 `internal/auth/permission.go` 枚举;ADR 不复制该清单。
- 使用者可组合出细碎角色,需在 UI 上对"权限点含义"做好说明,否则易配错。
- 并集语义意味着授权是"叠加放行",撤权须清理所有相关 RoleBinding,不能只删一处。
