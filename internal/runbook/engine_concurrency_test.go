// engine_concurrency_test.go execute 并发保护（C.5.1 / audit S10）。
//
// 验证 (runbook, incident) 维度执行锁：
//   - 已审批执行（approved=true）在途时，第二次触发返回 ErrExecuteInProgress，写步骤不重复触发。
//   - 只读干跑（approved=false）不加锁，可反复重试。
//   - 执行结束主动释放锁，后续合法重试可再次执行。
//   - 无 Redis 时降级为无锁，不阻断执行。
package runbook

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/kevin/vigil/ent/schema"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newWriteRunbookAndEngine 建一个含写步骤的 runbook + 挂 Redis 的引擎，返回写端点命中计数器。
func newWriteRunbookAndEngine(t *testing.T) (*Engine, int, *redis.Client, *int32) {
	t.Helper()
	var writeHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&writeHits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t)
	rb := createExecRunbook(t, c, []schema.RunbookStep{
		{
			ID: "s1", Name: "回滚",
			Action: schema.StepAction{
				Type:   "execute", // 写操作
				Target: schema.StepTarget{Kind: "http", Endpoint: srv.URL, Readonly: false},
			},
			RequireApproval: true,
			OnFailure:       "continue",
		},
	})

	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rc.Close() })

	eng := NewEngine(c, newTestRegistry())
	eng.SetRedis(rc, 0)
	return eng, rb.ID, rc, &writeHits
}

// TestExecute_ConcurrentBlocked 已有执行在途时，第二次已审批触发被拒（写端点不二次命中）。
func TestExecute_ConcurrentBlocked(t *testing.T) {
	eng, rbID, rc, hits := newWriteRunbookAndEngine(t)
	incID := 42

	// 模拟"第一次执行在途"：预先占用执行锁。
	if err := rc.SetNX(context.Background(), execLockKey(rbID, incID), 1, eng.execLockTTL).Err(); err != nil {
		t.Fatalf("preset lock: %v", err)
	}

	_, err := eng.Execute(context.Background(), rbID, incID, true, 0)
	if err == nil {
		t.Fatal("并发第二次执行应返回冲突错误，got nil")
	}
	if !errors.Is(err, ErrExecuteInProgress) {
		t.Fatalf("应为 ErrExecuteInProgress，got %v", err)
	}
	if got := atomic.LoadInt32(hits); got != 0 {
		t.Fatalf("被阻断的执行绝不能触发写端点，命中 %d 次", got)
	}
}

// TestExecute_DryRunNotLocked 只读干跑（approved=false）不受执行锁影响，可重试。
func TestExecute_DryRunNotLocked(t *testing.T) {
	eng, rbID, rc, hits := newWriteRunbookAndEngine(t)
	incID := 7

	// 即便锁被占用……
	if err := rc.SetNX(context.Background(), execLockKey(rbID, incID), 1, eng.execLockTTL).Err(); err != nil {
		t.Fatalf("preset lock: %v", err)
	}
	// ……干跑仍应正常返回（写步骤被跳过，不触发写端点）。
	res, err := eng.Execute(context.Background(), rbID, incID, false, 0)
	if err != nil {
		t.Fatalf("干跑不应被并发锁阻断，got %v", err)
	}
	if len(res.Steps) != 1 || !res.Steps[0].Skipped {
		t.Fatalf("写步骤在干跑下应被跳过，got %+v", res.Steps)
	}
	if got := atomic.LoadInt32(hits); got != 0 {
		t.Fatalf("干跑不得触发写端点，命中 %d 次", got)
	}
}

// TestExecute_LockReleasedAfterCompletion 执行结束释放锁，后续合法重试可再次执行。
func TestExecute_LockReleasedAfterCompletion(t *testing.T) {
	eng, rbID, rc, hits := newWriteRunbookAndEngine(t)
	incID := 99

	if _, err := eng.Execute(context.Background(), rbID, incID, true, 0); err != nil {
		t.Fatalf("首次执行: %v", err)
	}
	// 锁应已释放（key 不存在）。
	if n, _ := rc.Exists(context.Background(), execLockKey(rbID, incID)).Result(); n != 0 {
		t.Fatal("执行结束后应释放锁，但 key 仍存在")
	}
	// 合法重试：再执行一次应成功，写端点应第二次命中。
	if _, err := eng.Execute(context.Background(), rbID, incID, true, 0); err != nil {
		t.Fatalf("释放后重试: %v", err)
	}
	if got := atomic.LoadInt32(hits); got != 2 {
		t.Fatalf("两次成功执行应命中写端点 2 次，got %d", got)
	}
}

// TestExecute_NoRedisDegrade 无 Redis 时降级为无锁，不阻断执行（核心审批闸门仍在）。
func TestExecute_NoRedisDegrade(t *testing.T) {
	c := newTestClient(t)
	rb := createExecRunbook(t, c, []schema.RunbookStep{
		{
			ID: "s1", Name: "等待",
			Action:    schema.StepAction{Type: "wait"},
			OnFailure: "continue",
		},
	})
	eng := NewEngine(c, newTestRegistry()) // 未 SetRedis → redis==nil

	for i := 0; i < 2; i++ {
		if _, err := eng.Execute(context.Background(), rb.ID, 1, true, 0); err != nil {
			t.Fatalf("无 Redis 降级下执行不应报错（第 %d 次），got %v", i+1, err)
		}
	}
}
