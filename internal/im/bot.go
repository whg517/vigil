// Package im 实现能力域 8：IM 协同（ChatOps）的适配器抽象与操作链路。
//
// 对应 docs/capabilities/05-im-chatops.md：
//   - IMBot 接口（§9 可插拔）：卡片下发/更新、建群、回调解析——业务层不感知平台差异
//   - Card 数据结构 + 按权限渲染按钮（§3.1，无权按钮不显示，IM 不成权限后门）
//   - Mapper：im_unionid → User 映射（§6，IM 操作走与 Web 相同的鉴权链路）
//   - Handler：IM Webhook 回调 → 账号映射 → RBAC → incident.Service 动作 → 刷新卡片
//
// 平台支持矩阵（§2 + §10）：
//   - feishu：真实适配器（卡片更新✅ 建群✅ @人✅）
//   - dingtalk / wecom：占位 NoopBot，留待 PoC
package im

import (
	"context"
	"errors"
)

// IMBot IM 平台适配器接口。各平台（飞书/钉钉/企微）实现此接口。
// 对应 capabilities/05-im-chatops.md §9。
type IMBot interface {
	// Platform 平台标识：feishu | dingtalk | wecom
	Platform() string

	// Available 该适配器是否可用（凭证已配置、客户端就绪）。
	// false 时调用方应跳过该平台（降级为不发送），对应设计基线第 7 条降级模式。
	Available() bool

	// SendCard 向指定频道/会话下发卡片，返回卡片 ID（用于后续 UpdateCard）。
	SendCard(ctx context.Context, channel string, card *Card) (cardID string, err error)

	// UpdateCard 更新已下发卡片（平台能力依赖，部分平台不支持则降级为发新消息）。
	UpdateCard(ctx context.Context, cardID string, card *Card) error

	// CreateWarRoom 创建作战室临时群，返回群 ID。
	CreateWarRoom(ctx context.Context, name string, members []string) (roomID string, err error)

	// VerifyCallback 校验 IM 平台回调请求的签名/令牌（防伪造）。
	// 返回解密后的原始 payload 与 nil 表示校验通过。
	VerifyCallback(headers map[string]string, rawBody []byte) ([]byte, error)

	// ParseCallback 解析回调 payload 为标准化 IMEvent（按钮点击/@人/斜杠命令）。
	ParseCallback(payload []byte) (*IMEvent, error)
}

// IMEventType 回调事件类型。
type IMEventType string

const (
	EventCardAction IMEventType = "card_action" // 卡片按钮点击
	EventMention    IMEventType = "mention"     // @机器人（拉人协同）
	EventCommand    IMEventType = "command"     // 斜杠命令 /vigil xxx
	EventMessage    IMEventType = "message"     // 普通群消息（可选回写时间线）
)

// IMEvent 标准化回调事件（平台无关）。
type IMEvent struct {
	Type      IMEventType
	Platform  string // feishu | dingtalk | wecom
	UnionID   string // 操作者的 IM 平台唯一标识，用于映射 User
	ChannelID string // 事件来源频道/群/会话 ID
	// CardAction 专用：用户点击的按钮 action（如 ack/escalate/resolve）+ 关联 incident
	Action     string
	IncidentID string
	// Command 专用：斜杠命令解析后的命令名 + 原始参数
	Command    string
	CommandArg string
	// Mention/Message 专用：消息正文
	Text string
	// MentionAt 被 @ 的 IM 用户 unionID 列表（拉人协同场景）
	MentionAt []string
}

// ErrNotBound IM 账号未绑定到任何 Vigil User。
// 对应 capabilities §6：未绑定 IM 账号的用户操作被拒，提示去 Web 绑定。
var ErrNotBound = errors.New("im account not bound to any user")

// ErrUnsupported 不支持的平台操作（如某平台无卡片更新能力且未实现降级）。
var ErrUnsupported = errors.New("operation unsupported by im platform")

// ErrCardUpdateNoChannel 卡片降级重发时缺少目标 channel（无法定位重发目标）。
// 钉钉等无原地更新能力的平台，UpdateCard 靠「重发到原 channel」降级；
// cardID 未编码 channel（历史裸 msgID）时返回它——降级 best-effort 未成，不阻塞主流程。
var ErrCardUpdateNoChannel = errors.New("im: card update degrade needs channel but none encoded")
