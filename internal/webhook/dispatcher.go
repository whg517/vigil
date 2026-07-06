// Package webhook 实现能力域 14 的 Webhook 出口。
//
// 对应 docs/capabilities/10-integrations-analytics.md §A4：
// 用户订阅 incident 生命周期事件（created/acked/resolved/escalated/reopened/closed/responder_added），
// Vigil 在事件发生时推送到订阅 URL。
//
// 实现：Dispatcher 订阅领域事件总线（event.Bus）的 incident 变更事件，
// 把变更事件 POST 给所有订阅 URL（配置式，后续可扩展为动态订阅表）。
// 推送真异步（独立 goroutine + 独立 context），不阻塞主流程；
// Close() 等待在途推送完成，供优雅关闭调用。
//
// 安全（S13）：非空 signingSecret 时每次出站加 HMAC-SHA256 签名头 + 时间戳，接收端可验源防伪造/防重放。
// 可靠性（C24 死信）：重试耗尽仍失败的投递记 WebhookDelivery(status=failed) 死信，可查可重放，不再静默丢弃。
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/kevin/vigil/ent"
	domainevent "github.com/kevin/vigil/internal/event"
	"github.com/kevin/vigil/internal/incident"
	"github.com/kevin/vigil/internal/metrics"
)

// 签名头常量（接收端按此重算验源）。
const (
	// HeaderSignature 出站签名头：hex(HMAC-SHA256(secret, timestamp + "." + body))。
	HeaderSignature = "X-Vigil-Signature"
	// HeaderTimestamp 出站时间戳头（Unix 秒）：与 body 一同参与签名，接收端按容忍窗口防重放。
	HeaderTimestamp = "X-Vigil-Timestamp"
)

// DeliveryRecorder 出站投递记录器（死信底座，C24）。
//
// 由 webhook 包定义接口、装配层用 ent 实现，避免 dispatcher 直接持 *ent.Client 只为写一张表；
// 也便于测试注入内存桩。全部方法 best-effort：记录失败只应记日志，不影响出站主流程。
type DeliveryRecorder interface {
	// RecordDelivery 记录一次出站投递终态（success/failed）。deliveryID 为 0 表示新投递。
	RecordDelivery(ctx context.Context, rec DeliveryRecord)
}

// DeliveryRecord 一次出站投递的终态快照（供 DeliveryRecorder 落库）。
type DeliveryRecord struct {
	URL            string
	Event          string
	IncidentID     int
	Payload        []byte
	Success        bool
	Attempts       int
	LastError      string
	LastStatusCode int
}

// SubscriptionResolver 出站动态订阅解析器（N2.2）。dispatcher 出站时查 DB 活跃订阅，
// 与 env 静态订阅合并投递。由装配层用 ent 实现（EntSubscriptionResolver），nil 时只用 env（向后兼容）。
//
// 全部 best-effort：解析失败只应记日志/返回空，绝不阻塞出站主流程（出站本就是 best-effort 语义）。
type SubscriptionResolver interface {
	// Resolve 返回当前活跃（enabled）的动态订阅目标列表。
	Resolve(ctx context.Context) []Subscription
}

// Subscription 一条动态订阅目标（N2.2）：URL + 事件类型过滤 + 独立签名密钥。
type Subscription struct {
	URL string
	// EventTypes 订阅的事件类型（如 incident.created）。空=订阅所有事件类型（不过滤）。
	EventTypes []string
	// SigningSecret 该订阅独立的出站签名密钥（HMAC-SHA256）。空=该订阅出站不签名。
	SigningSecret string
}

// matches 判断该订阅是否订阅了 eventName（EventTypes 为空=订阅全部）。
func (s Subscription) matches(eventName string) bool {
	if len(s.EventTypes) == 0 {
		return true
	}
	for _, t := range s.EventTypes {
		if t == eventName {
			return true
		}
	}
	return false
}

// Dispatcher Webhook 出口分发器。
type Dispatcher struct {
	urls   []string // env 静态订阅 URL 列表（VIGIL_WEBHOOK_OUT_URLS，配置式）
	client *http.Client
	wg     sync.WaitGroup // 跟踪在途推送 goroutine，供 Close 等待
	// signingSecret env 静态订阅的全局出站签名密钥（S13）。空=env 订阅不签名（向后兼容）。
	signingSecret string
	// recorder 出站投递记录器（C24 死信）。nil=不记录（向后兼容/测试桩）。
	recorder DeliveryRecorder
	// subs 动态订阅解析器（N2.2）。nil=只用 env 静态订阅（向后兼容）。
	subs SubscriptionResolver
}

// NewDispatcher 创建分发器。urls 为 env 静态订阅 URL 列表（空则仅靠动态订阅推送）。
func NewDispatcher(urls []string) *Dispatcher {
	return &Dispatcher{
		urls:   urls,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// SetSigningSecret 设置 env 静态订阅的全局出站签名密钥（S13）。空串=不签名（向后兼容既有订阅端）。
func (d *Dispatcher) SetSigningSecret(secret string) { d.signingSecret = secret }

// SetDeliveryRecorder 注入出站投递记录器（C24 死信）。nil=不记录。
func (d *Dispatcher) SetDeliveryRecorder(r DeliveryRecorder) { d.recorder = r }

// SetSubscriptionResolver 注入动态订阅解析器（N2.2）。nil=只用 env 静态订阅。
func (d *Dispatcher) SetSubscriptionResolver(r SubscriptionResolver) { d.subs = r }

// HasSubscriptions 是否有 env 静态订阅（无则该源跳过；动态订阅在出站时实时查，不计入此判定）。
func (d *Dispatcher) HasSubscriptions() bool { return len(d.urls) > 0 }

// OnIncidentChanged 实现 incident 变更回调（供 incident.Service.SetOnIncidentChanged 注入）。
// 真异步推送：每个目标独立 goroutine，使用独立 context（脱离请求 ctx，
// 避免请求结束 ctx 取消导致推送中断）。返回不等待推送完成，由 Close() 等待。
//
// N2.2：合并 env 静态订阅（全局签名密钥）与 DB 动态订阅（按事件类型过滤 + 每订阅独立签名密钥）。
// 向后兼容：无动态订阅解析器时退化为仅 env 静态订阅（原行为）。
func (d *Dispatcher) OnIncidentChanged(ctx context.Context, inc *ent.Incident, action incident.Action) {
	eventName := fmt.Sprintf("incident.%s", action)

	// 合并投递目标：env 静态（全局密钥，不过滤事件类型） + DB 动态（按事件类型过滤，独立密钥）。
	targets := d.resolveTargets(ctx, eventName)
	if len(targets) == 0 {
		return
	}

	payload := map[string]any{
		"event":       eventName,
		"incident_id": inc.ID,
		"incident":    inc.Number,
		"status":      string(inc.Status),
		"severity":    string(inc.Severity),
		"title":       inc.Title,
		"summary":     inc.Summary,
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
	}
	body, _ := json.Marshal(payload)

	// 每个目标独立 goroutine 推送，wg 跟踪以便 Close 等待
	for _, tgt := range targets {
		d.wg.Add(1)
		go func(t deliveryTarget) {
			defer d.wg.Done()
			// 用独立 context.Background()，不被请求生命周期绑定
			d.push(context.Background(), t, eventName, inc.ID, body)
		}(tgt)
	}
}

// deliveryTarget 一个出站投递目标：URL + 该目标的签名密钥（env 用全局密钥，动态订阅用各自密钥）。
type deliveryTarget struct {
	url    string
	secret string
}

// resolveTargets 合并 env 静态订阅与 DB 动态订阅为本次事件的投递目标列表。
//   - env 静态订阅：全部投递（不按事件类型过滤，向后兼容全量语义），用全局 signingSecret。
//   - DB 动态订阅：按 eventName 过滤（EventTypes 为空=全部），用各订阅独立 SigningSecret。
//
// 去重：同一 URL 若既在 env 又在动态订阅命中，只投递一次（以先出现的 env 目标为准），
// 避免重复投递同一订阅端。
func (d *Dispatcher) resolveTargets(ctx context.Context, eventName string) []deliveryTarget {
	seen := make(map[string]bool)
	var targets []deliveryTarget
	// env 静态订阅（全局密钥）。
	for _, u := range d.urls {
		if seen[u] {
			continue
		}
		seen[u] = true
		targets = append(targets, deliveryTarget{url: u, secret: d.signingSecret})
	}
	// DB 动态订阅（按事件类型过滤 + 独立密钥）。
	if d.subs != nil {
		for _, s := range d.subs.Resolve(ctx) {
			if s.URL == "" || seen[s.URL] || !s.matches(eventName) {
				continue
			}
			seen[s.URL] = true
			targets = append(targets, deliveryTarget{url: s.URL, secret: s.SigningSecret})
		}
	}
	return targets
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

// push 推送单个目标（含重试）。全部重试失败进死信（若配置了 recorder）。
// tgt.secret 为该目标的签名密钥（env 用全局密钥，动态订阅用各自密钥），空则该目标出站不签名。
func (d *Dispatcher) push(ctx context.Context, tgt deliveryTarget, eventName string, incidentID int, body []byte) {
	const maxRetries = 3
	var lastErr error
	lastStatus := 0
	attempts := 0
	for attempt := 0; attempt < maxRetries; attempt++ {
		attempts++
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, tgt.url, bytes.NewReader(body))
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "Vigil-Webhook/1.0")
		// S13：非空密钥时加签名头（时间戳 + body 一同签，接收端验源 + 防重放）。
		signRequest(req, tgt.secret, body)
		resp, err := d.client.Do(req)
		if err != nil {
			lastErr = err
			lastStatus = 0
			// 退避：线性（1s, 2s, 3s），可被 ctx 取消
			if !sleepWithContext(ctx, time.Duration(attempt+1)*time.Second) {
				break
			}
			continue
		}
		_ = resp.Body.Close()
		lastStatus = resp.StatusCode
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			metrics.NotificationsSent.WithLabelValues("webhook_out", "success").Inc()
			d.record(ctx, tgt.url, eventName, incidentID, body, true, attempts, "", lastStatus)
			return
		}
		lastErr = fmt.Errorf("status %d", resp.StatusCode)
	}
	// 全部失败：记埋点 + 落死信（可查可重放），不阻塞主流程
	metrics.NotificationsSent.WithLabelValues("webhook_out", "failed").Inc()
	errStr := ""
	if lastErr != nil {
		errStr = lastErr.Error()
	}
	d.record(ctx, tgt.url, eventName, incidentID, body, false, attempts, errStr, lastStatus)
}

// SendResult 单次同步投递的结果（供重放端点判定成功/失败并回写记录）。
type SendResult struct {
	Success    bool
	StatusCode int // 0=连接失败未拿到响应
	Err        error
}

// SendOnce 同步向单个 URL 投递一次（不重试），返回结果。供死信重放复用出站签名逻辑。
//
// 与 push 的区别：push 是事件驱动的异步扇出（带重试 + 落记录）；SendOnce 是重放端点
// 的同步单发（重试与记录回写由调用方按重放语义控制——重放是人工触发，即时反馈成败即可）。
func (d *Dispatcher) SendOnce(ctx context.Context, url string, body []byte) SendResult {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return SendResult{Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Vigil-Webhook/1.0")
	// 重放沿用全局签名密钥（重放的是 env 静态订阅的死信；动态订阅 payload 也用同一算法）。
	signRequest(req, d.signingSecret, body)
	resp, err := d.client.Do(req)
	if err != nil {
		return SendResult{Err: err}
	}
	_ = resp.Body.Close()
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	result := "failed"
	if ok {
		result = "success"
	}
	metrics.NotificationsSent.WithLabelValues("webhook_out", result).Inc()
	return SendResult{Success: ok, StatusCode: resp.StatusCode}
}

// signRequest 给请求加 HMAC-SHA256 签名头（S13）。空密钥时 no-op（向后兼容）。
// 密钥参数化：env 静态订阅用全局密钥，动态订阅用各自密钥（N2.2），各目标独立签名。
//
// 签名基串 = timestamp + "." + body：把时间戳纳入签名，使接收端能在验签同时判定时效
// （拒绝超出容忍窗口的旧请求 → 防重放）。签名值为 hex 编码，头名见 HeaderSignature/HeaderTimestamp。
func signRequest(req *http.Request, secret string, body []byte) {
	if secret == "" {
		return
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))
	req.Header.Set(HeaderTimestamp, ts)
	req.Header.Set(HeaderSignature, sig)
}

// record best-effort 落投递记录（C24 死信/审计）。无 recorder 时 no-op。
func (d *Dispatcher) record(ctx context.Context, url, eventName string, incidentID int, body []byte, success bool, attempts int, errStr string, statusCode int) {
	if d.recorder == nil {
		return
	}
	d.recorder.RecordDelivery(ctx, DeliveryRecord{
		URL:            url,
		Event:          eventName,
		IncidentID:     incidentID,
		Payload:        body,
		Success:        success,
		Attempts:       attempts,
		LastError:      errStr,
		LastStatusCode: statusCode,
	})
}

// Sign 计算出站签名（供接收端/测试对拍复用同一算法）。返回 (timestamp, hexSignature)。
// 独立导出便于接收端库与测试用同一实现验签，避免各处重复实现导致口径漂移。
func Sign(secret string, body []byte, t time.Time) (timestamp, signature string) {
	ts := strconv.FormatInt(t.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)
	return ts, hex.EncodeToString(mac.Sum(nil))
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
