package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// AuditLog 审计日志（能力域 13 §审计日志，PRD M13.5）。
//
// 记录敏感管理操作（区别于 IncidentAction 记事件操作）：
//   - 角色变更（创建/删除/授权）
//   - 集成 token / API Key 管理
//   - 用户停用、配置变更
//   - 登录成功/失败（安全审计）
//
// 设计：
//   - 只追加，不修改/删除（事实记录，留痕可追溯）
//   - actor 记谁（user_id + name 快照，避免用户改名后追溯断链）
//   - resource 记操作对象（type + id + name 快照）
//   - detail 存结构化上下文（变更前后等，按操作类型填充）
//   - ip/user_agent 用于安全审计（异常登录溯源）
type AuditLog struct {
	ent.Schema
}

func (AuditLog) Fields() []ent.Field {
	return []ent.Field{
		// actor_user_id 操作者（0=系统/未鉴权尝试）
		field.Int("actor_user_id").Default(0).Comment("操作者 user_id，0=系统/匿名"),
		// actor_name 操作者名快照（用户改名后审计仍可读）
		field.String("actor_name").Default("system").Comment("操作者名快照"),
		// action 操作类型（自由字符串，语义由 internal/auth 的 Action* 常量集中约定）：
		// role.create/role.delete/role.assign/apikey.create/auth.login/
		// user.disable/user.enable/runbook.execute/im.denied/
		// integration.create/integration.update/integration.delete/...
		field.String("action").NotEmpty().Comment("操作类型，如 role.create"),
		// resource_type 操作对象类型（role/user/integration/api_key/...）
		field.String("resource_type").NotEmpty().Comment("操作对象类型"),
		// resource_id 操作对象 ID（0=非实体操作如登录）
		field.Int("resource_id").Default(0).Comment("操作对象 ID"),
		// resource_name 对象名快照
		field.String("resource_name").Optional().Comment("对象名快照"),
		// result 操作结果（success/failed/denied）
		field.Enum("result").Values("success", "failed", "denied").Default("success"),
		// detail 结构化上下文（变更前后、失败原因等）
		field.JSON("detail", map[string]any{}).Optional().Comment("结构化上下文"),
		// ip 来源 IP（安全审计溯源）
		field.String("ip").Optional().Comment("来源 IP"),
		// user_agent 来源 UA
		field.String("user_agent").Optional().Comment("来源 User-Agent"),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (AuditLog) Edges() []ent.Edge {
	// 审计日志不建 edge 关系：actor_user_id 故意用字段而非 edge，
	// 避免用户删除后审计日志的 edge 悬空（审计要的是不可变事实记录）。
	return nil
}

func (AuditLog) Indexes() []ent.Index {
	return []ent.Index{
		// 按操作者查（某人的操作历史）
		index.Fields("actor_user_id"),
		// 按操作类型查（如所有 role.create）
		index.Fields("action"),
		// 按对象查（某实体的变更历史）
		index.Fields("resource_type", "resource_id"),
		// 按时间查（默认倒序）
		index.Fields("created_at"),
	}
}
