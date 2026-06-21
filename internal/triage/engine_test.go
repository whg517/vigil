package triage

import (
	"context"
	"fmt"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/event"
	"github.com/kevin/vigil/ent/incident"

	"github.com/alicebob/miniredis/v2"
	_ "github.com/mattn/go-sqlite3"
	"github.com/redis/go-redis/v9"
)

// newTestClient 用 sqlite 内存库创建 ent client（含自动迁移）。
// redis 传 nil —— 去重降级跳过（测试不依赖 Redis）。
func newTestClient(t *testing.T) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:triage_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// seedServiceAndTeam 建一个 Team + Service（slug=payment, auto_create_incident=true）。
func seedServiceAndTeam(t *testing.T, c *ent.Client) *ent.Service {
	t.Helper()
	team, err := c.Team.Create().SetName("支付").SetSlug("pay").Save(context.Background())
	if err != nil {
		t.Fatalf("create team: %v", err)
	}
	svc, err := c.Service.Create().
		SetName("payment-api").
		SetSlug("payment").
		SetTeamID(team.ID).
		SetAutoCreateIncident(true).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	return svc
}

// createEvent 建一个 Event（firing/warning, labels.service=payment）。
func createEvent(t *testing.T, c *ent.Client, severity event.Severity, dedupKey string) *ent.Event {
	t.Helper()
	evt, err := c.Event.Create().
		SetSourceEventID(dedupKey).
		SetSource("prometheus").
		SetSeverity(severity).
		SetStatus(event.StatusFiring).
		SetSummary("支付服务告警 " + dedupKey).
		SetLabels(map[string]string{"service": "payment"}).
		SetDedupKey(dedupKey).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	return evt
}

// TestEngine_RouteHitAndCreateIncident 验证：路由命中 → 创建新 Incident。
func TestEngine_RouteHitAndCreateIncident(t *testing.T) {
	c := newTestClient(t)
	seedServiceAndTeam(t, c)
	eng := NewEngine(c, nil) // redis=nil 跳过去重

	evt := createEvent(t, c, event.SeverityWarning, "k1")
	res, err := eng.Process(context.Background(), evt.ID)
	if err != nil {
		t.Fatalf("Process failed: %v", err)
	}

	if res.Action != ActionIncidentCreated {
		t.Fatalf("Action: got %q, want incident_created", res.Action)
	}
	if res.ServiceName != "payment-api" {
		t.Errorf("ServiceName: got %q", res.ServiceName)
	}
	if res.IncidentNum == "" {
		t.Error("IncidentNum should not be empty")
	}

	// 验证 Incident 落库
	inc, err := c.Incident.Get(context.Background(), res.IncidentID)
	if err != nil {
		t.Fatalf("get incident: %v", err)
	}
	if string(inc.Status) != "triggered" {
		t.Errorf("incident status: got %q, want triggered", inc.Status)
	}
	if string(inc.Severity) != "warning" {
		t.Errorf("incident severity: got %q", inc.Severity)
	}
}

// TestEngine_AggregateIntoExisting 验证：同 service+severity 在窗口内并入既有 Incident。
func TestEngine_AggregateIntoExisting(t *testing.T) {
	c := newTestClient(t)
	seedServiceAndTeam(t, c)
	eng := NewEngine(c, nil)

	// 第一条：创建 Incident
	evt1 := createEvent(t, c, event.SeverityWarning, "k1")
	res1, err := eng.Process(context.Background(), evt1.ID)
	if err != nil {
		t.Fatalf("Process evt1: %v", err)
	}
	if res1.Action != ActionIncidentCreated {
		t.Fatalf("evt1 Action: got %q, want incident_created", res1.Action)
	}

	// 第二条（同 service+severity）：应并入既有
	evt2 := createEvent(t, c, event.SeverityWarning, "k2")
	res2, err := eng.Process(context.Background(), evt2.ID)
	if err != nil {
		t.Fatalf("Process evt2: %v", err)
	}
	if res2.Action != ActionAggregated {
		t.Fatalf("evt2 Action: got %q, want aggregated", res2.Action)
	}
	if res2.IncidentID != res1.IncidentID {
		t.Errorf("应并入同一 Incident: got %d, want %d", res2.IncidentID, res1.IncidentID)
	}
}

// TestEngine_Unrouted 验证：labels 无 service → 路由未命中 → unrouted。
func TestEngine_Unrouted(t *testing.T) {
	c := newTestClient(t)
	eng := NewEngine(c, nil)

	// 建 Event，labels 不含 service
	evt, err := c.Event.Create().
		SetSourceEventID("k-x").
		SetSource("test").
		SetSeverity(event.SeverityInfo).
		SetStatus(event.StatusFiring).
		SetSummary("无路由告警").
		SetLabels(map[string]string{"foo": "bar"}). // 无 service
		SetDedupKey("k-x").
		Save(context.Background())
	if err != nil {
		t.Fatalf("create event: %v", err)
	}

	res, err := eng.Process(context.Background(), evt.ID)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.Action != ActionUnrouted {
		t.Errorf("Action: got %q, want unrouted", res.Action)
	}
}

// TestEngine_ResolvedResolvesIncident 验证：resolved 事件推进 Incident 解决。
func TestEngine_ResolvedResolvesIncident(t *testing.T) {
	c := newTestClient(t)
	seedServiceAndTeam(t, c)
	eng := NewEngine(c, nil)

	// 先创建一个 firing 事件 → 产生 Incident
	evt1 := createEvent(t, c, event.SeverityWarning, "k1")
	res1, _ := eng.Process(context.Background(), evt1.ID)
	if res1.Action != ActionIncidentCreated {
		t.Fatalf("setup: evt1 Action=%q", res1.Action)
	}

	// 再来一个 resolved 事件（同 dedup_key 的恢复，告警源通常用不同 event id）
	evt2, err := c.Event.Create().
		SetSourceEventID("k1-resolved").
		SetSource("prometheus").
		SetSeverity(event.SeverityWarning).
		SetStatus(event.StatusResolved).
		SetSummary("支付服务告警恢复").
		SetLabels(map[string]string{"service": "payment"}).
		SetDedupKey("k1").
		Save(context.Background())
	if err != nil {
		t.Fatalf("create resolved event: %v", err)
	}

	res2, err := eng.Process(context.Background(), evt2.ID)
	if err != nil {
		t.Fatalf("Process resolved: %v", err)
	}
	if res2.Action != ActionResolved {
		t.Fatalf("Action: got %q, want resolved", res2.Action)
	}
	if res2.IncidentID != res1.IncidentID {
		t.Errorf("应解决同一 Incident: got %d, want %d", res2.IncidentID, res1.IncidentID)
	}

	// 验证 Incident 状态确为 resolved
	inc, _ := c.Incident.Get(context.Background(), res2.IncidentID)
	if string(inc.Status) != "resolved" {
		t.Errorf("incident status: got %q, want resolved", inc.Status)
	}
}

// TestSeverityToPriority 验证 severity → priority 映射。
func TestSeverityToPriority(t *testing.T) {
	cases := map[event.Severity]string{
		event.SeverityCritical: "p1",
		event.SeverityWarning:  "p2",
		event.SeverityInfo:     "p3",
	}
	for sev, want := range cases {
		if got := string(severityToPriority(sev)); got != want {
			t.Errorf("severityToPriority(%v): got %q, want %q", sev, got, want)
		}
	}
}

// newIsolatedClient 用独立 DSN 的 sqlite 内存库，避免 cache=shared 的交叉污染。
// 用于需要精确控制初始状态的编号测试。
func newIsolatedClient(t *testing.T, dsn string) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", dsn)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestCreateIncident_ConsecutiveNumbers 验证有 Redis 时连续创建多个 Incident
// 得到不重复的递增编号（Redis INCR 原子分配，并发安全）。
func TestCreateIncident_ConsecutiveNumbers(t *testing.T) {
	// 独立库，避免其他测试的 incident 污染编号断言
	c := newIsolatedClient(t, "file:triage_num_test?mode=memory&cache=shared&_fk=1")
	seedServiceAndTeam(t, c)

	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rc.Close() })

	eng := NewEngine(c, rc)

	var numbers []string
	// 用不同 severity 避免 aggregate 把多条事件并入同一 incident（aggregate 按 service+severity）
	sevs := []event.Severity{event.SeverityCritical, event.SeverityWarning, event.SeverityInfo}
	for i := 0; i < 3; i++ {
		evt := createEvent(t, c, sevs[i], fmt.Sprintf("kn%d", i))
		res, err := eng.Process(context.Background(), evt.ID)
		if err != nil {
			t.Fatalf("Process %d: %v", i, err)
		}
		if res.Action != ActionIncidentCreated {
			t.Fatalf("event %d should create incident, got action %s", i, res.Action)
		}
		numbers = append(numbers, res.IncidentNum)
	}

	// Redis INCR 保证编号严格递增且不重复
	want := []string{"INC-0001", "INC-0002", "INC-0003"}
	for i, n := range numbers {
		if n != want[i] {
			t.Errorf("number[%d]: got %s, want %s", i, n, want[i])
		}
	}
}

// TestNextIncidentNumber_RedisIncr 验证 Redis INCR 路径：连续调用返回递增编号。
func TestNextIncidentNumber_RedisIncr(t *testing.T) {
	c := newIsolatedClient(t, "file:triage_incr_test?mode=memory&cache=shared&_fk=1")
	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rc.Close() })

	eng := NewEngine(c, rc)
	n1, _ := eng.nextIncidentNumber(context.Background())
	n2, _ := eng.nextIncidentNumber(context.Background())
	n3, _ := eng.nextIncidentNumber(context.Background())
	if n1 != "INC-0001" || n2 != "INC-0002" || n3 != "INC-0003" {
		t.Errorf("sequence: got %s,%s,%s want INC-0001,INC-0002,INC-0003", n1, n2, n3)
	}
}

// TestNextIncidentNumber_NoRedisFallback 无 Redis 时降级 Count+1。
// Count+1 基于当前记录数，建 1 条后 next = INC-0002（基于计数，非最大编号）。
func TestNextIncidentNumber_NoRedisFallback(t *testing.T) {
	c := newIsolatedClient(t, "file:triage_noredis_test?mode=memory&cache=shared&_fk=1")
	eng := NewEngine(c, nil)
	if _, err := c.Incident.Create().
		SetNumber("INC-0042").
		SetTitle("x").
		SetSeverity(incident.SeverityInfo).
		SetStatus(incident.StatusTriggered).
		Save(context.Background()); err != nil {
		t.Fatalf("create: %v", err)
	}
	n, err := eng.nextIncidentNumber(context.Background())
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	// Count=1 → Count+1 = INC-0002（基于记录数，不解析现有最大编号）
	if n != "INC-0002" {
		t.Errorf("Count+1 fallback: got %s, want INC-0002", n)
	}
}
