package im

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

// All 返回全部已注册适配器（用于回调路由与状态页）。
func (r *Registry) All() []IMBot {
	out := make([]IMBot, 0, len(r.bots))
	for _, b := range r.bots {
		out = append(out, b)
	}
	return out
}
