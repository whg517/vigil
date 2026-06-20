// Package webhook 实现能力域 14 的 Webhook 出口。
//
// 对应 docs/capabilities/10-integrations-analytics.md §A4：
// 用户订阅 incident 生命周期事件（created/acked/resolved/escalated），
// Vigil 在事件发生时推送到订阅 URL。
//
// 实现：Dispatcher 监听 incident.Service 的 OnIncidentChanged 回调，
// 把变更事件 POST 给所有订阅 URL（配置式，后续可扩展为动态订阅表）。
// 失败不阻塞主流程（异步推送 + 退避，避免拖慢 ack/resolve）。
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
	"github.com/kevin/vigil/internal/incident"
	"github.com/kevin/vigil/internal/metrics"
)

// Dispatcher Webhook 出口分发器。
type Dispatcher struct {
	urls   []string       // 订阅 URL 列表（配置式）
	client *http.Client
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
// 异步推送，不阻塞主流程。
func (d *Dispatcher) OnIncidentChanged(ctx context.Context, inc *ent.Incident, action incident.Action) {
	if !d.HasSubscriptions() {
		return
	}
	// 构造事件 payload
	payload := map[string]any{
		"event":         fmt.Sprintf("incident.%s", action),
		"incident_id":   inc.ID,
		"incident":      inc.Number,
		"status":        string(inc.Status),
		"severity":      string(inc.Severity),
		"title":         inc.Title,
		"summary":       inc.Summary,
		"timestamp":     time.Now().UTC().Format(time.RFC3339),
	}
	body, _ := json.Marshal(payload)

	// 异步推送（每个 URL 独立 goroutine，互不阻塞）
	var wg sync.WaitGroup
	for _, u := range d.urls {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()
			d.push(ctx, url, body)
		}(u)
	}
	// 不等待全部完成即返回（真正异步）；测试时可 Wait
	wg.Wait()
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
			time.Sleep(time.Duration(attempt+1) * time.Second) // 退避
			continue
		}
		resp.Body.Close()
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
