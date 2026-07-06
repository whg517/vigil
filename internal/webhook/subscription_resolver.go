// subscription_resolver.go 出站 webhook 动态订阅解析器（N2.2）。
//
// dispatcher 出站时经此查 DB 活跃订阅（enabled=true），与 env 静态订阅合并投递。
// 每条订阅带事件类型过滤与独立签名密钥；密钥以密文存储（与 ticket_integration.callback_secret
// 同款：crypto.Cipher 加密，Sensitive 不回显），出站前解密。
//
// best-effort：查库/解密失败只记日志、返回已解析的部分，绝不阻塞出站主流程。
package webhook

import (
	"context"
	"log/slog"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/webhooksubscription"
	"github.com/kevin/vigil/internal/crypto"
)

// EntSubscriptionResolver 用 ent 读取动态订阅并解密签名密钥（SubscriptionResolver 生产实现）。
type EntSubscriptionResolver struct {
	db     *ent.Client
	cipher *crypto.Cipher // 解密 signing_secret（可选；nil 时按明文透传，向后兼容）
	log    *slog.Logger
}

// NewEntSubscriptionResolver 构造动态订阅解析器。
// cipher 为 nil 时 signing_secret 按明文透传（降级；生产建议配 VIGIL_CREDENTIAL_ENCRYPTION_KEY）。
func NewEntSubscriptionResolver(db *ent.Client, cipher *crypto.Cipher) *EntSubscriptionResolver {
	return &EntSubscriptionResolver{db: db, cipher: cipher, log: slog.Default()}
}

// Resolve 返回当前活跃（enabled=true）的动态订阅目标列表（实现 SubscriptionResolver）。
func (r *EntSubscriptionResolver) Resolve(ctx context.Context) []Subscription {
	if r == nil || r.db == nil {
		return nil
	}
	subs, err := r.db.WebhookSubscription.Query().
		Where(webhooksubscription.EnabledEQ(true)).
		All(ctx)
	if err != nil {
		// best-effort：查库失败不阻塞出站主流程（退化为仅 env 静态订阅）。
		r.log.Warn("resolve webhook subscriptions failed", "error", err)
		return nil
	}
	out := make([]Subscription, 0, len(subs))
	for _, s := range subs {
		out = append(out, Subscription{
			URL:           s.URL,
			EventTypes:    s.EventTypes,
			SigningSecret: r.decryptSecret(s.SigningSecret),
		})
	}
	return out
}

// decryptSecret 解出 signing_secret 明文（供出站签名）。
// cipher 非 nil 且能解密 → 明文；否则（无 cipher / 解密失败）按明文透传（向后兼容既有明文密钥）。
// 与 ticket.resolveCallbackSecret 同款逻辑（统一加密机制，T6.3）。
func (r *EntSubscriptionResolver) decryptSecret(stored string) string {
	if stored == "" || r.cipher == nil {
		return stored
	}
	if plain, err := r.cipher.Decrypt(stored); err == nil {
		return plain
	}
	return stored
}
