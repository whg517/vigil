// Package webhook 实现能力域 14 的 Webhook 出口。
//
// 对应 docs/capabilities/10-integrations-analytics.md §A4：
// 用户订阅 incident 生命周期事件（created/acked/resolved/escalated），
// Vigil 在事件发生时推送到订阅 URL。
//
// 实现：Dispatcher 订阅领域事件总线（event.Bus）的 incident 变更事件，
// 把变更事件 POST 给所有订阅 URL（配置式，后续可扩展为动态订阅表）。
// 推送真异步（独立 goroutine + 独立 context），不阻塞主流程；
// Close() 等待在途推送完成，供优雅关闭调用。
package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/kevin/vigil/ent"
	domainevent "github.com/kevin/vigil/internal/event"
	"github.com/kevin/vigil/internal/incident"
	"github.com/kevin/vigil/internal/metrics"
)

// Dispatcher Webhook 出口分发器。
type Dispatcher struct {
	urls   []string // 订阅 URL 列表（配置式）
	client *http.Client
	wg     sync.WaitGroup // 跟踪在途推送 goroutine，供 Close 等待
}

// NewDispatcher 创建分发器。urls 为订阅 URL 列表（空则不推送）。
func NewDispatcher(urls []string) *Dispatcher {
	return &Dispatcher{
		urls:   urls,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// HasSubscriptions 是否有订阅（无则跳过，避免无谓开销）。
func (d *Dispatcher) HasSubscriptions() bool { return len(d.urls) > 0 }

// OnIncidentChanged 实现 incident 变更回调（供 incident.Service.SetOnIncidentChanged 注入）。
// 真异步推送：每个 URL 独立 goroutine，使用独立 context（脱离请求 ctx，
// 避免请求结束 ctx 取消导致推送中断）。返回不等待推送完成，由 Close() 等待。
func (d *Dispatcher) OnIncidentChanged(_ context.Context, inc *ent.Incident, action incident.Action) {
	if !d.HasSubscriptions() {
		return
	}
	payload := map[string]any{
		"event":       fmt.Sprintf("incident.%s", action),
		"incident_id": inc.ID,
		"incident":    inc.Number,
		"status":      string(inc.Status),
		"severity":    string(inc.Severity),
		"title":       inc.Title,
		"summary":     inc.Summary,
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
	}
	body, _ := json.Marshal(payload)

	// 每个 URL 独立 goroutine 推送，wg 跟踪以便 Close 等待
	for _, u := range d.urls {
		d.wg.Add(1)
		go func(url string) {
			defer d.wg.Done()
			// 用独立 context.Background()，不被请求生命周期绑定
			d.push(context.Background(), url, body)
		}(u)
	}
}

// OnIncidentEvent 领域事件适配：收到 incident 变更事件时转发给 OnIncidentChanged。
// 实现 event.Handler，供装配时 bus.Subscribe 挂载（替代旧的 SetOnIncidentChanged 回调注入）。
func (d *Dispatcher) OnIncidentEvent(ctx context.Context, e domainevent.Event) error {
	if e.Incident == nil {
		return nil
	}
	d.OnIncidentChanged(ctx, e.Incident, incident.Action(e.Action))
	return nil
}

// Close 等待所有在途推送完成。供优雅关闭调用（main.go shutdown 时）。
func (d *Dispatcher) Close() {
	d.wg.Wait()
}

// push 推送单个 URL（含重试）。
func (d *Dispatcher) push(ctx context.Context, url string, body []byte) {
	const maxRetries = 3
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "Vigil-Webhook/1.0")
		resp, err := d.client.Do(req)
		if err != nil {
			lastErr = err
			// 退避：线性（1s, 2s, 3s），可被 ctx 取消
			if !sleepWithContext(ctx, time.Duration(attempt+1)*time.Second) {
				return
			}
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			metrics.NotificationsSent.WithLabelValues("webhook_out", "success").Inc()
			return
		}
		lastErr = fmt.Errorf("status %d", resp.StatusCode)
	}
	// 全部失败：记埋点，不阻塞主流程
	metrics.NotificationsSent.WithLabelValues("webhook_out", "failed").Inc()
	_ = lastErr
}

// sleepWithContext 可被 ctx 取消的 sleep，返回 false 表示被取消。
func sleepWithContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
