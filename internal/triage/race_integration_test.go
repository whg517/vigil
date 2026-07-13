//go:build integration

package triage

// PG 并发建单竞态集成测试（ADR-0012 修订 2026-07-14）。
//
// 单测环境是 sqlite（enttest），advisory lock 被方言守卫跳过，无法真实复现互斥；
// 本文件连真实 PostgreSQL 验证：同 service+severity 的多条不同指纹告警并发到达时，
// advisory xact lock 串行化「查活跃单 → 建单」临界区，最终只产生一个 Incident。
//
// 运行方式（与 e2e 同款依赖，localhost:5432 由 make dev-up 起）：
//
//	go test -tags=integration ./internal/triage/ -run TestIntegration
//
// 依赖不可达或表未迁移（未跑 make dev-setup）时 Skip，不影响默认 go test ./...
// （build tag 隔离，遵循 ADR-0035 的 e2e 隔离约定）。
// 注意：使用与 e2e 相同的 vigil 库，测试数据用唯一 slug 隔离并在结束后清理；
// 不要与 e2e suite（会 TRUNCATE 全表）同时运行。

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/event"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/service"

	"github.com/alicebob/miniredis/v2"
	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"
)

// integrationDSN 与 test/e2e/suite_test.go 的依赖约定一致（docker-compose 起的本地 PG）。
const integrationDSN = "host=localhost port=5432 user=vigil password=vigil dbname=vigil sslmode=disable"

// openIntegrationClient 连真实 PG；不可达或 schema 未迁移则 Skip。
func openIntegrationClient(t *testing.T) *ent.Client {
	t.Helper()
	c, err := ent.Open("postgres", integrationDSN)
	if err != nil {
		t.Skipf("integration: postgres 不可达（run 'make dev-up'）: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// 用一次轻量查询同时探活 + 校验表已迁移（未迁移时提示走 dev-setup，而非报错失败）。
	if _, err := c.Incident.Query().Limit(1).Count(ctx); err != nil {
		t.Skipf("integration: incidents 表不可用（run 'make dev-setup'）: %v", err)
	}
	return c
}

// newIntegrationEngine 建引擎：Redis 用 miniredis 并把编号计数器抬到远离既有数据的高位，
// 避免与共享 vigil 库里既有 Incident 的 INC-xxxx 撞号（本测试不重置库）。
func newIntegrationEngine(t *testing.T, c *ent.Client) *Engine {
	t.Helper()
	mr := miniredis.RunT(t)
	// 以纳秒时间戳截断作为编号基点，几乎不可能与历史编号冲突。
	base := time.Now().UnixNano() % 900_000_000
	mr.Set(incidentNumberKey, fmt.Sprintf("%d", 1_000_000_000+base))
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rc.Close() })
	return NewEngine(c, rc)
}

// seedIntegrationService 建唯一 slug 的 Team+Service，并注册清理（先删子表再删父表）。
func seedIntegrationService(t *testing.T, c *ent.Client, slug string) *ent.Service {
	t.Helper()
	ctx := context.Background()
	tm, err := c.Team.Create().SetName("竞态测试-" + slug).SetSlug(slug).Save(ctx)
	if err != nil {
		t.Fatalf("create team: %v", err)
	}
	svc, err := c.Service.Create().
		SetName(slug).SetSlug(slug).
		SetTeamID(tm.ID).
		SetAutoCreateIncident(true).
		Save(ctx)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	t.Cleanup(func() {
		// 清理顺序对齐外键依赖：event → incident → service → team。
		_, _ = c.Event.Delete().Where(event.HasServiceWith(service.IDEQ(svc.ID))).Exec(ctx)
		_, _ = c.Incident.Delete().Where(incident.HasServiceWith(service.IDEQ(svc.ID))).Exec(ctx)
		_ = c.Service.DeleteOne(svc).Exec(ctx)
		_ = c.Team.DeleteOne(tm).Exec(ctx)
	})
	return svc
}

// createIntegrationEvent 建 firing Event（不同 dedupKey 模拟不同指纹，labels 直达路由）。
func createIntegrationEvent(t *testing.T, c *ent.Client, slug string, sev event.Severity, dedupKey string) *ent.Event {
	t.Helper()
	evt, err := c.Event.Create().
		SetSourceEventID(dedupKey).
		SetSource("prometheus").
		SetSeverity(sev).
		SetStatus(event.StatusFiring).
		SetSummary("竞态测试告警 " + dedupKey).
		SetLabels(map[string]string{"service": slug}).
		SetDedupKey(dedupKey).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	return evt
}

// TestIntegration_AggregateRace_SingleIncident 复现原竞态场景：
// 同 service+severity 的 8 条不同指纹告警并发 Process（模拟 Asynq 并发 10 的告警风暴），
// advisory lock 串行化后应恰好 1 条建单、其余全部并入同一 Incident。
// 修复前（无锁 check-then-act）此场景会稳定产生多个 Incident（各自启动升级链，双倍打扰）。
func TestIntegration_AggregateRace_SingleIncident(t *testing.T) {
	c := openIntegrationClient(t)
	slug := fmt.Sprintf("race-agg-%d", time.Now().UnixNano())
	svc := seedIntegrationService(t, c, slug)
	eng := newIntegrationEngine(t, c)

	const n = 8
	events := make([]*ent.Event, n)
	for i := range events {
		events[i] = createIntegrationEvent(t, c, slug, event.SeverityCritical,
			fmt.Sprintf("%s-fp-%d", slug, i))
	}

	// 同时放行 n 个 goroutine，最大化临界区碰撞概率。
	var (
		start   = make(chan struct{})
		wg      sync.WaitGroup
		mu      sync.Mutex
		results []*Result
		errs    []error
	)
	for i := range events {
		wg.Add(1)
		go func(evtID int) {
			defer wg.Done()
			<-start
			res, err := eng.Process(context.Background(), evtID)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			results = append(results, res)
		}(events[i].ID)
	}
	close(start)
	wg.Wait()

	if len(errs) > 0 {
		t.Fatalf("并发 Process 出错: %v", errs)
	}
	created, aggregated := 0, 0
	incidentIDs := map[int]bool{}
	for _, r := range results {
		switch r.Action {
		case ActionIncidentCreated:
			created++
		case ActionAggregated:
			aggregated++
		default:
			t.Errorf("意外动作: %q", r.Action)
		}
		incidentIDs[r.IncidentID] = true
	}
	if created != 1 || aggregated != n-1 {
		t.Errorf("应恰好 1 建单 + %d 并入, got created=%d aggregated=%d", n-1, created, aggregated)
	}
	if len(incidentIDs) != 1 {
		t.Errorf("所有告警应归入同一 Incident, got %d 个: %v", len(incidentIDs), incidentIDs)
	}
	// 落库复核（不只信内存结果）：该 service 名下确实只有一个 Incident，且挂满 n 条 Event。
	count, err := c.Incident.Query().Where(incident.HasServiceWith(service.IDEQ(svc.ID))).Count(context.Background())
	if err != nil {
		t.Fatalf("count incidents: %v", err)
	}
	if count != 1 {
		t.Errorf("库中该 service 应只有 1 个 Incident, got %d", count)
	}
	attached, err := c.Event.Query().Where(event.HasIncident()).Where(event.HasServiceWith(service.IDEQ(svc.ID))).Count(context.Background())
	if err != nil {
		t.Fatalf("count attached events: %v", err)
	}
	if attached != n {
		t.Errorf("全部 %d 条 Event 应挂到 Incident 上, got %d", n, attached)
	}
}

// TestIntegration_AggregateRace_SeverityIsolation 验证锁粒度：不同 severity 键不同，
// 并发下互不串行、各建各的 Incident（锁只串行同键，不把整个 service 拍平成全局锁）。
func TestIntegration_AggregateRace_SeverityIsolation(t *testing.T) {
	c := openIntegrationClient(t)
	slug := fmt.Sprintf("race-sev-%d", time.Now().UnixNano())
	svc := seedIntegrationService(t, c, slug)
	eng := newIntegrationEngine(t, c)

	sevs := []event.Severity{event.SeverityCritical, event.SeverityWarning}
	var evts []*ent.Event
	for i, sev := range sevs {
		evts = append(evts, createIntegrationEvent(t, c, slug, sev, fmt.Sprintf("%s-fp-%d", slug, i)))
	}

	var (
		start = make(chan struct{})
		wg    sync.WaitGroup
		mu    sync.Mutex
		errs  []error
	)
	for _, evt := range evts {
		wg.Add(1)
		go func(evtID int) {
			defer wg.Done()
			<-start
			if _, err := eng.Process(context.Background(), evtID); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}(evt.ID)
	}
	close(start)
	wg.Wait()
	if len(errs) > 0 {
		t.Fatalf("并发 Process 出错: %v", errs)
	}

	count, err := c.Incident.Query().Where(incident.HasServiceWith(service.IDEQ(svc.ID))).Count(context.Background())
	if err != nil {
		t.Fatalf("count incidents: %v", err)
	}
	if count != len(sevs) {
		t.Errorf("不同 severity 应各建一单, want %d got %d", len(sevs), count)
	}
}
