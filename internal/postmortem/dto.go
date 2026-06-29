// dto.go Postmortem 响应展平（统一前后端 edge 契约）。
//
// 背景：ent 默认把 edge 关联序列化进 "edges" 嵌套对象（{"edges":{"action_items":...}}），
// 但前端 types.ts 按平铺定义（pm.action_items / pm.incident）。为统一契约，所有
// Postmortem 响应在返回前用 flatten 展平：把 edges.incident / edges.action_items
// 提到顶层，前端无需改动即可读取。
//
// 仅展平 incident + action_items（前端用到的两个 edge）；其余字段原样透传。
package postmortem

import "github.com/kevin/vigil/ent"

// postmortemResp 展平后的 Postmortem 响应。
// 嵌入 *ent.Postmortem（标量字段直接透传），并在顶层加 incident/action_items。
// 注意：json tag 用 "-" 抹掉嵌入的 Edges 字段，避免与展平字段重复。
type postmortemResp struct {
	*ent.Postmortem
	Incident    *ent.Incident     `json:"incident,omitempty"`
	ActionItems []*ent.ActionItem `json:"action_items,omitempty"`
	Edges       struct{}          `json:"-"` // 抹掉嵌入的嵌套 edges
}

// flatten 把 ent.Postmortem 展平为前端期望的响应结构（edge 提到顶层）。
func flatten(pm *ent.Postmortem) *postmortemResp {
	r := &postmortemResp{Postmortem: pm}
	if pm.Edges.Incident != nil {
		r.Incident = pm.Edges.Incident
	}
	if len(pm.Edges.ActionItems) > 0 {
		r.ActionItems = pm.Edges.ActionItems
	}
	return r
}

// flattenAll 批量展平。
func flattenAll(pms []*ent.Postmortem) []*postmortemResp {
	out := make([]*postmortemResp, len(pms))
	for i, pm := range pms {
		out[i] = flatten(pm)
	}
	return out
}
