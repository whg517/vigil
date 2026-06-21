package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// Role 角色 —— 使用者自定义，自由组合权限点。
// 对应 data-model.md §5.2 Role。
// 内置角色 builtin=true 可复制不可删；使用者可自由增删改。
type Role struct {
	ent.Schema
}

func (Role) Fields() []ent.Field {
	return []ent.Field{
		// name 唯一：种子幂等 + 业务自定义角色防重名
		field.String("name").Unique().NotEmpty(),
		field.Text("description").Optional(),
		// builtin 是否系统内置（内置可复制不可删）
		field.Bool("builtin").Default(false),
		// scope_level 此角色可用于哪个作用域
		field.Enum("scope_level").Values("org", "team"),
		// permissions 权限点集合（引用 internal/auth/permission.go 的 code 常量）
		// 存为字符串数组，业务层校验是否为合法权限点
		field.JSON("permissions", []string{}).Comment("权限点 code 列表，见 internal/auth/permission.go"),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (Role) Edges() []ent.Edge {
	return []ent.Edge{
		// Role -> RoleBinding（被用于的角色绑定）
		edge.To("role_bindings", RoleBinding.Type),
	}
}

// RoleBinding 角色绑定 —— 把 Role 授予 User（带作用域）。
// 对应 data-model.md §5.2 RoleBinding。
// scope 决定生效范围：org（全组织）或 team（限某团队）。
type RoleBinding struct {
	ent.Schema
}

func (RoleBinding) Fields() []ent.Field {
	return []ent.Field{
		// scope_level 作用域层级
		field.Enum("scope_level").Values("org", "team"),
		// team_id 当 scope_level=team 时必填
		field.String("team_id").Optional().Comment("team scope 时必填"),
		// granted_by 授权人
		field.String("granted_by").Optional(),
		// expires_at 可选，临时授权（如值班期间临时给某人 team_admin）
		field.Time("expires_at").Optional().Nillable().Comment("临时授权到期时间"),
		field.Time("granted_at").Default(time.Now).Immutable(),
	}
}

func (RoleBinding) Edges() []ent.Edge {
	return []ent.Edge{
		// RoleBinding <- Role（绑定的角色，Role.role_bindings 的反向）
		edge.From("role", Role.Type).Ref("role_bindings").Unique().Required(),
		// RoleBinding <- User（被授权用户，User.role_bindings 的反向）
		edge.From("user", User.Type).Ref("role_bindings").Unique().Required(),
		// RoleBinding <- Team（团队作用域，Team.role_bindings 的反向）
		edge.From("team", Team.Type).Ref("role_bindings"),
	}
}
