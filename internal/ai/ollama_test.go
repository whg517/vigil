package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestOllamaProvider_NoBaseURL 验证无 baseURL 时 Available=false（降级前提）+ 不发起网络调用。
func TestOllamaProvider_NoBaseURL(t *testing.T) {
	o := NewOllamaProvider("", "", "")
	if o.Available() {
		t.Error("无 baseURL 时 Available 应为 false")
	}
	if _, err := o.Complete(context.Background(), "x"); err == nil {
		t.Error("无 baseURL 时 Complete 应报错")
	}
	if _, err := o.Embed(context.Background(), "x"); err == nil {
		t.Error("无 baseURL 时 Embed 应报错")
	}
}

// TestOllamaProvider_Defaults 验证默认 model/embedModel。
func TestOllamaProvider_Defaults(t *testing.T) {
	o := NewOllamaProvider("http://localhost:11434", "", "")
	if !o.Available() {
		t.Error("有 baseURL 时 Available 应为 true")
	}
	if o.model != "llama3" {
		t.Errorf("默认 model: got %q, want llama3", o.model)
	}
	if o.embedModel != "nomic-embed-text" {
		t.Errorf("默认 embedModel: got %q, want nomic-embed-text", o.embedModel)
	}
}

// TestOllamaProvider_Complete 验证 /api/chat 请求构造 + .message.content 解析。
func TestOllamaProvider_Complete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("Complete 应打 /api/chat，got %q", r.URL.Path)
		}
		var req ollamaChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("解析请求体: %v", err)
		}
		if req.Stream {
			t.Error("请求应 stream=false")
		}
		if len(req.Messages) != 1 || req.Messages[0].Content != "诊断这个告警" {
			t.Errorf("messages 构造错误: %+v", req.Messages)
		}
		_ = json.NewEncoder(w).Encode(ollamaChatResponse{
			Model:   "llama3",
			Message: ollamaMessage{Role: "assistant", Content: "根因是连接池耗尽"},
			Done:    true,
		})
	}))
	defer srv.Close()

	o := NewOllamaProvider(srv.URL, "", "")
	out, err := o.Complete(context.Background(), "诊断这个告警")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if out != "根因是连接池耗尽" {
		t.Errorf("got %q, want 根因是连接池耗尽", out)
	}
}

// TestOllamaProvider_Embed 验证 /api/embeddings 请求构造 + .embedding 解析。
func TestOllamaProvider_Embed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embeddings" {
			t.Errorf("Embed 应打 /api/embeddings，got %q", r.URL.Path)
		}
		var req ollamaEmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("解析请求体: %v", err)
		}
		if req.Model != "nomic-embed-text" || req.Prompt != "hello" {
			t.Errorf("embed 请求构造错误: %+v", req)
		}
		_ = json.NewEncoder(w).Encode(ollamaEmbedResponse{Embedding: []float32{0.1, 0.2, 0.3}})
	}))
	defer srv.Close()

	o := NewOllamaProvider(srv.URL, "", "")
	vec, err := o.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != 3 || vec[0] != 0.1 {
		t.Errorf("embedding 解析错误: %+v", vec)
	}
}

// TestOllamaProvider_HTTPError 验证 HTTP 4xx/5xx 时报错（触发降级）。
func TestOllamaProvider_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer srv.Close()

	o := NewOllamaProvider(srv.URL, "", "")
	if _, err := o.Complete(context.Background(), "x"); err == nil {
		t.Error("HTTP 404 时 Complete 应报错")
	}
	if _, err := o.Embed(context.Background(), "x"); err == nil {
		t.Error("HTTP 404 时 Embed 应报错")
	}
}

// TestOllamaProvider_EmptyResponse 验证空内容/空向量时报错（不返回无效结果）。
func TestOllamaProvider_EmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/chat" {
			_ = json.NewEncoder(w).Encode(ollamaChatResponse{Done: true}) // 空 content
			return
		}
		_ = json.NewEncoder(w).Encode(ollamaEmbedResponse{}) // 空 embedding
	}))
	defer srv.Close()

	o := NewOllamaProvider(srv.URL, "", "")
	if _, err := o.Complete(context.Background(), "x"); err == nil {
		t.Error("空 content 时 Complete 应报错")
	}
	if _, err := o.Embed(context.Background(), "x"); err == nil {
		t.Error("空 embedding 时 Embed 应报错")
	}
}

// TestConfidenceThreshold_Config 验证 SetConfidenceThreshold 配置化生效（wire 装配读 cfg 值的语义）。
// <=0 保留默认 0.6，正值覆盖——这是 wire.go 注入 cfg.LLM.ConfidenceThreshold 的安全前提。
func TestConfidenceThreshold_Config(t *testing.T) {
	e := NewTriageAIEngine(nil, nil)
	if e.confidenceThreshold != defaultConfidenceThreshold {
		t.Errorf("默认门槛: got %v, want %v", e.confidenceThreshold, defaultConfidenceThreshold)
	}
	e.SetConfidenceThreshold(0.85)
	if e.confidenceThreshold != 0.85 {
		t.Errorf("覆盖后门槛: got %v, want 0.85", e.confidenceThreshold)
	}
	e.SetConfidenceThreshold(0) // <=0 保留默认（避免误配为 0 使一切建议都产出）
	if e.confidenceThreshold != 0.85 {
		t.Errorf("传 0 应保留上次值: got %v, want 0.85", e.confidenceThreshold)
	}

	c := NewCopilotEngine(nil, nil)
	c.SetConfidenceThreshold(0.9)
	if c.confidenceThreshold != 0.9 {
		t.Errorf("copilot 覆盖后门槛: got %v, want 0.9", c.confidenceThreshold)
	}
}
