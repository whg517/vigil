# ADR-0021: Runbook 诊断只读 / 处置写两档

| 字段 | 内容 |
|------|------|
| **状态** | Accepted |
| **日期** | 2026-07-09 |
| **相关** | [ADR-0022](./0022-aiinsight-hitl-evidence.md)、`ent/schema/runbook.go` |

## 背景

告警处置的"下一步"离不开操作手册(Runbook),但把"能搞垮生产的写操作"交给告警平台自动执行,风险极高——一旦逻辑或触发条件出错,Vigil 会变成"能搞垮生产的定时炸弹"。同时诊断类只读操作(查日志、看指标、看拓扑)门槛低、价值高,不应因噎废食一律禁掉。需要在"自动化提效"与"生产安全"之间划出清晰边界。

## 决策

Runbook 分两型:`document`(纯 Markdown,给人看)与 `executable`(可执行)。executable 的每个步骤带 `readonly` 标志:

- `readonly:true`(诊断:查日志/指标/拓扑):Vigil 内置执行门槛低,可自动执行。
- `readonly:false`(处置:重启/扩容/回滚):**不直接执行**——生成指令交由人确认,或对接外部平台执行。

处置类步骤默认 `require_approval:true`,必须人在 IM/Web 弹窗确认后才执行;`require_approval:false` 仅限高度可信场景且需 admin 显式配置。所有执行落 `IncidentAction` 留痕。步骤失败按 `on_failure` 处理,取值 `continue`(继续)/`abort`(中止)/`escalate`(处置失败自动升级)。

`Executor` 接口抽象执行后端(HTTP / 内置诊断 / Ansible / Jenkins / 内部平台),触发方式 `manual`/`on_incident`/`on_severity`/`on_label_match`。默认**只展示 Runbook 给人参考,不自动执行**(除非显式配置 auto-run 且步骤全为 readonly)。凭证加密存储,由 admin 管理。

## 理由

- 写操作交给用户既有的受控执行环境(Ansible/Jenkins/内部平台),权责清晰,Vigil 不越俎代庖碰生产写。
- 诊断只读默认可执行、处置写默认需确认,兼顾提效与安全,坚持 human-in-the-loop。
- `on_failure=escalate` 让处置失败能自动升级,失败不被吞掉。

## 备选方案

- **所有可执行步骤都自动跑**:一旦触发条件或步骤逻辑出错,直接误伤生产,风险不可接受。
- **一律不许执行、只做文档**:放弃了诊断只读这类低风险高价值的自动化,价值打折。

## 影响 / 权衡

- 处置类步骤引入"人确认"环节,牺牲了全自动的即时性,换取生产安全与权责清晰,这是刻意的取舍。
- 需要维护 `Executor` 多后端对接与凭证加密管理,复杂度上移到集成层。
- 与 AI 建议一样坚持 HITL(见 ADR-0022),全项目形成一致的"写操作/关键动作须人确认"基线。
