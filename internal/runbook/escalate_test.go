package runbook

import (
	"context"
	"strings"
	"testing"

	"github.com/kevin/vigil/ent/schema"
)

// stubEscalator 测试用 EscalationTrigger，记录调用。
type stubEscalator struct {
	called  bool
	incID   int
	reason  string
	actorID int
}

func (s *stubEscalator) Trigger(ctx context.Context, incID int, reason string, actorID int) error {
	s.called = true
	s.incID = incID
	s.reason = reason
	s.actorID = actorID
	return nil
}

// TestOnFailureEscalate_TriggersEscalator on_failure=escalate 时调用 escalator。
func TestOnFailureEscalate_TriggersEscalator(t *testing.T) {
	esc := &stubEscalator{}
	e := &Engine{escalator: esc}
	e.registry = NewRegistry()
	steps := []schema.RunbookStep{{
		Name:      "probe",
		Action:    schema.StepAction{Type: "execute", Target: schema.StepTarget{Kind: "http", Endpoint: "http://127.0.0.1:1/x"}},
		OnFailure: "escalate",
	}}

	res := e.executeSteps(context.Background(), 42, steps, true, 5, &ExecuteResult{})
	if !res.Aborted {
		t.Error("expected aborted=true on escalate")
	}
	if !esc.called {
		t.Error("escalator not triggered on on_failure=escalate")
	}
	if esc.incID != 42 {
		t.Errorf("escalator incID=%d, want 42", esc.incID)
	}
	if !strings.Contains(esc.reason, "probe") {
		t.Errorf("escalator reason missing step name: %q", esc.reason)
	}
}

// TestOnFailureEscalate_NoEscalator 无 escalator 时仅中止，不 panic。
func TestOnFailureEscalate_NoEscalator(t *testing.T) {
	e := &Engine{}
	e.registry = NewRegistry()
	steps := []schema.RunbookStep{{
		Name:      "probe",
		Action:    schema.StepAction{Type: "execute", Target: schema.StepTarget{Kind: "http", Endpoint: "http://127.0.0.1:1/x"}},
		OnFailure: "escalate",
	}}

	res := e.executeSteps(context.Background(), 1, steps, true, 0, &ExecuteResult{})
	if !res.Aborted {
		t.Error("expected aborted=true even without escalator")
	}
}

// TestOnFailureAbort_NoEscalate on_failure=abort 不触发 escalator。
func TestOnFailureAbort_NoEscalate(t *testing.T) {
	esc := &stubEscalator{}
	e := &Engine{escalator: esc}
	e.registry = NewRegistry()
	steps := []schema.RunbookStep{{
		Name:      "probe",
		Action:    schema.StepAction{Type: "execute", Target: schema.StepTarget{Kind: "http", Endpoint: "http://127.0.0.1:1/x"}},
		OnFailure: "abort",
	}}

	res := e.executeSteps(context.Background(), 1, steps, true, 0, &ExecuteResult{})
	if !res.Aborted {
		t.Error("expected aborted=true on abort")
	}
	if esc.called {
		t.Error("escalator should not be called on on_failure=abort")
	}
}
