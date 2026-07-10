// Package dingtalk 实现钉钉（DingTalk）IM 平台的真实适配器。
//
// 钉钉与飞书并列能力域 8 P0（capabilities §2），是企业用户主战场。
// 本包接入企业内部应用机器人，覆盖 IMBot 全部能力：
// 交互卡片✅（ActionCard）单聊✅ 群聊✅ @人✅ 回调校验✅。
// 卡片更新钉钉支持卡片数据更新（cardData update）；不支持场景降级为发新消息（§10 降级矩阵）。
//
// 鉴权：企业内部应用 access_token（appKey+appSecret 换 token，7200s 有效，本地缓存）。
// 回调签名校验：事件订阅用 aes_key（AES-256-CBC 解密）+ token（HMAC-SHA256 校验 x-dingtalk-signature）。
//
// 注意钉钉与飞书的关键差异：
//   - 双域名：oapi.dingtalk.com（旧版 gettoken）与 api.dingtalk.com（新版 v1.0 业务 API）。
//   - 鉴权头：v1.0 用 x-acs-dingtalk-access_token；旧版用 access_token 查询参数。
//   - 标识：用户 staffId/openUserId/userId，群 openConversationId（非飞书的 chat_id/open_id）。
//
// 凭证从 config 读，未配置时 Available()==false，降级为不发送
// （设计基线第 7 条：缺凭证自动降级，不阻断主流程）。
package dingtalk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const (
	// oapiBase 钉钉旧版域名（gettoken 等走这里）。
	oapiBase = "https://oapi.dingtalk.com"
	// apiBase 钉钉新版域名（v1.0 业务 API：发消息等走这里）。
	apiBase = "https://api.dingtalk.com"
)

// Config 钉钉适配器配置。
type Config struct {
	// AppKey 企业内部应用 AppKey（开发者后台获取）。
	AppKey string
	// AppSecret 企业内部应用 AppSecret。
	AppSecret string
	// RobotCode 机器人编码（机器人消息接口必填，即 AppKey）。
	RobotCode string
	// Token 事件订阅校验 token（明文校验，对应飞书 VerificationToken）。
	Token string
	// AesKey 事件订阅加密密钥（AES-256-CBC，base64 编码的 43 位串），空表示不加密。
	AesKey string
	// OapiBase 旧版域名，默认 oapi.dingtalk.com（测试可换）。
	OapiBase string
	// APIBase 新版域名，默认 api.dingtalk.com（测试可换）。
	APIBase string
}

// Client 钉钉 OpenAPI 客户端：封装 access_token 获取/缓存 + 通用请求。
type Client struct {
	cfg  Config
	http *http.Client

	mu       sync.Mutex
	token    string
	tokenExp time.Time
}

// NewClient 创建钉钉客户端。域名留空时用默认值。
func NewClient(cfg Config) *Client {
	if cfg.OapiBase == "" {
		cfg.OapiBase = oapiBase
	}
	if cfg.APIBase == "" {
		cfg.APIBase = apiBase
	}
	// RobotCode 缺省等于 AppKey（钉钉约定：机器人编码即 AppKey）。
	if cfg.RobotCode == "" {
		cfg.RobotCode = cfg.AppKey
	}
	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: 10 * time.Second},
	}
}

// Available 是否可用：AppKey + AppSecret 均配置才算就绪。
func (c *Client) Available() bool {
	return c.cfg.AppKey != "" && c.cfg.AppSecret != ""
}

// RobotCode 暴露机器人编码（发消息接口 body 必填）。
func (c *Client) RobotCode() string { return c.cfg.RobotCode }

// Token 暴露事件订阅校验 token（adapter 做回调校验用）。
func (c *Client) Token() string { return c.cfg.Token }

// AesKey 暴露事件订阅加密密钥（adapter 做回调解密用）。
func (c *Client) AesKey() string { return c.cfg.AesKey }

// tokenResponse gettoken 接口响应。
type tokenResponse struct {
	ErrCode     int    `json:"errcode"`
	ErrMsg      string `json:"errmsg"`
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"` // 秒
}

// accessToken 获取（带缓存）有效的 access_token。
// gettoken 接口走旧版域名 oapi.dingtalk.com/gettoken?appkey=&appsecret=。
func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// 缓存有效（提前 5 分钟刷新，避免边界过期）
	if c.token != "" && time.Now().Before(c.tokenExp.Add(-5*time.Minute)) {
		return c.token, nil
	}
	u := c.cfg.OapiBase + "/gettoken?appkey=" + url.QueryEscape(c.cfg.AppKey) +
		"&appsecret=" + url.QueryEscape(c.cfg.AppSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("dingtalk token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", fmt.Errorf("dingtalk token decode: %w", err)
	}
	if tr.ErrCode != 0 {
		return "", fmt.Errorf("dingtalk token error: code=%d msg=%s", tr.ErrCode, tr.ErrMsg)
	}
	c.token = tr.AccessToken
	// expires_in 缺省按 7200s 处理
	exp := tr.ExpiresIn
	if exp <= 0 {
		exp = 7200
	}
	c.tokenExp = time.Now().Add(time.Duration(exp) * time.Second)
	return c.token, nil
}

// apiResponse v1.0 业务 API 通用响应（失败时带 code/message）。
// 钉钉 v1.0 接口失败时 HTTP 状态码非 2xx，body 形如 {"code":"...","message":"...","requestid":"..."}。
type apiResponse struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	// 成功时各接口有不同业务字段，用 RawMessage 透传给调用方解析
	Process struct {
		AckTaskID string `json:"ackTaskID"` // 机器人发消息返回的任务 ID
	} `json:"process,omitempty"`
}

// doV1 执行一次 v1.0 业务 API 请求（走 api.dingtalk.com），鉴权头 x-acs-dingtalk-access-token。
// 成功响应体（含业务字段）原样返回供调用方解析。
func (c *Client) doV1(ctx context.Context, method, path string, payload any) (json.RawMessage, error) {
	token, err := c.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.APIBase+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-acs-dingtalk-access-token", token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dingtalk api %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		// 尝试解析错误信息
		var er apiResponse
		_ = json.Unmarshal(raw, &er)
		msg := er.Message
		if msg == "" {
			msg = string(raw)
		}
		return nil, fmt.Errorf("dingtalk api %s http %d: %s", path, resp.StatusCode, msg)
	}
	return raw, nil
}

// --- 发消息（单聊 / 群聊）---

// sendResponse 机器人发消息响应（单聊/群聊通用）。
// 单聊 batchSend：{"processQueryKey":"..."}；群聊 send：{"messageId":"..."}。
type sendResponse struct {
	ProcessQueryKey string `json:"processQueryKey"`
	MessageID       string `json:"messageId"`
}

// SendOTOMessages 批量发单聊消息。
// openAPI: POST /v1.0/robot/oToMessages/batchSend
// userIds 为接收人 staffId 列表；msgKey 消息类型；msgParam 消息内容（JSON 字符串）。
// 返回 processQueryKey（用于追踪/撤回），作卡片 ID 供后续 UpdateCard。
func (c *Client) SendOTOMessages(ctx context.Context, userIds []string, msgKey, msgParam string) (string, error) {
	payload := map[string]any{
		"robotCode": c.cfg.RobotCode,
		"userIds":   userIds,
		"msgKey":    msgKey,
		"msgParam":  msgParam,
	}
	raw, err := c.doV1(ctx, http.MethodPost, "/v1.0/robot/oToMessages/batchSend", payload)
	if err != nil {
		return "", err
	}
	var r sendResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", fmt.Errorf("dingtalk oto send decode: %w", err)
	}
	// 单聊用 processQueryKey 作卡片标识（钉钉单聊无独立 messageId，用 queryKey 追踪）
	id := r.ProcessQueryKey
	if id == "" {
		id = r.MessageID
	}
	return id, nil
}

// SendGroupMessage 机器人向群发消息。
// openAPI: POST /v1.0/robot/groupMessages/send
// openConversationId 群会话 ID；msgKey 消息类型；msgParam 消息内容（JSON 字符串）。
// 返回 messageId，作卡片 ID 供后续 UpdateCard。
func (c *Client) SendGroupMessage(ctx context.Context, openConversationID, msgKey, msgParam string) (string, error) {
	payload := map[string]any{
		"robotCode":          c.cfg.RobotCode,
		"openConversationId": openConversationID,
		"msgKey":             msgKey,
		"msgParam":           msgParam,
	}
	raw, err := c.doV1(ctx, http.MethodPost, "/v1.0/robot/groupMessages/send", payload)
	if err != nil {
		return "", err
	}
	var r sendResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", fmt.Errorf("dingtalk group send decode: %w", err)
	}
	return r.MessageID, nil
}
