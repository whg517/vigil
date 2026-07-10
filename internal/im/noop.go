package im

import "context"

// NoopBot 占位适配器，用于尚未接入真实 API 的 IM 平台（企微）。
// Available() 恒为 false——装配时 registry.Available() 据此跳过该平台，
// 对应 capabilities §10 平台能力降级矩阵中「留待 PoC」的条目。
//
// ★ 不静默丢失保证（企微当前状态）：
// NoopBot 因 Available()==false 被 registry.Available() 排除，故 IM 通道对企微「不发」，
// 但这不等于「告警丢失」——通知走的是 notification 包的逐通道兜底降级链（C12）：
// IM 环节对企微贡献为空时，同一 target 的通知会降级到链上下一通道（邮件/电话/短信），
// 整条链全失败才记 failed 并兜底告警 org_admin（B22）。即企微未接入只会让「IM 这一跳」空转，
// 绝不导致告警被静默吞掉。企微完整适配器（卡片/建群/@人/回调）是设计目标，体量大，留待 PoC。
//
// 所有操作返回 ErrUnsupported，避免误用（调用方本就不会调，因 Available()==false）。
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
