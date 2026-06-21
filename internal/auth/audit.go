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
//   AuditLog 记管理操作（谁能干什么的变更），IncidentAction 记事件操作（ack/resolve 等）。
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

// AuditEntry 审计日志条目（调用方构造，Recorder 持久化）。
type AuditEntry struct {
	ActorUserID   int            // 操作者 user_id，0=系统/匿名
	ActorName     string         // 操作者名快照
	Action        string         // 操作类型，如 "role.create"
	ResourceType  string         // 对象类型，如 "role"
	ResourceID    int            // 对象 ID，0=非实体操作
	ResourceName  string         // 对象名快照
	Result        AuditResult    // 结果
	Detail        map[string]any // 结构化上下文
	IP            string         // 来源 IP
	UserAgent     string         // 来源 UA
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
