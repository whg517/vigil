# 能力域 5-6：排班与升级

| 字段 | 内容 |
|------|------|
| **覆盖 PRD** | 能力域 5（排班）M5.1~M5.8、能力域 6（升级）M6.1~M6.6 |
| **文档版本** | v0.1 |
| **创建日期** | 2026-06-20 |
| **关联** | [`data-model.md`](../data-model.md) §3.2 Schedule/Rotation/EscalationPolicy；[`architecture.md`](../architecture.md) §3.3/§3.4 |

---

## 1. 目标

回答两个 oncall 的根本问题：

1. **排班（能力域 5）**：实时回答"此刻谁在班"——把"蓝图"（Schedule 配置）实时计算成"实例"（此刻值班人）。
2. **升级（能力域 6）**：响应不及时时"找下一个"——oncall 产品的灵魂，保证告警绝不无人响应。

---

## 2. 排班引擎

### 2.1 设计原则

- **Schedule 是纯蓝图**，不存"当前值班人"。每次需要时实时计算（data-model §3.2）。
- **实时计算**：排班变更立即生效，无快照一致性问题。
- **分层 + Override**：primary / secondary 多层，临时换班用 override 覆盖。

### 2.2 计算流程（呼应 architecture §3.3）

```
输入：Schedule + 查询时间 T + Override 层
   │
   ▼
1. 取 Schedule.timezone，把 T 转成当地时间
2. 遍历 layers（按 priority 升序）：
     对每层，根据 Rotation 规则算出 T 时刻的参与者
3. 应用 Override 层（临时换班覆盖，最高优先级）
4. 输出有序 oncall_users（primary → secondary → override）
```

### 2.3 Rotation 规则

```yaml
rotation:
  participants: [user_a, user_b, user_c]   # 轮班人员
  shift_length: "24h"                       # 每班时长（仅 rotation_type=custom 生效；daily/weekly 忽略）
  handoff_time: "09:00"                     # 交接时刻
  rotation_type: daily | weekly | custom    # 轮换周期：daily=固定 24h、weekly=固定 168h、custom=取 shift_length
  start_date: "2026-06-01"
  end_date: null                            # null = 无限期
```

- **算当前班次**：`班次序号 = floor((T - start_date) / 周期)`，`当前值班 = participants[班次序号 mod 人数]`；**周期由 `rotation_type` 决定**（daily=24h、weekly=168h、custom=`shift_length`）。
- **handoff_time**：交接时刻（如每天 09:00 换班），保证换班发生在工作时间。
- **跨时区**：每个 Schedule 独立 timezone，团队跨时区各自正确计算。

### 2.3.1 follow_the_sun（日不落接力，P3.2）

**设计意图**：多个分布在不同时区的 layer（如亚太/欧洲/美洲），按**当前 UTC 时刻落在哪个 layer 的本地工作时段**选出该时区值班人，实现"日不落"接力——亚太白天亚太值班、欧洲白天欧洲值班、美洲白天美洲值班，24h 无缝覆盖。

**时区/时段承载**：复用 `Schedule.layers`（JSON），每个 `ScheduleLayer` 扩展三字段（仅 follow_the_sun 型解析）：

```yaml
layer:
  timezone:   "Europe/London"   # IANA 时区名；空则回退 Schedule.timezone，再回退 UTC
  work_start: "09:00"           # 本地工作起（含），"HH:MM"
  work_end:   "17:00"           # 本地工作止（不含）；支持跨午夜（start>end，如夜班 22:00~06:00）
```

**接力解算**（`resolveFollowTheSun`）：对给定 UTC 时刻 T：

1. 把 T 转到各 layer 本地时间，判断是否落在 `[work_start, work_end)`（跨午夜时段：`t>=start || t<end`；任一字段空视为全天）。
2. **命中的 layer 全部返回**（多区工作时段重叠段两层同时在班，交接更平滑）；层内多人仍走 rotation 班次序号轮换（同一时区区域内多人轮班保留 rotation 语义）。
3. **空档兜底**：若无任何 layer 在工作时段（接力空档），取本地时间距各自 `work_start` **最快到来**的 layer 兜底返回真实在班人（层名加"（接力空档兜底）"提示），避免"无人值班"盲区。
4. 与 rotation/calendar 并存（仅 `type==follow_the_sun` 走此路径），跳过禁用用户、Override 叠加均沿用统一框架。

**preview** 对 follow_the_sun 按当天每 4h 采样接力在班层取并集，完整呈现"这一天由哪些时区区域接力覆盖"（单一时刻只反映某一区，会误导）。

**边界**：时区/DST 由 `time.LoadLocation` + `time.In` 处理（工作时段按本地墙钟判定，DST 切换自动跟随本地时刻）；无效时区名逐级降级不阻断解算。

### 2.4 Override（临时换班，M5.3）

```yaml
override:
  user_id: "user_d"          # 顶替的人
  start: "2026-06-20 18:00"
  end:   "2026-06-21 09:00"
  reason: "user_a 请假"
  created_by: "user_a"       # 需 schedule.override 权限（限自己或 admin）
```

- Override 是最高优先级层，时段内完全覆盖 Rotation 结果。
- 支持自我换班（值班人临时换）和 admin 指派换班。

### 2.5 缺席处理（M5.5）

- 值班人请假/离职 → 创建 Override 覆盖，或从 Rotation.participants 移除。
- **空班检测**：若计算结果为空（所有人缺席），触发告警通知 team_admin，避免出现"无人值班"。

### 2.6 缓存与预计算（M5.6/M5.8）

- **实时结果缓存**：按分钟级缓存到 Redis（排班不会秒级变化），降低重复计算压力。缓存 key = `schedule_id + 分钟级时间`。
- **预计算展示**：未来 N 天的排班可预计算，用于排班日历 UI 展示。
- **生效判断永远实时算**：预计算只用于展示，不用于"此刻谁在班"的决策。

### 2.7 排班 API（M5.7）

```http
GET /api/v1/schedules/{id}/oncall?time=<iso8601>
→ { schedule_id, schedule_name, layers: [ { name, priority, users: [ { id, name, username, override } ] } ] }
   # C7：实际响应为分层结构（primary/secondary 由 layer.priority 表达，数字小优先；
   #     Override 换班命中时作为最高优先级层置顶，users[].override=true）。

GET /api/v1/schedules/{id}/preview?days=14
→ { schedule_id, days: [ { date, layers: [...] }, ... ] }   # 排班日历预览
```

供值班大屏、外部系统查询。

### 2.8 Override 换班 API（M5.3）

```http
POST   /api/v1/schedules/{id}/overrides   # 建换班；权限 schedule.override
       body: { user_id, start_time, end_time, reason }
       # 换己班（user_id==登录人）仅需 schedule.override；换他人须叠加 schedule.update
       #（team_admin/org_admin 具备），防值班人越权指派他人替班。
GET    /api/v1/schedules/{id}/overrides   # 列换班（按 start_time 倒序）
DELETE /api/v1/schedules/{id}/overrides/{oid}  # 删换班；权限 schedule.override
```

- 命中判定：`start_time <= 查询时刻 < end_time`，且顶替人在职（禁用则不顶替）。
- 多条命中取最新创建的一条（后设覆盖先设）。
- 顶替人在 oncall 结果中以最高优先级层返回，`override=true`。

---

## 3. 升级引擎 ★

### 3.1 升级策略定义（data-model §3.2）

```yaml
escalation_policy:
  name: "支付服务升级策略"
  repeat_times: 2              # 当前 level 未 ack 时重复通知次数
  levels:
    - level: 1
      delay_minutes: 1         # 进入此 level 后多久通知
      targets:
        - type: schedule       # schedule | user | team
          schedule_id: "..."
      notify_channels: [im]
    - level: 2
      delay_minutes: 10
      targets:
        - type: schedule       # 同 schedule 二次，或不同
          schedule_id: "..."
      notify_channels: [im, phone]
    - level: 3
      delay_minutes: 20
      targets:
        - type: team           # 通知全团队
          team_id: "..."
      notify_channels: [im, phone, sms]
```

### 3.2 升级时序（呼应 architecture §3.4）

```
Incident 创建（triggered）
   │
   ▼
Enqueue level[0] 延迟任务（asynq.ProcessIn(delay_minutes[0])）
   │
   ├── 若在 delay 内 ack ──► 取消所有待触发任务（asynq.DeleteTask）→ acked 态
   │
   └── 超时未 ack（任务到期触发）
         │
         ▼
       执行 level[0]：
         · 排班引擎算 targets 的实际人员
         · 通知引擎分发（按 notify_channels）
         · 记 TimelineItem + IncidentAction
         │
         ├── repeat_times > 0 ──► 再 Enqueue 一次 level[0]（循环通知）
         │
         └── Enqueue level[1] 延迟任务 ──► ... → 末级后停止
```

### 3.3 目标解析

升级触发时，targets 解析成实际通知对象：

| target.type | 解析方式 |
|-------------|---------|
| `schedule` | 调排班引擎算"此刻在班人" |
| `user` | 直接是某 User |
| `team` | 该团队所有成员（通常用于末级兜底） |

多个 target 取并集去重。

### 3.4 关键正确性

- **ack 即取消**：任何 ack（Web/IM）通过事件总线删除该 Incident 的所有待触发 Asynq 任务（`asynq.DeleteTask`）。
- **状态守卫**（最终一致）：handler 执行前检查 `incident.status`，已 ack/resolved 的即使任务误触发也不动作（防止取消与触发竞态）。
- **幂等**：任务幂等键 = `incident_id + level + repeat序号`，重复触发不重复通知。
- **手动升级**（M6.5）：响应者主动 escalate → 立即跳到下一 level，取消当前 level 待触发任务。

### 3.5 循环通知（repeat）

- `repeat_times` 控制当前 level 未 ack 时的重复通知次数。
- **策略级语义（C6 澄清）**：`repeat_times` 是 **EscalationPolicy 级字段**（非各 level 独立配置）。
  对**每一层**都生效——某层触发后，未 ack 则在该层再重复通知 `repeat_times` 次，
  即**每层共通知 `repeat_times + 1` 次**（首次 + 重复），重复用尽后才推进下一层。
- **重复间隔 = 该层自身的 `delay_minutes`**：重复任务复用 `scheduleLevel` 以本层 delay 延迟入队，
  故 level A 的重复间隔取 A 的 delay，level B 的取 B 的 delay（各层可不同）。
- 任务幂等键含 `repeat序号`（`esc:{inc}:{level}:{repeatSeq}`），重复触发不重复通知。
- 防轰炸：`repeat_times` 有上限，且 ack/resolve 后状态守卫使残留重复任务不再动作。
- 实现见 `internal/escalation/engine.go` HandleTask（`repeatSeq < repeat_times` 则续排同层，否则推进下一层）；
  语义由 `TestRepeatTimesSemantics` 锁定。

### 3.6 升级事件记录（M6.6）

每次升级触发都产生：
- **TimelineItem**（type=`escalated`）：谁、何时、升到哪级。
- **IncidentAction**（type=`escalate`）：审计记录。
- 更新 `Incident.current_level` / `escalated_count`。

---

## 4. 升级与排班的协作

```
Incident 触发升级
   │
   ▼
升级引擎读 EscalationPolicy.levels[current]
   │
   ▼
对每个 target.type=schedule，调排班引擎：
   GET schedule.oncall(now) → [user, ...]
   │
   ▼
通知引擎对实际人员分发（能力域 7）
```

- 排班变更**立即影响下一次升级**：因为每次升级都实时算在班人，换班后下一级升级自动找到新人。
- 这是排班"实时计算"设计的直接受益点。

---

## 5. 可靠性

| 要求 | 实现 |
|------|------|
| **升级任务绝不丢** | Asynq 高优先级队列 + 高 MaxRetry + Redis 持久化；任务持久化在 Redis，worker 重启不丢 |
| **ack 取消可靠** | 状态守卫兜底（即使 DeleteTask 失败，handler 也会因 incident 已 ack 而不动作） |
| **不漏真故障** | 末级升级到全团队 + 多通道，保证最终有人响应 |
| **可观测** | 升级触发次数、各级 ack 率、平均升级层级暴露 metrics |

---

## 6. 开放问题

| # | 问题 | 倾向 |
|---|------|------|
| Q1 | ~~follow_the_sun（跟随太阳）排班类型的具体实现~~ | ✅ 已实现（P3.2）：每 layer 配 timezone + 本地工作时段，按当前 UTC 落在哪个 layer 工作时段接力选人，空档取最快上班层兜底。见 §2.3.1。 |
| Q2 | 升级策略的继承（子服务继承父服务策略） | 不继承，每个 Service 显式绑定（避免隐式行为） |
| Q3 | repeat 通知的退避策略 | 固定间隔为主，避免复杂退避逻辑 |
