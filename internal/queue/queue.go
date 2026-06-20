// Package queue 封装 Asynq 异步任务客户端与服务端。
//
// 对应 tech-stack.md §3.4：选用 Asynq（Go + Redis），承载 Vigil 的五类异步任务：
// · 事件流水线任务（接入归一化）
// · 延迟任务（升级计时 ★）
// · 定时任务（排班换班/报表聚合）
// · 通知重试任务
// · 长耗时/AI 任务
//
// 设计：业务模块通过 Queue 拿到 Client（入队）与注册 Handler（消费），
// 幂等键由业务在任务 payload 中保证（at-least-once 语义）。
package queue

import (
	"context"
	"fmt"

	"github.com/kevin/vigil/internal/config"

	"github.com/hibiken/asynq"
)

// Queue 聚合 Asynq 客户端与服务端。
// Client 用于业务入队；Server 用于消费（业务通过 Register 挂载 handler）。
type Queue struct {
	Client *asynq.Client
	Server *asynq.Server
	mux    *asynq.ServeMux
}

// New 创建 Queue。Client 立即可用；Server 由调用方 Start() 启动。
func New(cfg *config.Config) *Queue {
	redisOpt := asynq.RedisClientOpt{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	}

	// Server 配置：并发数来自配置；高优先级队列 escalation 优先消费（升级任务关键）
	srv := asynq.NewServer(redisOpt, asynq.Config{
		Concurrency: cfg.Asynq.Concurrency,
		Queues: map[string]int{
			"critical": 6, // 升级/核心告警（绝不能延迟）
			"default":  3, // 接入流水线/通知
			"low":      1, // 报表聚合/AI 等可延迟任务
		},
	})

	return &Queue{
		Client: asynq.NewClient(redisOpt),
		Server: srv,
		mux:    asynq.NewServeMux(),
	}
}

// Register 注册任务处理函数。
// 业务模块在启动时调用，把 task type 映射到 handler。
// func HandleX(ctx, *asynq.Task) error
func (q *Queue) Register(typeName string, handler func(context.Context, *asynq.Task) error) {
	q.mux.HandleFunc(typeName, handler)
}

// Start 启动消费服务（阻塞直到 Shutdown）。
func (q *Queue) Start() error {
	if err := q.Server.Start(q.mux); err != nil {
		return fmt.Errorf("start asynq server: %w", err)
	}
	return nil
}

// Shutdown 优雅停止消费服务。
func (q *Queue) Shutdown() {
	q.Server.Shutdown()
}

// Close 关闭客户端连接。
func (q *Queue) Close() error {
	return q.Client.Close()
}
