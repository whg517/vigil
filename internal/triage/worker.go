// worker.go 分诊异步任务。
package triage

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/hibiken/asynq"
)

// TaskTriage 分诊任务类型名。
const TaskTriage = "vigil:triage"

// triagePayload 分诊任务 payload。
type triagePayload struct {
	EventID int `json:"event_id"`
}

// Worker 分诊 worker：消费分诊任务，对 Event 执行 去重→路由→聚合。
type Worker struct {
	engine *Engine
}

// NewWorker 创建分诊 worker。
func NewWorker(engine *Engine) *Worker {
	return &Worker{engine: engine}
}

// EnqueueTask 构造并返回分诊任务（供 ingestion 调用入队）。
func EnqueueTask(eventID int) (*asynq.Task, error) {
	payload, err := json.Marshal(triagePayload{EventID: eventID})
	if err != nil {
		return nil, fmt.Errorf("marshal triage payload: %w", err)
	}
	return asynq.NewTask(TaskTriage, payload), nil
}

// Handle 处理分诊任务（注册到 queue）。
func (w *Worker) Handle(ctx context.Context, t *asynq.Task) error {
	var p triagePayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("unmarshal triage payload: %w", err)
	}
	slog.Info("triage worker: processing", "event_id", p.EventID)
	res, err := w.engine.Process(ctx, p.EventID)
	if err != nil {
		slog.Error("triage worker: process failed", "event_id", p.EventID, "error", err)
		return fmt.Errorf("triage event %d: %w", p.EventID, err)
	}
	slog.Info("triage worker: done", "event_id", p.EventID, "action", res.Action, "incident_id", res.IncidentID)
	return nil
}
