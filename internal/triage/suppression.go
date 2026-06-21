// suppression.go 抑制规则引擎 —— "少打扰"核心（能力域 3 M3.2）。
//
// 对应 capabilities/02-triage-routing.md §2.3。
// 满足条件（label 全等匹配 + 时间窗 + 严重度过滤）时主动抑制：
//   - action=suppress：Event 标记 is_noise=true，不创建/并入 Incident，仅留痕（可申诉，§2.5）
//   - action=reduce_severity：降低 Event 严重度（critical 不降，preserve_critical 守卫）
//
// 接入点：Engine.Process 在去重后、路由前调用 Evaluate（§2.1 三层处理顺序）。
package triage

import (
	"context"
	"fmt"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/event"
	"github.com/kevin/vigil/ent/suppressionrule"
)

// SuppressionAction 抑制命中后的动作。
type SuppressionAction string

const (
	// SuppressActionSuppress 标记噪音、不入 Incident。
	SuppressActionSuppress SuppressionAction = "suppress"
	// SuppressActionReduceSeverity 降低 Event 严重度（critical 不降）。
	SuppressActionReduceSeverity SuppressionAction = "reduce_severity"
)

// SuppressionOutcome 抑制评估结果。
type SuppressionOutcome struct {
	Matched  bool             // 是否命中某条规则
	RuleID   int              // 命中的规则 ID（未命中为 0）
	RuleName string           // 规则名
	Action   SuppressionAction // 命中动作
	// ReduceTo 仅当 Action=reduce_severity 时有值，表示降级后的目标严重度。
	ReduceTo string
}

// SuppressionEngine 抑制规则评估器。
// 无 db 时 Evaluate 恒返回未命中（降级，不抑制），与"缺依赖不阻断主流程"基线一致。
type SuppressionEngine struct {
	db *ent.Client
	// now 便于测试注入当前时间（默认 time.Now）。
	now func() time.Time
}

// NewSuppressionEngine 创建抑制引擎。
func NewSuppressionEngine(db *ent.Client) *SuppressionEngine {
	return &SuppressionEngine{db: db, now: time.Now}
}

// Evaluate 评估一条 Event 是否命中任何启用的抑制规则。
// 返回首个命中的规则结果（按 expires_at 升序、id 升序，先到期的先评估）。
// 命中 reduce_severity 但 preserve_critical 且 Event=critical 时，跳过该规则继续找下一条。
func (e *SuppressionEngine) Evaluate(ctx context.Context, evt *ent.Event) (*SuppressionOutcome, error) {
	if e.db == nil {
		return &SuppressionOutcome{}, nil // 无 db 不抑制
	}
	// 查所有启用的抑制规则（resolved 事件不评估，resolved 是收尾信号）
	if evt.Status == event.StatusResolved {
		return &SuppressionOutcome{}, nil
	}
	rules, err := e.db.SuppressionRule.Query().
		Where(suppressionrule.EnabledEQ(true)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query suppression rules: %w", err)
	}
	now := e.now()
	for _, r := range rules {
		// 过期规则跳过
		if r.ExpiresAt != nil && now.After(*r.ExpiresAt) {
			continue
		}
		if matchRule(r, evt, now) {
			out := &SuppressionOutcome{
				Matched:  true,
				RuleID:   r.ID,
				RuleName: r.Name,
				Action:   SuppressionAction(r.Action),
				ReduceTo: r.ReduceTo,
			}
			// reduce_severity 但 preserve_critical 且当前是 critical → 不降，跳过此规则
			if out.Action == SuppressActionReduceSeverity &&
				r.PreserveCritical && string(evt.Severity) == string(event.SeverityCritical) {
				continue
			}
			// suppress 且 preserve_critical 且 critical → 不抑制，跳过此规则
			if out.Action == SuppressActionSuppress &&
				r.PreserveCritical && string(evt.Severity) == string(event.SeverityCritical) {
				continue
			}
			return out, nil
		}
	}
	return &SuppressionOutcome{}, nil
}

// Apply 把抑制结果应用到 Event：suppress 标记 is_noise；reduce_severity 更新 severity。
// 返回更新后的 Event（便于上层流程用新 severity 继续）。
func (e *SuppressionEngine) Apply(ctx context.Context, evt *ent.Event, out *SuppressionOutcome) (*ent.Event, error) {
	if !out.Matched {
		return evt, nil
	}
	update := e.db.Event.UpdateOneID(evt.ID)
	switch out.Action {
	case SuppressActionSuppress:
		update.SetIsNoise(true)
		evt.IsNoise = true
	case SuppressActionReduceSeverity:
		target := normalizeSeverity(out.ReduceTo, evt.Severity)
		update.SetSeverity(event.Severity(target))
		evt.Severity = event.Severity(target)
	}
	if err := update.Exec(ctx); err != nil {
		return nil, fmt.Errorf("apply suppression: %w", err)
	}
	return evt, nil
}

// matchRule 判断单条规则是否命中 Event。
// 条件：match_labels 全等匹配 Event.labels；time_window（若有）当前时间落在窗内；
// severity_filter（若有）包含 Event.severity。
func matchRule(r *ent.SuppressionRule, evt *ent.Event, now time.Time) bool {
	// 1. label 全等匹配：规则 match_labels 的每个 k=v 必须都在 Event.labels 中且相等
	for k, v := range r.MatchLabels {
		got, ok := evt.Labels[k]
		if !ok || got != v {
			return false
		}
	}
	// 2. severity_filter：若配置了且不含当前 severity，不命中
	if len(r.SeverityFilter) > 0 {
		hit := false
		for _, s := range r.SeverityFilter {
			if s == string(evt.Severity) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	// 3. time_window：若有 start/end，检查当前时间是否落在窗内
	if startStr, ok := r.TimeWindow["start"].(string); ok {
		endStr, _ := r.TimeWindow["end"].(string)
		if startStr != "" {
			start, err1 := time.Parse(time.RFC3339, startStr)
			end, err2 := time.Parse(time.RFC3339, endStr)
			if err1 != nil || err2 != nil {
				return false // 时间窗配置非法，保守不命中
			}
			if now.Before(start) || now.After(end) {
				return false // 不在窗内
			}
		}
	}
	return true
}

// normalizeSeverity 归一化降级目标严重度。
// 无效或高于当前则降一级到 warning/info 兜底，绝不升 severity。
func normalizeSeverity(reduceTo string, current event.Severity) string {
	switch reduceTo {
	case "critical", "warning", "info":
		// 不允许 reduce 到比当前更高
		if rank(reduceTo) > rank(string(current)) {
			return reduceTo
		}
		return string(current) // 配置异常导致要"升级"，保守保持原级不降
	default:
		// 未指定目标：按 critical→warning→info 逐级降
		switch current {
		case event.SeverityCritical:
			return "warning"
		case event.SeverityWarning:
			return "info"
		default:
			return "info"
		}
	}
}

// rank 严重度排序：数字越大越低（critical=0 < warning=1 < info=2）。
// 用于判断"降级目标是否真的更低"。
func rank(sev string) int {
	switch sev {
	case "critical":
		return 0
	case "warning":
		return 1
	case "info":
		return 2
	default:
		return 3
	}
}
