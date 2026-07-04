package triage

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kevin/vigil/ent/event"
)

// fakeAIAnalyzer 记录 AnalyzeIncident 调用（T3.2 触发点断言）。
// 触发是异步 goroutine，用 channel 通知，测试侧带超时等待避免竞态/挂死。
type fakeAIAnalyzer struct {
	called  chan int // 每次调用把 incID 送入
	count   int32
	failErr error // 非 nil 时返回错误（验证失败不阻断主流程）
}

func newFakeAIAnalyzer() *fakeAIAnalyzer {
	return &fakeAIAnalyzer{called: make(chan int, 8)}
}

func (f *fakeAIAnalyzer) AnalyzeIncident(_ context.Context, incID int) error {
	atomic.AddInt32(&f.count, 1)
	f.called <- incID
	return f.failErr
}

// waitCalled 等待一次调用（带超时），返回 incID 与是否触发。
func (f *fakeAIAnalyzer) waitCalled(t *testing.T) (int, bool) {
	t.Helper()
	select {
	case id := <-f.called:
		return id, true
	case <-time.After(2 * time.Second):
		return 0, false
	}
}

// TestEngine_AITriggeredOnIncidentCreated 验证：新建 Incident 后异步触发分诊 AI 分析（T3.2）。
func TestEngine_AITriggeredOnIncidentCreated(t *testing.T) {
	c := newTestClient(t)
	seedServiceAndTeam(t, c)
	eng := NewEngine(c, nil)
	fa := newFakeAIAnalyzer()
	eng.SetAIAnalyzer(fa)

	evt := createEvent(t, c, event.SeverityWarning, "ai-k1")
	res, err := eng.Process(context.Background(), evt.ID)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.Action != ActionIncidentCreated {
		t.Fatalf("Action: got %q, want incident_created", res.Action)
	}
	// 异步触发：等待一次调用，且 incID 应为新建单。
	gotID, ok := fa.waitCalled(t)
	if !ok {
		t.Fatal("新建 Incident 后应异步触发分诊 AI 分析（未在超时内收到调用）")
	}
	if gotID != res.IncidentID {
		t.Errorf("AI 分析 incID: got %d, want %d", gotID, res.IncidentID)
	}
}

// TestEngine_AINotTriggeredOnAggregate 验证：聚合并入既有单（非新建）不触发分诊 AI
// （分诊 AI 只在建单时看一次，避免每条聚合告警都重复调 LLM 浪费成本）。
func TestEngine_AINotTriggeredOnAggregate(t *testing.T) {
	c := newTestClient(t)
	seedServiceAndTeam(t, c)
	eng := NewEngine(c, nil)
	fa := newFakeAIAnalyzer()
	eng.SetAIAnalyzer(fa)

	// 第一条建单（触发一次）
	e1 := createEvent(t, c, event.SeverityWarning, "agg-1")
	if _, err := eng.Process(context.Background(), e1.ID); err != nil {
		t.Fatalf("Process1: %v", err)
	}
	if _, ok := fa.waitCalled(t); !ok {
		t.Fatal("首条建单应触发 AI 分析")
	}
	// 第二条同 service+severity 在窗口内 → 聚合并入（不应再触发）
	e2 := createEvent(t, c, event.SeverityWarning, "agg-2")
	res2, err := eng.Process(context.Background(), e2.ID)
	if err != nil {
		t.Fatalf("Process2: %v", err)
	}
	if res2.Action != ActionAggregated {
		t.Fatalf("second event Action: got %q, want aggregated", res2.Action)
	}
	// 给异步 goroutine 一点时间——若错误触发会在此期间递增 count。
	time.Sleep(200 * time.Millisecond)
	if atomic.LoadInt32(&fa.count) != 1 {
		t.Errorf("聚合不应再触发 AI 分析：总调用数 got %d, want 1", atomic.LoadInt32(&fa.count))
	}
}

// TestEngine_AIAnalyzerFailure_DoesNotBlock 验证：AI 分析失败（返回 error）不影响建单主流程结果。
func TestEngine_AIAnalyzerFailure_DoesNotBlock(t *testing.T) {
	c := newTestClient(t)
	seedServiceAndTeam(t, c)
	eng := NewEngine(c, nil)
	fa := newFakeAIAnalyzer()
	fa.failErr = errors.New("llm boom")
	eng.SetAIAnalyzer(fa)

	evt := createEvent(t, c, event.SeverityWarning, "ai-fail")
	res, err := eng.Process(context.Background(), evt.ID)
	// 主流程必须成功建单（AI 失败只记日志，不上抛）。
	if err != nil {
		t.Fatalf("AI 分析失败不应阻断建单: %v", err)
	}
	if res.Action != ActionIncidentCreated {
		t.Fatalf("Action: got %q, want incident_created", res.Action)
	}
	if _, ok := fa.waitCalled(t); !ok {
		t.Fatal("应触发 AI 分析（即使内部失败）")
	}
}

// TestEngine_NoAIAnalyzer_NoPanic 验证：未注入分析器时建单正常（nil 安全）。
func TestEngine_NoAIAnalyzer_NoPanic(t *testing.T) {
	c := newTestClient(t)
	seedServiceAndTeam(t, c)
	eng := NewEngine(c, nil) // 不注入 AIAnalyzer

	evt := createEvent(t, c, event.SeverityWarning, "no-ai")
	res, err := eng.Process(context.Background(), evt.ID)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.Action != ActionIncidentCreated {
		t.Fatalf("Action: got %q, want incident_created", res.Action)
	}
}
