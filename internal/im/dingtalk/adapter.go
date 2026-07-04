// adapter.go 钉钉 IMBot 适配器，实现 im.IMBot。
//
// 与飞书同构，差异封装在本包：
//   - 卡片：ActionCard（card.go），按钮回调数据编码在 actionURL（vigil://action?act=&inc=）。
//   - 鉴权：access_token 走 gettoken，缓存策略与飞书一致。
//   - 回调校验：钉钉事件订阅用 aes_key（AES-256-CBC）+ token（HMAC-SHA256 校验签名），
//     与飞书的 EncryptKey 方案不同（密钥派生 + IV 用法不同）。
//   - 标识：用户 staffId/userId，群 openConversationId（channel 字符串前缀区分）。
package dingtalk

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kevin/vigil/internal/im"
)

// Adapter 钉钉 IMBot 适配器。
type Adapter struct {
	client *Client
}

// New 创建钉钉适配器。
func New(cfg Config) *Adapter {
	return &Adapter{client: NewClient(cfg)}
}

// NewWithClient 用已有 Client 创建适配器（测试注入用）。
func NewWithClient(c *Client) *Adapter {
	return &Adapter{client: c}
}

func (a *Adapter) Platform() string { return "dingtalk" }

// Available 委托给 client（AppKey+AppSecret 已配置才算就绪）。
func (a *Adapter) Available() bool { return a.client.Available() }

// --- 卡片下发 / 更新 ---

// channel 约定（与飞书 parseChannel 同构，前缀区分目标类型）：
//
//	"userId:xxx"            → 单聊（staffId/userId）
//	"openConversationId:xxx"→ 群聊
func (a *Adapter) SendCard(ctx context.Context, channel string, card *im.Card) (string, error) {
	idType, id, ok := parseChannel(channel)
	if !ok {
		return "", fmt.Errorf("dingtalk: invalid channel %q", channel)
	}
	// 标题加严重度 emoji（ActionCard 无 header 配色，靠 emoji 区分）
	card.Header = TitleWithSeverity(card)
	msgParam, err := CardToActionCard(card)
	if err != nil {
		return "", err
	}
	const msgKey = "sampleActionCard"
	var msgID string
	switch idType {
	case "userId", "staffId":
		msgID, err = a.client.SendOTOMessages(ctx, []string{id}, msgKey, msgParam)
	case "openConversationId", "conversationId", "cid":
		msgID, err = a.client.SendGroupMessage(ctx, id, msgKey, msgParam)
	default:
		return "", fmt.Errorf("dingtalk: unsupported channel type %q", idType)
	}
	if err != nil {
		return "", err
	}
	// B16 降级前置：钉钉机器人消息无法原地更新，UpdateCard 只能「重发到同一 channel」。
	// 故把 channel 编入返回的 cardID（channel|msgID），使无 channel 上下文的刷新路径
	// （card_refresher 经领域事件、Web→IM 同步）也能解出目标群重发。msgID 保留供追踪/撤回。
	return encodeCardID(channel, msgID), nil
}

// UpdateCard 更新已下发的卡片（B16 钉钉卡片降级）。
//
// 钉钉机器人 sampleActionCard 消息不支持在原消息上原地改卡片（与飞书卡片更新能力不同）。
// 按 capabilities §10 降级矩阵 Q1 的约定：降级为向同一 channel 发一条新消息标注最新状态，
// 使群内成员看到状态变更（如「⚠️ INC-xxx 已 acked by 张三」），不再永停在下发时的样子。
//
// cardID 由 SendCard 编码为 "channel|msgID"：解出 channel 重发。
// 未含 channel（历史裸 msgID 或无法解析）时无法定位目标群，返回 ErrCardUpdateNoChannel
// 让调用方感知降级未成（best-effort，主流程状态已落库，不阻塞）。
func (a *Adapter) UpdateCard(ctx context.Context, cardID string, card *im.Card) error {
	channel, _, ok := decodeCardID(cardID)
	if !ok || channel == "" {
		// 无 channel 上下文：无法重发（钉钉无原地更新能力），如实标注降级未成。
		return im.ErrCardUpdateNoChannel
	}
	// 重发新卡片到同一 channel（发新消息即降级方案）。SendCard 内部按 channel 前缀路由。
	if _, err := a.SendCard(ctx, channel, card); err != nil {
		return fmt.Errorf("dingtalk update-card degrade resend: %w", err)
	}
	return nil
}

// cardIDSep 分隔 channel 与真实 msgID（编码进 cardID，供 UpdateCard 降级重发定位群）。
const cardIDSep = "|"

// encodeCardID 把 channel 与 msgID 编码为单一 cardID 字符串（channel|msgID）。
func encodeCardID(channel, msgID string) string {
	return channel + cardIDSep + msgID
}

// decodeCardID 从 cardID 解出 (channel, msgID)。
// 无分隔符（历史裸 msgID）时 ok=false，channel 为空——调用方据此判定无法重发。
func decodeCardID(cardID string) (channel, msgID string, ok bool) {
	idx := strings.Index(cardID, cardIDSep)
	if idx < 0 {
		return "", cardID, false
	}
	return cardID[:idx], cardID[idx+1:], true
}

// CreateWarRoom 创建作战室群。members 为钉钉 staffId 列表。
func (a *Adapter) CreateWarRoom(ctx context.Context, name string, members []string) (string, error) {
	if len(members) == 0 {
		return "", fmt.Errorf("dingtalk: create warroom needs >=1 member")
	}
	return a.client.CreateChat(ctx, name, members)
}

// --- 回调校验 / 解析 ---

// VerifyCallback 校验钉钉事件回调的签名 + 解密。
//
// 钉钉事件订阅（企业内应用）回调规则：
//  1. 明文模式（无 aes_key）：原样返回 body，不校验。
//  2. 加密模式：body 形如 {"encrypt":"<base64>"}，
//     用 aes_key（base64 解出 32 字节 AES-256 key）做 AES-256-CBC 解密，
//     IV 取密文前 16 字节；签名 = HMAC-SHA256(aes_key_decoded, timestamp+token+nonce+encrypt)，
//     通过 header["sign"]（或 x-dingtalk-signature）对比。
//
// 本实现做：加密模式解密 + 签名校验；明文模式直接返回。
func (a *Adapter) VerifyCallback(headers map[string]string, rawBody []byte) ([]byte, error) {
	// 明文模式：无 aes_key 直接返回
	if a.client.AesKey() == "" {
		return rawBody, nil
	}

	// 解析 encrypt envelope
	var envelope struct {
		Encrypt string `json:"encrypt"`
	}
	if err := json.Unmarshal(rawBody, &envelope); err == nil && envelope.Encrypt != "" {
		// 签名校验（headers 带 timestamp/nonce/sign）
		if sign := headers["sign"]; sign != "" {
			expected := dingtalkSign(a.client.AesKey(),
				headers["timestamp"], a.client.Token(),
				headers["nonce"], envelope.Encrypt)
			if !strings.EqualFold(sign, expected) {
				return nil, fmt.Errorf("dingtalk: signature mismatch")
			}
		}
		decrypted, err := decryptAES(envelope.Encrypt, a.client.AesKey())
		if err != nil {
			return nil, fmt.Errorf("dingtalk decrypt: %w", err)
		}
		return decrypted, nil
	}
	return rawBody, nil
}

// ParseCallback 解析钉钉事件为标准化 IMEvent。
//
// 支持的事件类型：
//   - 卡片按钮回调：钉钉 ActionCard 按钮点击会以"机器人消息回执/卡片回调"形式推送，
//     content 含 actionURL（vigil://action?act=&inc=），从中解析 action + incident_id。
//   - 机器人收到 @ / 文本消息：text 里含 /vigil 斜杠命令时识别为 command。
//   - 普通群消息：按 message 返回（可选回写时间线）。
func (a *Adapter) ParseCallback(payload []byte) (*im.IMEvent, error) {
	// 钉钉事件 envelope：不同事件类型字段不同，统一先解一层
	var env struct {
		MsgID          string          `json:"msgId"`
		SenderNick     string          `json:"senderNick"`
		SenderID       string          `json:"senderId"`       // 发送者 staffId
		SenderStaffID  string          `json:"senderStaffId"`  // 新版字段
		ConversationID string          `json:"conversationId"` // 群 openConversationId（群消息）
		ChatbotUserID  string          `json:"chatbotUserId"`
		MsgType        string          `json:"msgType"`
		Content        json.RawMessage `json:"content"` // 消息内容（文本是 JSON 字符串）
		Text           struct {
			Content string `json:"content"`
		} `json:"text"`
		SessionWebhook string `json:"sessionWebhook"`
		// 卡片回调（richText / 互动卡片）专用字段
		EventOrgType string `json:"eventOrgType"`
		// B16 @人解析：钉钉群里 @机器人 时，被同时 @的其他人在 atUsers 里。
		// dingtalkId 是被 @人的钉钉 openId；staffId 是企业内 userId（optional，取决于机器人权限）。
		AtUsers []struct {
			DingtalkID string `json:"dingtalkId"`
			StaffID    string `json:"staffId"`
		} `json:"atUsers"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		return nil, fmt.Errorf("dingtalk parse envelope: %w", err)
	}

	ev := &im.IMEvent{Platform: "dingtalk", Type: im.EventMessage}

	// 操作者标识：优先 senderStaffId（新版），退而 senderId
	unionID := env.SenderStaffID
	if unionID == "" {
		unionID = env.SenderID
	}
	ev.UnionID = unionID
	ev.ChannelID = env.ConversationID

	// 1. 卡片按钮回调：content 的 actionURL 形如 vigil://action?act=ack&inc=42
	contentStr := ""
	if len(env.Content) > 0 {
		// 钉钉 content 可能是：
		//   - JSON 字符串（转义后的 JSON，如 "{\"actionUrl\":\"...\"}"）→ 先 unquote 再解析
		//   - JSON 对象（如 {"actionUrl":"..."} 或 {"content":"文本"}）→ 直接解析
		raw := env.Content
		// 尝试当 JSON 字符串解一层（content 是转义的 JSON 字符串时）
		var asString string
		if json.Unmarshal(raw, &asString) == nil {
			raw = []byte(asString)
		}
		var c map[string]any
		if json.Unmarshal(raw, &c) == nil {
			if u, ok := c["actionUrl"].(string); ok {
				contentStr = u
			} else if t, ok := c["content"].(string); ok {
				contentStr = t
			}
		} else {
			// 既非对象也非字符串：当裸文本处理
			contentStr = strings.Trim(string(raw), `"`)
		}
	}
	if contentStr == "" {
		contentStr = env.Text.Content
	}

	// 卡片回调：actionURL 解析
	if action, incID, ok := parseActionURL(contentStr); ok {
		ev.Type = im.EventCardAction
		ev.Action = action
		ev.IncidentID = incID
		return ev, nil
	}

	// 2. 文本消息：识别 /vigil 斜杠命令
	if cmd, arg, ok := parseSlashCommand(contentStr); ok {
		ev.Type = im.EventCommand
		ev.Command = cmd
		ev.CommandArg = arg
		return ev, nil
	}

	// 3. @人协同（B16）：@机器人的同时 @了其他人 → 拉人（与飞书 mention 对齐）。
	//    钉钉 atUsers 里优先取 staffId（企业内 userId，与绑定/发消息标识一致）；
	//    机器人无「按 staffId 拉群」权限时 staffId 可能为空，退而用 dingtalkId（openId）。
	//    ⚠️ 钉钉 API 限制：部分机器人权限下 atUsers 只回 dingtalkId 而无 staffId，
	//    此时映射需用户以 dingtalkId 绑定；若两者皆空则该 @无法解析（如实跳过，不臆造）。
	for _, at := range env.AtUsers {
		id := at.StaffID
		if id == "" {
			id = at.DingtalkID
		}
		if id != "" && id != ev.UnionID { // 排除操作者本人被 @的情形
			ev.MentionAt = append(ev.MentionAt, id)
		}
	}
	if len(ev.MentionAt) > 0 {
		ev.Type = im.EventMention
		ev.Text = contentStr // 正文含 incident 编号，供上层 resolveIncidentArg 解析
		return ev, nil
	}

	// 4. 普通消息：保留正文，供上层（可选回写时间线）
	ev.Text = contentStr
	return ev, nil
}

// parseChannel 解析 channel 字符串为 (type, id)。
// 格式 "userId:xxx" / "openConversationId:xxx" 等。
func parseChannel(channel string) (idType, id string, ok bool) {
	idx := strings.Index(channel, ":")
	if idx <= 0 {
		return "", "", false
	}
	return channel[:idx], channel[idx+1:], true
}

// parseActionURL 从 vigil://action?act=ack&inc=42 解析出 (action, incident_id)。
func parseActionURL(s string) (action, incID string, ok bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "vigil://action") {
		return "", "", false
	}
	q := s[strings.Index(s, "?")+1:]
	for _, kv := range strings.Split(q, "&") {
		eq := strings.Index(kv, "=")
		if eq < 0 {
			continue
		}
		k, v := kv[:eq], kv[eq+1:]
		switch k {
		case "act":
			action = v
		case "inc":
			incID = v
		}
	}
	if action == "" {
		return "", "", false
	}
	return action, incID, true
}

// parseSlashCommand 解析 "/vigil ack INC-0042" 形式的斜杠命令。
// 返回 (command, arg, ok)。arg 为命令名之后的全部文本。
// 与飞书逻辑同构，避免跨包依赖。
func parseSlashCommand(text string) (string, string, bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/vigil") {
		return "", "", false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(text, "/vigil"))
	if rest == "" {
		return "", "", false
	}
	parts := strings.SplitN(rest, " ", 2)
	cmd := parts[0]
	arg := ""
	if len(parts) > 1 {
		arg = strings.TrimSpace(parts[1])
	}
	return cmd, arg, true
}

// --- 钉钉回调签名 + AES 解密 ---

// dingtalkSign 计算钉钉事件订阅签名。
// 规则：sign = HMAC-SHA256(key=aesKey_decoded_bytes, msg=timestamp+token+nonce+encrypt)，
// 再 hex 编码。注意 aes_key 是 base64(43 字符) → 解码得 32 字节 key。
//
// 参考：钉钉开放平台「加密及签名机制」。
func dingtalkSign(aesKeyB64, timestamp, token, nonce, encrypt string) string {
	keyBytes, err := decodeAesKey(aesKeyB64)
	if err != nil {
		return ""
	}
	msg := timestamp + token + nonce + encrypt
	mac := hmac.New(sha256.New, keyBytes)
	mac.Write([]byte(msg))
	return hex.EncodeToString(mac.Sum(nil))
}

// decodeAesKey 把钉钉 aes_key（base64 编码，典型 43 字符无填充）解码为 32 字节 key。
// 兼容标准 base64（带 "=" 填充）和无填充两种形式：不足 4 的倍数时补 "="。
func decodeAesKey(aesKeyB64 string) ([]byte, error) {
	s := strings.TrimRight(aesKeyB64, "=")
	if pad := len(s) % 4; pad != 0 {
		s += strings.Repeat("=", 4-pad)
	}
	return base64.StdEncoding.DecodeString(s)
}

// decryptAES 钉钉 AES-256-CBC 解密。
// 钉钉规则：aes_key(base64 43 字符)→解码得 32 字节 key；
// 密文 = base64decode(encrypt)；前 16 字节为随机前缀，第 16-20 字节为 msg_len（4 字节大端），
// 之后为变长明文，末尾为 corpId。
// IV 全零（钉钉规则）。
func decryptAES(encryptB64, aesKeyB64 string) ([]byte, error) {
	keyBytes, err := decodeAesKey(aesKeyB64)
	if err != nil {
		return nil, fmt.Errorf("decode aes key: %w", err)
	}
	if len(keyBytes) != 32 {
		return nil, fmt.Errorf("aes key must be 32 bytes, got %d", len(keyBytes))
	}
	cipherText, err := base64.StdEncoding.DecodeString(encryptB64)
	if err != nil {
		return nil, fmt.Errorf("decode encrypt: %w", err)
	}
	block, err := aes.NewCipher(keyBytes)
	if err != nil {
		return nil, err
	}
	if len(cipherText)%block.BlockSize() != 0 || len(cipherText) < 32 {
		return nil, fmt.Errorf("ciphertext invalid length %d", len(cipherText))
	}
	// IV 全零（钉钉规则）
	iv := make([]byte, block.BlockSize())
	mode := cipher.NewCBCDecrypter(block, iv)
	plain := make([]byte, len(cipherText))
	mode.CryptBlocks(plain, cipherText)
	// PKCS7 去填充
	if pad := int(plain[len(plain)-1]); pad > 0 && pad <= block.BlockSize() {
		plain = plain[:len(plain)-pad]
	}
	// 跳过 16 字节随机前缀 + 4 字节 msg_len，取 msg_len 字节明文（末尾 corpId 忽略）
	if len(plain) < 20 {
		return nil, fmt.Errorf("decrypted too short")
	}
	msgLen := int(plain[16])<<24 | int(plain[17])<<16 | int(plain[18])<<8 | int(plain[19])
	if 20+msgLen > len(plain) {
		// msg_len 异常时返回去掉前缀的全部明文
		return plain[16:], nil
	}
	return plain[20 : 20+msgLen], nil
}
