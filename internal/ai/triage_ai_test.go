package ai

import (
	"context"
	"errors"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/aiinsight"
	"github.com/kevin/vigil/ent/enttest"
	entincident "github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/timelineitem"
	"github.com/kevin/vigil/internal/timeline"

	_ "github.com/mattn/go-sqlite3"
)

// --- 测试脚手架 ---

// stubFinder 桩 SimilarFinder：返回预置候选（dedup 建议测试用）。
type stubFinder struct {
	result []*ent.Incident
	err    error
}

func (s *stubFinder) FindSimilar(_ context.Context, _, _ int) ([]*ent.Incident, error) {
	return s.result, s.err
}

func newTriageTestClient(t *testing.T) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:triageai_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// seedIncidentWithEvents 造一个 incident + N 条关联 firing Event（作 severity 建议的 evidence）。
func seedIncidentWithEvents(t *testing.T, c *ent.Client, sev string, nEvents int) *ent.Incident {
	t.Helper()
	ctx := context.Background()
	inc, err := c.Incident.Create().
		SetNumber("INC-T1").SetTitle("磁盘使用率告警").
		SetSeverity(entincident.Severity(sev)).SetStatus("triggered").
		SetPriority("p2").SetSummary("磁盘 95%").SetTriggerType("auto").Save(ctx)
	if err != nil {
		t.Fatalf("create incident: %v", err)
	}
	for i := 0; i < nEvents; i++ {
		_, err := c.Event.Create().
			SetSourceEventID("e" + string(rune('a'+i))).SetSource("prometheus").
			SetSeverity("critical").SetStatus("firing").
			SetSummary("磁盘写满即将宕机").SetDedupKey("dk").
			SetIncidentID(inc.ID).Save(ctx)
		if err != nil {
			t.Fatalf("create event: %v", err)
		}
	}
	return inc
}

// --- severity_adjustment ---

// TestTriageAI_SeveritySuggestion 验证 severity 建议产出：带 evidence、status=suggested、
// content 含 target_severity（accept 走 applied 路径）。
func TestTriageAI_SeveritySuggestion(t *testing.T) {
	c := newTriageTestClient(t)
	inc := seedIncidentWithEvents(t, c, "warning", 2)
	mp := &mockProvider{avail: true,
		resp: `{"target_severity":"critical","confidence":0.85,"reason":"磁盘将写满导致宕机"}`}
	e := NewTriageAIEngine(c, mp)
	e.SetRecorder(timeline.NewRecorder(c))

	res, err := e.AnalyzeIncident(context.Background(), inc.ID)
	if err != nil {
		t.Fatalf("AnalyzeIncident: %v", err)
	}
	if res.Severity == nil {
		t.Fatal("应产出 severity_adjustment 建议")
	}
	ins := res.Severity
	if string(ins.Type) != "severity_adjustment" {
		t.Errorf("type: got %q, want severity_adjustment", ins.Type)
	}
	if string(ins.Stage) != "triage" {
		t.Errorf("stage: got %q, want triage", ins.Stage)
	}
	if string(ins.Status) != "suggested" {
		t.Errorf("status: got %q, want suggested", ins.Status)
	}
	// 基线：每建议必带 evidence
	if len(ins.Evidence) == 0 {
		t.Error("severity 建议必须带 evidence（无 evidence 不应产出）")
	}
	// content 必须含 target_severity，使 accept 能走 T3.1 applyInsight 真改严重度
	if ins.Content["target_severity"] != "critical" {
		t.Errorf("content.target_severity: got %v, want critical", ins.Content["target_severity"])
	}
	// 应写 ai_insight 时间线
	cnt, _ := c.TimelineItem.Query().
		Where(timelineitem.HasIncidentWith(entincident.IDEQ(inc.ID)),
			timelineitem.TypeEQ(timelineitem.TypeAiInsight)).Count(context.Background())
	if cnt == 0 {
		t.Error("产出建议后应写 ai_insight 时间线")
	}
}

// TestTriageAI_SeverityAccept_Applied 验证 accept severity 建议 → 走 T3.1 applied 路径真改严重度。
// 这是 T3.2 产出侧与 T3.1 应用侧的闭环：分诊 AI 产出 target_severity，accept 后真正生效。
func TestTriageAI_SeverityAccept_Applied(t *testing.T) {
	c := newTriageTestClient(t)
	inc := seedIncidentWithEvents(t, c, "warning", 1)
	mp := &mockProvider{avail: true,
		resp: `{"target_severity":"critical","confidence":0.9,"reason":"x"}`}
	e := NewTriageAIEngine(c, mp)

	res, err := e.AnalyzeIncident(context.Background(), inc.ID)
	if err != nil || res.Severity == nil {
		t.Fatalf("produce severity suggestion: err=%v res=%+v", err, res)
	}

	// accept 走 DiagnoseEngine.ResolveInsight（applyInsight 真改严重度 → applied 终态）。
	diag := NewDiagnoseEngine(c, nil)
	got, err := diag.ResolveInsight(context.Background(), res.Severity.ID, 7, true)
	if err != nil {
		t.Fatalf("ResolveInsight: %v", err)
	}
	if string(got.Status) != "applied" {
		t.Errorf("accept severity 应走 applied: got %q", got.Status)
	}
	inc2, _ := c.Incident.Get(context.Background(), inc.ID)
	if string(inc2.Severity) != "critical" {
		t.Errorf("accept 后 incident 严重度应改为 critical: got %q", inc2.Severity)
	}
}

// TestTriageAI_Severity_NoEvidence_NotProduced 验证无关联 Event（无 evidence）时不产出 severity 建议。
func TestTriageAI_Severity_NoEvidence_NotProduced(t *testing.T) {
	c := newTriageTestClient(t)
	ctx := context.Background()
	inc, _ := c.Incident.Create().SetNumber("INC-NOEV").SetTitle("x").
		SetSeverity("warning").SetStatus("triggered").Save(ctx)
	mp := &mockProvider{avail: true,
		resp: `{"target_severity":"critical","confidence":0.9,"reason":"x"}`}
	e := NewTriageAIEngine(c, mp)

	res, err := e.AnalyzeIncident(ctx, inc.ID)
	if err != nil {
		t.Fatalf("AnalyzeIncident: %v", err)
	}
	if res.Severity != nil {
		t.Error("无 evidence（无关联 Event）不应产出 severity 建议")
	}
	// 不应落任何 AIInsight
	cnt, _ := c.AIInsight.Query().Count(ctx)
	if cnt != 0 {
		t.Errorf("无 evidence 不应落 AIInsight, got %d", cnt)
	}
}

// TestTriageAI_Severity_LowConfidence_NotProduced 验证低于置信度门槛（0.6）不产出。
func TestTriageAI_Severity_LowConfidence_NotProduced(t *testing.T) {
	c := newTriageTestClient(t)
	inc := seedIncidentWithEvents(t, c, "warning", 1)
	mp := &mockProvider{avail: true,
		resp: `{"target_severity":"critical","confidence":0.4,"reason":"把握不大"}`}
	e := NewTriageAIEngine(c, mp)

	res, err := e.AnalyzeIncident(context.Background(), inc.ID)
	if err != nil {
		t.Fatalf("AnalyzeIncident: %v", err)
	}
	if res.Severity != nil {
		t.Error("置信度 0.4 < 0.6 门槛，不应产出 severity 建议")
	}
}

// TestTriageAI_Severity_SameSeverity_NotProduced 验证建议目标与当前一致时不产出（无「调整」）。
func TestTriageAI_Severity_SameSeverity_NotProduced(t *testing.T) {
	c := newTriageTestClient(t)
	inc := seedIncidentWithEvents(t, c, "warning", 1)
	mp := &mockProvider{avail: true,
		resp: `{"target_severity":"warning","confidence":0.9,"reason":"合理"}`}
	e := NewTriageAIEngine(c, mp)

	res, err := e.AnalyzeIncident(context.Background(), inc.ID)
	if err != nil {
		t.Fatalf("AnalyzeIncident: %v", err)
	}
	if res.Severity != nil {
		t.Error("目标严重度与当前一致，不应产出建议")
	}
}

// --- dedup_suggestion ---

// TestTriageAI_DedupSuggestion 验证 dedup 建议产出：带 evidence（候选单）、只保留候选集内的 id。
func TestTriageAI_DedupSuggestion(t *testing.T) {
	c := newTriageTestClient(t)
	ctx := context.Background()
	inc := seedIncidentWithEvents(t, c, "critical", 1)
	// 候选活跃单（可合并）
	cand, _ := c.Incident.Create().SetNumber("INC-C2").SetTitle("磁盘告警(另一实例)").
		SetSeverity("critical").SetStatus("triggered").SetSummary("同集群磁盘满").
		SetTriggerType("auto").Save(ctx)

	mp := &mockProvider{avail: true,
		resp: `{"should_merge":true,"merge_ids":[` + itoa(cand.ID) + `],"confidence":0.8,"reason":"同集群磁盘满"}`}
	e := NewTriageAIEngine(c, mp)
	e.SetSimilarFinder(&stubFinder{result: []*ent.Incident{cand}})
	e.SetRecorder(timeline.NewRecorder(c))

	res, err := e.AnalyzeIncident(ctx, inc.ID)
	if err != nil {
		t.Fatalf("AnalyzeIncident: %v", err)
	}
	if res.Dedup == nil {
		t.Fatal("应产出 dedup_suggestion 建议")
	}
	ins := res.Dedup
	if string(ins.Type) != "dedup_suggestion" {
		t.Errorf("type: got %q, want dedup_suggestion", ins.Type)
	}
	if len(ins.Evidence) == 0 {
		t.Error("dedup 建议必须带 evidence（候选单）")
	}
	// evidence 应含候选 incident_id（in-memory 实体为 int；经 JSON round-trip 会变 float64，两者都容忍）。
	found := false
	for _, ev := range ins.Evidence {
		if evidenceIntEq(ev["incident_id"], cand.ID) {
			found = true
		}
	}
	if !found {
		t.Errorf("evidence 应含候选 incident_id=%d, got %v", cand.ID, ins.Evidence)
	}
}

// TestTriageAI_Dedup_HallucinatedID_Filtered 验证 LLM 返回不存在于候选集的 id 被过滤（防幻觉），
// 过滤后无有效 merge id → 不产出。
func TestTriageAI_Dedup_HallucinatedID_Filtered(t *testing.T) {
	c := newTriageTestClient(t)
	ctx := context.Background()
	inc := seedIncidentWithEvents(t, c, "critical", 1)
	cand, _ := c.Incident.Create().SetNumber("INC-C3").SetTitle("y").
		SetSeverity("critical").SetStatus("triggered").SetSummary("y").
		SetTriggerType("auto").Save(ctx)

	// LLM 编造一个不在候选集里的 id 99999
	mp := &mockProvider{avail: true,
		resp: `{"should_merge":true,"merge_ids":[99999],"confidence":0.9,"reason":"x"}`}
	e := NewTriageAIEngine(c, mp)
	e.SetSimilarFinder(&stubFinder{result: []*ent.Incident{cand}})

	res, err := e.AnalyzeIncident(ctx, inc.ID)
	if err != nil {
		t.Fatalf("AnalyzeIncident: %v", err)
	}
	if res.Dedup != nil {
		t.Error("幻觉 id 被过滤后无有效候选，不应产出 dedup 建议")
	}
}

// TestTriageAI_Dedup_NoCandidates_NotProduced 验证无相似候选时不产出 dedup 建议。
func TestTriageAI_Dedup_NoCandidates_NotProduced(t *testing.T) {
	c := newTriageTestClient(t)
	inc := seedIncidentWithEvents(t, c, "critical", 1)
	mp := &mockProvider{avail: true, resp: `{"should_merge":true,"merge_ids":[1],"confidence":0.9}`}
	e := NewTriageAIEngine(c, mp)
	e.SetSimilarFinder(&stubFinder{result: nil}) // 无候选

	res, err := e.AnalyzeIncident(context.Background(), inc.ID)
	if err != nil {
		t.Fatalf("AnalyzeIncident: %v", err)
	}
	if res.Dedup != nil {
		t.Error("无候选时不应产出 dedup 建议")
	}
}

// TestTriageAI_Dedup_ShouldNotMerge_NotProduced 验证 LLM 判断不合并时不产出。
func TestTriageAI_Dedup_ShouldNotMerge_NotProduced(t *testing.T) {
	c := newTriageTestClient(t)
	ctx := context.Background()
	inc := seedIncidentWithEvents(t, c, "critical", 1)
	cand, _ := c.Incident.Create().SetNumber("INC-C4").SetTitle("无关单").
		SetSeverity("critical").SetStatus("triggered").SetSummary("无关").
		SetTriggerType("auto").Save(ctx)
	mp := &mockProvider{avail: true,
		resp: `{"should_merge":false,"merge_ids":[],"confidence":0.9,"reason":"无关"}`}
	e := NewTriageAIEngine(c, mp)
	e.SetSimilarFinder(&stubFinder{result: []*ent.Incident{cand}})

	res, err := e.AnalyzeIncident(ctx, inc.ID)
	if err != nil {
		t.Fatalf("AnalyzeIncident: %v", err)
	}
	if res.Dedup != nil {
		t.Error("LLM 判断不合并时不应产出 dedup 建议")
	}
}

// --- 降级 ---

// TestTriageAI_NoProvider_Degrades 验证 LLM 不可用时降级：不产出、不报错（主流程不阻断）。
func TestTriageAI_NoProvider_Degrades(t *testing.T) {
	c := newTriageTestClient(t)
	inc := seedIncidentWithEvents(t, c, "warning", 1)
	e := NewTriageAIEngine(c, nil) // 无 provider

	res, err := e.AnalyzeIncident(context.Background(), inc.ID)
	if err != nil {
		t.Fatalf("无 provider 应降级不报错: %v", err)
	}
	if res.Severity != nil || res.Dedup != nil {
		t.Error("无 provider 时不应产出任何建议（降级）")
	}
	cnt, _ := c.AIInsight.Query().Count(context.Background())
	if cnt != 0 {
		t.Errorf("降级时不应落 AIInsight, got %d", cnt)
	}
}

// TestTriageAI_ProviderUnavailable_Degrades 验证 provider 存在但 Available()=false 时降级。
func TestTriageAI_ProviderUnavailable_Degrades(t *testing.T) {
	c := newTriageTestClient(t)
	inc := seedIncidentWithEvents(t, c, "warning", 1)
	e := NewTriageAIEngine(c, &mockProvider{avail: false})

	res, err := e.AnalyzeIncident(context.Background(), inc.ID)
	if err != nil {
		t.Fatalf("provider 不可用应降级: %v", err)
	}
	if res.Severity != nil || res.Dedup != nil {
		t.Error("provider 不可用时不应产出建议")
	}
}

// TestTriageAI_LLMCallFailed_Degrades 验证 LLM 调用失败（401/超时）时降级：不产出、不报错、不落库。
func TestTriageAI_LLMCallFailed_Degrades(t *testing.T) {
	c := newTriageTestClient(t)
	inc := seedIncidentWithEvents(t, c, "warning", 1)
	mp := &mockProvider{avail: true, err: errors.New("glm http 401")}
	e := NewTriageAIEngine(c, mp)
	e.SetSimilarFinder(&stubFinder{result: nil})

	res, err := e.AnalyzeIncident(context.Background(), inc.ID)
	if err != nil {
		t.Fatalf("LLM 调用失败应降级不报错: %v", err)
	}
	if res.Severity != nil {
		t.Error("LLM 调用失败不应产出 severity 建议")
	}
	cnt, _ := c.AIInsight.Query().Count(context.Background())
	if cnt != 0 {
		t.Errorf("LLM 调用失败降级不应落 AIInsight, got %d", cnt)
	}
}

// TestTriageAI_MissingIncident 验证不存在的 incident id 归一为 error（手动端点据此返 404）。
func TestTriageAI_MissingIncident(t *testing.T) {
	c := newTriageTestClient(t)
	e := NewTriageAIEngine(c, &mockProvider{avail: true, resp: "{}"})
	if _, err := e.AnalyzeIncident(context.Background(), 999999); err == nil {
		t.Fatal("不存在的 incident 应返回 error, got nil")
	}
}

// --- 解析/守卫单元 ---

// TestParseSeverityOutput_NonJSON 验证非 JSON 输出解析失败返回空 target（不产出）。
func TestParseSeverityOutput_NonJSON(t *testing.T) {
	target, _, _ := parseSeverityOutput("我觉得应该调高")
	if target != "" {
		t.Errorf("非 JSON 应返回空 target, got %q", target)
	}
}

// TestParseSeverityOutput_ConfCapped 验证置信度上限 1.0。
func TestParseSeverityOutput_ConfCapped(t *testing.T) {
	_, conf, _ := parseSeverityOutput(`{"target_severity":"critical","confidence":1.7}`)
	if conf != 1.0 {
		t.Errorf("置信度应被限制为 1.0, got %f", conf)
	}
}

// TestIsValidSeverity 验证严重度枚举校验。
func TestIsValidSeverity(t *testing.T) {
	for _, s := range []string{"critical", "warning", "info"} {
		if !isValidSeverity(s) {
			t.Errorf("%q 应为合法严重度", s)
		}
	}
	for _, s := range []string{"", "bogus", "CRITICAL"} {
		if isValidSeverity(s) {
			t.Errorf("%q 不应为合法严重度", s)
		}
	}
}

// TestSetConfidenceThreshold 验证门槛可覆盖，<=0 保留默认。
func TestSetConfidenceThreshold(t *testing.T) {
	e := NewTriageAIEngine(nil, nil)
	if e.confidenceThreshold != defaultConfidenceThreshold {
		t.Errorf("默认门槛应为 %f, got %f", defaultConfidenceThreshold, e.confidenceThreshold)
	}
	e.SetConfidenceThreshold(0.8)
	if e.confidenceThreshold != 0.8 {
		t.Errorf("覆盖后门槛应为 0.8, got %f", e.confidenceThreshold)
	}
	e.SetConfidenceThreshold(0) // <=0 保留
	if e.confidenceThreshold != 0.8 {
		t.Errorf("<=0 应保留门槛 0.8, got %f", e.confidenceThreshold)
	}
}

// evidenceIntEq 容忍 evidence 里的整数值以 int（in-memory）或 float64（JSON round-trip）出现。
func evidenceIntEq(v any, want int) bool {
	switch x := v.(type) {
	case int:
		return x == want
	case int64:
		return int(x) == want
	case float64:
		return int(x) == want
	default:
		return false
	}
}

// helper：避免引入 strconv 到测试顶层（保持 import 精简）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// 静态断言：*TriageAIEngine 用 aiinsight 枚举常量，防止枚举漂移编译期即暴露。
var _ = aiinsight.TypeSeverityAdjustment
var _ = aiinsight.TypeDedupSuggestion
