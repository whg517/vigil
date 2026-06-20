package im

import "context"

// NoopBot 占位适配器，用于尚未接入真实 API 的 IM 平台（钉钉/企微）。
// Available() 恒为 false——装配时调用方据此跳过该平台，
// 对应 capabilities §10 平台能力降级矩阵中「留待 PoC」的条目。
//
// 所有操作返回 ErrUnsupported，避免误用。
type NoopBot struct {
	platform string
}

// NewNoopBot 创建占位适配器。platform 为 dingtalk | wecom。
func NewNoopBot(platform string) *NoopBot {
	return &NoopBot{platform: platform}
}

func (b *NoopBot) Platform() string { return b.platform }
func (b *NoopBot) Available() bool  { return false }
func (b *NoopBot) SendCard(_ context.Context, _ string, _ *Card) (string, error) {
	return "", ErrUnsupported
}
func (b *NoopBot) UpdateCard(_ context.Context, _ string, _ *Card) error {
	return ErrUnsupported
}
func (b *NoopBot) CreateWarRoom(_ context.Context, _ string, _ []string) (string, error) {
	return "", ErrUnsupported
}
func (b *NoopBot) VerifyCallback(_ map[string]string, _ []byte) ([]byte, error) {
	return nil, ErrUnsupported
}
func (b *NoopBot) ParseCallback(_ []byte) (*IMEvent, error) {
	return nil, ErrUnsupported
}

// Registry 平台注册表：按 platform 名查找适配器。
type Registry struct {
	bots map[string]IMBot
}

// NewRegistry 创建注册表。
func NewRegistry() *Registry {
	return &Registry{bots: make(map[string]IMBot)}
}

// Register 注册适配器（按 Platform() 索引）。
func (r *Registry) Register(b IMBot) {
	r.bots[b.Platform()] = b
}

// Get 按平台名取适配器。
func (r *Registry) Get(platform string) (IMBot, bool) {
	b, ok := r.bots[platform]
	return b, ok
}

// Available 返回全部已配置且可用的适配器（Available()==true）。
func (r *Registry) Available() []IMBot {
	out := make([]IMBot, 0, len(r.bots))
	for _, b := range r.bots {
		if b.Available() {
			out = append(out, b)
		}
	}
	return out
}

// All 返回全部已注册适配器（含不可用的占位，用于回调路由）。
func (r *Registry) All() []IMBot {
	out := make([]IMBot, 0, len(r.bots))
	for _, b := range r.bots {
		out = append(out, b)
	}
	return out
}
