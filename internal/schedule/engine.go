// Package schedule 实现能力域 5：排班。
//
// 对应 docs/capabilities/03-scheduling-escalation.md §2：
// · Schedule 是纯蓝图，不存"当前值班人"，由引擎实时计算
// · 班次序号 = floor((T - start_date) / shift_length)
// · 当前值班 = participants[序号 mod 人数]
// · Override 层覆盖临时换班
// · 分层：primary（优先）→ secondary → override
package schedule

import (
	"context"
	"fmt"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/schema"

	"github.com/redis/go-redis/v9"
)

// Engine 排班引擎：把 Schedule 蓝图实时计算成"此刻在班人"。
type Engine struct {
	db    *ent.Client
	redis *redis.Client
}

// NewEngine 创建排班引擎。
func NewEngine(db *ent.Client, rc *redis.Client) *Engine {
	return &Engine{db: db, redis: rc}
}

// OncallResult 排班计算结果。
type OncallResult struct {
	ScheduleID   int
	ScheduleName string
	// 按层有序：primary → secondary → override
	Layers []OncallLayer
}

// OncallLayer 某一层的在班人。
type OncallLayer struct {
	Name     string // 层名（如"一线"）
	Priority int
	Users    []OncallUser
}

// OncallUser 在班的用户。
type OncallUser struct {
	ID       int
	Name     string
	Username string
	Override bool // 是否为临时换班顶替
}

// Oncall 实时计算某 Schedule 在指定时刻的在班人。
// at 为零值时用 time.Now()。结果按层优先级排序。
func (e *Engine) Oncall(ctx context.Context, schedID int, at time.Time) (*OncallResult, error) {
	if at.IsZero() {
		at = time.Now()
	}

	sched, err := e.db.Schedule.Get(ctx, schedID)
	if err != nil {
		return nil, fmt.Errorf("get schedule %d: %w", schedID, err)
	}

	// 转到 Schedule 所在时区计算（capabilities §2.2 第 1 步）
	loc, err := time.LoadLocation(sched.Timezone)
	if err != nil {
		loc = time.UTC // 时区无效降级 UTC，不阻断计算
	}
	localAt := at.In(loc)

	// 查所有 Rotation（含 participants）
	rotations, err := sched.QueryRotations().
		WithParticipants().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query rotations: %w", err)
	}

	res := &OncallResult{ScheduleID: sched.ID, ScheduleName: sched.Name}

	// 把 Rotation 算出的在班人，归入对应 layer（用 Schedule.layers 的 priority）
	layerMap := buildLayerMap(sched.Layers) // rotation_id -> ScheduleLayer
	for _, rot := range rotations {
		layer, ok := layerMap[fmt.Sprint(rot.ID)]
		if !ok {
			// 无 layer 配置则用默认 priority（按 rotation 创建顺序）
			layer = schema.ScheduleLayer{ID: fmt.Sprint(rot.ID), Name: rot.Name, Priority: 100}
		}
		users := computeRotationUsers(rot, localAt)
		if len(users) == 0 {
			continue
		}
		res.Layers = append(res.Layers, OncallLayer{
			Name:     layer.Name,
			Priority: layer.Priority,
			Users:    users,
		})
	}

	// 按层优先级排序（数字小优先）
	sortLayersByPriority(res.Layers)

	return res, nil
}

// OncallNow 计算"此刻"在班人（带分钟级 Redis 缓存，capabilities §2.6）。
func (e *Engine) OncallNow(ctx context.Context, schedID int) (*OncallResult, error) {
	// TODO: Redis 缓存（key=schedule_id+分钟时间），降低重复计算压力
	return e.Oncall(ctx, schedID, time.Now())
}

// Preview 预览未来 N 天的排班（capabilities §2.6，用于日历展示）。
// 返回每天的在班人。
func (e *Engine) Preview(ctx context.Context, schedID int, days int) ([]DayOncall, error) {
	if days <= 0 {
		days = 14
	}
	sched, err := e.db.Schedule.Get(ctx, schedID)
	if err != nil {
		return nil, err
	}
	loc, err := time.LoadLocation(sched.Timezone)
	if err != nil {
		loc = time.UTC
	}
	rotations, err := sched.QueryRotations().WithParticipants().All(ctx)
	if err != nil {
		return nil, err
	}
	layerMap := buildLayerMap(sched.Layers)

	now := time.Now().In(loc)
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)

	var out []DayOncall
	for d := 0; d < days; d++ {
		dayAt := startOfDay.AddDate(0, 0, d).Add(12 * time.Hour) // 取当天中午作为代表时刻
		day := DayOncall{Date: startOfDay.AddDate(0, 0, d)}
		for _, rot := range rotations {
			layer := layerMap[fmt.Sprint(rot.ID)]
			if layer.Name == "" {
				layer = schema.ScheduleLayer{Name: rot.Name, Priority: 100}
			}
			users := computeRotationUsers(rot, dayAt)
			if len(users) > 0 {
				day.Layers = append(day.Layers, OncallLayer{
					Name: layer.Name, Priority: layer.Priority, Users: users,
				})
			}
		}
		sortLayersByPriority(day.Layers)
		out = append(out, day)
	}
	return out, nil
}

// DayOncall 某天的在班情况（预览用）。
type DayOncall struct {
	Date   time.Time
	Layers []OncallLayer
}

// computeRotationUsers 纯函数：根据 Rotation 规则算出 at 时刻的在班人。
// 核心算法：班次序号 = floor((at - start_date) / shift)，值班 = participants[序号 mod 人数]。
func computeRotationUsers(rot *ent.Rotation, at time.Time) []OncallUser {
	participants := rot.Edges.Participants
	if len(participants) == 0 {
		return nil
	}
	shiftDuration := parseShiftLength(rot.ShiftLength)
	if shiftDuration <= 0 {
		return nil
	}

	// 班次序号
	elapsed := at.Sub(rot.StartDate)
	if elapsed < 0 {
		// at 早于轮班开始：取首人
		return userModels(participants[:1], false)
	}
	// 考虑 handoff_time：把 at 调整到交接时刻后判断
	handoff := parseHandoff(rot.HandoffTime, at)
	if at.Before(handoff) && elapsed >= shiftDuration {
		// 还没到今天交接时刻，沿用昨天班次
		elapsed -= shiftDuration
	}
	shiftNo := int(elapsed / shiftDuration)
	idx := shiftNo % len(participants)
	if idx < 0 {
		idx += len(participants)
	}
	return userModels(participants[idx:idx+1], false)
}

// userModels 把 ent.User 转为 OncallUser。
func userModels(us ent.Users, override bool) []OncallUser {
	out := make([]OncallUser, 0, len(us))
	for _, u := range us {
		out = append(out, OncallUser{ID: u.ID, Name: u.Name, Username: u.Username, Override: override})
	}
	return out
}

// parseShiftLength 解析班次时长字符串（"24h"/"1week"/"168h"）。
// 简化实现：支持 h 结尾的纯小时。完整支持需 time.ParseDuration 的扩展。
func parseShiftLength(s string) time.Duration {
	if s == "" {
		return 24 * time.Hour // 默认一天
	}
	// 尝试 time.ParseDuration（支持 24h/168h 等）
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	// week 单位特殊处理
	if s == "1week" || s == "week" {
		return 7 * 24 * time.Hour
	}
	return 24 * time.Hour
}

// parseHandoff 解析交接时刻（"09:00"）为 at 当天的具体时间。
func parseHandoff(hhmm string, at time.Time) time.Time {
	var h, m int
	_, _ = fmt.Sscanf(hhmm, "%d:%d", &h, &m) // 解析失败时 h/m 保持 0（零点），可接受
	return time.Date(at.Year(), at.Month(), at.Day(), h, m, 0, 0, at.Location())
}

// buildLayerMap 把 Schedule.Layers（JSON）转为 rotation_id -> schema.ScheduleLayer 映射。
func buildLayerMap(layers []schema.ScheduleLayer) map[string]schema.ScheduleLayer {
	m := make(map[string]schema.ScheduleLayer, len(layers))
	for _, l := range layers {
		m[l.RotationID] = l
	}
	return m
}

// sortLayersByPriority 按优先级升序排序（数字小优先）。
func sortLayersByPriority(ls []OncallLayer) {
	for i := 1; i < len(ls); i++ {
		for j := i; j > 0 && ls[j].Priority < ls[j-1].Priority; j-- {
			ls[j], ls[j-1] = ls[j-1], ls[j]
		}
	}
}
