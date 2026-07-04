// channels_phone.go 电话/SMS 通知通道（能力域 7 M7.2）。
//
// 本期做抽象层 + 占位实现（不接真实云厂商）：
//   - 占位实现：把通知 POST 到配置的 webhook URL（用户可自行对接阿里云/腾讯云语音 API）
//   - 无配置时降级为不发送
//
// 真实云厂商对接（阿里云/腾讯云语音 API）留 TODO.md，本期不绑定具体厂商。
// 这样既让通道注册表完整（升级兜底链可用 phone/sms），又不引入云厂商 SDK 依赖。
package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// PhoneChannel 电话通知通道（占位：转发到 webhook 供用户对接云语音）。
// PRD M7.2：电话是强打扰，仅用于升级兜底。
type PhoneChannel struct {
	Client    *http.Client
	Config    VoiceProviderConfig             // 云语音配置（webhook 占位）
	GetPhones func(targets []Target) []string // 从 targets 解析电话号码列表
}

// SMSChannel 短信通知通道（占位：同 PhoneChannel，转发 webhook）。
type SMSChannel struct {
	Client    *http.Client
	Config    VoiceProviderConfig
	GetPhones func(targets []Target) []string
}

// VoiceProviderConfig 语音/SMS 提供商配置（占位）。
// WebhookURL 非空时，通知会 POST 到此 URL（payload 含事件 + 电话号码），
// 用户在 webhook 端对接阿里云/腾讯云等语音 API。
type VoiceProviderConfig struct {
	WebhookURL string // 语音/SMS 接收端点（用户对接云厂商的中间层）
	From       string // 发件/主叫标识（可选）
}

// Available 电话通道是否启用（配了 webhook）。
func (p *PhoneChannel) Available() bool { return p != nil && p.Config.WebhookURL != "" }
func (p *SMSChannel) Available() bool   { return p != nil && p.Config.WebhookURL != "" }

func (PhoneChannel) Name() string { return "phone" }
func (SMSChannel) Name() string   { return "sms" }

func (p *PhoneChannel) Send(ctx context.Context, msg *Message) ([]SendResult, error) {
	return sendVoice(ctx, "phone", p.Config, p.Client, p.GetPhones, msg)
}

func (p *SMSChannel) Send(ctx context.Context, msg *Message) ([]SendResult, error) {
	return sendVoice(ctx, "sms", p.Config, p.Client, p.GetPhones, msg)
}

// sendVoice 电话/SMS 占位发送：POST 到 webhook URL，payload 含事件信息 + 号码列表。
// phone 和 sms 共用此逻辑，仅 channel 标识不同。
func sendVoice(ctx context.Context, channel string, cfg VoiceProviderConfig, client *http.Client, getPhones func([]Target) []string, msg *Message) ([]SendResult, error) {
	if cfg.WebhookURL == "" || getPhones == nil {
		return nil, nil // 未配置降级
	}
	phones := getPhones(msg.Targets)
	if len(phones) == 0 {
		return nil, nil
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	// C3：未路由兜底通知无关联 Incident（msg.Incident 可为 nil），编号留空不 deref。
	incNumber := ""
	if msg.Incident != nil {
		incNumber = msg.Incident.Number
	}
	payload, _ := json.Marshal(map[string]any{
		"channel":    channel,
		"incident":   incNumber,
		"title":      msg.Title,
		"summary":    msg.Summary,
		"level":      msg.Level,
		"recipients": phones,
		"from":       cfg.From,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.WebhookURL, bytes.NewReader(payload))
	if err != nil {
		return singleErr(channel, cfg.WebhookURL, err), nil
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return singleErr(channel, cfg.WebhookURL, err), nil
	}
	_ = resp.Body.Close()
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	r := SendResult{Channel: channel, Target: cfg.WebhookURL, Success: ok}
	if !ok {
		r.Error = fmt.Sprintf("status %d", resp.StatusCode)
	}
	return []SendResult{r}, nil
}

// singleErr 构造单个失败结果。
func singleErr(channel, target string, err error) []SendResult {
	return []SendResult{{Channel: channel, Target: target, Error: err.Error()}}
}
