// channels_builtin.go 内置通知通道。
package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/kevin/vigil/ent"
)

// WebhookChannel Webhook 通知通道。
// 把通知 payload POST 到用户配置的 webhook URL。
// 配置（URL 列表）由 Notifier 根据团队/事件解析后传入。
type WebhookChannel struct {
	Client  *http.Client
	GetURLs func(inc *ent.Incident) []string // 解析该事件应推送的 webhook URL 列表
}

// NewWebhookChannel 创建 Webhook 通道。getURLs 返回应推送的 URL。
func NewWebhookChannel(getURLs func(inc *ent.Incident) []string) *WebhookChannel {
	return &WebhookChannel{
		Client:  &http.Client{Timeout: 10 * time.Second},
		GetURLs: getURLs,
	}
}

func (WebhookChannel) Name() string { return "webhook" }

func (w *WebhookChannel) Send(ctx context.Context, msg *Message) ([]SendResult, error) {
	if w.GetURLs == nil {
		return nil, nil
	}
	urls := w.GetURLs(msg.Incident)
	if len(urls) == 0 {
		return nil, nil
	}
	payload, _ := json.Marshal(map[string]any{
		"event":     "incident.escalated",
		"incident":  msg.Incident.Number,
		"title":     msg.Title,
		"summary":   msg.Summary,
		"level":     msg.Level,
		"action_url": msg.ActionURL,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})

	var results []SendResult
	for _, u := range urls {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
		if err != nil {
			results = append(results, SendResult{Channel: "webhook", Target: u, Error: err.Error()})
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := w.Client.Do(req)
		if err != nil {
			results = append(results, SendResult{Channel: "webhook", Target: u, Error: err.Error()})
			continue
		}
		resp.Body.Close()
		ok := resp.StatusCode >= 200 && resp.StatusCode < 300
		r := SendResult{Channel: "webhook", Target: u, Success: ok}
		if !ok {
			r.Error = fmt.Sprintf("status %d", resp.StatusCode)
		}
		results = append(results, r)
	}
	return results, nil
}

// EmailChannel 邮件通知通道（占位实现）。
// 真实 SMTP 发送待接入邮件库；当前仅按 target 模拟发送结果，保证通道注册可用。
type EmailChannel struct {
	GetEmails func(targets []Target) []string // 从 targets 解析 email 列表
}

func (EmailChannel) Name() string { return "email" }

func (e *EmailChannel) Send(ctx context.Context, msg *Message) ([]SendResult, error) {
	if e.GetEmails == nil {
		return nil, nil
	}
	emails := e.GetEmails(msg.Targets)
	var results []SendResult
	for _, addr := range emails {
		// TODO: 接入 SMTP 实际发送（net/smtp 或 gomail）
		// 当前模拟成功，保证通道链路通畅
		results = append(results, SendResult{
			Channel: "email",
			Target:  addr,
			Success: true,
		})
	}
	return results, nil
}

// FormatTitle 格式化通知标题：[severity] INC-xxxx summary。
func FormatTitle(inc *ent.Incident) string {
	sev := strings.ToUpper(string(inc.Severity))
	return fmt.Sprintf("[%s] %s %s", sev, inc.Number, inc.Title)
}

// FormatSummary 格式化通知摘要。
func FormatSummary(inc *ent.Incident, level int) string {
	return fmt.Sprintf("事件 %s 已升级到 level %d。%s", inc.Number, level+1, inc.Summary)
}
