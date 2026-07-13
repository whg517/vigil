// Package schedule 实现能力域 5：排班。
//
// 设计见 ADR-0015（排班蓝图 + 实时计算，不存快照）：
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
	entoverride "github.com/kevin/vigil/ent/override"
	"github.com/kevin/vigil/ent/schema"
	"github.com/kevin/vigil/ent/user"
	"github.com/kevin/vigil/internal/metrics"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// EmptyShiftAlerter 空班告警回调（C4）：某 Schedule 在某时刻算不出任何在班人时触发。
// 由装配方注入（如通知该 Schedule 所属 team 的 team_admin）。
// 引擎只负责检测与调用；如何告警（发通知/建单）由实现方决定，避免引擎反向依赖通知层。
type EmptyShiftAlerter interface {
	AlertEmptyShift(ctx context.Context, sched *ent.Schedule, at time.Time)
}

// Engine 排班引擎：把 Schedule 蓝图实时计算成"此刻在班人"。
type Engine struct {
	db      *ent.Client
	redis   *redis.Client
	logger  *zap.Logger       // 日志，nil 用 Nop
	alerter EmptyShiftAlerter // 空班告警回调（C4），nil 时仅记 metric + 日志
}

// NewEngine 创建排班引擎。
func NewEngine(db *ent.Client, rc *redis.Client) *Engine {
	return &Engine{db: db, redis: rc}
}

// SetLogger 注入日志器。未注入时用 zap.NewNop()，不影响功能。
func (e *Engine) SetLogger(l *zap.Logger) { e.logger = l }

// SetEmptyShiftAlerter 注入空班告警回调（C4）。nil 时空班只记 metric + Warn 日志。
func (e *Engine) SetEmptyShiftAlerter(a EmptyShiftAlerter) { e.alerter = a }

func (e *Engine) log() *zap.Logger {
	if e.logger == nil {
		return zap.NewNop()
	}
	return e.logger
}

// OncallResult 排班计算结果。
// json tag 必填：前端/OpenAPI 按 snake_case 读取，无 tag 时 json.Marshal 会输出
// PascalCase（Layers/Users/…）导致前端读不到 → 一直显示「无在班人」。
type OncallResult struct {
	ScheduleID   int    `json:"schedule_id"`
	ScheduleName string `json:"schedule_name"`
	// 按层有序：primary → secondary → override
	Layers []OncallLayer `json:"layers"`
}

// OncallLayer 某一层的在班人。
type OncallLayer struct {
	Name     string       `json:"name"` // 层名（如"一线"）
	Priority int          `json:"priority"`
	Users    []OncallUser `json:"users"`
}

// OncallUser 在班的用户。
type OncallUser struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username"`
	Override bool   `json:"override"` // 是否为临时换班顶替
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

	// 查所有 Rotation（含在职参与者：B21 禁用用户不被解算通知）。
	// 用 WithParticipants + 过滤器只加载 status=active 的成员，禁用者不进在班计算。
	rotations, err := sched.QueryRotations().
		WithParticipants(func(q *ent.UserQuery) {
			q.Where(user.StatusEQ(user.StatusActive))
		}).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query rotations: %w", err)
	}

	res := &OncallResult{ScheduleID: sched.ID, ScheduleName: sched.Name}

	// 把 Rotation 算出的在班人，归入对应 layer（用 Schedule.layers 的 priority）。
	// B21：按 Schedule.type 差异化解算——calendar 取全体在班（无轮换），
	// rotation 走班次序号轮换算法。
	// P3.2：follow_the_sun 走跨时区接力，由 resolveFollowTheSun 专门解算（见下）。
	layerMap := buildLayerMap(sched.Layers) // rotation_id -> ScheduleLayer

	if sched.Type.String() == "follow_the_sun" {
		// follow_the_sun：不逐 rotation 平铺，而是按各 layer 时区/工作时段接力选出在班层。
		res.Layers = resolveFollowTheSun(sched, rotations, layerMap, at)
	} else {
		for _, rot := range rotations {
			layer, ok := layerMap[fmt.Sprint(rot.ID)]
			if !ok {
				// 无 layer 配置则用默认 priority（按 rotation 创建顺序）
				layer = schema.ScheduleLayer{ID: fmt.Sprint(rot.ID), Name: rot.Name, Priority: 100}
			}
			users := computeLayerUsers(sched.Type.String(), rot, localAt)
			if len(users) == 0 {
				continue
			}
			res.Layers = append(res.Layers, OncallLayer{
				Name:     layer.Name,
				Priority: layer.Priority,
				Users:    users,
			})
		}
	}

	// 按层优先级排序（数字小优先）
	sortLayersByPriority(res.Layers)

	// C5/M8：应用 Override 层（临时换班），命中时段的顶替人为最高优先级层，
	// 完全覆盖 Rotation 结果（capabilities §2.4）。
	if overrideLayer := e.resolveOverride(ctx, sched, at); overrideLayer != nil {
		// Override 是最高优先级：置于所有层之前（priority 最小）。
		res.Layers = append([]OncallLayer{*overrideLayer}, res.Layers...)
	}

	// C4 空班检测：所有层算不出任何在班人 → 空班（无人值班）严重信号，
	// 记 metric + Warn 日志 + 告警 team_admin（避免"无人值班"盲区）。
	if isEmptyShift(res.Layers) {
		metrics.ScheduleEmptyShifts.WithLabelValues(fmt.Sprint(sched.ID)).Inc()
		e.log().Warn("schedule empty shift: no oncall user resolved",
			zap.Int("schedule_id", sched.ID),
			zap.String("schedule_name", sched.Name),
			zap.Time("at", at))
		if e.alerter != nil {
			e.alerter.AlertEmptyShift(ctx, sched, at)
		}
	}

	return res, nil
}

// resolveOverride 解算 Override 层（C5/M8）：查该 Schedule 在 at 时刻命中的换班，
// 命中则返回一个最高优先级 OncallLayer（override=true）。无命中返回 nil。
// 命中条件：start_time <= at < end_time，且顶替人在职（禁用则不顶替，B21）。
// 多条命中取最新创建的一条（后设的换班覆盖先设的）。
func (e *Engine) resolveOverride(ctx context.Context, sched *ent.Schedule, at time.Time) *OncallLayer {
	ov, err := sched.QueryOverrides().
		Where(
			entoverride.StartTimeLTE(at),
			entoverride.EndTimeGT(at),
			entoverride.HasUserWith(user.StatusEQ(user.StatusActive)),
		).
		WithUser().
		Order(ent.Desc(entoverride.FieldCreatedAt)).
		First(ctx)
	if err != nil {
		// 无命中（NotFound）是常态，不记错误；其它错误记 Warn 但不阻断解算。
		if !ent.IsNotFound(err) {
			e.log().Warn("resolve override failed",
				zap.Int("schedule_id", sched.ID), zap.Error(err))
		}
		return nil
	}
	sub := ov.Edges.User
	if sub == nil {
		return nil
	}
	return &OncallLayer{
		Name:     "Override",
		Priority: -1, // 最高优先级（数字最小），置于所有 Rotation 层之前
		Users:    userModels(ent.Users{sub}, true),
	}
}

// isEmptyShift 判定是否空班：所有层都无在班人（含无层）。
func isEmptyShift(layers []OncallLayer) bool {
	for _, l := range layers {
		if len(l.Users) > 0 {
			return false
		}
	}
	return true
}

// computeLayerUsers 按 Schedule.type 差异化解算某层在 at 时刻的在班人（B21）。
//   - calendar：日历排班，无轮换，取该层全体在职参与者（由外部 calendar 事件决定，
//     初期简化为全体在班，交由 Override 精确覆盖）。
//   - rotation：班次轮换，走序号算法。
//
// 注意：follow_the_sun 不走此函数——其跨时区接力由 resolveFollowTheSun 专门解算（P3.2）。
func computeLayerUsers(schedType string, rot *ent.Rotation, at time.Time) []OncallUser {
	if schedType == "calendar" {
		// calendar：无 Rotation 轮换语义，取全体在职参与者。
		return userModels(rot.Edges.Participants, false)
	}
	// rotation：班次轮换。
	return computeRotationUsers(rot, at)
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
	// 与 Oncall 一致：只加载在职参与者（B21 禁用不解算）。
	rotations, err := sched.QueryRotations().
		WithParticipants(func(q *ent.UserQuery) {
			q.Where(user.StatusEQ(user.StatusActive))
		}).
		All(ctx)
	if err != nil {
		return nil, err
	}
	layerMap := buildLayerMap(sched.Layers)

	now := time.Now().In(loc)
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)

	isFTS := sched.Type.String() == "follow_the_sun"

	var out []DayOncall
	for d := 0; d < days; d++ {
		dayStart := startOfDay.AddDate(0, 0, d)
		day := DayOncall{Date: dayStart}
		if isFTS {
			// follow_the_sun：单一时刻无法体现跨时区接力，取当天多个代表时刻（每 4h）
			// 的接力在班层并集，反映"这一天由哪些时区区域接力覆盖"。
			day.Layers = previewFollowTheSunDay(sched, rotations, layerMap, dayStart)
		} else {
			dayAt := dayStart.Add(12 * time.Hour) // 取当天中午作为代表时刻
			for _, rot := range rotations {
				layer := layerMap[fmt.Sprint(rot.ID)]
				if layer.Name == "" {
					layer = schema.ScheduleLayer{Name: rot.Name, Priority: 100}
				}
				users := computeLayerUsers(sched.Type.String(), rot, dayAt)
				if len(users) > 0 {
					day.Layers = append(day.Layers, OncallLayer{
						Name: layer.Name, Priority: layer.Priority, Users: users,
					})
				}
			}
		}
		sortLayersByPriority(day.Layers)
		out = append(out, day)
	}
	return out, nil
}

// previewFollowTheSunDay 计算 follow_the_sun 某天的接力在班层并集（预览用）。
// 单一时刻只反映某时区区域，故按当天每 4h 采样，合并所有出现过的在班层，
// 让预览完整呈现"这一天由亚太→欧洲→美洲哪些区域接力覆盖"。
// dayStart 为该天 00:00（Schedule 时区），采样点为 dayStart 起每 4h 的 UTC 绝对时刻。
func previewFollowTheSunDay(
	sched *ent.Schedule,
	rotations []*ent.Rotation,
	layerMap map[string]schema.ScheduleLayer,
	dayStart time.Time,
) []OncallLayer {
	seen := make(map[string]bool) // 去重键：layer 名 + 首个在班人 id
	var merged []OncallLayer
	for hour := 0; hour < 24; hour += 4 {
		at := dayStart.Add(time.Duration(hour) * time.Hour)
		for _, l := range resolveFollowTheSun(sched, rotations, layerMap, at) {
			key := l.Name
			if len(l.Users) > 0 {
				key = fmt.Sprintf("%s#%d", l.Name, l.Users[0].ID)
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			merged = append(merged, l)
		}
	}
	return merged
}

// DayOncall 某天的在班情况（预览用）。
type DayOncall struct {
	Date   time.Time     `json:"date"`
	Layers []OncallLayer `json:"layers"`
}

// PreviewResult 排班预览结果（preview handler 返回）。
type PreviewResult struct {
	ScheduleID int         `json:"schedule_id"`
	Days       []DayOncall `json:"days"`
}

// computeRotationUsers 纯函数：根据 Rotation 规则算出 at 时刻的在班人。
// 核心算法：班次序号 = floor((at - start_date) / shift)，值班 = participants[序号 mod 人数]。
func computeRotationUsers(rot *ent.Rotation, at time.Time) []OncallUser {
	participants := rot.Edges.Participants
	if len(participants) == 0 {
		return nil
	}
	shiftDuration := shiftDurationFor(rot)
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

// resolveFollowTheSun 解算 follow_the_sun（日不落接力）在 at 时刻（UTC 基准）的在班层（P3.2）。
//
// 设计意图：每个 layer 代表一个时区区域（亚太/欧洲/美洲），配本地时区 + 工作时段。
// 把 at 转到各 layer 本地时间，落在哪个 layer 工作时段内即由该 layer 值班——
// 亚太白天亚太值班、欧洲白天欧洲值班、美洲白天美洲值班，形成 24h 无缝接力。
//
// 解算规则：
//   - 对每个 layer（其 rotation 有在职参与者），判断 at 是否落在该 layer 本地工作时段内。
//   - 命中的 layer 全部返回（多个 layer 同时在工作时段，如接力重叠段）；
//     层内在班人仍走 rotation 轮换（同一时区区域内多人轮班），保持 rotation 语义。
//   - 若无任何 layer 命中工作时段（接力空档）：兜底返回"下一个即将上班"的 layer——
//     即本地时间距离各自 WorkStart 最近（最快到来）的 layer，避免完全无人值班。
//     兜底层标记 Name 后缀提示，但仍返回真实在班人。
//
// 与 rotation/calendar 并存：仅 Schedule.type==follow_the_sun 时调用此函数，不影响其它类型。
// sched.Timezone 作为 layer 未配 timezone 时的回退。
func resolveFollowTheSun(
	sched *ent.Schedule,
	rotations []*ent.Rotation,
	layerMap map[string]schema.ScheduleLayer,
	at time.Time,
) []OncallLayer {
	var active []OncallLayer
	// 兜底候选：记录距离上班最近的 layer 及其等待时长。
	var fallback *OncallLayer
	var fallbackWait time.Duration = 1<<63 - 1 // maxDuration

	for _, rot := range rotations {
		layer, ok := layerMap[fmt.Sprint(rot.ID)]
		if !ok {
			layer = schema.ScheduleLayer{ID: fmt.Sprint(rot.ID), Name: rot.Name, Priority: 100}
		}
		// 该层本地时区：优先 layer.Timezone，回退 Schedule.timezone，再回退 UTC。
		loc := loadLayerLocation(layer.Timezone, sched.Timezone)
		localAt := at.In(loc)

		// 层内在班人：同一时区区域内多人时走 rotation 轮换（沿用班次序号算法）。
		users := computeRotationUsers(rot, localAt)
		if len(users) == 0 {
			continue // 无在班人（空层/全禁用）不参与接力
		}

		if inWorkWindow(localAt, layer.WorkStart, layer.WorkEnd) {
			active = append(active, OncallLayer{
				Name:     layer.Name,
				Priority: layer.Priority,
				Users:    users,
			})
			continue
		}
		// 未在工作时段：作为兜底候选，取距离本地 WorkStart 最近者。
		wait := untilWorkStart(localAt, layer.WorkStart)
		if wait < fallbackWait {
			fallbackWait = wait
			fb := OncallLayer{
				Name:     layer.Name + "（接力空档兜底）",
				Priority: layer.Priority,
				Users:    users,
			}
			fallback = &fb
		}
	}

	if len(active) > 0 {
		return active
	}
	// 接力空档：无 layer 在工作时段，兜底返回最快上班的 layer（若有在班人）。
	if fallback != nil {
		return []OncallLayer{*fallback}
	}
	return nil
}

// loadLayerLocation 解析 layer 时区：优先 layerTZ，为空回退 schedTZ，再回退 UTC。
// 无效时区名（LoadLocation 失败）逐级降级，绝不阻断解算。
func loadLayerLocation(layerTZ, schedTZ string) *time.Location {
	for _, tz := range []string{layerTZ, schedTZ} {
		if tz == "" {
			continue
		}
		if loc, err := time.LoadLocation(tz); err == nil {
			return loc
		}
	}
	return time.UTC
}

// inWorkWindow 判定 localAt（已在目标时区）是否落在 [start, end) 工作时段内。
//   - start/end 为 "HH:MM"；任一为空视为全天工作（恒 true）。
//   - 普通时段（start < end，如 09:00~17:00）：start <= t < end。
//   - 跨午夜时段（start > end，如 22:00~06:00）：t >= start 或 t < end。
//   - start == end：解释为整天（如 00:00~00:00 全天）。
func inWorkWindow(localAt time.Time, start, end string) bool {
	if start == "" || end == "" {
		return true // 未配工作时段 = 全天在班
	}
	sMin, sok := parseHHMM(start)
	eMin, eok := parseHHMM(end)
	if !sok || !eok {
		return true // 配置无法解析时不误判空档，视为全天
	}
	nowMin := localAt.Hour()*60 + localAt.Minute()
	if sMin == eMin {
		return true // 首尾相同 = 全天
	}
	if sMin < eMin {
		// 普通时段（不跨午夜）
		return nowMin >= sMin && nowMin < eMin
	}
	// 跨午夜时段（如 22:00~06:00）
	return nowMin >= sMin || nowMin < eMin
}

// untilWorkStart 返回 localAt 到下一次工作时段起点（本地 start 时刻）的等待时长。
// 用于接力空档兜底时挑"最快上班"的 layer。start 无法解析时返回极大值（不优先）。
func untilWorkStart(localAt time.Time, start string) time.Duration {
	sMin, ok := parseHHMM(start)
	if !ok {
		return 1<<63 - 1
	}
	nowMin := localAt.Hour()*60 + localAt.Minute()
	diff := sMin - nowMin
	if diff <= 0 {
		diff += 24 * 60 // 今天已过 start，取明天的 start
	}
	return time.Duration(diff) * time.Minute
}

// parseHHMM 解析 "HH:MM" 为当天分钟数（0~1439）。ok=false 表示解析失败或越界。
func parseHHMM(s string) (int, bool) {
	var h, m int
	n, err := fmt.Sscanf(s, "%d:%d", &h, &m)
	if err != nil || n != 2 {
		return 0, false
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
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
// shiftDurationFor 由「轮值类型」决定一个班次的时长（周期）：
//   - daily  → 固定 24h
//   - weekly → 固定 7×24h
//   - custom → 由 shift_length 自定义（支持 "12h"/"48h"/"1week" 等）
//
// 修复前引擎只看 shift_length、无视 rotation_type，导致下拉里的「每日/每周/自定义」
// 对轮换周期毫无影响（选「每周」但 shift_length=24h 仍是每天轮换，标签骗人）。
// 现让 rotation_type 作为周期主选择，仅 custom 时才取用 shift_length。
func shiftDurationFor(rot *ent.Rotation) time.Duration {
	switch rot.RotationType.String() {
	case "daily":
		return 24 * time.Hour
	case "weekly":
		return 7 * 24 * time.Hour
	default: // custom（及未知值兜底）由 shift_length 决定
		return parseShiftLength(rot.ShiftLength)
	}
}

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
