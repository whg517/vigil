package queue

import (
	"context"
	"testing"

	"github.com/hibiken/asynq"
)

// TestRegister 注册 handler 不 panic，且能被 mux 路由。
// Queue 的 New 需要 Redis opt 但不立即连接，Register 只操作内存 mux，无需真实 Redis。
// ServeMux 实现了 asynq.Handler 接口（ProcessTask 方法），用它验证路由。
func TestRegister(t *testing.T) {
	q := &Queue{
		mux: asynq.NewServeMux(),
	}
	called := false
	q.Register("test:task", func(ctx context.Context, task *asynq.Task) error {
		called = true
		return nil
	})
	// ServeMux 实现了 asynq.Handler，调 ProcessTask 验证路由
	err := q.mux.ProcessTask(context.Background(), asynq.NewTask("test:task", []byte("{}")))
	if err != nil {
		t.Fatalf("ProcessTask: %v", err)
	}
	if !called {
		t.Error("registered handler not called")
	}
}

// TestRegister_MultipleTypes 多个 task type 各自路由正确。
func TestRegister_MultipleTypes(t *testing.T) {
	q := &Queue{mux: asynq.NewServeMux()}
	calls := map[string]bool{}
	q.Register("a:1", func(ctx context.Context, task *asynq.Task) error { calls["a"] = true; return nil })
	q.Register("b:1", func(ctx context.Context, task *asynq.Task) error { calls["b"] = true; return nil })

	_ = q.mux.ProcessTask(context.Background(), asynq.NewTask("a:1", nil))
	_ = q.mux.ProcessTask(context.Background(), asynq.NewTask("b:1", nil))

	if !calls["a"] || !calls["b"] {
		t.Errorf("handlers not routed correctly: %+v", calls)
	}
}
