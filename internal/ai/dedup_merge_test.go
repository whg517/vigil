package ai

import (
	"context"
	"errors"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/aiinsight"
)

// stubMerger 记录 Merge 调用参数（AI dedup accept 联动测试用）。
type stubMerger struct {
	called    bool
	targetID  int
	sourceIDs []int
	err       error
}

func (s *stubMerger) Merge(_ context.Context, targetID int, sourceIDs []int, _ int) error {
	s.called = true
	s.targetID = targetID
	s.sourceIDs = sourceIDs
	return s.err
}

// seedDedupInsight 造一个 incident + 一条 dedup_suggestion（suggested）AIInsight。
func seedDedupInsight(t *testing.T, c *ent.Client, mergeIDs []int) (*ent.Incident, *ent.AIInsight) {
	t.Helper()
	ctx := context.Background()
	inc, err := c.Incident.Create().SetNumber("INC-D1").SetTitle("主单").
		SetSeverity("critical").SetStatus("triggered").SetSummary("s").
		SetTriggerType("auto").Save(ctx)
	if err != nil {
		t.Fatalf("create incident: %v", err)
	}
	// merge_candidate_ids 用 []any（模拟 JSON round-trip 后的形态：数值为 float64）。
	cand := make([]any, len(mergeIDs))
	for i, id := range mergeIDs {
		cand[i] = float64(id)
	}
	ins, err := c.AIInsight.Create().
		SetIncidentID(inc.ID).
		SetStage(aiinsight.StageTriage).
		SetType(aiinsight.TypeDedupSuggestion).
		SetContent(map[string]any{"merge_candidate_ids": cand, "reason": "同根因"}).
		SetConfidence(0.9).
		SetEvidence([]map[string]any{{"kind": "incident", "incident_id": mergeIDs[0]}}).
		SetStatus(aiinsight.StatusSuggested).
		Save(ctx)
	if err != nil {
		t.Fatalf("create dedup insight: %v", err)
	}
	return inc, ins
}

// TestResolveDedup_Accept_TriggersMerge_Applied 验证 accept dedup_suggestion → 调用 merger 真合并 → applied。
func TestResolveDedup_Accept_TriggersMerge_Applied(t *testing.T) {
	c := newTriageTestClient(t)
	ctx := context.Background()
	inc, ins := seedDedupInsight(t, c, []int{101, 102})

	merger := &stubMerger{}
	diag := NewDiagnoseEngine(c, nil)
	diag.SetIncidentMerger(merger)

	got, err := diag.ResolveInsight(ctx, ins.ID, 7, true)
	if err != nil {
		t.Fatalf("ResolveInsight: %v", err)
	}
	// 应真正调用合并：target=主单 id，sources=候选 id。
	if !merger.called {
		t.Fatal("accept dedup_suggestion 应触发 merger.Merge")
	}
	if merger.targetID != inc.ID {
		t.Errorf("merge target: got %d, want %d", merger.targetID, inc.ID)
	}
	if len(merger.sourceIDs) != 2 || merger.sourceIDs[0] != 101 || merger.sourceIDs[1] != 102 {
		t.Errorf("merge sources: got %v, want [101 102]", merger.sourceIDs)
	}
	// 合并成功 → 终态 applied。
	if string(got.Status) != "applied" {
		t.Errorf("accept dedup 合并成功应为 applied: got %q", got.Status)
	}
}

// TestResolveDedup_Accept_MergeFails_KeepsAccepted 验证合并失败时不谎报 applied，保持 accepted。
func TestResolveDedup_Accept_MergeFails_KeepsAccepted(t *testing.T) {
	c := newTriageTestClient(t)
	ctx := context.Background()
	_, ins := seedDedupInsight(t, c, []int{201})

	merger := &stubMerger{err: errors.New("source already merged")}
	diag := NewDiagnoseEngine(c, nil)
	diag.SetIncidentMerger(merger)

	got, err := diag.ResolveInsight(ctx, ins.ID, 7, true)
	if err != nil {
		t.Fatalf("ResolveInsight: %v", err)
	}
	if !merger.called {
		t.Fatal("应尝试调用 merger.Merge")
	}
	if string(got.Status) != "accepted" {
		t.Errorf("合并失败应保持 accepted（不谎报 applied）: got %q", got.Status)
	}
}

// TestResolveDedup_Accept_NoMerger_KeepsAccepted 验证未注入 merger 时降级为仅 accepted（不合并）。
func TestResolveDedup_Accept_NoMerger_KeepsAccepted(t *testing.T) {
	c := newTriageTestClient(t)
	ctx := context.Background()
	_, ins := seedDedupInsight(t, c, []int{301})

	diag := NewDiagnoseEngine(c, nil) // 未注入 merger

	got, err := diag.ResolveInsight(ctx, ins.ID, 7, true)
	if err != nil {
		t.Fatalf("ResolveInsight: %v", err)
	}
	if string(got.Status) != "accepted" {
		t.Errorf("无 merger 时 accept dedup 应为 accepted: got %q", got.Status)
	}
}

// TestResolveDedup_Reject_NoMerge 验证 reject dedup_suggestion 不触发合并，终态 rejected。
func TestResolveDedup_Reject_NoMerge(t *testing.T) {
	c := newTriageTestClient(t)
	ctx := context.Background()
	_, ins := seedDedupInsight(t, c, []int{401})

	merger := &stubMerger{}
	diag := NewDiagnoseEngine(c, nil)
	diag.SetIncidentMerger(merger)

	got, err := diag.ResolveInsight(ctx, ins.ID, 7, false)
	if err != nil {
		t.Fatalf("ResolveInsight: %v", err)
	}
	if merger.called {
		t.Error("reject 不应触发合并")
	}
	if string(got.Status) != "rejected" {
		t.Errorf("reject 终态应为 rejected: got %q", got.Status)
	}
}

// TestParseMergeCandidateIDs 验证兼容 float64（JSON）与 int（同进程），过滤非数值/<=0。
func TestParseMergeCandidateIDs(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want []int
	}{
		{"float64 slice", []any{float64(1), float64(2)}, []int{1, 2}},
		{"int slice", []int{3, 4}, []int{3, 4}},
		{"mixed with garbage", []any{float64(5), "x", float64(0), float64(-1), float64(6)}, []int{5, 6}},
		{"non-slice", "notaslice", nil},
		{"empty", []any{}, []int{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseMergeCandidateIDs(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("len: got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("idx %d: got %d, want %d", i, got[i], tc.want[i])
				}
			}
		})
	}
}
