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
	"github.com/kevin/vigil/ent/auditlog"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/role"
	"github.com/kevin/vigil/ent/rolebinding"
	"github.com/kevin/vigil/ent/schema"
	"github.com/kevin/vigil/internal/auth"
	imincident "github.com/kevin/vigil/internal/incident"
	"github.com/kevin/vigil/internal/timeline"

	"github.com/labstack/echo/v5"
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
	sentCards   []*Card // 记录 SendCard 的入参（供断言按钮等，QA 审计 C5）
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
	b.sentCards = append(b.sentCards, c)
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

// newHandlerWith 构造完整 handler（真实 authz + incident service + stub bot + 审计记录器）。
func newHandlerWith(t *testing.T, c *ent.Client, bot *stubBot) (*Handler, *MemoryCardStore) {
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
	// 注入真实审计记录器（同库），使 IM 越权拒绝落审计（S9）可断言。
	h := NewHandler(c, reg, mapper, authz, incSvc, renderer, cards, auth.NewAuditRecorder(c))
	return h, cards
}

// TestHandleCardAction_AckSuccess 完整链路：回调→映射→鉴权→ack→刷新卡片。
func TestHandleCardAction_AckSuccess(t *testing.T) {
	c := newHandlerClient(t)
	incID, userID, teamID := seedFullSetup(t, c, true) // 授 ack
	bot := newStubBot("feishu", true)
	h, cards := newHandlerWith(t, c, bot)
	// 预置一张卡片（模拟之前发过的）
	cards.Put(context.Background(), incID, "feishu", "card_pre")

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

// TestHandleCardAction_DeniedAudited 无权限越权 → 403 且落一条 denied 审计（S9）。
// 审计须记对操作者（已解析出的 user）+ action=im.denied + result=denied + incident/im_action 上下文。
func TestHandleCardAction_DeniedAudited(t *testing.T) {
	c := newHandlerClient(t)
	incID, userID, _ := seedFullSetup(t, c, false) // 绑定了 IM 但未授 ack 权限
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
		t.Fatalf("status: got %d, want 403", rec.Code)
	}
	logs, err := c.AuditLog.Query().Where(auditlog.ActionEQ(auth.ActionIMDenied)).All(context.Background())
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 im.denied audit, got %d", len(logs))
	}
	lg := logs[0]
	if lg.Result != auditlog.ResultDenied {
		t.Errorf("result = %q, want denied", lg.Result)
	}
	// actor 应是已解析出的 user（越权者可追溯），不是 0。
	if lg.ActorUserID != userID {
		t.Errorf("actor = %d, want %d (resolved user)", lg.ActorUserID, userID)
	}
	if lg.ResourceType != "incident" || lg.ResourceID != incID {
		t.Errorf("resource = %s/%d, want incident/%d", lg.ResourceType, lg.ResourceID, incID)
	}
	if lg.Detail["im_action"] != ActionAck {
		t.Errorf("detail.im_action = %v, want %s", lg.Detail["im_action"], ActionAck)
	}
}

// TestHandleCardAction_NotBoundAudited 未绑定账号越权 → 落一条 actor=0 的 denied 审计（S9）。
func TestHandleCardAction_NotBoundAudited(t *testing.T) {
	c := newHandlerClient(t)
	incID, _, _ := seedFullSetup(t, c, false)
	bot := newStubBot("feishu", true)
	h, _ := newHandlerWith(t, c, bot)

	e := echo.New()
	payload := mustJSON(t, IMEvent{
		Type: EventCardAction, Platform: "feishu", UnionID: "ou_stranger",
		Action: ActionAck, IncidentID: strconv.Itoa(incID),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/im/feishu/callback", bytes.NewReader(payload))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.POST("/api/v1/im/:platform/callback", h.callback)
	e.ServeHTTP(rec, req)

	logs, _ := c.AuditLog.Query().Where(auditlog.ActionEQ(auth.ActionIMDenied)).All(context.Background())
	if len(logs) != 1 {
		t.Fatalf("expected 1 im.denied audit, got %d", len(logs))
	}
	// 账号未绑定 → actor 未知（0），靠 detail.union_id 溯源。
	if logs[0].ActorUserID != 0 {
		t.Errorf("actor = %d, want 0 (unbound)", logs[0].ActorUserID)
	}
	if logs[0].Detail["union_id"] != "ou_stranger" {
		t.Errorf("detail.union_id = %v, want ou_stranger", logs[0].Detail["union_id"])
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

// TestHandleMention_DingtalkAddResponder B16：钉钉 @人拉入 —— 操作者（持 add_responder）@被 @人，
// 被 @人经 dingtalk 绑定映射成 User 后加入 responders（与飞书 mention 对齐）。
func TestHandleMention_DingtalkAddResponder(t *testing.T) {
	c := newHandlerClient(t)
	ctx := context.Background()
	team, _ := c.Team.Create().SetName("支付").SetSlug("pay2").Save(ctx)
	// 操作者：dingtalk 绑定 + team 级 add_responder 权限。
	op, err := c.User.Create().SetUsername("op").SetEmail("op@x.com").
		SetImAccounts([]schema.IMAccount{{Platform: "dingtalk", AccountID: "dt_op"}}).Save(ctx)
	if err != nil {
		t.Fatalf("create op: %v", err)
	}
	// 被拉的人：dingtalk 绑定（staffId=dt_bob）。
	bob, err := c.User.Create().SetUsername("bob").SetEmail("bob@x.com").
		SetImAccounts([]schema.IMAccount{{Platform: "dingtalk", AccountID: "dt_bob"}}).Save(ctx)
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	inc, err := c.Incident.Create().SetNumber("INC-0009").SetTitle("db").
		SetSeverity(incident.SeverityCritical).SetStatus(incident.StatusTriggered).
		SetTeamID(team.ID).Save(ctx)
	if err != nil {
		t.Fatalf("create incident: %v", err)
	}
	// 授操作者 team 级 add_responder。
	rl, _ := c.Role.Create().SetName("puller").SetScopeLevel(role.ScopeLevelTeam).
		SetPermissions([]string{string(auth.PermIncidentAddResponder)}).Save(ctx)
	if _, err := c.RoleBinding.Create().SetUserID(op.ID).SetRoleID(rl.ID).
		SetScopeLevel(rolebinding.ScopeLevelTeam).SetTeamID(strconv.Itoa(team.ID)).
		SetGrantedAt(time.Now()).Save(ctx); err != nil {
		t.Fatalf("bind role: %v", err)
	}

	bot := newStubBot("dingtalk", true)
	h, _ := newHandlerWith(t, c, bot)

	e := echo.New()
	// 钉钉 mention 事件：操作者 dt_op，@了 dt_bob，正文含 incident number。
	payload := mustJSON(t, IMEvent{
		Type: EventMention, Platform: "dingtalk", UnionID: "dt_op",
		ChannelID: "ocid_g", Text: inc.Number + " 帮看 DB", MentionAt: []string{"dt_bob"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/im/dingtalk/callback", bytes.NewReader(payload))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.POST("/api/v1/im/:platform/callback", h.callback)
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("mention callback = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// bob 应已成为该 incident 的 responder。
	responders, err := inc.QueryResponders().All(ctx)
	if err != nil {
		t.Fatalf("query responders: %v", err)
	}
	found := false
	for _, r := range responders {
		if r.ID == bob.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("bob 应被加入 responders，got %d responders", len(responders))
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
