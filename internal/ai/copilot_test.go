package ai

import (
	"context"
	"errors"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/aiinsight"
	"github.com/kevin/vigil/ent/enttest"
	entincident "github.com/kevin/vigil/ent/incident"
	entrunbook "github.com/kevin/vigil/ent/runbook"
	"github.com/kevin/vigil/ent/schema"
	"github.com/kevin/vigil/ent/timelineitem"
	"github.com/kevin/vigil/internal/runbook"
	"github.com/kevin/vigil/internal/timeline"

	_ "github.com/mattn/go-sqlite3"
)

// --- 测试脚手架 ---

// seqProvider 顺序返回多段响应的桩 Provider。Copilot 一次 AnalyzeIncident 会发两次
// Complete（runbook 推荐 + 摘要草拟），需按调用顺序给不同响应。
type seqProvider struct {
	resps []string // 按调用顺序返回；用尽后返回最后一条
	err   error
	avail bool
	calls int
}

func (s *seqProvider) Complete(_ context.Context, _ string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	i := s.calls
	s.calls++
	if i >= len(s.resps) {
		if len(s.resps) == 0 {
			return "", nil
		}
		return s.resps[len(s.resps)-1], nil
	}
	return s.resps[i], nil
}
func (s *seqProvider) Embed(_ context.Context, _ string) ([]float32, error) {
	return []float32{0.1, 0.2, 0.3}, nil
}
func (s *seqProvider) Available() bool { return s.avail }

func newCopilotTestClient(t *testing.T) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:copilot_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// seedIncidentWithService 造一个 incident + 关联 Service，Service 关联 nRunbooks 个可执行 Runbook。
// 首个 Runbook 含一个写操作步骤（readonly=false + require_approval），供安全红线测试。
// 返回 incident、service 及创建的 runbook 列表（第 0 个是含写步骤的）。
func seedIncidentWithService(t *testing.T, c *ent.Client) (*ent.Incident, *ent.Service, []*ent.Runbook) {
	t.Helper()
	ctx := context.Background()

	// 含写操作步骤的可执行 Runbook（回滚——写操作，require_approval）。
	rbWrite, err := c.Runbook.Create().
		SetName("支付回滚处置").SetType(entrunbook.TypeExecutable).
		SetSteps([]schema.RunbookStep{
			{
				ID:   "s1",
				Name: "回滚上一版本",
				Action: schema.StepAction{
					Type: "execute",
					Target: schema.StepTarget{
						Kind: "jenkins", Endpoint: "http://jenkins/job/rollback",
						Readonly: false, // ★ 写操作
					},
				},
				OnFailure:       "abort",
				RequireApproval: true,
			},
		}).Save(ctx)
	if err != nil {
		t.Fatalf("create write runbook: %v", err)
	}
	rbDiag, err := c.Runbook.Create().
		SetName("支付诊断").SetType(entrunbook.TypeExecutable).Save(ctx)
	if err != nil {
		t.Fatalf("create diag runbook: %v", err)
	}

	svc, err := c.Service.Create().
		SetName("payment").SetSlug("payment").
		AddRunbooks(rbWrite, rbDiag).Save(ctx)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}

	inc, err := c.Incident.Create().
		SetNumber("INC-COP1").SetTitle("支付 5xx 激增").
		SetSeverity("critical").SetStatus("triggered").
		SetPriority("p1").SetSummary("支付接口 5xx 错误率升高").
		SetTriggerType("auto").SetServiceID(svc.ID).Save(ctx)
	if err != nil {
		t.Fatalf("create incident: %v", err)
	}
	return inc, svc, []*ent.Runbook{rbWrite, rbDiag}
}

// --- runbook_suggestion ---

// TestCopilot_RunbookSuggestion 验证 runbook 推荐产出：带 evidence（含被推荐 Runbook）、
// stage=copilot、type=runbook_suggestion、status=suggested、content 含推荐对象但不含执行指令。
func TestCopilot_RunbookSuggestion(t *testing.T) {
	c := newCopilotTestClient(t)
	inc, _, rbs := seedIncidentWithService(t, c)
	rbWrite := rbs[0]

	// 第 1 次调用（runbook 推荐）返回推荐写操作 Runbook；第 2 次（摘要）无时间线不产出，随便给。
	mp := &seqProvider{avail: true, resps: []string{
		`{"should_recommend":true,"runbook_id":` + itoa(rbWrite.ID) + `,"confidence":0.85,"reason":"这类支付 5xx 通常先回滚"}`,
		`当前支付 5xx 升高，尚在处置中`,
	}}
	e := NewCopilotEngine(c, mp)
	e.SetRecorder(timeline.NewRecorder(c))

	res, err := e.AnalyzeIncident(context.Background(), inc.ID)
	if err != nil {
		t.Fatalf("AnalyzeIncident: %v", err)
	}
	if res.Runbook == nil {
		t.Fatal("应产出 runbook_suggestion 建议")
	}
	ins := res.Runbook
	if string(ins.Type) != "runbook_suggestion" {
		t.Errorf("type: got %q, want runbook_suggestion", ins.Type)
	}
	if string(ins.Stage) != "copilot" {
		t.Errorf("stage: got %q, want copilot", ins.Stage)
	}
	if string(ins.Status) != "suggested" {
		t.Errorf("status: got %q, want suggested", ins.Status)
	}
	// 基线：每建议必带 evidence（含被推荐 Runbook）。
	if len(ins.Evidence) == 0 {
		t.Error("runbook 推荐必须带 evidence（无 evidence 不应产出）")
	}
	foundRb := false
	for _, ev := range ins.Evidence {
		if ev["kind"] == "runbook" && evidenceIntEq(ev["runbook_id"], rbWrite.ID) {
			foundRb = true
		}
	}
	if !foundRb {
		t.Errorf("evidence 应含被推荐 runbook_id=%d, got %v", rbWrite.ID, ins.Evidence)
	}
	// content 应含推荐对象，但绝不含任何执行指令字段（安全红线：只呈现不执行）。
	if !evidenceIntEq(ins.Content["recommended_runbook_id"], rbWrite.ID) {
		t.Errorf("content.recommended_runbook_id: got %v, want %d", ins.Content["recommended_runbook_id"], rbWrite.ID)
	}
	for _, k := range []string{"execute", "approved", "run", "steps", "auto_execute"} {
		if _, ok := ins.Content[k]; ok {
			t.Errorf("content 不应含执行指令字段 %q（推荐只呈现不执行）", k)
		}
	}
	// 应写 ai_insight 时间线。
	cnt, _ := c.TimelineItem.Query().
		Where(timelineitem.HasIncidentWith(entincident.IDEQ(inc.ID)),
			timelineitem.TypeEQ(timelineitem.TypeAiInsight)).Count(context.Background())
	if cnt == 0 {
		t.Error("产出建议后应写 ai_insight 时间线")
	}
}

// TestCopilot_RunbookAccept_DoesNotBypassApproval 是本任务最重要的安全测试：
// AI 推荐的 Runbook accept 后，绝不绕过 require_approval —— accept 只把建议置 accepted
// （呈现/高亮），不触发执行；真正执行仍走 Runbook 两档安全（未审批的写步骤被阻断）。
func TestCopilot_RunbookAccept_DoesNotBypassApproval(t *testing.T) {
	c := newCopilotTestClient(t)
	ctx := context.Background()
	inc, _, rbs := seedIncidentWithService(t, c)
	rbWrite := rbs[0] // 含写操作步骤（require_approval）

	mp := &seqProvider{avail: true, resps: []string{
		`{"should_recommend":true,"runbook_id":` + itoa(rbWrite.ID) + `,"confidence":0.9,"reason":"回滚"}`,
		``, // 摘要无时间线不产出
	}}
	e := NewCopilotEngine(c, mp)
	res, err := e.AnalyzeIncident(ctx, inc.ID)
	if err != nil || res.Runbook == nil {
		t.Fatalf("produce runbook suggestion: err=%v res=%+v", err, res)
	}

	// accept 走 DiagnoseEngine.ResolveInsight —— runbook_suggestion 无实际应用动作，
	// applyInsight 返回 false，终态应为 accepted（非 applied），且不触发任何执行。
	diag := NewDiagnoseEngine(c, nil)
	got, err := diag.ResolveInsight(ctx, res.Runbook.ID, 7, true)
	if err != nil {
		t.Fatalf("ResolveInsight: %v", err)
	}
	if string(got.Status) != "accepted" {
		t.Errorf("accept runbook 推荐应为 accepted（不 applied，不触发执行）: got %q", got.Status)
	}

	// ★ 核心断言：accept 后没有任何「已执行」的 runbook_executed 时间线产生
	//（若 accept 误触发执行，会写 runbook_executed）。
	execCnt, _ := c.TimelineItem.Query().
		Where(timelineitem.HasIncidentWith(entincident.IDEQ(inc.ID)),
			timelineitem.TypeEQ(timelineitem.TypeRunbookExecuted)).Count(ctx)
	if execCnt != 0 {
		t.Errorf("accept AI 推荐绝不能触发执行，不应有 runbook_executed 时间线, got %d", execCnt)
	}

	// ★ 独立验证两档安全仍生效：直接跑该 Runbook 的写步骤，approved=false 必被阻断（skip），
	//   不产生任何写副作用——证明「执行仍走审批闸门」，AI 推荐没有削弱它。
	rbEngine := runbook.NewEngine(c, runbook.NewRegistry())
	exec, err := rbEngine.Execute(ctx, rbWrite.ID, inc.ID, false /* approved */, 7)
	if err != nil {
		t.Fatalf("execute runbook: %v", err)
	}
	if !exec.PendingApproval {
		t.Error("写步骤 approved=false 应被审批闸门阻断（PendingApproval=true）")
	}
	if len(exec.Steps) == 0 || !exec.Steps[0].Skipped {
		t.Errorf("未审批的写步骤应被 skip，不执行: %+v", exec.Steps)
	}
}

// TestCopilot_Runbook_NoCandidates_NotProduced 验证 Service 未关联任何 Runbook 时不产出推荐。
func TestCopilot_Runbook_NoCandidates_NotProduced(t *testing.T) {
	c := newCopilotTestClient(t)
	ctx := context.Background()
	svc, _ := c.Service.Create().SetName("svc").SetSlug("svc-no-rb").Save(ctx)
	inc, _ := c.Incident.Create().SetNumber("INC-NORB").SetTitle("x").
		SetSeverity("warning").SetStatus("triggered").SetServiceID(svc.ID).Save(ctx)

	mp := &seqProvider{avail: true, resps: []string{
		`{"should_recommend":true,"runbook_id":1,"confidence":0.9,"reason":"x"}`,
	}}
	e := NewCopilotEngine(c, mp)

	res, err := e.AnalyzeIncident(ctx, inc.ID)
	if err != nil {
		t.Fatalf("AnalyzeIncident: %v", err)
	}
	if res.Runbook != nil {
		t.Error("Service 未关联 Runbook 时不应产出推荐")
	}
	cnt, _ := c.AIInsight.Query().
		Where(aiinsight.TypeEQ(aiinsight.TypeRunbookSuggestion)).Count(ctx)
	if cnt != 0 {
		t.Errorf("无候选 Runbook 不应落 runbook_suggestion, got %d", cnt)
	}
}

// TestCopilot_Runbook_HallucinatedID_Filtered 验证 LLM 推荐的 id 不在候选集（幻觉）时不产出。
func TestCopilot_Runbook_HallucinatedID_Filtered(t *testing.T) {
	c := newCopilotTestClient(t)
	inc, _, _ := seedIncidentWithService(t, c)

	// LLM 编造一个不在候选集里的 id 99999。
	mp := &seqProvider{avail: true, resps: []string{
		`{"should_recommend":true,"runbook_id":99999,"confidence":0.9,"reason":"x"}`,
		``,
	}}
	e := NewCopilotEngine(c, mp)

	res, err := e.AnalyzeIncident(context.Background(), inc.ID)
	if err != nil {
		t.Fatalf("AnalyzeIncident: %v", err)
	}
	if res.Runbook != nil {
		t.Error("推荐的 runbook_id 不在候选集（幻觉），不应产出")
	}
}

// TestCopilot_Runbook_LowConfidence_NotProduced 验证低于置信度门槛（0.6）不产出推荐。
func TestCopilot_Runbook_LowConfidence_NotProduced(t *testing.T) {
	c := newCopilotTestClient(t)
	inc, _, rbs := seedIncidentWithService(t, c)
	mp := &seqProvider{avail: true, resps: []string{
		`{"should_recommend":true,"runbook_id":` + itoa(rbs[0].ID) + `,"confidence":0.4,"reason":"把握不大"}`,
		``,
	}}
	e := NewCopilotEngine(c, mp)

	res, err := e.AnalyzeIncident(context.Background(), inc.ID)
	if err != nil {
		t.Fatalf("AnalyzeIncident: %v", err)
	}
	if res.Runbook != nil {
		t.Error("置信度 0.4 < 0.6 门槛，不应产出 runbook 推荐")
	}
}

// TestCopilot_Runbook_ShouldNotRecommend_NotProduced 验证 LLM 判断无合适 Runbook 时不产出。
func TestCopilot_Runbook_ShouldNotRecommend_NotProduced(t *testing.T) {
	c := newCopilotTestClient(t)
	inc, _, _ := seedIncidentWithService(t, c)
	mp := &seqProvider{avail: true, resps: []string{
		`{"should_recommend":false,"runbook_id":0,"confidence":0.9,"reason":"都不合适"}`,
		``,
	}}
	e := NewCopilotEngine(c, mp)

	res, err := e.AnalyzeIncident(context.Background(), inc.ID)
	if err != nil {
		t.Fatalf("AnalyzeIncident: %v", err)
	}
	if res.Runbook != nil {
		t.Error("LLM 判断无合适 Runbook 时不应产出推荐")
	}
}

// --- draft_summary ---

// TestCopilot_DraftSummary 验证摘要草拟产出：type=draft_summary、stage=copilot、
// 带 evidence（时间线）、content 含 summary。
func TestCopilot_DraftSummary(t *testing.T) {
	c := newCopilotTestClient(t)
	ctx := context.Background()
	inc, _, _ := seedIncidentWithService(t, c)
	// 造两条时间线作为摘要 evidence。
	rec := timeline.NewRecorder(c)
	_ = rec.Record(ctx, inc.ID, timelineitem.TypeIncidentCreated, "事件创建",
		timeline.Actor{Kind: "system"}, timelineitem.SourceSystem, nil)
	_ = rec.Record(ctx, inc.ID, timelineitem.TypeAck, "张三已认领",
		timeline.Actor{Kind: "user", ID: "1"}, timelineitem.SourceWeb, nil)

	// 第 1 次（runbook 推荐）判断不推荐；第 2 次（摘要）返回摘要文本。
	mp := &seqProvider{avail: true, resps: []string{
		`{"should_recommend":false}`,
		`支付 5xx 已创建事件，张三认领处置中，尚未定位根因。`,
	}}
	e := NewCopilotEngine(c, mp)
	e.SetRecorder(rec)

	res, err := e.AnalyzeIncident(ctx, inc.ID)
	if err != nil {
		t.Fatalf("AnalyzeIncident: %v", err)
	}
	if res.Summary == nil {
		t.Fatal("应产出 draft_summary 建议")
	}
	ins := res.Summary
	if string(ins.Type) != "draft_summary" {
		t.Errorf("type: got %q, want draft_summary", ins.Type)
	}
	if string(ins.Stage) != "copilot" {
		t.Errorf("stage: got %q, want copilot", ins.Stage)
	}
	if len(ins.Evidence) == 0 {
		t.Error("draft_summary 应带 evidence（时间线）")
	}
	if s, _ := ins.Content["summary"].(string); s == "" {
		t.Error("content 应含 summary 文本")
	}
}

// TestCopilot_DraftSummary_NoTimeline_NotProduced 验证无时间线（无 evidence）时不产出摘要。
func TestCopilot_DraftSummary_NoTimeline_NotProduced(t *testing.T) {
	c := newCopilotTestClient(t)
	ctx := context.Background()
	// Service 无 Runbook（runbook 推荐也不产出），且 incident 无时间线。
	svc, _ := c.Service.Create().SetName("svc").SetSlug("svc-empty").Save(ctx)
	inc, _ := c.Incident.Create().SetNumber("INC-NOTL").SetTitle("x").
		SetSeverity("warning").SetStatus("triggered").SetServiceID(svc.ID).Save(ctx)

	mp := &seqProvider{avail: true, resps: []string{``, `这段摘要不该被产出`}}
	e := NewCopilotEngine(c, mp)

	res, err := e.AnalyzeIncident(ctx, inc.ID)
	if err != nil {
		t.Fatalf("AnalyzeIncident: %v", err)
	}
	if res.Summary != nil {
		t.Error("无时间线（无 evidence）不应产出 draft_summary")
	}
}

// TestCopilot_DraftSummary_DistinctFromPostmortemDraft 锁定 draft_summary 与 postmortem_draft
// 的区分：draft_summary=处理中实时摘要（stage=copilot），postmortem_draft=复盘全文（stage=postmortem）。
// Copilot 只产 draft_summary，绝不产 postmortem_draft。
func TestCopilot_DraftSummary_DistinctFromPostmortemDraft(t *testing.T) {
	c := newCopilotTestClient(t)
	ctx := context.Background()
	inc, _, _ := seedIncidentWithService(t, c)
	rec := timeline.NewRecorder(c)
	_ = rec.Record(ctx, inc.ID, timelineitem.TypeIncidentCreated, "事件创建",
		timeline.Actor{Kind: "system"}, timelineitem.SourceSystem, nil)

	mp := &seqProvider{avail: true, resps: []string{`{"should_recommend":false}`, `实时状态摘要`}}
	e := NewCopilotEngine(c, mp)
	res, err := e.AnalyzeIncident(ctx, inc.ID)
	if err != nil || res.Summary == nil {
		t.Fatalf("produce draft_summary: err=%v res=%+v", err, res)
	}
	// 产出的必须是 draft_summary + stage=copilot，绝不是 postmortem_draft。
	if string(res.Summary.Type) == "postmortem_draft" {
		t.Error("Copilot 摘要绝不能是 postmortem_draft（那是复盘全文，走 postmortem 引擎）")
	}
	if string(res.Summary.Type) != "draft_summary" || string(res.Summary.Stage) != "copilot" {
		t.Errorf("应为 draft_summary/copilot: got type=%q stage=%q", res.Summary.Type, res.Summary.Stage)
	}
	// 库里不应有任何 postmortem_draft（Copilot 不产复盘全文）。
	pmCnt, _ := c.AIInsight.Query().
		Where(aiinsight.TypeEQ(aiinsight.TypePostmortemDraft)).Count(ctx)
	if pmCnt != 0 {
		t.Errorf("Copilot 不应产出 postmortem_draft, got %d", pmCnt)
	}
}

// --- 降级 ---

// TestCopilot_NoProvider_Degrades 验证 LLM 不可用时降级：不产出、不报错、不落库（主流程不阻断）。
func TestCopilot_NoProvider_Degrades(t *testing.T) {
	c := newCopilotTestClient(t)
	inc, _, _ := seedIncidentWithService(t, c)
	e := NewCopilotEngine(c, nil) // 无 provider

	res, err := e.AnalyzeIncident(context.Background(), inc.ID)
	if err != nil {
		t.Fatalf("无 provider 应降级不报错: %v", err)
	}
	if res.Runbook != nil || res.Summary != nil {
		t.Error("无 provider 时不应产出任何建议（降级）")
	}
	cnt, _ := c.AIInsight.Query().Count(context.Background())
	if cnt != 0 {
		t.Errorf("降级时不应落 AIInsight, got %d", cnt)
	}
}

// TestCopilot_ProviderUnavailable_Degrades 验证 provider 存在但 Available()=false 时降级。
func TestCopilot_ProviderUnavailable_Degrades(t *testing.T) {
	c := newCopilotTestClient(t)
	inc, _, _ := seedIncidentWithService(t, c)
	e := NewCopilotEngine(c, &seqProvider{avail: false})

	res, err := e.AnalyzeIncident(context.Background(), inc.ID)
	if err != nil {
		t.Fatalf("provider 不可用应降级: %v", err)
	}
	if res.Runbook != nil || res.Summary != nil {
		t.Error("provider 不可用时不应产出建议")
	}
}

// TestCopilot_LLMCallFailed_Degrades 验证 LLM 调用失败（401/超时）时降级：不产出、不报错、不落库。
func TestCopilot_LLMCallFailed_Degrades(t *testing.T) {
	c := newCopilotTestClient(t)
	ctx := context.Background()
	inc, _, _ := seedIncidentWithService(t, c)
	// 造时间线，确保若非降级 summary 本会产出——以此证明是「调用失败」导致不产出。
	rec := timeline.NewRecorder(c)
	_ = rec.Record(ctx, inc.ID, timelineitem.TypeIncidentCreated, "事件创建",
		timeline.Actor{Kind: "system"}, timelineitem.SourceSystem, nil)

	mp := &seqProvider{avail: true, err: errors.New("glm http 401")}
	e := NewCopilotEngine(c, mp)
	e.SetRecorder(rec)

	res, err := e.AnalyzeIncident(ctx, inc.ID)
	if err != nil {
		t.Fatalf("LLM 调用失败应降级不报错: %v", err)
	}
	if res.Runbook != nil || res.Summary != nil {
		t.Error("LLM 调用失败不应产出任何建议")
	}
	cnt, _ := c.AIInsight.Query().Count(ctx)
	if cnt != 0 {
		t.Errorf("LLM 调用失败降级不应落 AIInsight, got %d", cnt)
	}
}

// TestCopilot_MissingIncident 验证不存在的 incident id 归一为 error（手动端点据此返 404）。
func TestCopilot_MissingIncident(t *testing.T) {
	c := newCopilotTestClient(t)
	e := NewCopilotEngine(c, &seqProvider{avail: true, resps: []string{"{}"}})
	if _, err := e.AnalyzeIncident(context.Background(), 999999); err == nil {
		t.Fatal("不存在的 incident 应返回 error, got nil")
	}
}

// --- 解析/守卫单元 ---

// TestParseRunbookOutput_NonJSON 验证非 JSON 输出解析失败返回 id=0（不产出）。
func TestParseRunbookOutput_NonJSON(t *testing.T) {
	id, _, _ := parseRunbookOutput("建议用回滚手册")
	if id != 0 {
		t.Errorf("非 JSON 应返回 id=0, got %d", id)
	}
}

// TestParseRunbookOutput_ShouldNotRecommend 验证 should_recommend=false 时返回 id=0（不产出）。
func TestParseRunbookOutput_ShouldNotRecommend(t *testing.T) {
	id, _, _ := parseRunbookOutput(`{"should_recommend":false,"runbook_id":5,"confidence":0.9}`)
	if id != 0 {
		t.Errorf("should_recommend=false 应返回 id=0, got %d", id)
	}
}

// TestParseRunbookOutput_ConfCapped 验证置信度上限 1.0。
func TestParseRunbookOutput_ConfCapped(t *testing.T) {
	_, conf, _ := parseRunbookOutput(`{"should_recommend":true,"runbook_id":5,"confidence":1.7}`)
	if conf != 1.0 {
		t.Errorf("置信度应被限制为 1.0, got %f", conf)
	}
}

// 静态断言：Copilot 用 aiinsight 枚举常量，防止枚举漂移编译期即暴露。
var _ = aiinsight.TypeRunbookSuggestion
var _ = aiinsight.TypeDraftSummary
