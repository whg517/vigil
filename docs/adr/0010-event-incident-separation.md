# ADR-0010： Event 与 Incident 分离

| 字段 | 内容 |
|------|------|
| **状态** | Accepted |
| **日期** | 2026-07-09 |
| **相关** | [`0012-triage-three-stage-pipeline.md`](./0012-triage-three-stage-pipeline.md)、`ent/schema/event.go`、`ent/schema/incident.go` |

## 背景

告警系统里"原始信号"与"处理单元"混为一谈会同时拖累两端:信号海量涌入需要高吞吐只追加的写入,而人真正介入处理的对象需要状态、责任人与上下文。二者基数与语义完全不同,用同一实体建模会互相拖累。

## 决策

拆成两个实体,分诊层聚合 Event → Incident(N:1):

- **Event** = 原始告警信号:海量、不可变、只追加。
- **Incident** = 值得人介入的处理单元:少量、有上下文、有状态机。

Incident 状态机:

- `triggered → acked → resolved → closed`
- `triggered → escalated → (ack → acked)`

**硬约束**:任何状态变更必须产生一条 `TimelineItem`(时间线只追加,保证可溯源)。

## 理由

- 两者基数与语义完全不同:信号(海量、不可变)vs 处理单元(少量、有状态)。
- 只有 Incident 才需要状态机与责任人;Event 保持纯追加,利于高吞吐接入与失败重放。

## 备选方案

- 单一实体承载"告警"既做信号又做处理单元:状态字段与海量追加写混在一起,查询与生命周期管理都被拖累,否决。

## 影响 / 权衡

- 引入分诊层负责 Event → Incident 的聚合(去重/抑制/聚合,见 ADR-0012),多一层但换来清晰的职责边界。
- 状态变更强制产生 TimelineItem,写路径多一步,换来完整可溯源的处理时间线。
