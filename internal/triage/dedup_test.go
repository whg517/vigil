package triage

import (
	"context"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/event"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// dedup_test.go 锁定 checkDedup 的降级契约（审计 B23）。
//
// 去重有三条确定行为，必须锁定防回归：
//   1. Redis 未注入（nil）→ 降级放行（不去重），窗口内重复告警不被丢弃（靠聚合兜底防爆量）。
//   2. Redis 在线 → 正常去重，同 dedup_key 二次进来被标噪音、返回 skipped。
//   3. Redis 运行时故障 → 不放行，返回 error（供分诊 Asynq 任务重试），而非静默失去去重。
//
// 契约细节见 engine.go checkDedup 的降级契约表。

// createEventWithSrcID 建一个 firing Event，source_event_id 与 dedup_key 分别可控。
// 用于「同 dedup_key、不同 source_event_id」场景（绕开 (source,source_event_id,status) 唯一索引）。
func createEventWithSrcID(t *testing.T, c *ent.Client, severity event.Severity, srcID, dedupKey string) *ent.Event {
	t.Helper()
	evt, err := c.Event.Create().
		SetSourceEventID(srcID).
		SetSource("prometheus").
		SetSeverity(severity).
		SetStatus(event.StatusFiring).
		SetSummary("支付服务告警 " + srcID).
		SetLabels(map[string]string{"service": "payment"}).
		SetDedupKey(dedupKey).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	return evt
}

// TestCheckDedup_RedisNil_PassThrough 契约①：无 Redis 时去重降级放行（同 key 二次不算重复）。
func TestCheckDedup_RedisNil_PassThrough(t *testing.T) {
	c := newTestClient(t)
	eng := NewEngine(c, nil) // redis=nil

	// 同一 dedupKey 连续两次 checkDedup：无 Redis 时两次都不算重复（都放行）。
	skipped1, err := eng.checkDedup(context.Background(), "dk-nil")
	if err != nil {
		t.Fatalf("checkDedup #1: %v", err)
	}
	if skipped1 {
		t.Fatalf("Redis nil 时首次不应算重复 (skipped=%v)", skipped1)
	}
	skipped2, err := eng.checkDedup(context.Background(), "dk-nil")
	if err != nil {
		t.Fatalf("checkDedup #2: %v", err)
	}
	if skipped2 {
		t.Errorf("Redis nil 契约：去重失效，同 key 二次仍不算重复（放行），got skipped=%v", skipped2)
	}
}

// TestProcess_RedisNil_NoDedupButAggregates 契约①端到端：无 Redis 时重复 firing 不被去重丢弃，
// 但同 service+severity 靠聚合并入同一 Incident（不爆量建单）——这是去重失效时的关键兜底。
func TestProcess_RedisNil_NoDedupButAggregates(t *testing.T) {
	c := newTestClient(t)
	seedServiceAndTeam(t, c)
	eng := NewEngine(c, nil) // redis=nil → 去重失效

	// 两条相同 dedupKey 的 firing（真实场景：同一告警在去重窗口内重复推送）。
	// 注意：Event 有 (source, source_event_id, status) 唯一索引，故两条用不同 source_event_id
	// 但相同 dedup_key——模拟「同一告警被上游重复投递、各生成一条 Event，但去重键相同」。
	evt1 := createEventWithSrcID(t, c, event.SeverityWarning, "src-1", "dup-key")
	res1, err := eng.Process(context.Background(), evt1.ID)
	if err != nil {
		t.Fatalf("Process evt1: %v", err)
	}
	if res1.Action != ActionIncidentCreated {
		t.Fatalf("evt1 应建单，got %q", res1.Action)
	}

	// 第二条：dedupKey 相同。有 Redis 时会被去重丢弃（ActionDedupSkipped）；
	// 无 Redis 时去重失效 → 不丢弃 → 走聚合并入既有单（ActionAggregated），而非再建一单。
	evt2 := createEventWithSrcID(t, c, event.SeverityWarning, "src-2", "dup-key")
	res2, err := eng.Process(context.Background(), evt2.ID)
	if err != nil {
		t.Fatalf("Process evt2: %v", err)
	}
	if res2.Action == ActionDedupSkipped {
		t.Fatalf("无 Redis 时不应发生去重跳过（去重已失效），got %q", res2.Action)
	}
	if res2.Action != ActionAggregated || res2.IncidentID != res1.IncidentID {
		t.Errorf("去重失效时兜底：应聚合并入同一单，got action=%q inc=%d want aggregated inc=%d",
			res2.Action, res2.IncidentID, res1.IncidentID)
	}

	// 关键断言：即使去重失效，也只有 1 张 Incident（聚合兜底防爆量建单）。
	cnt, err := c.Incident.Query().Count(context.Background())
	if err != nil {
		t.Fatalf("count incidents: %v", err)
	}
	if cnt != 1 {
		t.Errorf("去重失效不应爆量建单：incident 数 got %d, want 1（聚合兜底）", cnt)
	}
}

// TestCheckDedup_RedisOnline_Dedups 契约②：Redis 在线时同 dedupKey 二次进来被判重复（skipped）。
func TestCheckDedup_RedisOnline_Dedups(t *testing.T) {
	c := newTestClient(t)
	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rc.Close() })
	eng := NewEngine(c, rc)

	skipped1, err := eng.checkDedup(context.Background(), "dk-online")
	if err != nil {
		t.Fatalf("checkDedup #1: %v", err)
	}
	if skipped1 {
		t.Fatalf("首次不应算重复 (skipped=%v)", skipped1)
	}
	skipped2, err := eng.checkDedup(context.Background(), "dk-online")
	if err != nil {
		t.Fatalf("checkDedup #2: %v", err)
	}
	if !skipped2 {
		t.Errorf("Redis 在线契约：同 key 二次应判重复（skipped=true），got %v", skipped2)
	}
}

// TestCheckDedup_RedisFailure_ReturnsError 契约③：Redis 运行时故障时不降级放行，
// 而是返回 error（供分诊任务失败重试）——避免 Redis 抖动时静默失去去重。
// 用 mr.Close() 关掉 miniredis 模拟连接故障，SetNX 会返回网络错误。
func TestCheckDedup_RedisFailure_ReturnsError(t *testing.T) {
	c := newTestClient(t)
	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rc.Close() })
	eng := NewEngine(c, rc)

	// 关掉 Redis 服务端，制造运行时故障（连接被拒/超时）。
	mr.Close()

	skipped, err := eng.checkDedup(context.Background(), "dk-fail")
	if err == nil {
		t.Fatalf("Redis 故障契约：checkDedup 应返回 error（供重试），got nil err skipped=%v", skipped)
	}
	// 关键：故障时不能返回「已跳过」——那会把无法确认的告警当重复丢掉（漏真故障）。
	if skipped {
		t.Errorf("Redis 故障时不应返回 skipped=true（不能把不确定的告警当重复丢弃）")
	}
}

// TestProcess_RedisFailure_Errors 契约③端到端：Redis 故障时 Process 上抛 error，
// 使分诊 Asynq 任务失败并重试（worker.Handle 返回非 nil error → asynq 重试）。
func TestProcess_RedisFailure_Errors(t *testing.T) {
	c := newTestClient(t)
	seedServiceAndTeam(t, c)
	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rc.Close() })
	eng := NewEngine(c, rc)

	evt := createEvent(t, c, event.SeverityWarning, "fail-key")
	mr.Close() // Redis 故障

	_, err := eng.Process(context.Background(), evt.ID)
	if err == nil {
		t.Fatalf("Redis 故障时 Process 应上抛 error（触发 Asynq 重试），got nil")
	}
}
