package feishu

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kevin/vigil/internal/im"
)

// TestAvailable_CredentialsMissing 缺 AppSecret 时不可用。
func TestAvailable_CredentialsMissing(t *testing.T) {
	a := New(Config{AppID: "cli_xxx"}) // 只有 AppID
	if a.Available() {
		t.Error("should be unavailable without app_secret")
	}
}

// TestAvailable_BothConfigured AppID+AppSecret 齐备则可用。
func TestAvailable_BothConfigured(t *testing.T) {
	a := New(Config{AppID: "cli_xxx", AppSecret: "sec_xxx"})
	if !a.Available() {
		t.Error("should be available with both app_id and app_secret")
	}
	if a.Platform() != "feishu" {
		t.Errorf("platform: got %s, want feishu", a.Platform())
	}
}

// TestCardToFeishu_RenderStructure 卡片转飞书 JSON 结构正确。
func TestCardToFeishu_RenderStructure(t *testing.T) {
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
			{Label: "✓ 确认", Value: "ack", Type: "primary"},
		},
	}
	raw, err := CardToFeishu(card)
	if err != nil {
		t.Fatalf("CardToFeishu: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	// schema 2.0
	if out["schema"] != "2.0" {
		t.Errorf("schema: got %v, want 2.0", out["schema"])
	}
	header := out["header"].(map[string]any)
	title := header["title"].(map[string]any)
	if !strings.Contains(title["content"].(string), "INC-0042") {
		t.Errorf("title missing incident number: %v", title["content"])
	}
	// critical → red 配色
	if header["template"] != "red" {
		t.Errorf("template: got %v, want red", header["template"])
	}
	// 元素应有 column_set（正文）+ div（状态）+ action（按钮）
	elements := out["elements"].([]any)
	if len(elements) < 3 {
		t.Errorf("elements: got %d, want >=3", len(elements))
	}
	// 按钮带 incident_id 回调值
	action := elements[2].(map[string]any)
	btns := action["actions"].([]any)
	btn := btns[0].(map[string]any)
	val := btn["value"].(map[string]any)
	if val["action"] != "ack" || val["incident_id"] != "1" {
		t.Errorf("button value: got %v", val)
	}
}

// TestCardToFeishu_NilCard nil 卡片报错。
func TestCardToFeishu_NilCard(t *testing.T) {
	_, err := CardToFeishu(nil)
	if err == nil {
		t.Fatal("expected error for nil card")
	}
}

// TestSeverityTemplate 严重度配色映射。
func TestSeverityTemplate(t *testing.T) {
	cases := map[string]string{
		"critical": "red",
		"warning":  "orange",
		"info":     "blue",
		"":         "turquoise",
	}
	for sev, want := range cases {
		if got := severityTemplate(sev); got != want {
			t.Errorf("severityTemplate(%q): got %s, want %s", sev, got, want)
		}
	}
}

// TestParseSlashCommand 斜杠命令解析。
func TestParseSlashCommand(t *testing.T) {
	cases := []struct {
		text     string
		wantCmd  string
		wantArg  string
		wantOK   bool
	}{
		{"/vigil ack INC-0042", "ack", "INC-0042", true},
		{"/vigil escalate 1", "escalate", "1", true},
		{"/vigil status INC-0042", "status", "INC-0042", true},
		{"/vigil ack", "ack", "", true},
		{"hello world", "", "", false},      // 非 vigil 命令
		{"ack INC-1", "", "", false},        // 缺 / 前缀
		{"/vigil  ", "", "", false},         // 无命令名
	}
	for _, c := range cases {
		cmd, arg, ok := parseSlashCommand(c.text)
		if ok != c.wantOK || cmd != c.wantCmd || arg != c.wantArg {
			t.Errorf("parseSlashCommand(%q): got (%q,%q,%v), want (%q,%q,%v)",
				c.text, cmd, arg, ok, c.wantCmd, c.wantArg, c.wantOK)
		}
	}
}

// TestParseChannel channel 格式解析。
func TestParseChannel(t *testing.T) {
	cases := []struct {
		channel string
		wantType, wantID string
		wantOK bool
	}{
		{"open_id:ou_xxx", "open_id", "ou_xxx", true},
		{"chat_id:oc_yyy", "chat_id", "oc_yyy", true},
		{"invalid", "", "", false},
		{":noID", "", "", false},
	}
	for _, c := range cases {
		idType, id, ok := parseChannel(c.channel)
		if ok != c.wantOK || idType != c.wantType || id != c.wantID {
			t.Errorf("parseChannel(%q): got (%q,%q,%v), want (%q,%q,%v)",
				c.channel, idType, id, ok, c.wantType, c.wantID, c.wantOK)
		}
	}
}

// TestParseCallback_CardAction 卡片按钮回调解析为 IMEvent。
func TestParseCallback_CardAction(t *testing.T) {
	a := New(Config{AppID: "x", AppSecret: "y", VerificationToken: "tok"})
	payload := []byte(`{
		"schema": "2.0",
		"header": {"token": "tok", "event_type": "card.action.trigger", "app_id": "cli_x"},
		"event": {
			"operator": {"open_id": "ou_op"},
			"action": {"value": {"action": "ack", "incident_id": "42"}},
			"open_conversation_id": "oc_c"
		}
	}`)
	evt, err := a.ParseCallback(payload)
	if err != nil {
		t.Fatalf("ParseCallback: %v", err)
	}
	if evt.Type != im.EventCardAction {
		t.Errorf("type: got %s, want %s", evt.Type, im.EventCardAction)
	}
	if evt.UnionID != "ou_op" {
		t.Errorf("union_id: got %s, want ou_op", evt.UnionID)
	}
	if evt.Action != "ack" || evt.IncidentID != "42" {
		t.Errorf("action/incident: got %s/%s", evt.Action, evt.IncidentID)
	}
}

// TestParseCallback_TokenMismatch 校验 token 不匹配则拒绝。
func TestParseCallback_TokenMismatch(t *testing.T) {
	a := New(Config{AppID: "x", AppSecret: "y", VerificationToken: "correct"})
	payload := []byte(`{
		"schema":"2.0",
		"header":{"token":"wrong","event_type":"card.action.trigger"},
		"event":{"operator":{"open_id":"o"},"action":{"value":{}}}
	}`)
	_, err := a.ParseCallback(payload)
	if err == nil {
		t.Fatal("expected token mismatch error, got nil")
	}
}

// TestParseCallback_MessageCommand @机器人 + 斜杠命令解析为 command 事件。
func TestParseCallback_MessageCommand(t *testing.T) {
	a := New(Config{AppID: "x", AppSecret: "y", VerificationToken: "tok"})
	payload := []byte(`{
		"schema":"2.0",
		"header":{"token":"tok","event_type":"im.message.receive_v1"},
		"event":{
			"sender":{"sender_id":{"open_id":"ou_sender"}},
			"message":{"chat_id":"oc_1","content":"{\"text\":\"/vigil ack INC-9\"}"}
		}
	}`)
	evt, err := a.ParseCallback(payload)
	if err != nil {
		t.Fatalf("ParseCallback: %v", err)
	}
	if evt.Type != im.EventCommand {
		t.Errorf("type: got %s, want %s", evt.Type, im.EventCommand)
	}
	if evt.Command != "ack" || evt.CommandArg != "INC-9" {
		t.Errorf("command/arg: got %s/%s", evt.Command, evt.CommandArg)
	}
}

// TestVerifyCallback_Plaintext 无 EncryptKey 时明文直通。
func TestVerifyCallback_Plaintext(t *testing.T) {
	a := New(Config{AppID: "x", AppSecret: "y"}) // 无 EncryptKey
	body := []byte(`{"schema":"2.0","header":{},"event":{}}`)
	out, err := a.VerifyCallback(nil, body)
	if err != nil {
		t.Fatalf("VerifyCallback: %v", err)
	}
	if string(out) != string(body) {
		t.Error("plaintext body should pass through unchanged")
	}
}
