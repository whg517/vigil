package feishu

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kevin/vigil/internal/im"
)

// Adapter 飞书 IMBot 适配器，实现 im.IMBot。
type Adapter struct {
	client *Client
}

// New 创建飞书适配器。
func New(cfg Config) *Adapter {
	return &Adapter{client: NewClient(cfg)}
}

// NewWithClient 用已有 Client 创建适配器（测试注入用）。
func NewWithClient(c *Client) *Adapter {
	return &Adapter{client: c}
}

func (a *Adapter) Platform() string { return "feishu" }

// Available 委托给 client（AppID+AppSecret 已配置才算就绪）。
func (a *Adapter) Available() bool { return a.client.Available() }

// --- 卡片下发 / 更新 ---

// SendCard 向 receiveID（open_id/chat_id）下发卡片。
// channel 约定为 "open_id:xxx" 或 "chat_id:xxx" 格式，便于自动识别 receive_id_type。
func (a *Adapter) SendCard(ctx context.Context, channel string, card *im.Card) (string, error) {
	receiveIDType, receiveID, ok := parseChannel(channel)
	if !ok {
		return "", fmt.Errorf("feishu: invalid channel %q", channel)
	}
	cardJSON, err := CardToFeishu(card)
	if err != nil {
		return "", err
	}
	return a.client.SendInteractiveCard(ctx, receiveIDType, receiveID, cardJSON)
}

// UpdateCard 更新已下发的卡片（飞书支持卡片更新，对应 §3.2 实时刷新）。
func (a *Adapter) UpdateCard(ctx context.Context, cardID string, card *im.Card) error {
	cardJSON, err := CardToFeishu(card)
	if err != nil {
		return err
	}
	return a.client.PatchInteractiveCard(ctx, cardID, cardJSON)
}

// CreateWarRoom 创建作战室群。members 为飞书 open_id 列表。
func (a *Adapter) CreateWarRoom(ctx context.Context, name string, members []string) (string, error) {
	return a.client.CreateChat(ctx, name, "", members)
}

// --- 回调校验 / 解析 ---

// VerifyCallback 校验飞书事件回调。
// 飞书 v2 事件订阅：若配置了 EncryptKey，payload 是 {"encrypt": "<base64>"}，
// 需 AES-256-CBC 解密；明文模式直接返回原始 body。
// 校验 VerificationToken 在 ParseCallback 内做（payload 里有 token 字段）。
func (a *Adapter) VerifyCallback(headers map[string]string, rawBody []byte) ([]byte, error) {
	// 1. 加密模式：解密
	if a.client.EncryptKey() != "" {
		var envelope struct {
			Encrypt string `json:"encrypt"`
		}
		if err := json.Unmarshal(rawBody, &envelope); err == nil && envelope.Encrypt != "" {
			decrypted, err := decrypt(envelope.Encrypt, a.client.EncryptKey())
			if err != nil {
				return nil, fmt.Errorf("feishu decrypt: %w", err)
			}
			return decrypted, nil
		}
	}
	// 明文模式：直接返回
	return rawBody, nil
}

// ParseCallback 解析飞书事件为标准化 IMEvent。
// 飞书 v2 事件结构：{"schema":"2.0","header":{...},"event":{...}}
// 支持：卡片按钮回调（card.action.trigger）、@机器人、斜杠命令。
func (a *Adapter) ParseCallback(payload []byte) (*im.IMEvent, error) {
	var env struct {
		Schema string          `json:"schema"`
		Header json.RawMessage `json:"header"`
		Event  json.RawMessage `json:"event"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		return nil, fmt.Errorf("feishu parse envelope: %w", err)
	}

	// 校验 VerificationToken
	var header struct {
		Token     string `json:"token"`
		EventType string `json:"event_type"`
		AppID     string `json:"app_id"`
	}
	_ = json.Unmarshal(env.Header, &header)
	if a.client.VerificationToken() != "" && header.Token != a.client.VerificationToken() {
		return nil, fmt.Errorf("feishu: verification token mismatch")
	}

	ev := &im.IMEvent{Platform: "feishu", Type: im.EventMessage}
	switch header.EventType {
	case "card.action.trigger":
		// 卡片按钮回调：value 里带 action + incident_id
		var cardEvt struct {
			Operator struct {
				OpenID string `json:"open_id"`
			} `json:"operator"`
			Action struct {
				Value map[string]string `json:"value"`
			} `json:"action"`
			Token              string `json:"token"`
			OpenConversationID string `json:"open_conversation_id"`
		}
		if err := json.Unmarshal(env.Event, &cardEvt); err != nil {
			return nil, fmt.Errorf("feishu parse card event: %w", err)
		}
		ev.Type = im.EventCardAction
		ev.UnionID = cardEvt.Operator.OpenID
		ev.ChannelID = cardEvt.OpenConversationID
		ev.Action = cardEvt.Action.Value["action"]
		ev.IncidentID = cardEvt.Action.Value["incident_id"]
	case "im.message.receive_v1":
		// 普通消息 / @机器人：文本里可能含斜杠命令
		var msgEvt struct {
			Sender struct {
				SenderID struct {
					OpenID string `json:"open_id"`
				} `json:"sender_id"`
			} `json:"sender"`
			Message struct {
				ChatID   string `json:"chat_id"`
				Content  string `json:"content"`
				Mentions []struct {
					Key string `json:"key"`
					ID  struct {
						OpenID string `json:"open_id"`
					} `json:"id"`
					Name string `json:"name"`
				} `json:"mentions"`
			} `json:"message"`
		}
		if err := json.Unmarshal(env.Event, &msgEvt); err != nil {
			return nil, fmt.Errorf("feishu parse message event: %w", err)
		}
		ev.UnionID = msgEvt.Sender.SenderID.OpenID
		ev.ChannelID = msgEvt.Message.ChatID
		// 解析消息正文（飞书 content 是 JSON 字符串 {"text":"..."}）
		var content struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal([]byte(msgEvt.Message.Content), &content)
		ev.Text = content.Text
		// 斜杠命令识别：以 /vigil 开头
		if cmd, arg, ok := parseSlashCommand(content.Text); ok {
			ev.Type = im.EventCommand
			ev.Command = cmd
			ev.CommandArg = arg
		} else {
			// 收集 @的人（拉人协同场景）
			for _, m := range msgEvt.Message.Mentions {
				ev.MentionAt = append(ev.MentionAt, m.ID.OpenID)
			}
			if len(ev.MentionAt) > 0 {
				ev.Type = im.EventMention
			}
		}
	default:
		// 其它事件类型暂不处理，按普通消息返回
	}
	return ev, nil
}

// parseChannel 解析 channel 字符串为 (receive_id_type, receive_id)。
// 格式 "open_id:ou_xxx" / "chat_id:oc_xxx" / "user_id:xxx"。
func parseChannel(channel string) (idType, id string, ok bool) {
	idx := strings.Index(channel, ":")
	if idx <= 0 {
		return "", "", false
	}
	return channel[:idx], channel[idx+1:], true
}

// parseSlashCommand 解析 "/vigil ack INC-0042" 形式的斜杠命令。
// 返回 (command, arg, ok)。arg 为命令名之后的全部文本。
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

// decrypt 飞书 AES-256-CBC 解密 + 签名校验。
// 飞书规则：key = SHA256(EncryptKey)；密文 = base64decode(encrypt)；
// 前 16 字节为随机前缀，中间为 AES-256-CBC(PKCS7) 密文，后 32 字节为签名
// SHA256(key + 前缀 + 密文)。
// 解密前先校验签名，不匹配拒绝（防伪造），IV 用 16 字节 0。
func decrypt(encryptB64, encryptKey string) ([]byte, error) {
	cipherText, err := base64.StdEncoding.DecodeString(encryptB64)
	if err != nil {
		return nil, err
	}
	keyHash := sha256.Sum256([]byte(encryptKey))
	if len(cipherText) < 50 { // 16 前缀 + 至少 16 密文 + 32 签名
		return nil, fmt.Errorf("feishu: ciphertext too short")
	}
	// 前 16 字节随机前缀 + 密文 + 后 32 字节签名
	prefix := cipherText[:16]
	actualCipher := cipherText[16 : len(cipherText)-32]
	sig := cipherText[len(cipherText)-32:]

	// 签名校验：SHA256(key + prefix + cipher)，防伪造。
	// 用 hmac 等价常量比较避免时序攻击。
	sigBuf := sha256.Sum256(append(append(keyHash[:], prefix...), actualCipher...))
	if !hmac.Equal(sigBuf[:], sig) {
		return nil, fmt.Errorf("feishu: signature mismatch (possible forgery)")
	}

	block, err := aes.NewCipher(keyHash[:])
	if err != nil {
		return nil, err
	}
	if len(actualCipher)%block.BlockSize() != 0 {
		return nil, fmt.Errorf("feishu: ciphertext not block-aligned")
	}
	// IV 全零（飞书规则：前缀只是占位，IV 用 16 字节 0）
	iv := make([]byte, block.BlockSize())
	mode := cipher.NewCBCDecrypter(block, iv)
	plain := make([]byte, len(actualCipher))
	mode.CryptBlocks(plain, actualCipher)
	// PKCS7 去填充
	if pad := int(plain[len(plain)-1]); pad > 0 && pad <= block.BlockSize() {
		plain = plain[:len(plain)-pad]
	}
	return plain, nil
}

// --- 卡片 JSON 转换 ---

// feishuCard 飞书交互卡片 schema（精简版，覆盖 Vigil 需要的标题/正文/按钮）。
type feishuCard struct {
	Schema string `json:"schema"`
	Header struct {
		Title    feishuText `json:"title"`
		Template string     `json:"template"` // red | orange | green | ...
	} `json:"header"`
	Elements []feishuElement `json:"elements"`
}

type feishuText struct {
	Tag     string `json:"tag"` // plain_text | lark_md
	Content string `json:"content"`
}

type feishuElement struct {
	Tag     string         `json:"tag"`
	Fields  []feishuField  `json:"fields,omitempty"` // column_set 用
	Text    *feishuText    `json:"text,omitempty"`
	Content *feishuText    `json:"content,omitempty"`
	Actions []feishuButton `json:"actions,omitempty"` // action 用
	// column_set
	Columns  []feishuColumn `json:"columns,omitempty"`
	FlexMode string         `json:"flex_mode,omitempty"`
	// div
	LarkMd string `json:"lark_md,omitempty"`
}

type feishuField struct {
	IsShort bool       `json:"is_short"`
	Text    feishuText `json:"text"`
}

type feishuColumn struct {
	Tag      string       `json:"tag"`
	Width    string       `json:"weight,omitempty"`
	Elements []feishuText `json:"elements,omitempty"`
}

type feishuButton struct {
	Tag   string            `json:"tag"` // button
	Text  feishuText        `json:"text"`
	Type  string            `json:"type"`  // primary | default
	Value map[string]string `json:"value"` // 回调携带的数据
}

// CardToFeishu 把平台无关 Card 转成飞书卡片 JSON。
func CardToFeishu(card *im.Card) (json.RawMessage, error) {
	if card == nil {
		return nil, fmt.Errorf("nil card")
	}
	out := feishuCard{Schema: "2.0"}
	out.Header.Title = feishuText{Tag: "plain_text", Content: card.Header}
	out.Header.Template = severityTemplate(card.Severity)

	// 正文：键值行用 column_set（两列）
	if len(card.Rows) > 0 {
		left := make([]feishuText, 0, len(card.Rows))
		right := make([]feishuText, 0, len(card.Rows))
		for i, r := range card.Rows {
			if i%2 == 0 {
				left = append(left, feishuText{Tag: "lark_md", Content: fmt.Sprintf("**%s**\n%s", r.Label, r.Value)})
			} else {
				right = append(right, feishuText{Tag: "lark_md", Content: fmt.Sprintf("**%s**\n%s", r.Label, r.Value)})
			}
		}
		cs := feishuElement{Tag: "column_set", FlexMode: "none"}
		cs.Columns = []feishuColumn{
			{Tag: "column", Elements: left},
			{Tag: "column", Elements: right},
		}
		out.Elements = append(out.Elements, cs)
	}
	// 状态标识（已确认 by xxx 等）
	if card.StatusBadge != "" {
		out.Elements = append(out.Elements, feishuElement{
			Tag: "div", LarkMd: card.StatusBadge,
		})
	}
	// 按钮：action 元素，每个按钮 value 带 incident_id 供回调定位
	if len(card.Buttons) > 0 {
		action := feishuElement{Tag: "action"}
		for _, b := range card.Buttons {
			btnType := b.Type
			if btnType == "" {
				btnType = "default"
			}
			action.Actions = append(action.Actions, feishuButton{
				Tag:   "button",
				Text:  feishuText{Tag: "plain_text", Content: b.Label},
				Type:  btnType,
				Value: map[string]string{"action": b.Value, "incident_id": card.IncidentID},
			})
		}
		out.Elements = append(out.Elements, action)
	}

	return json.Marshal(out)
}

// severityTemplate 飞书卡片 header 配色按严重度映射。
func severityTemplate(severity string) string {
	switch strings.ToLower(severity) {
	case "critical":
		return "red"
	case "warning":
		return "orange"
	case "info":
		return "blue"
	default:
		return "turquoise"
	}
}
