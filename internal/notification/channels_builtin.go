// channels_builtin.go 内置通知通道。
package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/smtp"
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
	// C3：未路由兜底通知无关联 Incident（msg.Incident 可为 nil），号名/编号留空，不 deref。
	eventName, incNumber := "incident.escalated", ""
	if msg.Incident != nil {
		incNumber = msg.Incident.Number
	} else {
		eventName = "event.unrouted"
	}
	payload, _ := json.Marshal(map[string]any{
		"event":      eventName,
		"incident":   incNumber,
		"title":      msg.Title,
		"summary":    msg.Summary,
		"level":      msg.Level,
		"action_url": msg.ActionURL,
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
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
		_ = resp.Body.Close()
		ok := resp.StatusCode >= 200 && resp.StatusCode < 300
		r := SendResult{Channel: "webhook", Target: u, Success: ok}
		if !ok {
			r.Error = fmt.Sprintf("status %d", resp.StatusCode)
		}
		results = append(results, r)
	}
	return results, nil
}

// EmailChannel 邮件通知通道（能力域 7 M7.3）。
// 用 net/smtp 发送（标准库，无额外依赖）。
// SMTPConfig 未配置（Host 空）时降级为不发送（设计基线第 7 条，可用性优先）。
type EmailChannel struct {
	Config    SMTPConfig                      // SMTP 服务器配置
	GetEmails func(targets []Target) []string // 从 targets 解析 email 列表
	From      string                          // 发件人地址
}

// SMTPConfig SMTP 服务器配置。
type SMTPConfig struct {
	Host     string // SMTP 服务器地址（如 smtp.example.com），空=邮件通道禁用
	Port     int    // 端口（25/465/587）
	Username string // 认证用户名（空=匿名发送）
	Password string // 认证密码
}

// SMTPAddr 返回 host:port。
func (s SMTPConfig) SMTPAddr() string {
	port := s.Port
	if port == 0 {
		port = 25
	}
	return fmt.Sprintf("%s:%d", s.Host, port)
}

// Available SMTP 是否已配置（Host 非空）。
func (e *EmailChannel) Available() bool { return e.Config.Host != "" }

func (EmailChannel) Name() string { return "email" }

func (e *EmailChannel) Send(ctx context.Context, msg *Message) ([]SendResult, error) {
	if !e.Available() || e.GetEmails == nil {
		return nil, nil // 未配置降级：不发送
	}
	emails := e.GetEmails(msg.Targets)
	if len(emails) == 0 {
		return nil, nil
	}
	from := e.From
	if from == "" {
		from = "vigil@localhost"
	}
	subject := msg.Title
	if subject == "" && msg.Incident != nil {
		subject = fmt.Sprintf("[Vigil] %s", msg.Incident.Number)
	}
	body := msg.Summary
	if body == "" && msg.Incident != nil {
		body = msg.Incident.Summary
	}

	var auth smtp.Auth
	if e.Config.Username != "" {
		auth = smtp.PlainAuth("", e.Config.Username, e.Config.Password, e.Config.Host)
	}

	var results []SendResult
	for _, addr := range emails {
		// RFC 5322 邮件格式：From/To/Subject 头 + 空行 + 正文
		message := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n",
			from, addr, subject, body)
		err := smtp.SendMail(e.Config.SMTPAddr(), auth, from, []string{addr}, []byte(message))
		if err != nil {
			results = append(results, SendResult{Channel: "email", Target: addr, Error: err.Error()})
			continue
		}
		results = append(results, SendResult{Channel: "email", Target: addr, Success: true})
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
