package dingtalk

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kevin/vigil/internal/im"
)

// TestAvailable_CredentialsMissing 缺 AppSecret 时不可用。
func TestAvailable_CredentialsMissing(t *testing.T) {
	a := New(Config{AppKey: "dingxxx"})
	if a.Available() {
		t.Error("缺 AppSecret 时 Available 应为 false")
	}
}

// TestAvailable_BothConfigured AppKey+AppSecret 齐备则可用。
func TestAvailable_BothConfigured(t *testing.T) {
	a := New(Config{AppKey: "dingxxx", AppSecret: "sec"})
	if !a.Available() {
		t.Error("AppKey+AppSecret 齐备时 Available 应为 true")
	}
	if a.Platform() != "dingtalk" {
		t.Errorf("platform: got %s, want dingtalk", a.Platform())
	}
}

// TestRobotCodeDefault 未显式配 RobotCode 时默认等于 AppKey（钉钉约定）。
func TestRobotCodeDefault(t *testing.T) {
	c := NewClient(Config{AppKey: "dingabc", AppSecret: "sec"})
	if c.RobotCode() != "dingabc" {
		t.Errorf("RobotCode 缺省应等于 AppKey，got %q", c.RobotCode())
	}
}

// newMockServer 启动一个钉钉 mock 服务端：
//   - /gettoken 返回 access_token
//   - /v1.0/robot/oToMessages/batchSend、/v1.0/robot/groupMessages/send 返回发消息结果
//
// 返回服务端实例（用于断言请求计数）与 (oapiBase, apiBase) 注入 Config。
type mockServer struct {
	*httptest.Server
	tokenHits   int32
	otoHits     int32
	groupHits   int32
	lastOTOBody map[string]any
}

func newMockServer(t *testing.T) *mockServer {
	m := &mockServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/gettoken", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&m.tokenHits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok","access_token":"mock-token","expires_in":7200}`))
	})
	mux.HandleFunc("/v1.0/robot/oToMessages/batchSend", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&m.otoHits, 1)
		if r.Header.Get("x-acs-dingtalk-access-token") != "mock-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &m.lastOTOBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"processQueryKey":"pqk_001"}`))
	})
	mux.HandleFunc("/v1.0/robot/groupMessages/send", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&m.groupHits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messageId":"msg_002"}`))
	})
	m.Server = httptest.NewServer(mux)
	t.Cleanup(m.Close)
	return m
}

// adapterFromMock 用 mock 服务端构造适配器，域名指向 mock。
func adapterFromMock(m *mockServer) *Adapter {
	return New(Config{
		AppKey:    "dingxxx",
		AppSecret: "sec",
		OapiBase:  m.URL,
		APIBase:   m.URL,
	})
}

// TestSendCard_OTOSingleChat userId 前缀走单聊接口，body 含 robotCode/userIds/msgKey/msgParam。
func TestSendCard_OTOSingleChat(t *testing.T) {
	m := newMockServer(t)
	a := adapterFromMock(m)
	card := &im.Card{IncidentID: "7", Header: "[CRITICAL] db down", Severity: "critical"}

	id, err := a.SendCard(context.Background(), "userId:staff123", card)
	if err != nil {
		t.Fatalf("SendCard oto: %v", err)
	}
	// cardID 编码了 channel（供 UpdateCard 降级重发定位），msgID 部分应是 pqk_001。
	if _, msgID, _ := decodeCardID(id); msgID != "pqk_001" {
		t.Errorf("card id msgID: got %q, want pqk_001", msgID)
	}
	if atomic.LoadInt32(&m.otoHits) != 1 {
		t.Errorf("oto 接口应被调 1 次，got %d", m.otoHits)
	}
	if m.lastOTOBody["msgKey"] != "sampleActionCard" {
		t.Errorf("msgKey: got %v", m.lastOTOBody["msgKey"])
	}
	uids, _ := m.lastOTOBody["userIds"].([]any)
	if len(uids) != 1 || uids[0] != "staff123" {
		t.Errorf("userIds: got %v", uids)
	}
	// msgParam 应是 JSON 字符串且含标题
	mp, _ := m.lastOTOBody["msgParam"].(string)
	if !strings.Contains(mp, "db down") {
		t.Errorf("msgParam 缺标题: %s", mp)
	}
}

// TestSendCard_Group 群前缀走群发接口，返回 messageId。
func TestSendCard_Group(t *testing.T) {
	m := newMockServer(t)
	a := adapterFromMock(m)
	card := &im.Card{IncidentID: "7", Header: "[WARNING] cpu high", Severity: "warning"}

	id, err := a.SendCard(context.Background(), "openConversationId:ocid_group", card)
	if err != nil {
		t.Fatalf("SendCard group: %v", err)
	}
	if _, msgID, _ := decodeCardID(id); msgID != "msg_002" {
		t.Errorf("card id msgID: got %q, want msg_002", msgID)
	}
	if atomic.LoadInt32(&m.groupHits) != 1 {
		t.Errorf("group 接口应被调 1 次，got %d", m.groupHits)
	}
}

// TestSendCard_InvalidChannel 无法识别的 channel 前缀应报错。
func TestSendCard_InvalidChannel(t *testing.T) {
	m := newMockServer(t)
	a := adapterFromMock(m)
	_, err := a.SendCard(context.Background(), "bogus:noprefix", &im.Card{})
	if err == nil {
		t.Error("无效 channel 应报错")
	}
}

// TestSendCard_ButtonActionURLEncoding 卡片按钮编码成 actionURL（vigil://action?act=&inc=）。
func TestSendCard_ButtonActionURLEncoding(t *testing.T) {
	m := newMockServer(t)
	a := adapterFromMock(m)
	card := &im.Card{
		IncidentID: "42",
		Header:     "test",
		Buttons:    []im.CardButton{{Label: "确认", Value: im.ActionAck, Type: "primary"}},
	}
	_, err := a.SendCard(context.Background(), "userId:s1", card)
	if err != nil {
		t.Fatalf("SendCard: %v", err)
	}
	mp, _ := m.lastOTOBody["msgParam"].(string)
	var p actionCardParam
	if err := json.Unmarshal([]byte(mp), &p); err != nil {
		t.Fatalf("unmarshal msgParam: %v", err)
	}
	if len(p.Btns) != 1 || !strings.Contains(p.Btns[0].ActionURL, "act=ack") ||
		!strings.Contains(p.Btns[0].ActionURL, "inc=42") {
		t.Errorf("按钮 actionURL 编码错误: %+v", p.Btns)
	}
}

// TestUpdateCard_NoChannel 裸 cardID（无编码 channel，历史数据）无法定位群重发，
// UpdateCard 应返回 ErrCardUpdateNoChannel（降级未成，best-effort 让调用方感知，不阻塞主流程）。
func TestUpdateCard_NoChannel(t *testing.T) {
	a := New(Config{AppKey: "k", AppSecret: "s"})
	err := a.UpdateCard(context.Background(), "msg_x", &im.Card{})
	if !errors.Is(err, im.ErrCardUpdateNoChannel) {
		t.Errorf("裸 cardID 应返回 ErrCardUpdateNoChannel，got %v", err)
	}
}

// TestUpdateCard_DegradeResend B16：钉钉卡片降级——UpdateCard 从 cardID 解出 channel，
// 向同一群重发一条新消息标注最新状态（钉钉无原地更新能力）。
func TestUpdateCard_DegradeResend(t *testing.T) {
	m := newMockServer(t)
	a := adapterFromMock(m)
	// 先发一张群卡片，返回的 cardID 应编码了 channel。
	cardID, err := a.SendCard(context.Background(), "openConversationId:ocid_group", &im.Card{
		IncidentID: "42", Header: "[CRITICAL] INC-0042 db down", Severity: "critical",
	})
	if err != nil {
		t.Fatalf("SendCard: %v", err)
	}
	groupBefore := atomic.LoadInt32(&m.groupHits)
	// UpdateCard 降级：解出 channel 重发新消息（群发接口应再被调一次）。
	statusCard := &im.Card{
		IncidentID: "42", Header: "[CRITICAL] INC-0042 db down", Severity: "critical",
		StatusBadge: "⚠️ INC-0042 已确认（by 张三）",
	}
	if err := a.UpdateCard(context.Background(), cardID, statusCard); err != nil {
		t.Fatalf("UpdateCard 降级重发应成功，got %v", err)
	}
	if got := atomic.LoadInt32(&m.groupHits); got != groupBefore+1 {
		t.Errorf("降级应向群重发一条新消息，groupHits: got %d, want %d", got, groupBefore+1)
	}
}

// TestSendCard_CardIDEncodesChannel SendCard 返回的 cardID 应编码 channel（供 UpdateCard 降级重发定位群）。
func TestSendCard_CardIDEncodesChannel(t *testing.T) {
	m := newMockServer(t)
	a := adapterFromMock(m)
	cardID, err := a.SendCard(context.Background(), "openConversationId:ocid_group", &im.Card{IncidentID: "1", Header: "x"})
	if err != nil {
		t.Fatalf("SendCard: %v", err)
	}
	channel, msgID, ok := decodeCardID(cardID)
	if !ok || channel != "openConversationId:ocid_group" {
		t.Errorf("cardID 应编码 channel，got channel=%q ok=%v", channel, ok)
	}
	if msgID != "msg_002" {
		t.Errorf("cardID 应保留原 msgID，got %q", msgID)
	}
}

// TestParseCallback_Mention B16：钉钉群 @机器人 同时 @他人 → 解析为 mention（拉人）。
func TestParseCallback_Mention(t *testing.T) {
	a := New(Config{AppKey: "k", AppSecret: "s"})
	// atUsers 含被 @的人（staffId 优先），文本含 incident 编号供上层解析。
	body := []byte(`{"senderStaffId":"staff1","conversationId":"ocid_g","text":{"content":"@机器人 INC-0042 帮看 DB"},"atUsers":[{"dingtalkId":"ding_bob","staffId":"staff_bob"}]}`)
	ev, err := a.ParseCallback(body)
	if err != nil {
		t.Fatalf("ParseCallback: %v", err)
	}
	if ev.Type != im.EventMention {
		t.Fatalf("type: got %v, want mention", ev.Type)
	}
	if len(ev.MentionAt) != 1 || ev.MentionAt[0] != "staff_bob" {
		t.Errorf("MentionAt: got %v, want [staff_bob]", ev.MentionAt)
	}
	if ev.UnionID != "staff1" {
		t.Errorf("operator unionID: got %q, want staff1", ev.UnionID)
	}
}

// TestParseCallback_MentionFallbackDingtalkID staffId 缺失时（钉钉权限限制）退回 dingtalkId。
func TestParseCallback_MentionFallbackDingtalkID(t *testing.T) {
	a := New(Config{AppKey: "k", AppSecret: "s"})
	body := []byte(`{"senderStaffId":"staff1","conversationId":"ocid_g","text":{"content":"@机器人 42"},"atUsers":[{"dingtalkId":"ding_bob"}]}`)
	ev, err := a.ParseCallback(body)
	if err != nil {
		t.Fatalf("ParseCallback: %v", err)
	}
	if ev.Type != im.EventMention || len(ev.MentionAt) != 1 || ev.MentionAt[0] != "ding_bob" {
		t.Errorf("staffId 缺失应退回 dingtalkId，got type=%v mentions=%v", ev.Type, ev.MentionAt)
	}
}

// TestParseCallback_MentionExcludesSelf 被 @的人里若含操作者本人，应排除（不把自己拉进来）。
func TestParseCallback_MentionExcludesSelf(t *testing.T) {
	a := New(Config{AppKey: "k", AppSecret: "s"})
	body := []byte(`{"senderStaffId":"staff1","conversationId":"ocid_g","text":{"content":"@机器人 42"},"atUsers":[{"staffId":"staff1"}]}`)
	ev, err := a.ParseCallback(body)
	if err != nil {
		t.Fatalf("ParseCallback: %v", err)
	}
	// 只 @了自己 → 无有效被拉人 → 退化为普通消息（非 mention）。
	if ev.Type == im.EventMention {
		t.Errorf("只 @自己不应成为 mention，got type=%v mentions=%v", ev.Type, ev.MentionAt)
	}
}

// TestParseCallback_CardAction actionURL 卡片回调解析出 action+incident_id。
func TestParseCallback_CardAction(t *testing.T) {
	a := New(Config{AppKey: "k", AppSecret: "s"})
	// 钉钉卡片回调 content 为 {"actionUrl":"vigil://action?act=ack&inc=42"}
	body := []byte(`{"senderStaffId":"staff1","conversationId":"ocid_g","content":"{\"actionUrl\":\"vigil://action?act=ack&inc=42\"}"}`)
	ev, err := a.ParseCallback(body)
	if err != nil {
		t.Fatalf("ParseCallback: %v", err)
	}
	if ev.Type != im.EventCardAction {
		t.Errorf("type: got %v, want card_action", ev.Type)
	}
	if ev.Action != "ack" || ev.IncidentID != "42" {
		t.Errorf("action/inc: got %q/%q", ev.Action, ev.IncidentID)
	}
	if ev.UnionID != "staff1" {
		t.Errorf("unionID: got %q", ev.UnionID)
	}
}

// TestParseCallback_SlashCommand /vigil 斜杠命令识别。
func TestParseCallback_SlashCommand(t *testing.T) {
	a := New(Config{AppKey: "k", AppSecret: "s"})
	body := []byte(`{"senderStaffId":"staff1","conversationId":"ocid_g","text":{"content":"/vigil ack INC-0042"}}`)
	ev, err := a.ParseCallback(body)
	if err != nil {
		t.Fatalf("ParseCallback: %v", err)
	}
	if ev.Type != im.EventCommand {
		t.Errorf("type: got %v, want command", ev.Type)
	}
	if ev.Command != "ack" || ev.CommandArg != "INC-0042" {
		t.Errorf("command/arg: got %q/%q", ev.Command, ev.CommandArg)
	}
}

// TestParseCallback_PlainMessage 普通文本消息按 message 返回。
func TestParseCallback_PlainMessage(t *testing.T) {
	a := New(Config{AppKey: "k", AppSecret: "s"})
	body := []byte(`{"senderStaffId":"staff1","text":{"content":"hello world"}}`)
	ev, err := a.ParseCallback(body)
	if err != nil {
		t.Fatalf("ParseCallback: %v", err)
	}
	if ev.Type != im.EventMessage {
		t.Errorf("type: got %v, want message", ev.Type)
	}
	if ev.Text != "hello world" {
		t.Errorf("text: got %q", ev.Text)
	}
}

// TestVerifyCallback_Plaintext 无 aes_key（明文模式）原样返回。
func TestVerifyCallback_Plaintext(t *testing.T) {
	a := New(Config{AppKey: "k", AppSecret: "s"}) // 无 AesKey
	in := []byte(`{"foo":"bar"}`)
	out, err := a.VerifyCallback(map[string]string{}, in)
	if err != nil {
		t.Fatalf("VerifyCallback: %v", err)
	}
	if string(out) != string(in) {
		t.Errorf("明文模式应原样返回")
	}
}

// TestVerifyCallback_Encrypted 加密模式：解密回原文。
// 用真实 AES-256-CBC 加密一份 payload，再让适配器解密验证。
func TestVerifyCallback_Encrypted(t *testing.T) {
	// 生成 32 字节 aes key，base64 编码（钉钉 aes_key 是 base64 43 字符，这里用标准 44 字符验证逻辑）
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	aesKeyB64 := base64.StdEncoding.EncodeToString(key)
	// 钉钉密文布局：16 字节随机前缀 + 4 字节 msg_len + msg + corpId，AES-256-CBC(IV=0)
	plaintext := []byte(`{"event":"test"}`)
	if len(plaintext) > 0xFF {
		t.Fatalf("测试 payload 超过单字节 msg_len 可表示范围: %d", len(plaintext))
	}
	prefix := make([]byte, 16)
	msgLen := make([]byte, 4)
	msgLen[3] = byte(len(plaintext) & 0xFF)
	corp := []byte("ding123")
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	plain := append(append(append(prefix, msgLen...), plaintext...), corp...)
	// PKCS7 填充到块整数倍
	bs := block.BlockSize()
	pad := bs - len(plain)%bs
	if pad < 1 || pad > 0xFF {
		t.Fatalf("PKCS7 pad 超出 byte 范围: %d", pad)
	}
	for i := 0; i < pad; i++ {
		plain = append(plain, byte(pad&0xFF))
	}
	cipherText := make([]byte, len(plain))
	iv := make([]byte, bs)
	// 用 CBC 加密器加密（与解密对称）
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(cipherText, plain)
	encryptB64 := base64.StdEncoding.EncodeToString(cipherText)

	a := NewWithClient(NewClient(Config{AppKey: "k", AppSecret: "s", AesKey: aesKeyB64}))
	envelope := `{"encrypt":"` + encryptB64 + `"}`
	out, err := a.VerifyCallback(map[string]string{}, []byte(envelope))
	if err != nil {
		t.Fatalf("VerifyCallback decrypt: %v", err)
	}
	if string(out) != string(plaintext) {
		t.Errorf("解密结果不符：got %q, want %q", string(out), string(plaintext))
	}
}

// TestDingtalkSign 签名计算稳定（同输入同输出，且非空）。
func TestDingtalkSign(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	s1 := dingtalkSign(key, "1700000000000", "tok", "n1", "enc123")
	s2 := dingtalkSign(key, "1700000000000", "tok", "n1", "enc123")
	if s1 == "" || s1 != s2 {
		t.Errorf("签名应稳定非空: %q vs %q", s1, s2)
	}
	// 输入变化则签名变化
	s3 := dingtalkSign(key, "1700000000001", "tok", "n1", "enc123")
	if s1 == s3 {
		t.Error("时间戳变化签名应变化")
	}
}

// TestCardToActionCard_RenderStructure ActionCard msgParam 结构正确。
func TestCardToActionCard_RenderStructure(t *testing.T) {
	card := &im.Card{
		IncidentID:  "1",
		Header:      "[CRITICAL] INC-0042 db down",
		Severity:    "critical",
		StatusBadge: "已确认 by 张三",
		Rows: []im.CardRow{
			{Label: "状态", Value: "待响应"},
			{Label: "负责人", Value: "张三"},
		},
		Buttons: []im.CardButton{
			{Label: "✓ 确认", Value: im.ActionAck, Type: "primary"},
			{Label: "📋 详情", Value: im.ActionDetail, Type: "default"},
		},
	}
	raw, err := CardToActionCard(card)
	if err != nil {
		t.Fatalf("CardToActionCard: %v", err)
	}
	var p actionCardParam
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !strings.Contains(p.Title, "INC-0042") {
		t.Errorf("title 缺编号: %s", p.Title)
	}
	if !strings.Contains(p.Text, "已确认") {
		t.Errorf("text 缺状态徽章: %s", p.Text)
	}
	if !strings.Contains(p.Text, "负责人") {
		t.Errorf("text 缺键值行: %s", p.Text)
	}
	if len(p.Btns) != 2 {
		t.Fatalf("btns: got %d, want 2", len(p.Btns))
	}
	// ack 按钮走 vigil://action，detail 走 incidents 链接
	if !strings.Contains(p.Btns[0].ActionURL, "act=ack") {
		t.Errorf("ack 按钮 actionURL: %s", p.Btns[0].ActionURL)
	}
	if !strings.Contains(p.Btns[1].ActionURL, "/incidents/1") {
		t.Errorf("detail 按钮 actionURL: %s", p.Btns[1].ActionURL)
	}
}

// TestParseChannel channel 前缀解析。
func TestParseChannel(t *testing.T) {
	cases := []struct {
		in       string
		wantType string
		wantID   string
		ok       bool
	}{
		{"userId:staff1", "userId", "staff1", true},
		{"openConversationId:ocid_x", "openConversationId", "ocid_x", true},
		{"noprefix", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		typ, id, ok := parseChannel(c.in)
		if ok != c.ok || typ != c.wantType || id != c.wantID {
			t.Errorf("parseChannel(%q): got (%q,%q,%v), want (%q,%q,%v)",
				c.in, typ, id, ok, c.wantType, c.wantID, c.ok)
		}
	}
}
