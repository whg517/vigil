package im

import (
	"context"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/incident"

	_ "github.com/mattn/go-sqlite3"
)

func newClient(t *testing.T) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:im_card_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestBuildCard_ContainsIncidentID 卡片骨架含 incident_id，供按钮回调定位。
func TestBuildCard_ContainsIncidentID(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()
	team, _ := c.Team.Create().SetName("t").SetSlug("t").Save(ctx)
	inc, err := c.Incident.Create().
		SetNumber("INC-0042").
		SetTitle("db down").
		SetSeverity(incident.SeverityCritical).
		SetStatus(incident.StatusTriggered).
		SetTeamID(team.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create incident: %v", err)
	}
	card := BuildCard(inc, "张三")
	if card.IncidentID != "1" {
		t.Errorf("incident_id: got %q, want 1", card.IncidentID)
	}
	if card.Header == "" {
		t.Error("header empty")
	}
	// 应含状态行
	foundStatus := false
	for _, r := range card.Rows {
		if r.Label == "状态" {
			foundStatus = true
		}
	}
	if !foundStatus {
		t.Error("status row missing")
	}
}

// TestWithPermittedButtons_OnlyGrantedButtons 按权限裁剪：无权的按钮不渲染。
func TestWithPermittedButtons_OnlyGrantedButtons(t *testing.T) {
	// 只授予 ack 权限
	renderer := NewRenderer(func(userID int, _ *int, perms []string) (map[string]bool, error) {
		out := make(map[string]bool, len(perms))
		for _, p := range perms {
			out[p] = p == "incident.ack" // 仅 ack 授权
		}
		return out, nil
	})
	card := &Card{IncidentID: "1", Header: "h"}
	err := renderer.WithPermittedButtons(card, 1, nil, DefaultButtons())
	if err != nil {
		t.Fatalf("WithPermittedButtons: %v", err)
	}
	// 只应有 1 个按钮（ack）
	if len(card.Buttons) != 1 {
		t.Fatalf("buttons: got %d, want 1", len(card.Buttons))
	}
	if card.Buttons[0].Value != ActionAck {
		t.Errorf("button value: got %s, want %s", card.Buttons[0].Value, ActionAck)
	}
}

// TestWithPermittedButtons_NilAuthConservative 无鉴权回调时保守不渲染任何按钮。
func TestWithPermittedButtons_NilAuthConservative(t *testing.T) {
	renderer := &Renderer{} // HasPermission 为 nil
	card := &Card{IncidentID: "1"}
	err := renderer.WithPermittedButtons(card, 1, nil, DefaultButtons())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(card.Buttons) != 0 {
		t.Errorf("expected 0 buttons when no auth, got %d", len(card.Buttons))
	}
}

// TestCardStore_PutGet 卡片 ID 存取。
func TestCardStore_PutGet(t *testing.T) {
	s := NewCardStore()
	s.Put(42, "feishu", "msg_xxx")
	if id, ok := s.Get(42, "feishu"); !ok || id != "msg_xxx" {
		t.Errorf("Get: got %q ok=%v, want msg_xxx true", id, ok)
	}
	if _, ok := s.Get(42, "dingtalk"); ok {
		t.Error("dingtalk should not exist")
	}
}
