# 已知问题与限制(Known Issues)

> 记录**已确认、尚未修复**的缺陷与限制,防止"发现过的问题被遗忘"。
> 每条带代码出处与影响;修复合入 main 后从本清单移除(git 历史可追溯)。
> 历史背景:2026-07-03 全旅程源码审计发现的 15 项问题中,生产安全类(M0:鉴权旁路/越权/冒充/审计断链/静默失败家族)已全部修复;下列为 2026-07-10 逐项复核后仍开放的残留项。

## 1. 用户禁用对已签发 access JWT 非即时生效

- **现象**:禁用用户后,refresh token 与 API Key 即时失效(均校验 `User.status`),但**已签发的 access JWT 有效期内仍可用**——`updateUser` 置 `status=disabled` 时未 bump `token_version`,而 access JWT 校验只比对 `token_version` 不查 status。
- **出处**:`internal/auth/resolver.go`(tokenVersionValid)、`internal/auth/handler_user_team.go`(updateUser)。
- **影响**:低(access token 短时效);彻底堵死需禁用时 `AddTokenVersion(1)`。

## 2. 内置 `oncall` 角色缺 `postmortem.view`

- **现象**:oncall 角色可见复盘列表入口,点详情 403(`responder` 角色有该权限,`oncall` 没有)。
- **出处**:`internal/auth/seed.go` oncall 权限集。
- **影响**:低,体验瑕疵;属产品决策:值班角色是否应可读复盘。

## 3. `responder_lead` 无 `postmortem.actionitem.manage`(权责错位)

- **现象**:responder_lead 能创建/发布复盘,却不能增删改其中的改进项(ActionItem manage 仅 team_admin/org_admin)。
- **出处**:`internal/auth/seed.go` responder_lead 权限集;`internal/postmortem/handler.go` actionItem 端点要求 `PermPostmortemActionItemManage`。
- **影响**:中,复盘负责人无法闭环自己发布的改进项;需产品裁决是否给 lead 加权限点。

## 4. 前端无时间线备注输入框

- **现象**:后端 `POST /incidents/:id/timeline`(note_added)端点已就绪,但事件详情页只读展示时间线,无备注输入表单——人工备注只能走 API。
- **出处**:`web/src/pages/incident-detail.tsx`(无 note 输入与对应 hook)。
- **影响**:中,处置过程中的人工记录(打电话联系了谁、试了什么)无法从 UI 沉淀到时间线。
