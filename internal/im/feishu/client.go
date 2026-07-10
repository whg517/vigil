// Package feishu 实现飞书（Lark）IM 平台的真实适配器。
//
// 飞书是本期唯一接入真实 API 的 IM 平台（capabilities §2 P0），
// 因其卡片能力最全：交互卡片✅ 卡片更新✅ @人✅ 命令机器人✅。
//
// 鉴权：tenant_access_token 模式（app_id + app_secret 换 token，2h 有效期，本地缓存）。
// 回调签名校验：飞书事件订阅 v2 用 EncryptKey（AES）+ VerificationToken（明文校验）。
//
// 凭证从 config 读，未配置时 Available()==false，降级为不发送
// （设计基线第 7 条：缺凭证自动降级，不阻断主流程）。
package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// tokenTTL 飞书 tenant_access_token 有效期（2 小时），提前 5 分钟刷新。
const tokenTTL = 2 * time.Hour

// Config 飞书适配器配置。
type Config struct {
	AppID             string
	AppSecret         string
	VerificationToken string // 事件订阅校验 token
	EncryptKey        string // 事件订阅加密密钥（AES-256-CBC），空表示不加密
	BaseURL           string // 飞书 OpenAPI 根，默认 https://open.feishu.cn/open-apis
}

// Client 飞书 OpenAPI 客户端：封装 token 获取/缓存 + 通用请求。
type Client struct {
	cfg  Config
	http *http.Client

	mu       sync.Mutex
	token    string
	tokenExp time.Time
}

// NewClient 创建飞书客户端。BaseURL 为空时用默认开放平台地址。
func NewClient(cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://open.feishu.cn/open-apis"
	}
	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: 10 * time.Second},
	}
}

// Available 是否可用：AppID + AppSecret 均配置才算就绪。
func (c *Client) Available() bool {
	return c.cfg.AppID != "" && c.cfg.AppSecret != ""
}

// tokenResponse tenant_access_token 接口响应。
type tokenResponse struct {
	Code              int    `json:"code"`
	Msg               string `json:"msg"`
	TenantAccessToken string `json:"tenant_access_token"`
	Expire            int    `json:"expire"` // 秒
}

// accessToken 获取（带缓存）有效的 tenant_access_token。
func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// 缓存有效（提前 5 分钟刷新，避免边界过期）
	if c.token != "" && time.Now().Before(c.tokenExp.Add(-5*time.Minute)) {
		return c.token, nil
	}
	body, _ := json.Marshal(map[string]string{
		"app_id":     c.cfg.AppID,
		"app_secret": c.cfg.AppSecret,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.cfg.BaseURL+"/auth/v3/tenant_access_token/internal", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("feishu token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", fmt.Errorf("feishu token decode: %w", err)
	}
	if tr.Code != 0 {
		return "", fmt.Errorf("feishu token error: code=%d msg=%s", tr.Code, tr.Msg)
	}
	c.token = tr.TenantAccessToken
	c.tokenExp = time.Now().Add(tokenTTL)
	return c.token, nil
}

// apiResponse 飞书 OpenAPI 通用响应壳。
type apiResponse struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// do 执行一次带鉴权的 OpenAPI 请求，把 data 解析到 out。
func (c *Client) do(ctx context.Context, method, path string, payload any, out any) error {
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.BaseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("feishu api %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var ar apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		return fmt.Errorf("feishu api %s decode: %w", path, err)
	}
	if ar.Code != 0 {
		return fmt.Errorf("feishu api %s error: code=%d msg=%s", path, ar.Code, ar.Msg)
	}
	if out != nil && len(ar.Data) > 0 {
		return json.Unmarshal(ar.Data, out)
	}
	return nil
}

// --- 卡片下发 / 更新 ---

// sendCardResponse 卡片下发响应。
type sendCardResponse struct {
	MessageID string `json:"message_id"`
}

// SendInteractiveCard 发送交互卡片到 open_id/chat_id，返回 message_id。
// openAPI: /im/v1/messages?receive_id_type=...
func (c *Client) SendInteractiveCard(ctx context.Context, receiveIDType, receiveID string, cardJSON json.RawMessage) (string, error) {
	payload := map[string]any{
		"receive_id": receiveID,
		"msg_type":   "interactive",
		"content":    string(cardJSON),
	}
	var resp sendCardResponse
	path := fmt.Sprintf("/im/v1/messages?receive_id_type=%s", receiveIDType)
	if err := c.do(ctx, http.MethodPost, path, payload, &resp); err != nil {
		return "", err
	}
	return resp.MessageID, nil
}

// PatchInteractiveCard 更新已发送的交互卡片内容（飞书卡片更新 API）。
// openAPI: /im/v1/messages/{message_id} PATCH（更新卡片）
func (c *Client) PatchInteractiveCard(ctx context.Context, messageID string, cardJSON json.RawMessage) error {
	payload := map[string]any{
		"msg_type": "interactive",
		"content":  string(cardJSON),
	}
	path := fmt.Sprintf("/im/v1/messages/%s", messageID)
	return c.do(ctx, http.MethodPatch, path, payload, nil)
}

// VerificationToken 暴露给 adapter 做回调校验。
func (c *Client) VerificationToken() string { return c.cfg.VerificationToken }

// EncryptKey 暴露给 adapter 做回调解密。
func (c *Client) EncryptKey() string { return c.cfg.EncryptKey }
