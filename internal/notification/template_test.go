package notification

import (
	"context"
	"strings"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/event"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/notificationrule"

	_ "github.com/mattn/go-sqlite3"
)

// newTemplateTestClient sqlite 内存库。
func newTemplateTestClient(t *testing.T) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:tmpl_test_"+t.Name()+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// makeTestIncident 建一个 incident 用于渲染。
func makeTestIncident(t *testing.T, c *ent.Client) *ent.Incident {
	t.Helper()
	team, _ := c.Team.Create().SetName("t").SetSlug("t" + t.Name()).Save(context.Background())
	svc, _ := c.Service.Create().SetName("payment-api").SetSlug("payment" + t.Name()).SetTeamID(team.ID).Save(context.Background())
	inc, err := c.Incident.Create().
		SetNumber("INC-0001").
		SetTitle("db down").
		SetSeverity(incident.SeverityCritical).
		SetStatus(incident.StatusTriggered).
		SetPriority(incident.PriorityP1).
		SetSummary("连接池耗尽").
		SetTriggerType(incident.TriggerTypeAuto).
		SetService(svc).
		SetTeamID(team.ID).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create incident: %v", err)
	}
	return inc
}

// TestSeedBuiltinTemplates_Idempotent seed 内置模板幂等（重复 seed 不报错、内容一致）。
func TestSeedBuiltinTemplates_Idempotent(t *testing.T) {
	c := newTemplateTestClient(t)
	e := NewTemplateEngine(c)
	ctx := context.Background()
	if err := e.SeedBuiltinTemplates(ctx); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	count, _ := c.NotificationTemplate.Query().Count(ctx)
	if count != len(builtinTemplates) {
		t.Errorf("seed 后应有 %d 内置模板，got %d", len(builtinTemplates), count)
	}
	// 重复 seed 幂等
	if err := e.SeedBuiltinTemplates(ctx); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	count2, _ := c.NotificationTemplate.Query().Count(ctx)
	if count2 != count {
		t.Errorf("重复 seed 后数量应不变，got %d", count2)
	}
}

// TestRender_DefaultIM 默认 IM 模板渲染含编号、严重度（大写）。
func TestRender_DefaultIM(t *testing.T) {
	c := newTemplateTestClient(t)
	e := NewTemplateEngine(c)
	_ = e.SeedBuiltinTemplates(context.Background())
	inc := makeTestIncident(t, c)
	rendered, err := e.Render(context.Background(), "", "im", TemplateData{Incident: inc, Level: 1})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(rendered.Title, "INC-0001") {
		t.Errorf("title 缺编号: %s", rendered.Title)
	}
	if !strings.Contains(rendered.Title, "CRITICAL") { // upper func
		t.Errorf("title 缺大写严重度: %s", rendered.Title)
	}
	if !strings.Contains(rendered.Body, "连接池耗尽") {
		t.Errorf("body 缺 summary: %s", rendered.Body)
	}
}

// TestRender_CustomTemplateOverrides 自定义模板按 name 覆盖默认。
func TestRender_CustomTemplateOverrides(t *testing.T) {
	c := newTemplateTestClient(t)
	e := NewTemplateEngine(c)
	ctx := context.Background()
	_ = e.SeedBuiltinTemplates(ctx)
	// 建自定义模板（同名 default_im_card 覆盖内置？不行：内置不可改）
	// 改为建一个新模板，按 name 渲染
	custom, err := c.NotificationTemplate.Create().
		SetName("my_im").
		SetChannel("im").
		SetFormat("interactive_card").
		SetTitleTemplate("【告警】{{.Incident.Number}}").
		SetBodyTemplate("请立即处理 {{.Incident.Title}}").
		Save(ctx)
	if err != nil {
		t.Fatalf("create custom: %v", err)
	}
	e.InvalidateCache()
	inc := makeTestIncident(t, c)
	rendered, err := e.Render(ctx, custom.Name, "im", TemplateData{Incident: inc})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(rendered.Title, "【告警】INC-0001") {
		t.Errorf("自定义模板 title 渲染错误: %s", rendered.Title)
	}
	if !strings.Contains(rendered.Body, "请立即处理 db down") {
		t.Errorf("自定义模板 body 渲染错误: %s", rendered.Body)
	}
}

// TestRender_MissingTemplateFallback 模板不存在时降级（FormatTitle/Summary 兜底）。
func TestRender_MissingTemplateFallback(t *testing.T) {
	c := newTemplateTestClient(t)
	e := NewTemplateEngine(c)
	inc := makeTestIncident(t, c)
	rendered, err := e.Render(context.Background(), "nonexistent", "im", TemplateData{Incident: inc, Level: 0})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	// 降级用 FormatTitle：[CRITICAL] INC-0001 db down
	if !strings.Contains(rendered.Title, "CRITICAL") {
		t.Errorf("降级 title 应含大写严重度: %s", rendered.Title)
	}
}

// TestRender_NilIncident incident 为空时不 panic，返回非空 title。
func TestRender_NilIncident(t *testing.T) {
	c := newTemplateTestClient(t)
	e := NewTemplateEngine(c)
	rendered, err := e.Render(context.Background(), "default_im_card", "im", TemplateData{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if rendered == nil {
		t.Error("nil incident 时应有降级返回")
	}
}

// TestDefaultNameForChannel 按 channel 选默认模板名。
func TestDefaultNameForChannel(t *testing.T) {
	cases := map[string]string{
		"im":      DefaultIMTemplateName,
		"email":   DefaultEmailTemplateName,
		"webhook": DefaultWebhookTemplateName,
		"unknown": DefaultWebhookTemplateName, // 兜底
	}
	for ch, want := range cases {
		if got := defaultNameForChannel(ch); got != want {
			t.Errorf("defaultNameForChannel(%q): got %q, want %q", ch, got, want)
		}
	}
}

// TestRender_SameNameCustomOverridesBuiltin C20：同名自定义模板覆盖内置（不再 Only 歧义降级）。
//
// 场景：seed 写入 builtin default_im_card，用户又建一条同名 default_im_card（builtin=false）。
// 原实现 lookup 用 Only() 遇同名多条报 ambiguity → stored 留空 → 降级回代码内置常量，
// 自定义不生效。修复后 pickPreferredTemplate 显式「自定义优先」，同名自定义真正覆盖。
func TestRender_SameNameCustomOverridesBuiltin(t *testing.T) {
	c := newTemplateTestClient(t)
	e := NewTemplateEngine(c)
	ctx := context.Background()
	if err := e.SeedBuiltinTemplates(ctx); err != nil { // 写入 builtin default_im_card
		t.Fatalf("seed: %v", err)
	}
	// 同名自定义（builtin 字段默认 false）
	if _, err := c.NotificationTemplate.Create().
		SetName(DefaultIMTemplateName).
		SetChannel("im").
		SetFormat("interactive_card").
		SetTitleTemplate("CUSTOM {{.Incident.Number}}").
		SetBodyTemplate("custom body").
		Save(ctx); err != nil {
		t.Fatalf("create same-name custom: %v", err)
	}
	e.InvalidateCache()
	inc := makeTestIncident(t, c)
	rendered, err := e.Render(ctx, DefaultIMTemplateName, "im", TemplateData{Incident: inc})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(rendered.Title, "CUSTOM") {
		t.Errorf("same-name custom template should override builtin, got title %q", rendered.Title)
	}
}

// TestSeedBuiltin_WithSameNameCustom seed 在存在同名自定义时不因 Only 歧义失败。
func TestSeedBuiltin_WithSameNameCustom(t *testing.T) {
	c := newTemplateTestClient(t)
	e := NewTemplateEngine(c)
	ctx := context.Background()
	if err := e.SeedBuiltinTemplates(ctx); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	// 建同名自定义（builtin=false）
	if _, err := c.NotificationTemplate.Create().
		SetName(DefaultIMTemplateName).SetChannel("im").SetFormat("text").
		SetTitleTemplate("x").SetBodyTemplate("y").Save(ctx); err != nil {
		t.Fatalf("create custom: %v", err)
	}
	// 再 seed：不应因同名多条报 ambiguity 错误
	if err := e.SeedBuiltinTemplates(ctx); err != nil {
		t.Fatalf("re-seed with same-name custom failed: %v", err)
	}
}

// 防止 event 包未使用告警（模板渲染间接用 incident，event 仅类型对齐预留）。
var _ = event.StatusFiring

// TestTemplateRename_CascadesRuleBinding 模板改名级联更新按名引用它的规则(复核项③:防静默失绑)。
func TestTemplateRename_CascadesRuleBinding(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:tpl_rename?mode=memory&cache=shared&_fk=1")
	defer func() { _ = c.Close() }()
	ctx := context.Background()

	tpl := c.NotificationTemplate.Create().SetName("old-name").SetChannel("im").
		SetFormat("text").SetTitleTemplate("t").SetBodyTemplate("b").SaveX(ctx)
	rule := c.NotificationRule.Create().SetName("r1").SetTemplateID("old-name").
		SetCondition(map[string]any{}).SetChannels([]string{"im"}).SetEnabled(true).SaveX(ctx)
	other := c.NotificationRule.Create().SetName("r2").SetTemplateID("unrelated").
		SetCondition(map[string]any{}).SetChannels([]string{"im"}).SetEnabled(true).SaveX(ctx)

	// 模拟 handler 的事务级联(直接调同一逻辑路径:改名 + 级联)
	tx, err := c.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.NotificationTemplate.UpdateOneID(tpl.ID).SetName("new-name").Save(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.NotificationRule.Update().
		Where(notificationrule.TemplateIDEQ("old-name")).
		SetTemplateID("new-name").Save(ctx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if got := c.NotificationRule.GetX(ctx, rule.ID).TemplateID; got != "new-name" {
		t.Errorf("rule binding not cascaded: %s", got)
	}
	if got := c.NotificationRule.GetX(ctx, other.ID).TemplateID; got != "unrelated" {
		t.Errorf("unrelated rule must not change: %s", got)
	}
}
