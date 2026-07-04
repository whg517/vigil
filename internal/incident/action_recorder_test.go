package incident

import (
	"context"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/incidentaction"
	"github.com/kevin/vigil/ent/schema"
	"github.com/kevin/vigil/internal/event"
	"github.com/kevin/vigil/internal/timeline"

	_ "github.com/mattn/go-sqlite3"
)

// wireActionRecorder 建 Service + ActionRecorder 订阅同一 bus，返回三者。
// 复刻 wire.go 装配：处置动作经 Service 发事件，ActionRecorder 订阅后落 IncidentAction。
func wireActionRecorder(t *testing.T, c *ent.Client) (*Service, *ActionRecorder) {
	t.Helper()
	bus := event.New()
	svc := NewService(c, timeline.NewRecorder(c), bus)
	ar := NewActionRecorder(c)
	ar.Subscribe(bus)
	return svc, ar
}

// actionsFor 查某 incident 的全部 IncidentAction（按时间升序）。
func actionsFor(t *testing.T, c *ent.Client, incID int) []*ent.IncidentAction {
	t.Helper()
	items, err := c.IncidentAction.Query().
		Where(incidentaction.HasIncidentWith(incident.IDEQ(incID))).
		Order(ent.Asc(incidentaction.FieldTimestamp)).
		All(context.Background())
	if err != nil {
		t.Fatalf("query actions: %v", err)
	}
	return items
}

// TestActionRecorder_Ack 验证：ack 落一条 type=ack、via=web、actor=user 的 IncidentAction。
func TestActionRecorder_Ack(t *testing.T) {
	c := newClient(t)
	inc := seedIncident(t, c, incident.StatusTriggered)
	user, _ := c.User.Create().SetUsername("u").SetEmail("u@x.com").Save(context.Background())
	svc, _ := wireActionRecorder(t, c)

	if _, err := svc.Ack(context.Background(), inc.ID, user.ID, SourceWeb); err != nil {
		t.Fatalf("Ack: %v", err)
	}

	acts := actionsFor(t, c, inc.ID)
	if len(acts) != 1 {
		t.Fatalf("action count: got %d, want 1", len(acts))
	}
	a := acts[0]
	if a.Type != incidentaction.TypeAck {
		t.Errorf("type: got %q, want ack", a.Type)
	}
	if a.Via != incidentaction.ViaWeb {
		t.Errorf("via: got %q, want web", a.Via)
	}
	if a.Result != incidentaction.ResultSuccess {
		t.Errorf("result: got %q, want success", a.Result)
	}
	if a.Actor["kind"] != "user" || a.Actor["id"] != itoa(user.ID) {
		t.Errorf("actor: got %v, want user/%d", a.Actor, user.ID)
	}
}

// TestActionRecorder_ViaDerivation 验证 via 按来源正确派生：
// web→web、im→im、api→api、runbook/system→automation。
func TestActionRecorder_ViaDerivation(t *testing.T) {
	cases := []struct {
		name string
		src  Source
		want incidentaction.Via
	}{
		{"web", SourceWeb, incidentaction.ViaWeb},
		{"im", SourceIM, incidentaction.ViaIm},
		{"api", SourceAPI, incidentaction.ViaAPI},
		{"runbook", SourceRunbook, incidentaction.ViaAutomation},
		{"system", SourceSystem, incidentaction.ViaAutomation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newClient(t)
			inc := seedIncident(t, c, incident.StatusTriggered)
			svc, _ := wireActionRecorder(t, c)

			// 用 resolve（任意活跃态可解决，不挑来源）触发一条动作。
			if _, err := svc.Resolve(context.Background(), inc.ID, 5, tc.src); err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			acts := actionsFor(t, c, inc.ID)
			if len(acts) != 1 {
				t.Fatalf("action count: got %d, want 1", len(acts))
			}
			if acts[0].Via != tc.want {
				t.Errorf("via for src=%s: got %q, want %q", tc.src, acts[0].Via, tc.want)
			}
			if acts[0].Type != incidentaction.TypeResolve {
				t.Errorf("type: got %q, want resolve", acts[0].Type)
			}
		})
	}
}

// TestActionRecorder_AllActions 验证 ack/escalate/resolve/reopen/close/add_responder
// 各动作都落对应 type 的 IncidentAction（走完整状态机链路）。
func TestActionRecorder_AllActions(t *testing.T) {
	c := newClient(t)
	inc := seedIncident(t, c, incident.StatusTriggered)
	// 升级策略：给 incident 绑定一个含 2 层的策略，使 Escalate 能推进并发事件。
	policy, err := c.EscalationPolicy.Create().
		SetName("p").
		SetLevels([]schema.EscalationLevel{{Level: 0}, {Level: 1}}).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create policy: %v", err)
	}
	if err := c.Incident.UpdateOneID(inc.ID).SetEscalationPolicyID(policy.ID).Exec(context.Background()); err != nil {
		t.Fatalf("bind policy: %v", err)
	}
	op, _ := c.User.Create().SetUsername("op").SetEmail("op@x.com").Save(context.Background())
	target, _ := c.User.Create().SetUsername("t").SetEmail("t@x.com").Save(context.Background())
	svc, _ := wireActionRecorder(t, c)
	ctx := context.Background()

	// ack → escalate → resolve → close → reopen → add_responder
	// Ack 会设 assignee，actorID 必须是真实 user（否则 FK 约束失败）。
	if _, err := svc.Ack(ctx, inc.ID, op.ID, SourceWeb); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if _, err := svc.Escalate(ctx, inc.ID, op.ID, SourceWeb); err != nil {
		t.Fatalf("Escalate: %v", err)
	}
	if _, err := svc.Resolve(ctx, inc.ID, op.ID, SourceWeb); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, err := svc.Close(ctx, inc.ID, op.ID, SourceWeb); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := svc.Reopen(ctx, inc.ID, op.ID, SourceWeb); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if _, err := svc.AddResponder(ctx, inc.ID, op.ID, target.ID, SourceWeb); err != nil {
		t.Fatalf("AddResponder: %v", err)
	}

	acts := actionsFor(t, c, inc.ID)
	gotTypes := make([]incidentaction.Type, 0, len(acts))
	for _, a := range acts {
		gotTypes = append(gotTypes, a.Type)
	}
	want := []incidentaction.Type{
		incidentaction.TypeAck,
		incidentaction.TypeEscalate,
		incidentaction.TypeResolve,
		incidentaction.TypeClose,
		incidentaction.TypeReopen,
		incidentaction.TypeAddResponder,
	}
	if len(gotTypes) != len(want) {
		t.Fatalf("action types: got %v, want %v", gotTypes, want)
	}
	for i, w := range want {
		if gotTypes[i] != w {
			t.Errorf("action[%d]: got %q, want %q", i, gotTypes[i], w)
		}
	}
}

// TestActionRecorder_SystemAutoResolve 验证：triage 自动恢复发的 IncidentResolved 事件
// （ActorID=0、Via 空）也被审计——via=automation、actor=system。
func TestActionRecorder_SystemAutoResolve(t *testing.T) {
	c := newClient(t)
	inc := seedIncident(t, c, incident.StatusTriggered)
	bus := event.New()
	ar := NewActionRecorder(c)
	ar.Subscribe(bus)

	// 直接发一条系统触发的 resolve 事件（模拟 triage.handleResolved：ActorID=0、无 Via）。
	bus.Publish(context.Background(), event.Event{
		Type:            event.IncidentResolved,
		Incident:        inc,
		Action:          event.Action("resolve"),
		ActorID:         0,
		SystemTriggered: true,
	})

	acts := actionsFor(t, c, inc.ID)
	if len(acts) != 1 {
		t.Fatalf("action count: got %d, want 1", len(acts))
	}
	if acts[0].Via != incidentaction.ViaAutomation {
		t.Errorf("via: got %q, want automation", acts[0].Via)
	}
	if acts[0].Actor["kind"] != "system" {
		t.Errorf("actor.kind: got %q, want system", acts[0].Actor["kind"])
	}
}

// TestActionRecorder_QueryPagination 验证 QueryActions/CountActions 分页与升序。
func TestActionRecorder_QueryPagination(t *testing.T) {
	c := newClient(t)
	inc := seedIncident(t, c, incident.StatusTriggered)
	user, _ := c.User.Create().SetUsername("pg").SetEmail("pg@x.com").Save(context.Background())
	svc, ar := wireActionRecorder(t, c)
	ctx := context.Background()

	// 造两条动作：ack（设 assignee，需真实 user）+ resolve
	if _, err := svc.Ack(ctx, inc.ID, user.ID, SourceWeb); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if _, err := svc.Resolve(ctx, inc.ID, user.ID, SourceIM); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	total, err := ar.CountActions(ctx, inc.ID)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if total != 2 {
		t.Fatalf("count: got %d, want 2", total)
	}
	all, err := ar.QueryActions(ctx, inc.ID, 0, 0)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("query all: got %d, want 2", len(all))
	}
	// 升序：ack 先于 resolve
	if all[0].Type != incidentaction.TypeAck || all[1].Type != incidentaction.TypeResolve {
		t.Errorf("order: got %q,%q want ack,resolve", all[0].Type, all[1].Type)
	}
}
