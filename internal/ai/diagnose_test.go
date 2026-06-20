package ai

import (
	"context"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/timelineitem"

	_ "github.com/mattn/go-sqlite3"
)

func newDiagTestClient(t *testing.T) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:diag_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func seedIncidentForDiag(t *testing.T, c *ent.Client) *ent.Incident {
	t.Helper()
	ctx := context.Background()
	inc, err := c.Incident.Create().
		SetNumber("INC-D1").SetTitle("支付5xx错误").
		SetSeverity("critical").SetStatus("triggered").
		SetPriority("p1").SetSummary("DB连接池耗尽").
		SetTriggerType("auto").Save(ctx)
	if err != nil {
		t.Fatalf("create incident: %v", err)
	}
	// 时间线
	for _, it := range []struct {
		typ     timelineitem.Type
		content string
	}{
		{timelineitem.TypeIncidentCreated, "事件创建"},
		{timelineitem.TypeEscalated, "升级 level 1"},
		{timelineitem.TypeAck, "DBA 张三接手"},
	} {
		_, _ = c.TimelineItem.Create().
			SetIncidentID(inc.ID).SetType(it.typ).
			SetContent(it.content).SetSource(timelineitem.SourceSystem).
			SetActor(map[string]string{"kind": "system"}).
			Save(ctx)
	}
	return inc
}

// TestDiagnose_NoProvider 验证无 LLM 时降级（返回 nil，不报错）。
func TestDiagnose_NoProvider(t *testing.T) {
	c := newDiagTestClient(t)
	e := NewDiagnoseEngine(c, nil) // 无 provider
	res, err := e.Diagnose(context.Background(), 1)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if res != nil {
		t.Error("无 provider 时应返回 nil（降级）")
	}
}

// TestDiagnose_WithMockProvider 验证诊断落 AIInsight。
func TestDiagnose_WithMockProvider(t *testing.T) {
	c := newDiagTestClient(t)
	inc := seedIncidentForDiag(t, c)
	mp := &mockProvider{
		resp:  `{"root_cause":"可能因DB连接池配置过小","confidence":0.8}`,
		avail: true,
	}
	e := NewDiagnoseEngine(c, mp)

	res, err := e.Diagnose(context.Background(), inc.ID)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if res == nil {
		t.Fatal("应返回诊断结果")
	}
	if res.RootCause != "可能因DB连接池配置过小" {
		t.Errorf("root_cause: got %q", res.RootCause)
	}
	if res.Confidence != 0.8 {
		t.Errorf("confidence: got %f", res.Confidence)
	}
	// AIInsight 应入库，status=suggested
	ins, _ := c.AIInsight.Get(context.Background(), res.InsightID)
	if string(ins.Status) != "suggested" {
		t.Errorf("status: got %q, want suggested", ins.Status)
	}
	if string(ins.Stage) != "diagnose" {
		t.Errorf("stage: got %q", ins.Stage)
	}
}

// TestResolveInsight 验证 accept/reject。
func TestResolveInsight(t *testing.T) {
	c := newDiagTestClient(t)
	inc := seedIncidentForDiag(t, c)
	mp := &mockProvider{resp: `{"root_cause":"x","confidence":0.5}`, avail: true}
	e := NewDiagnoseEngine(c, mp)

	res, _ := e.Diagnose(context.Background(), inc.ID)

	// accept
	if err := e.ResolveInsight(context.Background(), res.InsightID, true); err != nil {
		t.Fatalf("accept: %v", err)
	}
	ins, _ := c.AIInsight.Get(context.Background(), res.InsightID)
	if string(ins.Status) != "accepted" {
		t.Errorf("after accept: got %q, want accepted", ins.Status)
	}
	// reject
	_ = e.ResolveInsight(context.Background(), res.InsightID, false)
	ins, _ = c.AIInsight.Get(context.Background(), res.InsightID)
	if string(ins.Status) != "rejected" {
		t.Errorf("after reject: got %q, want rejected", ins.Status)
	}
}

// TestParseDiagnoseOutput_JSON 验证 JSON 输出解析。
func TestParseDiagnoseOutput_JSON(t *testing.T) {
	rc, conf := parseDiagnoseOutput(`{"root_cause":"DB问题","confidence":0.85}`)
	if rc != "DB问题" || conf != 0.85 {
		t.Errorf("got rc=%q conf=%f", rc, conf)
	}
}

// TestParseDiagnoseOutput_PlainText 验证非 JSON 降级为纯文本。
func TestParseDiagnoseOutput_PlainText(t *testing.T) {
	rc, conf := parseDiagnoseOutput("可能是 DB 连接问题")
	if rc != "可能是 DB 连接问题" {
		t.Errorf("rc: got %q", rc)
	}
	if conf != 0.3 {
		t.Errorf("降级置信度: got %f, want 0.3", conf)
	}
}

// TestParseDiagnoseOutput_ConfidenceCapped 验证置信度上限 1.0。
func TestParseDiagnoseOutput_ConfidenceCapped(t *testing.T) {
	_, conf := parseDiagnoseOutput(`{"root_cause":"x","confidence":1.5}`)
	if conf != 1.0 {
		t.Errorf("置信度应被限制为 1.0: got %f", conf)
	}
}

// TestExtractKeyword 验证关键词提取。
func TestExtractKeyword(t *testing.T) {
	cases := map[string]string{
		"支付5xx错误":      "支付", // 中文取前 2 字
		"DB connection": "DB",  // 英文取首词保留大小写
		"[critical] 支付": "支付", // 去 severity 前缀后取前 2 字
	}
	for in, want := range cases {
		if got := extractKeyword(in); got != want {
			t.Errorf("extractKeyword(%q): got %q, want %q", in, got, want)
		}
	}
}

// TestBuildDiagnosePrompt 验证 prompt 含不确定性要求 + JSON 格式要求。
func TestBuildDiagnosePrompt(t *testing.T) {
	inc := &ent.Incident{Title: "测试", Severity: "critical", Summary: "概要"}
	prompt := buildDiagnosePrompt(inc, nil)
	if !contains(prompt, "可能") && !contains(prompt, "不确定性") {
		t.Error("prompt 应要求不确定性措辞")
	}
	if !contains(prompt, "JSON") {
		t.Error("prompt 应要求 JSON 输出格式")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
