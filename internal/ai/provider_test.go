package ai

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// mockProvider 测试用 Provider。
type mockProvider struct {
	resp   string
	err    error
	avail  bool
	called int
}

func (m *mockProvider) Complete(_ context.Context, _ string) (string, error) {
	m.called++
	if m.err != nil {
		return "", m.err
	}
	return m.resp, nil
}
func (m *mockProvider) Embed(_ context.Context, _ string) ([]float32, error) {
	return []float32{0.1, 0.2, 0.3}, nil
}
func (m *mockProvider) Available() bool { return m.avail }

// TestGLMProvider_NoKey 验证无 key 时 Available=false（降级前提）。
func TestGLMProvider_NoKey(t *testing.T) {
	g := NewGLMProvider("", "", "")
	if g.Available() {
		t.Error("无 key 时 Available 应为 false")
	}
	// Complete 应直接返回错误，不发起网络调用
	_, err := g.Complete(context.Background(), "x")
	if err == nil {
		t.Error("无 key 时 Complete 应报错")
	}
}

// TestGLMProvider_WithKey 验证有 key 时 Available=true。
func TestGLMProvider_WithKey(t *testing.T) {
	g := NewGLMProvider("test-key", "", "")
	if !g.Available() {
		t.Error("有 key 时 Available 应为 true")
	}
}

// TestGLMProvider_Defaults 验证默认 model/baseURL。
func TestGLMProvider_Defaults(t *testing.T) {
	g := NewGLMProvider("k", "", "")
	if g.model != "glm-4-flash" {
		t.Errorf("默认 model: got %q", g.model)
	}
	if !strings.Contains(g.baseURL, "bigmodel.cn") {
		t.Errorf("默认 baseURL: got %q", g.baseURL)
	}
}

// TestAdapter_Available 验证适配器的可用性判断。
func TestAdapter_Available(t *testing.T) {
	// nil provider
	a1 := NewPostmortemDraftAdapter(nil)
	if a1.Available() {
		t.Error("nil provider 时 Available 应为 false")
	}
	// 不可用 provider
	a2 := NewPostmortemDraftAdapter(&mockProvider{avail: false})
	if a2.Available() {
		t.Error("不可用 provider 时 Available 应为 false")
	}
	// 可用 provider
	a3 := NewPostmortemDraftAdapter(&mockProvider{avail: true})
	if !a3.Available() {
		t.Error("可用 provider 时 Available 应为 true")
	}
}

// TestAdapter_DraftSection 验证起草委托给 Provider。
func TestAdapter_DraftSection(t *testing.T) {
	mp := &mockProvider{resp: "AI 草稿内容", avail: true}
	a := NewPostmortemDraftAdapter(mp)

	out, err := a.DraftSection(context.Background(), "summary", map[string]any{"title": "测试"})
	if err != nil {
		t.Fatalf("DraftSection: %v", err)
	}
	if out != "AI 草稿内容" {
		t.Errorf("got %q, want 'AI 草稿内容'", out)
	}
	if mp.called != 1 {
		t.Errorf("Provider 应被调用 1 次，got %d", mp.called)
	}
}

// TestAdapter_DraftSection_Unavailable 验证不可用时降级（返回错误，调用方走 fallback）。
func TestAdapter_DraftSection_Unavailable(t *testing.T) {
	a := NewPostmortemDraftAdapter(&mockProvider{avail: false})
	_, err := a.DraftSection(context.Background(), "summary", nil)
	if err == nil {
		t.Error("不可用时 DraftSection 应返回错误（触发降级）")
	}
}

// TestAdapter_DraftSection_ProviderError 验证 Provider 出错时透传错误。
func TestAdapter_DraftSection_ProviderError(t *testing.T) {
	a := NewPostmortemDraftAdapter(&mockProvider{err: errors.New("network"), avail: true})
	_, err := a.DraftSection(context.Background(), "summary", nil)
	if err == nil {
		t.Error("Provider 出错时应返回错误")
	}
}

// TestBuildDraftPrompt 验证 prompt 构造含事件信息 + 章节指引。
func TestBuildDraftPrompt(t *testing.T) {
	prompt := buildDraftPrompt("root_cause", map[string]any{
		"title":    "支付5xx",
		"severity": "critical",
		"summary":  "DB连接池耗尽",
	})
	// 应含事件信息
	if !strings.Contains(prompt, "支付5xx") {
		t.Error("prompt 应含 title")
	}
	if !strings.Contains(prompt, "critical") {
		t.Error("prompt 应含 severity")
	}
	// 根因章节应有不确定性指引
	if !strings.Contains(prompt, "不确定") && !strings.Contains(prompt, "可能") {
		t.Error("root_cause prompt 应含不确定性指引")
	}
}

// TestSectionName 验证中文章节名映射。
func TestSectionName(t *testing.T) {
	cases := map[string]string{
		"summary":         "摘要",
		"impact":          "影响",
		"root_cause":      "根因分析",
		"what_went_well":  "做得好的",
		"unknown_section": "unknown_section", // 未知章节原样返回
	}
	for in, want := range cases {
		if got := sectionName(in); got != want {
			t.Errorf("sectionName(%q): got %q, want %q", in, got, want)
		}
	}
}
