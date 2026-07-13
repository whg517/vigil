// adapter.go 定义告警源适配器接口与注册表。
//
// 归一化职责（ADR-0011；新增告警源的全部触点见 docs/extending.md）：
// 每个 Adapter 把特定源 payload 归一化为统一 NormalizedEvent。
package ingestion

import (
	"context"
	"fmt"

	"github.com/kevin/vigil/ent"
)

// NormalizedEvent 适配器归一化产物（与 ent.Event 字段对齐，但解耦于 ent 便于测试）。
type NormalizedEvent struct {
	SourceEventID string
	Source        string
	Severity      string // critical | warning | info
	Status        string // firing | resolved
	Summary       string
	Detail        map[string]any
	Labels        map[string]string
	DedupKey      string
}

// Adapter 告警源适配器。把原始 payload 归一化为 NormalizedEvent。
// 对应 capabilities §4.2：字段映射、严重度归一、标签提取、去重键生成、保留原文。
type Adapter interface {
	// Type 返回适配器类型标识（对应 ent.Integration.Type）。
	Type() string
	// Normalize 把 raw payload 归一化为多个 NormalizedEvent。
	// 返回切片：一次 webhook 可能含多条 alert（如 Prometheus/Grafana alerts[] 数组），
	// 每条 alert 归一化为一个独立 Event。单条 alert 的源返回单元素切片即可。
	// integ 是接入点配置（可读 config 字段）；raw 是原始记录（可读 headers）。
	Normalize(ctx context.Context, raw []byte, integ *ent.Integration, rawEvent *ent.RawEvent) ([]*NormalizedEvent, error)
}

// AdapterRegistry 适配器注册表。按 Integration.Type 查找对应适配器。
type AdapterRegistry struct {
	adapters map[string]Adapter
}

// NewAdapterRegistry 创建注册表并注册内置适配器。
func NewAdapterRegistry() *AdapterRegistry {
	r := &AdapterRegistry{adapters: make(map[string]Adapter)}
	r.RegisterBuiltins()
	return r
}

// Register 注册一个适配器。
func (r *AdapterRegistry) Register(a Adapter) {
	r.adapters[a.Type()] = a
}

// RegisterBuiltins 注册内置适配器。
func (r *AdapterRegistry) RegisterBuiltins() {
	r.Register(&PrometheusAdapter{})
	r.Register(&GrafanaAdapter{})
	r.Register(&GenericJSONAdapter{})
	r.Register(&EmailAdapter{}) // SMTP 入向(ADR-0038):消费 smtp_server 落库的邮件信封
}

// Get 按 Integration.Type 取适配器。
func (r *AdapterRegistry) Get(sourceType string) (Adapter, bool) {
	a, ok := r.adapters[sourceType]
	return a, ok
}

// dedupKey 生成去重键：source + sourceEventID 的稳定哈希前缀。
// 简单实现用拼接（生产可换 sha1）；保证同一告警重复推送键一致。
func dedupKey(source, sourceEventID string) string {
	return fmt.Sprintf("%s:%s", source, sourceEventID)
}
