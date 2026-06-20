// Package ai 实现能力域 11：AI 智能（横向 Copilot）。
//
// 对应 docs/capabilities/07-timeline-ai.md §B：
// · LLM Provider 抽象（可插拔），满足 postmortem.LLMProvider 接口
// · 智谱 GLM 实现（中文优先、合规友好）
// · Key 为空时自动降级（设计基线第 7 条：AI 可降级，不阻塞主流程）
//
// 按容错率分阶段：本包先实现"复盘/摘要起草"（容错率高，人校对），
// 根因诊断/降噪/自动处置等高风险场景后续按 human-in-the-loop 接入。
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Provider LLM 提供方抽象。由具体实现（GLM/OpenAI/Ollama）填充。
// Complete 是核心：输入 prompt 返回补全文本。
type Provider interface {
	// Complete 单轮补全。
	Complete(ctx context.Context, prompt string) (string, error)
	// Available 是否可用（key 已配置等）。不可用时调用方应降级。
	Available() bool
}

// GLMProvider 智谱 GLM 实现。
// API 对齐智谱 OpenAPI（chat/completions，与 OpenAI 格式兼容）。
type GLMProvider struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
}

// NewGLMProvider 创建智谱 GLM Provider。apiKey 为空时 Available() 返回 false（降级）。
func NewGLMProvider(apiKey, model, baseURL string) *GLMProvider {
	if baseURL == "" {
		baseURL = "https://open.bigmodel.cn/api/paas/v4"
	}
	if model == "" {
		model = "glm-4-flash"
	}
	return &GLMProvider{
		apiKey:  apiKey,
		model:   model,
		baseURL: baseURL,
		client:  &http.Client{Timeout: 60 * time.Second},
	}
}

// Available key 已配置即为可用。
func (g *GLMProvider) Available() bool { return g.apiKey != "" }

// glmRequest 智谱 chat/completions 请求体（与 OpenAI 兼容）。
type glmRequest struct {
	Model    string         `json:"model"`
	Messages []glmMessage   `json:"messages"`
}

type glmMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// glmResponse 智谱响应体。
type glmResponse struct {
	Choices []struct {
		Message      glmMessage `json:"message"`
		FinishReason string     `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

// Complete 调用智谱 GLM 完成补全。
func (g *GLMProvider) Complete(ctx context.Context, prompt string) (string, error) {
	if !g.Available() {
		return "", fmt.Errorf("glm provider unavailable (no api key)")
	}
	reqBody, _ := json.Marshal(glmRequest{
		Model: g.model,
		Messages: []glmMessage{
			{Role: "user", Content: prompt},
		},
	})
	url := g.baseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+g.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("call glm: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("glm http %d: %s", resp.StatusCode, string(body))
	}
	var r glmResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return "", fmt.Errorf("parse glm response: %w", err)
	}
	if len(r.Choices) == 0 {
		return "", fmt.Errorf("glm empty choices")
	}
	return r.Choices[0].Message.Content, nil
}
