package im

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/role"
	"github.com/kevin/vigil/ent/rolebinding"
	"github.com/kevin/vigil/ent/schema"
	"github.com/kevin/vigil/internal/auth"
	imincident "github.com/kevin/vigil/internal/incident"
	"github.com/kevin/vigil/internal/timeline"

	"github.com/labstack/echo/v4"
	_ "github.com/mattn/go-sqlite3"
)

// stubBot 测试用 IMBot，记录调用，不做真实 IO。
type stubBot struct {
	platform    string
	available   bool
	updated     []*Card           // 记录 UpdateCard 的入参
	cardIDs     map[string]string // channel → 生成的 cardID
	sendCount   int
	updateCount int
}

func newStubBot(platform string, available bool) *stubBot {
	return &stubBot{platform: platform, available: available, cardIDs: map[string]string{}}
}

func (b *stubBot) Platform() string { return b.platform }
func (b *stubBot) Available() bool  { return b.available }
func (b *stubBot) SendCard(_ context.Context, ch string, c *Card) (string, error) {
	b.sendCount++
	id := "card_" + strconv.Itoa(b.sendCount)
	b.cardIDs[ch] = id
	return id, nil
}
func (b *stubBot) UpdateCard(_ context.Context, _ string, c *Card) error {
	b.updateCount++
	b.updated = append(b.updated, c)
	return nil
}
func (b *stubBot) CreateWarRoom(_ context.Context, _ string, _ []string) (string, error) {
	return "room_x", nil
}
func (b *stubBot) VerifyCallback(_ map[string]string, body []byte) ([]byte, error) { return body, nil }
func (b *stubBot) ParseCallback(payload []byte) (*IMEvent, error) {
	var e IMEvent
	_ = json.Unmarshal(payload, &e)
	return &e, nil
}

func newHandlerClient(t *testing.T) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:im_handler_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// seedFullSetup 建团队 + 事件 + 用户(绑 feishu) + 授 ack 权限的角色绑定。
// 返回 incident id 与 user id。grantAck=true 时授权，false 时不授权。
func seedFullSetup(t *testing.T, c *ent.Client, grantAck bool) (incID, userID, teamID int) {
	t.Helper()
	ctx := context.Background()
	team, _ := c.Team.Create().SetName("支付").SetSlug("pay").Save(ctx)
	teamID = team.ID
	u, err := c.User.Create().
		SetUsername("zs").
		SetEmail("zs@x.com").
		SetImAccounts([]schema.IMAccount{{Platform: "feishu", AccountID: "ou_zs"}}).
		Save(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	userID = u.ID
	inc, err := c.Incident.Create().
		SetNumber("INC-0001").
		SetTitle("db down").
		SetSeverity(incident.SeverityCritical).
		SetStatus(incident.StatusTriggered).
		SetTeamID(team.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create incident: %v", err)
	}
	incID = inc.ID

	if grantAck {
		// 建 team 级角色，含 incident.ack 权限
		rl, err := c.Role.Create().
			SetName("responder").
			SetScopeLevel(role.ScopeLevelTeam).
			SetPermissions([]string{string(auth.PermIncidentAck)}).
			Save(ctx)
		if err != nil {
			t.Fatalf("create role: %v", err)
		}
		_, err = c.RoleBinding.Create().
			SetUserID(u.ID).
			SetRoleID(rl.ID).
			SetScopeLevel(rolebinding.ScopeLevelTeam).
			SetTeamID(strconv.Itoa(team.ID)).
			SetGrantedAt(time.Now()).
			Save(ctx)
		if err != nil {
			t.Fatalf("create binding: %v", err)
		}
	}
	return
}

// newHandlerWith 构造完整 handler（真实 authz + incident service + stub bot）。
func newHandlerWith(t *testing.T, c *ent.Client, bot *stubBot) (*Handler, *CardStore) {
	t.Helper()
	authz := auth.NewAuthorizer(c)
	rec := timeline.NewRecorder(c)
	incSvc := imincident.NewService(c, rec, nil)
	mapper := NewMapper(c)
	cards := NewCardStore()
	renderer := NewRenderer(func(userID int, teamScope *int, perms []string) (map[string]bool, error) {
		pp := make([]auth.Permission, 0, len(perms))
		for _, p := range perms {
			pp = append(pp, auth.Permission(p))
		}
		g, err := authz.CheckAny(context.Background(), userID, teamScope, pp)
		if err != nil {
			return nil, err
		}
		out := make(map[string]bool, len(g))
		for p, ok := range g {
			out[string(p)] = ok
		}
		return out, nil
	})
	reg := NewRegistry()
	reg.Register(bot)
	h := NewHandler(c, reg, mapper, authz, incSvc, renderer, cards)
	return h, cards
}

// TestHandleCardAction_AckSuccess 完整链路：回调→映射→鉴权→ack→刷新卡片。
func TestHandleCardAction_AckSuccess(t *testing.T) {
	c := newHandlerClient(t)
	incID, userID, teamID := seedFullSetup(t, c, true) // 授 ack
	bot := newStubBot("feishu", true)
	h, cards := newHandlerWith(t, c, bot)
	// 预置一张卡片（模拟之前发过的）
	cards.Put(incID, "feishu", "card_pre")

	_ = userID
	_ = teamID
	e := echo.New()
	payload := mustJSON(t, IMEvent{
		Type:       EventCardAction,
		Platform:   "feishu",
		UnionID:    "ou_zs",
		ChannelID:  "oc_c",
		Action:     ActionAck,
		IncidentID: strconv.Itoa(incID),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/im/feishu/callback", bytes.NewReader(payload))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.POST("/api/v1/im/:platform/callback", h.callback)
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// incident 应已 acked
	inc, _ := c.Incident.Get(context.Background(), incID)
	if inc.Status != incident.StatusAcked {
		t.Errorf("incident status: got %s, want acked", inc.Status)
	}
	// 应触发卡片更新
	if bot.updateCount == 0 {
		t.Error("expected UpdateCard to be called")
	}
}

// TestHandleCardAction_ForbiddenNoPermission 无 ack 权限 → 403。
func TestHandleCardAction_ForbiddenNoPermission(t *testing.T) {
	c := newHandlerClient(t)
	incID, _, _ := seedFullSetup(t, c, false) // 不授权
	bot := newStubBot("feishu", true)
	h, _ := newHandlerWith(t, c, bot)

	e := echo.New()
	payload := mustJSON(t, IMEvent{
		Type: EventCardAction, Platform: "feishu", UnionID: "ou_zs",
		Action: ActionAck, IncidentID: strconv.Itoa(incID),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/im/feishu/callback", bytes.NewReader(payload))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.POST("/api/v1/im/:platform/callback", h.callback)
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status: got %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	// incident 未变更
	inc, _ := c.Incident.Get(context.Background(), incID)
	if inc.Status != incident.StatusTriggered {
		t.Errorf("incident should stay triggered, got %s", inc.Status)
	}
}

// TestHandleCardAction_NotBound 未绑定 IM 账号 → 403。
func TestHandleCardAction_NotBound(t *testing.T) {
	c := newHandlerClient(t)
	incID, _, _ := seedFullSetup(t, c, false)
	bot := newStubBot("feishu", true)
	h, _ := newHandlerWith(t, c, bot)

	e := echo.New()
	// union_id 是未绑定的
	payload := mustJSON(t, IMEvent{
		Type: EventCardAction, Platform: "feishu", UnionID: "ou_stranger",
		Action: ActionAck, IncidentID: strconv.Itoa(incID),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/im/feishu/callback", bytes.NewReader(payload))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.POST("/api/v1/im/:platform/callback", h.callback)
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status: got %d, want 403 (not bound)", rec.Code)
	}
}

// TestCallback_UnknownPlatform 未知平台 → 404。
func TestCallback_UnknownPlatform(t *testing.T) {
	c := newHandlerClient(t)
	bot := newStubBot("feishu", true)
	h, _ := newHandlerWith(t, c, bot)
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/im/unknown/callback", bytes.NewReader([]byte("{}")))
	rec := httptest.NewRecorder()
	e.POST("/api/v1/im/:platform/callback", h.callback)
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", rec.Code)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
