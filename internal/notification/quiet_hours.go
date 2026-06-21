// quiet_hours.go 静默时段（能力域 7 M7.8）—— "少打扰"核心之二。
//
// 对应 capabilities/04-notification.md §5。
// 非 critical 在 quiet_hours 内不打扰（值班人除外——值班人始终通知，
// 因为他们就是为接告警而值班，静默对他们是失效的）。
// critical 穿透静默（bypass_for=[critical]）。
package notification

import (
	"fmt"
	"strings"
	"time"
)

// QuietHours 静默时段配置。
// 对应 NotificationRule.quiet_hours JSON（capabilities/04 §5）。
type QuietHours struct {
	Enabled   bool     `json:"enabled"`
	Start     string   `json:"start"`      // 本地起始 "HH:MM"，如 "22:00"
	End       string   `json:"end"`        // 本地结束 "HH:MM"，如 "07:00"（可跨午夜）
	Timezone  string   `json:"timezone"`   // IANA 时区，如 "Asia/Shanghai"
	BypassFor []string `json:"bypass_for"` // 穿透静默的 severity，默认 ["critical"]
}

// ShouldSuppress 判断一条通知是否应在当前时间被静默（不立即发送）。
//
//	返回 true = 应静默（不立即发送）；false = 可发送。
//
// 规则（capabilities/04 §5）：
//  1. 未启用静默 → 不静默（false）
//  2. severity 在 bypass_for 中（默认 critical）→ 不静默（穿透）
//  3. 通知目标是值班人（isOncall=true）→ 不静默（值班人始终通知）
//  4. 当前时间落在 [Start, End) 窗内（支持跨午夜）→ 静默
//  5. 否则不静默
//
// now 为 nil 时用 time.Now。timezone 非法时按 UTC 兜底（保守不静默）。
func (q *QuietHours) ShouldSuppress(severity string, isOncall bool, now *time.Time) bool {
	if q == nil || !q.Enabled {
		return false
	}
	// critical 等穿透
	bypass := q.BypassFor
	if len(bypass) == 0 {
		bypass = []string{"critical"}
	}
	for _, b := range bypass {
		if strings.EqualFold(b, severity) {
			return false
		}
	}
	// 值班人始终通知
	if isOncall {
		return false
	}
	// 解析时间窗
	cur := time.Now()
	if now != nil {
		cur = *now
	}
	loc, err := time.LoadLocation(q.Timezone)
	if err != nil || loc == nil {
		loc = time.UTC // 时区非法按 UTC，保守判断
	}
	local := cur.In(loc)
	if !inTimeWindow(local, q.Start, q.End) {
		return false // 不在静默窗内
	}
	return true
}

// inTimeWindow 判断本地时刻 t 是否落在 [start, end) 窗内，支持跨午夜（start>end）。
// start/end 格式 "HH:MM"，非法返回 false（保守不静默）。
func inTimeWindow(t time.Time, start, end string) bool {
	hStart, mStart, ok1 := parseClock(start)
	hEnd, mEnd, ok2 := parseClock(end)
	if !ok1 || !ok2 {
		return false
	}
	minutes := t.Hour()*60 + t.Minute()
	sMin := hStart*60 + mStart
	eMin := hEnd*60 + mEnd
	if sMin == eMin {
		return false // 窗长为 0 视为未配置
	}
	if sMin < eMin {
		// 同日窗：[sMin, eMin)
		return minutes >= sMin && minutes < eMin
	}
	// 跨午夜窗：[sMin, 24:00) ∪ [00:00, eMin)
	return minutes >= sMin || minutes < eMin
}

// parseClock 解析 "HH:MM"，返回 (时, 分, ok)。
func parseClock(s string) (int, int, bool) {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	h, m := 0, 0
	for _, c := range parts[0] {
		if c < '0' || c > '9' {
			return 0, 0, false
		}
		h = h*10 + int(c-'0')
	}
	for _, c := range parts[1] {
		if c < '0' || c > '9' {
			return 0, 0, false
		}
		m = m*10 + int(c-'0')
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

// String 便于日志。
func (q QuietHours) String() string {
	return fmt.Sprintf("QuietHours{enabled=%v %s-%s %s bypass=%v}",
		q.Enabled, q.Start, q.End, q.Timezone, q.BypassFor)
}
