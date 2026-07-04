package ws

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// recvWithin 在 timeout 内从 client.send 读一条消息；超时则 t.Fatal。
func recvWithin(t *testing.T, c *client, timeout time.Duration, ctx string) Message {
	t.Helper()
	select {
	case raw := <-c.send:
		var m Message
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("%s: unmarshal: %v", ctx, err)
		}
		return m
	case <-time.After(timeout):
		t.Fatalf("%s: no message within %v", ctx, timeout)
		return Message{}
	}
}

// assertNoMessage 断言 timeout 内 client.send 无消息（用于去重/隔离验证）。
func assertNoMessage(t *testing.T, c *client, timeout time.Duration, ctx string) {
	t.Helper()
	select {
	case raw := <-c.send:
		t.Fatalf("%s: unexpected message: %s", ctx, raw)
	case <-time.After(timeout):
		// 预期：无消息
	}
}

// newHubWithPubSub 建一个挂了 Redis pub/sub 中继的 hub（共享同一 miniredis 后端模拟多副本）。
func newHubWithPubSub(t *testing.T, addr string) (*Hub, *RedisPubSub, func()) {
	t.Helper()
	rc := redis.NewClient(&redis.Options{Addr: addr})
	hub := NewHub()
	ps := NewRedisPubSub(rc, hub, nil)
	hub.SetRelay(ps)
	stop := ps.Start(context.Background())
	cleanup := func() {
		stop()
		_ = rc.Close()
	}
	return hub, ps, cleanup
}

// TestPubSub_CrossReplicaDelivery T6.4 核心：副本 A 广播的消息经 Redis 转发到连在副本 B 的连接。
// 模拟两个 hub 实例（=两个副本）共享同一 Redis，客户端连在副本 B，副本 A 处理事件并广播，
// 副本 B 的连接应收到——这正是进程内广播做不到、多副本 pub/sub 要修的点。
func TestPubSub_CrossReplicaDelivery(t *testing.T) {
	mr := miniredis.RunT(t)

	hubA, _, cleanupA := newHubWithPubSub(t, mr.Addr())
	defer cleanupA()
	hubB, _, cleanupB := newHubWithPubSub(t, mr.Addr())
	defer cleanupB()

	// 客户端只连在副本 B。
	cB := newClient()
	defer hubB.Subscribe(42, cB)()

	// 副本 A 处理事件并广播（A 上无该 incident 的连接）。
	hubA.BroadcastIncident(42, "ack", nil)

	// 副本 B 的连接应经 Redis 收到跨副本消息。
	m := recvWithin(t, cB, 2*time.Second, "cross-replica")
	if m.Type != MsgIncidentChanged || m.IncidentID != 42 || m.Action != "ack" {
		t.Errorf("unexpected cross-replica message: %+v", m)
	}
}

// TestPubSub_NoDuplicateOnOriginReplica 去重防回环：广播的副本自己不会因 Redis 回环收到第二次。
// 副本 A 上有该 incident 的连接，A 广播时 deliverLocal 已直发一次；Redis 把消息广播回 A
// （pub/sub 发给所有订阅者含自己），A 据 replicaID==自己 丢弃，连接只收到一次。
func TestPubSub_NoDuplicateOnOriginReplica(t *testing.T) {
	mr := miniredis.RunT(t)

	hubA, _, cleanupA := newHubWithPubSub(t, mr.Addr())
	defer cleanupA()

	cA := newClient()
	defer hubA.Subscribe(7, cA)()

	hubA.BroadcastIncident(7, "resolve", nil)

	// 第一条：本地直发。
	m := recvWithin(t, cA, 2*time.Second, "local-direct")
	if m.Action != "resolve" {
		t.Errorf("first message action: got %q, want resolve", m.Action)
	}
	// 不应有第二条（Redis 回环被 replicaID 去重丢弃）。
	assertNoMessage(t, cA, 500*time.Millisecond, "dedup: no redis echo")
}

// TestPubSub_BothReplicasHaveSubscribers 两副本各有订阅者：A 广播，A 本地直发 + B 经 Redis 收到，
// 各自恰好收到一次（A 不重复、B 不遗漏）。
func TestPubSub_BothReplicasHaveSubscribers(t *testing.T) {
	mr := miniredis.RunT(t)

	hubA, _, cleanupA := newHubWithPubSub(t, mr.Addr())
	defer cleanupA()
	hubB, _, cleanupB := newHubWithPubSub(t, mr.Addr())
	defer cleanupB()

	cA := newClient()
	defer hubA.Subscribe(9, cA)()
	cB := newClient()
	defer hubB.Subscribe(9, cB)()

	hubA.BroadcastIncident(9, "escalate", nil)

	// A：本地直发一次，无 Redis 回环重复。
	mA := recvWithin(t, cA, 2*time.Second, "A local")
	if mA.Action != "escalate" {
		t.Errorf("A action: got %q, want escalate", mA.Action)
	}
	assertNoMessage(t, cA, 500*time.Millisecond, "A no duplicate")

	// B：经 Redis 收到一次。
	mB := recvWithin(t, cB, 2*time.Second, "B cross-replica")
	if mB.Action != "escalate" {
		t.Errorf("B action: got %q, want escalate", mB.Action)
	}
	assertNoMessage(t, cB, 500*time.Millisecond, "B no duplicate")
}

// TestPubSub_TimelineAddedCrossReplica timeline_added 消息也跨副本转发（复用同一广播路径）。
func TestPubSub_TimelineAddedCrossReplica(t *testing.T) {
	mr := miniredis.RunT(t)

	hubA, _, cleanupA := newHubWithPubSub(t, mr.Addr())
	defer cleanupA()
	hubB, _, cleanupB := newHubWithPubSub(t, mr.Addr())
	defer cleanupB()

	cB := newClient()
	defer hubB.Subscribe(11, cB)()

	hubA.BroadcastTimelineAdded(11, map[string]any{"id": 5, "type": "status_changed"})

	m := recvWithin(t, cB, 2*time.Second, "timeline cross-replica")
	if m.Type != MsgTimelineAdded || m.IncidentID != 11 {
		t.Errorf("unexpected timeline message: %+v", m)
	}
	if m.Data == nil {
		t.Error("timeline_added should carry item Data across replicas")
	}
}

// TestPubSub_OnlyToSubscribedIncident 跨副本转发仍按 incident 隔离：B 只订阅 incident 1，
// A 广播 incident 2 不应转发给 B 的连接。
func TestPubSub_OnlyToSubscribedIncident(t *testing.T) {
	mr := miniredis.RunT(t)

	hubA, _, cleanupA := newHubWithPubSub(t, mr.Addr())
	defer cleanupA()
	hubB, _, cleanupB := newHubWithPubSub(t, mr.Addr())
	defer cleanupB()

	cB := newClient()
	defer hubB.Subscribe(1, cB)()

	hubA.BroadcastIncident(2, "ack", nil) // B 未订阅 2

	assertNoMessage(t, cB, 800*time.Millisecond, "cross-replica incident isolation")
}

// TestHub_NoRelayInProcessOnly 向后兼容：未装配 relay（单副本/无 Redis）时纯进程内广播，
// 行为与旧版一致——本地订阅者收到，不涉及 Redis。
func TestHub_NoRelayInProcessOnly(t *testing.T) {
	h := NewHub() // 不 SetRelay
	if h.relay != nil {
		t.Fatal("default hub should have nil relay")
	}
	c := newClient()
	defer h.Subscribe(1, c)()

	h.BroadcastIncident(1, "ack", nil)

	m := recvWithin(t, c, time.Second, "in-process")
	if m.Action != "ack" {
		t.Errorf("action: got %q, want ack", m.Action)
	}
}

// TestPubSub_NilRedisClient nil Redis client 下 Publish/Start 安全降级（不 panic），退化进程内。
func TestPubSub_NilRedisClient(t *testing.T) {
	hub := NewHub()
	ps := NewRedisPubSub(nil, hub, nil)
	hub.SetRelay(ps)
	stop := ps.Start(context.Background()) // 不应 panic
	defer stop()

	c := newClient()
	defer hub.Subscribe(1, c)()

	hub.BroadcastIncident(1, "ack", nil) // Publish 内 rc==nil 直接返回，本地仍直发

	m := recvWithin(t, c, time.Second, "nil-redis in-process")
	if m.Action != "ack" {
		t.Errorf("action: got %q, want ack", m.Action)
	}
}

// TestPubSub_ReplicaIDsDistinct 不同副本 replicaID 不同（去重前提）。
func TestPubSub_ReplicaIDsDistinct(t *testing.T) {
	mr := miniredis.RunT(t)
	_, psA, cleanupA := newHubWithPubSub(t, mr.Addr())
	defer cleanupA()
	_, psB, cleanupB := newHubWithPubSub(t, mr.Addr())
	defer cleanupB()

	if psA.replicaID == psB.replicaID {
		t.Errorf("replica ids should differ, both = %q", psA.replicaID)
	}
	if psA.replicaID == "" {
		t.Error("replica id should not be empty")
	}
}

// TestPubSub_StopHaltsForwarding stop 后不再转发跨副本消息（订阅 goroutine 退出）。
func TestPubSub_StopHaltsForwarding(t *testing.T) {
	mr := miniredis.RunT(t)

	hubA, _, cleanupA := newHubWithPubSub(t, mr.Addr())
	defer cleanupA()

	rcB := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rcB.Close() }()
	hubB := NewHub()
	psB := NewRedisPubSub(rcB, hubB, nil)
	hubB.SetRelay(psB)
	stopB := psB.Start(context.Background())

	cB := newClient()
	defer hubB.Subscribe(3, cB)()

	// 停止 B 的订阅后，A 广播不应再转发到 B。
	stopB()
	// 给订阅 goroutine 一点时间退出。
	time.Sleep(100 * time.Millisecond)
	hubA.BroadcastIncident(3, "ack", nil)

	assertNoMessage(t, cB, 800*time.Millisecond, "after stop no forwarding")
}
