# ADR-0015： 排班蓝图 + 实时计算(不存快照)

| 字段 | 内容 |
|------|------|
| **状态** | Accepted |
| **日期** | 2026-07-09 |
| **相关** | [`0016-escalation-asynq-delayed.md`](./0016-escalation-asynq-delayed.md)、`ent/schema/schedule.go`、[`../architecture.md`](../architecture.md) |

## 背景

排班(Schedule)决定"某时刻谁在班",是升级引擎解析 `target.type=schedule` 时的输入。若把"当前值班人"物化成快照存库,排班规则一变(轮换调整、临时换班、层级变更)就要重算全表,且快照与蓝图之间容易漂移——升级找到的"值班人"可能已经不是真正在班的人。oncall 场景下这种漂移直接等于"叫错人、没人响应"。

## 决策

Schedule 只存**纯蓝图**,不存"当前值班人",每次按 `timezone + layers(priority 升序,数字越小优先级越高) + Rotation + Override` **实时计算** `oncall_users`。

- **缓存但不物化生效**:分钟级计算结果缓存 Redis(key = `schedule_id + 分钟级时间`),但生效判断永远实时算,预计算只用于日历展示。
- **Rotation**:`班次序号 = floor((T - start_date) / 周期)`,`当前值班 = participants[序号 mod 人数]`;周期由 `rotation_type` 决定(daily=24h / weekly=168h / custom=shift_length);`handoff_time` 保证换班落在工作时间。
- **follow_the_sun(P3.2 已实现)**:layer 扩展 `timezone / work_start / work_end`(支持跨午夜),按当前 UTC 落在哪个 layer 的本地工作时段来选人;命中的 layer **全部返回**(重叠段两层同时在班,交接更平滑);无任何 layer 处于工作时段时取"最快上班"层兜底;DST 由 `time.LoadLocation` + `time.In` 处理;preview 按每 4h 采样取并集。
- **Override**:最高优先级层,时段内(`start <= T < end` 且顶替人在职)完全覆盖 Rotation,多条取最新。越权守卫按「**顶班人是否为操作者本人**」判定(与班次原属谁无关):把自己登记为顶班人仅需 `schedule.override`;把他人登记为顶班人须叠加 `schedule.update`(team_admin / org_admin),防止值班人越权指派他人替班。
- **空班检测**:计算结果为空则告警 team_admin。

## 理由

- 排班变更**立即生效**,没有快照一致性问题。
- 直接受益点:换班后,下一级升级自动找到新人,无需任何回填或重算。
- 计算是纯函数,可缓存加速展示,又不牺牲生效判断的实时性。

## 备选方案

- **预生成值班快照**:变更需重算全表,快照与蓝图易漂移,升级可能取到过期的值班人——否决。

## 影响 / 权衡

- 每次判定都要实时计算,依赖计算函数正确与高效;分钟级 Redis 缓存缓解展示侧压力。
- 跨时区 / DST / follow_the_sun 逻辑复杂度集中在计算函数,须由充分单测锁定。
- 越权守卫区分"换己班"与"换他人",避免 `schedule.override` 被滥用为改他人排班。
