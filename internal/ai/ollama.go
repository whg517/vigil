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

// OllamaProvider 本地 Ollama 实现（M11.10：数据不出境的隐私场景）。
//
// 与 GLMProvider 走不同的 HTTP 契约（Ollama 原生 /api/chat、/api/embeddings，
// 非 OpenAI 兼容层），但对上层实现同一 Provider 接口，业务层不感知选型。
//
// ⚠️ embed 维度提示：Ollama 常见 embed 模型维度与 GLM（embedding-3=1536）不同——
// 例如默认的 nomic-embed-text 是 768 维。pgvector 的 Incident.embedding 列是 vector(1536)，
// 维度不匹配会导致向量写入/余弦检索不可用。切换到 Ollama embed 前须确保：
//   - 要么 pgvector 列维度与所选 embed 模型一致，
//   - 要么接受相似检索降级为 LIKE 文本匹配（diagnose.go 的 FindSimilar 已有该降级路径）。
//
// 本 Provider 不在构造期探测本地服务可达性（与 GLM 的容错哲学一致）：Available() 只看
// baseURL 是否配置，实际不可达时在 Complete/Embed 层报错，由调用方降级。
type OllamaProvider struct {
	baseURL    string
	model      string // 补全模型，默认 llama3
	embedModel string // embedding 模型，默认 nomic-embed-text（768 维，注意 pgvector 匹配）
	client     *http.Client
}

// NewOllamaProvider 创建 Ollama 本地 Provider。
// baseURL 为空时 Available() 返回 false（降级）。model/embedModel 为空时取默认值。
func NewOllamaProvider(baseURL, model, embedModel string) *OllamaProvider {
	if model == "" {
		model = "llama3"
	}
	if embedModel == "" {
		embedModel = "nomic-embed-text"
	}
	return &OllamaProvider{
		baseURL:    baseURL,
		model:      model,
		embedModel: embedModel,
		client:     &http.Client{Timeout: 60 * time.Second},
	}
}

// Available baseURL 已配置即视为可用（本地服务可达性不在构造期探测，失败走 provider 层降级）。
func (o *OllamaProvider) Available() bool { return o.baseURL != "" }

// ollamaChatRequest Ollama /api/chat 请求体。stream=false 取一次性完整响应。
type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
}

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ollamaChatResponse Ollama /api/chat 响应体（stream=false 时单个 JSON 对象）。
type ollamaChatResponse struct {
	Model   string        `json:"model"`
	Message ollamaMessage `json:"message"`
	Done    bool          `json:"done"`
}

// Complete 调用本地 Ollama 完成补全。POST {baseURL}/api/chat，解析 .message.content。
func (o *OllamaProvider) Complete(ctx context.Context, prompt string) (string, error) {
	if !o.Available() {
		return "", fmt.Errorf("ollama provider unavailable (no base url)")
	}
	reqBody, _ := json.Marshal(ollamaChatRequest{
		Model: o.model,
		Messages: []ollamaMessage{
			{Role: "user", Content: prompt},
		},
		Stream: false,
	})
	url := o.baseURL + "/api/chat"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("call ollama: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("ollama http %d: %s", resp.StatusCode, string(body))
	}
	var r ollamaChatResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return "", fmt.Errorf("parse ollama response: %w", err)
	}
	if r.Message.Content == "" {
		return "", fmt.Errorf("ollama empty content")
	}
	return r.Message.Content, nil
}

// ollamaEmbedRequest Ollama /api/embeddings 请求体（单条 prompt）。
type ollamaEmbedRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

// ollamaEmbedResponse Ollama /api/embeddings 响应体。
type ollamaEmbedResponse struct {
	Embedding []float32 `json:"embedding"`
}

// Embed 调用本地 Ollama 把文本转向量。POST {baseURL}/api/embeddings，解析 .embedding。
//
// ⚠️ 维度提示：默认 nomic-embed-text 是 768 维，而 pgvector 列 Incident.embedding 是 vector(1536)。
// 维度不符时向量写入/检索不可用——须保证 pgvector 列维度与所选 embed 模型一致，
// 否则应仅用文本降级（FindSimilar 的 LIKE 兜底）。
func (o *OllamaProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	if !o.Available() {
		return nil, fmt.Errorf("ollama provider unavailable (no base url)")
	}
	reqBody, _ := json.Marshal(ollamaEmbedRequest{
		Model:  o.embedModel,
		Prompt: text,
	})
	url := o.baseURL + "/api/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call ollama embed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("ollama embed http %d: %s", resp.StatusCode, string(body))
	}
	var r ollamaEmbedResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("parse ollama embed response: %w", err)
	}
	if len(r.Embedding) == 0 {
		return nil, fmt.Errorf("ollama embed empty embedding")
	}
	return r.Embedding, nil
}
