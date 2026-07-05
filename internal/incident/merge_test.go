package incident

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/incidentaction"
	"github.com/kevin/vigil/ent/timelineitem"
	"github.com/kevin/vigil/internal/auth"
	"github.com/kevin/vigil/internal/event"
	"github.com/kevin/vigil/internal/timeline"

	"github.com/labstack/echo/v5"
)

// seedNamedIncident 建一个指定编号/状态的 Incident（复用 seedIncident 的 team 需另建，故直接建 team）。
func seedNamedIncident(t *testing.T, c *ent.Client, number string, status incident.Status) *ent.Incident {
	t.Helper()
	ctx := context.Background()
	team, err := c.Team.Create().SetName("t-" + number).SetSlug("s-" + number).Save(ctx)
	if err != nil {
		t.Fatalf("create team: %v", err)
	}
	inc, err := c.Incident.Create().
		SetNumber(number).
		SetTitle("事件 " + number).
		SetSeverity(incident.SeverityWarning).
		SetStatus(status).
		SetTeamID(team.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create incident %s: %v", number, err)
	}
	return inc
}

// TestMerge_Success 合并成功：源单 merged_into 指向主单 + closed，events/responders 转移，
// 双写时间线，主单落 merge 审计。
func TestMerge_Success(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()
	rec := timeline.NewRecorder(c)
	bus := event.New()
	svc := NewService(c, rec, bus)

	// 订阅 IncidentMerged，断言对源单发布了合并事件。
	var mergedIDs []int
	bus.Subscribe(event.IncidentMerged, func(_ context.Context, e event.Event) error {
		if e.Incident != nil {
			mergedIDs = append(mergedIDs, e.Incident.ID)
		}
		return nil
	})

	target := seedNamedIncident(t, c, "INC-T", incident.StatusTriggered)
	src := seedNamedIncident(t, c, "INC-S", incident.StatusTriggered)

	// 源单挂一个 Event + 一个 responder。
	ev, err := c.Event.Create().
		SetSourceEventID("e1").SetSource("prom").SetSeverity("warning").
		SetStatus("firing").SetSummary("x").SetDedupKey("dk1").
		SetIncidentID(src.ID).Save(ctx)
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	u, _ := c.User.Create().SetUsername("resp").SetEmail("r@x.com").Save(ctx)
	if err := c.Incident.UpdateOneID(src.ID).AddResponderIDs(u.ID).Exec(ctx); err != nil {
		t.Fatalf("add responder to src: %v", err)
	}

	op, _ := c.User.Create().SetUsername("op").SetEmail("op@x.com").Save(ctx)
	merged, err := svc.Merge(ctx, target.ID, []int{src.ID}, op.ID, SourceWeb)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}

	// 主单返回快照应含并入的 responder。
	respIDs, _ := merged.QueryResponders().IDs(ctx)
	if len(respIDs) != 1 || respIDs[0] != u.ID {
		t.Errorf("target responders: got %v, want [%d]", respIDs, u.ID)
	}

	// 源单：merged_into 指向主单，status=closed，closed_at 已设。
	srcAfter, _ := c.Incident.Get(ctx, src.ID)
	if srcAfter.MergedInto != itoa(target.ID) {
		t.Errorf("source merged_into: got %q, want %q", srcAfter.MergedInto, itoa(target.ID))
	}
	if srcAfter.Status != incident.StatusClosed {
		t.Errorf("source status: got %s, want closed", srcAfter.Status)
	}
	if srcAfter.ClosedAt == nil {
		t.Error("source closed_at should be set")
	}

	// Event 转移到主单。
	evAfter, _ := c.Event.Get(ctx, ev.ID)
	incOfEvent, _ := evAfter.QueryIncident().Only(ctx)
	if incOfEvent == nil || incOfEvent.ID != target.ID {
		t.Errorf("event incident: got %v, want target %d", incOfEvent, target.ID)
	}

	// IncidentMerged 事件对源单发布。
	if len(mergedIDs) != 1 || mergedIDs[0] != src.ID {
		t.Errorf("IncidentMerged published for: got %v, want [%d]", mergedIDs, src.ID)
	}

	// 时间线：主单 merged（role=target）+ 源单 merged（role=source）。
	tItems, _ := c.TimelineItem.Query().
		Where(timelineitem.HasIncidentWith(incident.IDEQ(target.ID)),
			timelineitem.TypeEQ(timelineitem.TypeMerged)).All(ctx)
	if len(tItems) != 1 {
		t.Errorf("target merged timeline items: got %d, want 1", len(tItems))
	}
	sItems, _ := c.TimelineItem.Query().
		Where(timelineitem.HasIncidentWith(incident.IDEQ(src.ID)),
			timelineitem.TypeEQ(timelineitem.TypeMerged)).All(ctx)
	if len(sItems) != 1 {
		t.Errorf("source merged timeline items: got %d, want 1", len(sItems))
	}

	// 主单落 IncidentAction merge 审计。
	acts, _ := c.IncidentAction.Query().
		Where(incidentaction.HasIncidentWith(incident.IDEQ(target.ID)),
			incidentaction.TypeEQ(incidentaction.TypeMerge)).All(ctx)
	if len(acts) != 1 {
		t.Fatalf("target merge actions: got %d, want 1", len(acts))
	}
	if acts[0].Via != incidentaction.ViaWeb {
		t.Errorf("merge action via: got %s, want web", acts[0].Via)
	}
}

// TestMerge_IntoSelf 合并进自己应拒绝。
func TestMerge_IntoSelf(t *testing.T) {
	c := newClient(t)
	svc := NewService(c, timeline.NewRecorder(c), event.New())
	target := seedNamedIncident(t, c, "INC-1", incident.StatusTriggered)
	_, err := svc.Merge(context.Background(), target.ID, []int{target.ID}, 0, SourceWeb)
	if !errors.Is(err, ErrMergeIntoSelf) {
		t.Fatalf("merge into self: got %v, want ErrMergeIntoSelf", err)
	}
}

// TestMerge_NoSources 源为空（去自身后为空）应拒绝。
func TestMerge_NoSources(t *testing.T) {
	c := newClient(t)
	svc := NewService(c, timeline.NewRecorder(c), event.New())
	target := seedNamedIncident(t, c, "INC-1", incident.StatusTriggered)
	_, err := svc.Merge(context.Background(), target.ID, []int{}, 0, SourceWeb)
	if !errors.Is(err, ErrMergeNoSources) {
		t.Fatalf("merge no sources: got %v, want ErrMergeNoSources", err)
	}
}

// TestMerge_TargetTerminal 目标已 closed 应拒绝。
func TestMerge_TargetTerminal(t *testing.T) {
	c := newClient(t)
	svc := NewService(c, timeline.NewRecorder(c), event.New())
	target := seedNamedIncident(t, c, "INC-T", incident.StatusClosed)
	src := seedNamedIncident(t, c, "INC-S", incident.StatusTriggered)
	_, err := svc.Merge(context.Background(), target.ID, []int{src.ID}, 0, SourceWeb)
	if !errors.Is(err, ErrMergeTargetTerminal) {
		t.Fatalf("merge into terminal target: got %v, want ErrMergeTargetTerminal", err)
	}
}

// TestMerge_SourceTerminal 源单已 closed 应拒绝，且整体不写（校验先行）。
func TestMerge_SourceTerminal(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()
	svc := NewService(c, timeline.NewRecorder(c), event.New())
	target := seedNamedIncident(t, c, "INC-T", incident.StatusTriggered)
	srcOK := seedNamedIncident(t, c, "INC-OK", incident.StatusTriggered)
	srcBad := seedNamedIncident(t, c, "INC-BAD", incident.StatusClosed)

	// 混入一个合法源 + 一个已终态源：应整体拒绝，合法源也不被合并（先校验后写）。
	_, err := svc.Merge(ctx, target.ID, []int{srcOK.ID, srcBad.ID}, 0, SourceWeb)
	if !errors.Is(err, ErrMergeSourceTerminal) {
		t.Fatalf("merge terminal source: got %v, want ErrMergeSourceTerminal", err)
	}
	// 合法源不应被改动（整体校验失败前置，无部分写）。
	okAfter, _ := c.Incident.Get(ctx, srcOK.ID)
	if okAfter.Status != incident.StatusTriggered || okAfter.MergedInto != "" {
		t.Errorf("valid source should be untouched on validation failure: status=%s merged_into=%q",
			okAfter.Status, okAfter.MergedInto)
	}
}

// TestMerge_AlreadyMergedSource 已被合并（merged_into 非空）的单不能再作为源。
func TestMerge_AlreadyMergedSource(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()
	svc := NewService(c, timeline.NewRecorder(c), event.New())
	target := seedNamedIncident(t, c, "INC-T", incident.StatusTriggered)
	// 造一张已被合并的单（merged_into 非空，即使状态非 closed 也应拒绝）。
	src := seedNamedIncident(t, c, "INC-S", incident.StatusTriggered)
	if err := c.Incident.UpdateOneID(src.ID).SetMergedInto("999").Exec(ctx); err != nil {
		t.Fatalf("set merged_into: %v", err)
	}
	_, err := svc.Merge(ctx, target.ID, []int{src.ID}, 0, SourceWeb)
	if !errors.Is(err, ErrMergeSourceTerminal) {
		t.Fatalf("merge already-merged source: got %v, want ErrMergeSourceTerminal", err)
	}
}

// TestMerge_NotFound 主单/源单不存在归一为 ErrNotFound。
func TestMerge_NotFound(t *testing.T) {
	c := newClient(t)
	svc := NewService(c, timeline.NewRecorder(c), event.New())
	target := seedNamedIncident(t, c, "INC-T", incident.StatusTriggered)
	_, err := svc.Merge(context.Background(), target.ID, []int{99999}, 0, SourceWeb)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("merge nonexistent source: got %v, want ErrNotFound", err)
	}
}

// TestHandler_Merge_Endpoint 端到端：POST /incidents/:id/merge，源单被合并进主单。
func TestHandler_Merge_Endpoint(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()
	svc := NewService(c, timeline.NewRecorder(c), event.New())
	h := NewHandler(c, svc)

	target := seedNamedIncident(t, c, "INC-HT", incident.StatusTriggered)
	src := seedNamedIncident(t, c, "INC-HS", incident.StatusTriggered)

	e := echo.New()
	v1 := e.Group("/api/v1", auth.RequireUser(false, nil))
	h.Register(v1)

	body := `{"source_incident_ids":[` + itoa(src.ID) + `]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/incidents/"+itoa(target.ID)+"/merge", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("merge: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	srcAfter, _ := c.Incident.Get(ctx, src.ID)
	if srcAfter.Status != incident.StatusClosed || srcAfter.MergedInto != itoa(target.ID) {
		t.Errorf("source after merge: status=%s merged_into=%q, want closed/%d",
			srcAfter.Status, srcAfter.MergedInto, target.ID)
	}
}

// TestHandler_Merge_EmptySources 空 source_incident_ids → 400。
func TestHandler_Merge_EmptySources(t *testing.T) {
	c := newClient(t)
	svc := NewService(c, timeline.NewRecorder(c), event.New())
	h := NewHandler(c, svc)
	target := seedNamedIncident(t, c, "INC-HE", incident.StatusTriggered)

	e := echo.New()
	v1 := e.Group("/api/v1", auth.RequireUser(false, nil))
	h.Register(v1)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/incidents/"+itoa(target.ID)+"/merge",
		strings.NewReader(`{"source_incident_ids":[]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty sources: got %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}
