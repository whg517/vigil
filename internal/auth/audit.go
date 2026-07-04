// audit.go 审计日志记录器（能力域 13 §审计日志，PRD M13.5）。
//
// 解耦设计：Recorder 是独立组件，各 service/handler 注入后调用 Record()。
// 记录失败不影响主流程（审计是旁路，不能因审计失败导致业务失败）。
//
// 审计范围（PRD M13.5）：
//   - 角色变更（创建/删除/授权）
//   - 集成 token / API Key 管理
//   - 用户停用、配置变更
//   - 登录成功/失败（安全审计）
//
// 与 IncidentAction（域 13 §6.2 记事件操作）的区别：
//
//	AuditLog 记管理操作（谁能干什么的变更），IncidentAction 记事件操作（ack/resolve 等）。
package auth

import (
	"context"
	"net/http"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/auditlog"
)

// AuditResult 审计操作结果。
type AuditResult string

const (
	AuditResultSuccess AuditResult = "success"
	AuditResultFailed  AuditResult = "failed"
	AuditResultDenied  AuditResult = "denied" // 鉴权拒绝（潜在攻击探测）
)

// Action 审计操作类型集中定义（M13.5 敏感操作审计总线）。
//
// action 在 ent schema 里是自由字符串（非 ent enum），但语义必须收敛：
// 各调用点统一引用这些常量，避免散落的字符串字面量拼写漂移，也便于按 action 检索/告警。
// 已存在的登录/角色/apikey action 仍用字面量（历史范例），新增的敏感操作一律走常量。
const (
	// IM 越权拒绝（S9）：IM 操作未过 RBAC 时留痕（潜在越权探测）。
	ActionIMDenied = "im.denied"
	// Runbook 执行（S10/C14）：谁在生产上执行/审批了处置动作。
	ActionRunbookExecute = "runbook.execute"
	// 用户启停（C21 / S2）：禁用是高危动作，单独可检索。
	ActionUserDisable = "user.disable"
	ActionUserEnable  = "user.enable"
	// 建用户 / 管理员重置他人密码（M1，T2.6）：账号生命周期高危动作，须可追溯。
	// 重置密码会自增 token_version 吊销他人所有旧 token，等同强制下线，尤须留痕。
	ActionUserCreate        = "user.create"
	ActionUserResetPassword = "user.reset_password"
	// IM 账号解绑（M11）：IM 账号是 IM 操作的鉴权桥梁，误绑/恶意解绑须可追溯。
	ActionUserIMUnbind = "user.im_unbind"
	// 角色权限集编辑（M2，T2.7）：改角色 = 改一批人的权限边界，是 RBAC 最敏感动作。
	ActionRoleUpdate = "role.update"
	// 团队成员增删（M3 / S15，T2.7）：成员是数据归属边界的一部分，增删须可审计。
	ActionTeamMemberAdd    = "team.member.add"
	ActionTeamMemberRemove = "team.member.remove"
	// 接入点配置变更（C21）：告警源接入是攻击面，创建/改动/删除都留痕。
	ActionIntegrationCreate = "integration.create"
	ActionIntegrationUpdate = "integration.update"
	ActionIntegrationDelete = "integration.delete"
	// 接入 token 轮换（T5.1）：旧 token 立即失效，等同重置接入点凭据，须可追溯。
	ActionIntegrationRotateToken = "integration.rotate_token"
	// 原始告警重放（T5.5）：把 parse_failed/requeued 的 raw_event 重新投入归一化，
	// 是接入排障的写动作（会产生新 Event/触发分诊），须留痕（谁重放了哪条）。
	ActionRawEventReplay = "raw_event.replay"
	// 出向工单集成配置变更（T4.3）：工单集成持凭据、决定 ActionItem 往哪建单，
	// 是外连攻击面 + 凭据面，创建/改动/删除都留痕。
	ActionTicketIntegrationCreate = "ticket_integration.create"
	ActionTicketIntegrationUpdate = "ticket_integration.update"
	ActionTicketIntegrationDelete = "ticket_integration.delete"
	// 加密托管凭据配置变更（T6.3/S16）：凭据持外部平台鉴权 token，创建/改动/删除都留痕。
	// ★ 审计只记元数据（名/类型/id），绝不含密文/明文（明文不进审计）。
	ActionCredentialCreate = "credential.create" //nolint:gosec // G101 误报：审计 action 标识符，非密钥
	ActionCredentialUpdate = "credential.update" //nolint:gosec // G101 误报：审计 action 标识符，非密钥
	ActionCredentialDelete = "credential.delete" //nolint:gosec // G101 误报：审计 action 标识符，非密钥
	// AI 建议改判（S11）：采纳/拒绝会影响后续自动化/复盘，谁在何时改判须可审计。
	ActionAIInsightResolve = "ai.insight.resolve"
	// 出站 webhook 死信重放（T5.2）：把失败的出站投递重新推给订阅端，会向外部再发一次事件，
	// 属对外写动作，须留痕（谁重放了哪条、成功与否）。
	ActionWebhookDeliveryReplay = "webhook_delivery.replay"
)

// AuditEntry 审计日志条目（调用方构造，Recorder 持久化）。
type AuditEntry struct {
	ActorUserID  int            // 操作者 user_id，0=系统/匿名
	ActorName    string         // 操作者名快照
	Action       string         // 操作类型，如 "role.create"
	ResourceType string         // 对象类型，如 "role"
	ResourceID   int            // 对象 ID，0=非实体操作
	ResourceName string         // 对象名快照
	Result       AuditResult    // 结果
	Detail       map[string]any // 结构化上下文
	IP           string         // 来源 IP
	UserAgent    string         // 来源 UA
}

// AuditRecorder 审计日志记录器。
type AuditRecorder struct {
	db *ent.Client
}

// NewAuditRecorder 构造记录器。db 为 nil 时 Record 静默跳过（降级）。
func NewAuditRecorder(db *ent.Client) *AuditRecorder {
	return &AuditRecorder{db: db}
}

// Record 记录一条审计日志。失败仅返回 error，不 panic（调用方应 best-effort 忽略）。
func (r *AuditRecorder) Record(ctx context.Context, e AuditEntry) error {
	if r == nil || r.db == nil {
		return nil // 降级：无 recorder 时静默跳过
	}
	result := e.Result
	if result == "" {
		result = AuditResultSuccess
	}
	_, err := r.db.AuditLog.Create().
		SetActorUserID(e.ActorUserID).
		SetActorName(e.ActorName).
		SetAction(e.Action).
		SetResourceType(e.ResourceType).
		SetResourceID(e.ResourceID).
		SetResourceName(e.ResourceName).
		SetResult(auditlog.Result(result)).
		SetDetail(e.Detail).
		SetIP(e.IP).
		SetUserAgent(e.UserAgent).
		Save(ctx)
	return err
}

// MustRecord 记录审计，失败不阻塞主流程（best-effort，记日志即可）。
// 适用于 handler 里"操作已完成，补记审计"的场景——审计失败不应回滚业务。
func (r *AuditRecorder) MustRecord(ctx context.Context, e AuditEntry) {
	_ = r.Record(ctx, e)
}

// AuditEntryFromRequest 从 echo 请求提取 IP/UA，构造审计条目的公共字段。
// 调用方在此基础上填 Action/Resource 等业务字段。
func AuditEntryFromRequest(req *http.Request, actorUserID int, actorName string) AuditEntry {
	return AuditEntry{
		ActorUserID: actorUserID,
		ActorName:   actorName,
		IP:          clientIP(req),
		UserAgent:   req.UserAgent(),
		Detail:      map[string]any{},
	}
}

// clientIP 提取客户端 IP（优先 X-Forwarded-For，回退 RemoteAddr）。
func clientIP(req *http.Request) string {
	if xff := req.Header.Get("X-Forwarded-For"); xff != "" {
		// 取第一个（最原始客户端）
		for i := 0; i < len(xff); i++ {
			if xff[i] == ',' {
				return xff[:i]
			}
		}
		return xff
	}
	// RemoteAddr 形如 "1.2.3.4:5678"，截掉端口
	if req.RemoteAddr == "" {
		return ""
	}
	for i := len(req.RemoteAddr) - 1; i >= 0; i-- {
		if req.RemoteAddr[i] == ':' {
			return req.RemoteAddr[:i]
		}
	}
	return req.RemoteAddr
}
