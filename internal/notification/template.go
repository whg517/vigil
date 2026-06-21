// template.go 通知模板系统（能力域 7 M7.5）。
//
// 对应 capabilities/04-notification.md §6。
// 用 Go text/template 渲染 title_template / body_template，
// 模板变量为 incident/service/targets/level/action_url/now。
// 内置默认模板（builtin=true）由代码常量提供，seed 时 upsert；用户模板按 name 覆盖。
//
// 渲染失败时降级用 FormatTitle/FormatSummary 兜底（保证送达，不因模板错误丢通知）。
package notification

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/notificationtemplate"
	"github.com/kevin/vigil/ent/schema"
)

// TemplateData 模板渲染变量（注入到 Go template 上下文）。
type TemplateData struct {
	Incident  *ent.Incident
	Service   *ent.Service // 可空（unrouted 事件）
	Targets   []Target     // 通知目标
	Level     int          // 升级层级
	ActionURL string       // ack/查看链接
	Now       time.Time    // 渲染时刻
}

// RenderedMessage 模板渲染结果。
type RenderedMessage struct {
	Title   string         // 渲染后的标题
	Body    string         // 渲染后的正文
	Actions []RenderAction // 卡片按钮（interactive_card 格式）
}

// RenderAction 渲染后的卡片按钮。
type RenderAction struct {
	Type  string // ack | escalate | resolve | detail
	Label string // 渲染后的按钮文案
}

// TemplateEngine 通知模板引擎：按 name + channel 查模板，Go template 渲染。
type TemplateEngine struct {
	db    *ent.Client
	mu    sync.RWMutex
	cache map[string]*template.Template // key = name|channel → 编译后的 body 模板
}

// NewTemplateEngine 创建模板引擎。db 为 nil 时仅用默认模板（不查库）。
func NewTemplateEngine(db *ent.Client) *TemplateEngine {
	return &TemplateEngine{db: db, cache: make(map[string]*template.Template)}
}

// --- 内置默认模板 ---

const (
	// DefaultIMTemplateName 默认 IM 卡片模板名。
	DefaultIMTemplateName = "default_im_card"
	// DefaultEmailTemplateName 默认邮件模板名。
	DefaultEmailTemplateName = "default_email"
	// DefaultWebhookTemplateName 默认 webhook 模板名。
	DefaultWebhookTemplateName = "default_webhook"
)

// builtinTemplates 内置默认模板定义（seed 时 upsert 到 DB）。
// 变量：{{.Incident.Number}} {{.Incident.Severity}} {{.Incident.Title}}
//
//	{{.Incident.Summary}} {{.Service.Name}} {{.Level}} {{.Now.Format ...}}。
var builtinTemplates = []ent.NotificationTemplate{
	{
		Name:          DefaultIMTemplateName,
		Channel:       notificationtemplate.ChannelIm,
		Format:        notificationtemplate.FormatInteractiveCard,
		TitleTemplate: `[{{upper .Incident.Severity}}] {{.Incident.Number}} {{.Incident.Title}}`,
		BodyTemplate:  `状态：{{.Incident.Status}}{{if .Service}}\n服务：{{.Service.Name}}{{end}}\n严重度：{{.Incident.Severity}}\n层级：Level {{.Level}}\n{{.Incident.Summary}}`,
		Actions:       nil, // IM 按钮由 im.BuildCard 注入，模板不重复定义
		Builtin:       true,
	},
	{
		Name:          DefaultEmailTemplateName,
		Channel:       notificationtemplate.ChannelEmail,
		Format:        notificationtemplate.FormatText,
		TitleTemplate: `[{{upper .Incident.Severity}}] {{.Incident.Number}} {{.Incident.Title}}`,
		BodyTemplate:  `告警事件 {{.Incident.Number}} 需要您关注。\n\n严重度：{{.Incident.Severity}}\n状态：{{.Incident.Status}}\n概要：{{.Incident.Summary}}\n升级层级：Level {{.Level}}\n时间：{{.Now.Format "2006-01-02 15:04"}}`,
		Builtin:       true,
	},
	{
		Name:          DefaultWebhookTemplateName,
		Channel:       notificationtemplate.ChannelWebhook,
		Format:        notificationtemplate.FormatText,
		TitleTemplate: `incident.escalated {{.Incident.Number}}`,
		BodyTemplate:  `{"incident":"{{.Incident.Number}}","severity":"{{.Incident.Severity}}","status":"{{.Incident.Status}}","level":{{.Level}},"summary":"{{.Incident.Summary}}"}`,
		Builtin:       true,
	},
}

// SeedBuiltinTemplates 写入内置默认模板（幂等：已存在则更新内容）。
// 在服务启动时调用，保证有可用模板。
func (e *TemplateEngine) SeedBuiltinTemplates(ctx context.Context) error {
	if e.db == nil {
		return nil
	}
	for _, bt := range builtinTemplates {
		existing, err := e.db.NotificationTemplate.Query().
			Where(notificationtemplate.NameEQ(bt.Name)).Only(ctx)
		if ent.IsNotFound(err) {
			// 新建
			if _, err := e.createOne(ctx, &bt); err != nil {
				return fmt.Errorf("seed template %s: %w", bt.Name, err)
			}
			continue
		}
		if err != nil {
			return err
		}
		// 已存在：更新内容（保持 id）
		if _, err := e.db.NotificationTemplate.UpdateOneID(existing.ID).
			SetChannel(bt.Channel).
			SetFormat(bt.Format).
			SetTitleTemplate(bt.TitleTemplate).
			SetBodyTemplate(bt.BodyTemplate).
			SetBuiltin(true).
			Save(ctx); err != nil {
			return fmt.Errorf("update template %s: %w", bt.Name, err)
		}
	}
	e.invalidateCache()
	return nil
}

// createOne 用模板定义建一条记录（抽公共，避免重复 Set 链）。
func (e *TemplateEngine) createOne(ctx context.Context, bt *ent.NotificationTemplate) (*ent.NotificationTemplate, error) {
	b := e.db.NotificationTemplate.Create().
		SetName(bt.Name).
		SetChannel(bt.Channel).
		SetFormat(bt.Format).
		SetTitleTemplate(bt.TitleTemplate).
		SetBodyTemplate(bt.BodyTemplate).
		SetBuiltin(bt.Builtin)
	if len(bt.Actions) > 0 {
		b.SetActions(bt.Actions)
	}
	return b.Save(ctx)
}

// Render 按 name + channel 渲染模板。
// 找不到模板时：im 用默认 IM 模板，email/webhook 用各自默认；全失败降级用 FormatTitle/Summary。
func (e *TemplateEngine) Render(ctx context.Context, name string, channel string, data TemplateData) (*RenderedMessage, error) {
	if data.Now.IsZero() {
		data.Now = time.Now()
	}
	tmpl, err := e.lookup(ctx, name, channel)
	if err != nil {
		// 降级：用 FormatTitle/FormatSummary 兜底
		inc := data.Incident
		if inc == nil {
			return &RenderedMessage{Title: name, Body: ""}, nil
		}
		return &RenderedMessage{
			Title: FormatTitle(inc),
			Body:  FormatSummary(inc, data.Level),
		}, nil
	}
	out := &RenderedMessage{}
	out.Title, err = renderText(tmpl.title, data)
	if err != nil {
		return e.fallback(data), nil
	}
	out.Body, err = renderText(e.bodyTmpl(ctx, name, channel, tmpl), data)
	if err != nil {
		return e.fallback(data), nil
	}
	// IM 卡片按钮：从模板 actions 映射
	for _, a := range tmpl.actions {
		out.Actions = append(out.Actions, RenderAction{Type: a.Type, Label: a.Label})
	}
	return out, nil
}

// fallback 降级渲染（模板错误时保证有内容）。
func (e *TemplateEngine) fallback(data TemplateData) *RenderedMessage {
	if data.Incident == nil {
		return &RenderedMessage{Title: "告警通知", Body: ""}
	}
	return &RenderedMessage{
		Title: FormatTitle(data.Incident),
		Body:  FormatSummary(data.Incident, data.Level),
	}
}

// compiledTemplates 已编译的 title/body 模板对 + actions。
type compiledTemplates struct {
	title   *template.Template
	bodySrc string // body 源码（延迟编译，便于缓存键分离）
	actions []schema.TemplateAction
}

// lookup 按 name 查模板；name 为空时按 channel 取默认模板。
func (e *TemplateEngine) lookup(ctx context.Context, name, channel string) (*compiledTemplates, error) {
	if name == "" {
		name = defaultNameForChannel(channel)
	}
	// 查 DB（含内置）
	var stored *ent.NotificationTemplate
	if e.db != nil {
		t, err := e.db.NotificationTemplate.Query().
			Where(notificationtemplate.NameEQ(name)).Only(ctx)
		if err == nil {
			stored = t
		}
	}
	// DB 没有 → 用内置同名模板
	if stored == nil {
		for i := range builtinTemplates {
			if builtinTemplates[i].Name == name {
				bt := builtinTemplates[i]
				stored = &bt
				break
			}
		}
	}
	if stored == nil {
		return nil, fmt.Errorf("template %q not found", name)
	}
	// 编译 title（缓存）
	titleKey := "title:" + name
	e.mu.RLock()
	titleTmpl, ok := e.cache[titleKey]
	e.mu.RUnlock()
	if !ok {
		t, err := template.New(titleKey).Funcs(templateFuncs()).Parse(stored.TitleTemplate)
		if err != nil {
			return nil, fmt.Errorf("parse title template %s: %w", name, err)
		}
		titleTmpl = t
		e.mu.Lock()
		e.cache[titleKey] = titleTmpl
		e.mu.Unlock()
	}
	return &compiledTemplates{
		title:   titleTmpl,
		bodySrc: stored.BodyTemplate,
		actions: stored.Actions,
	}, nil
}

// bodyTmpl 编译 body 模板（缓存）。
func (e *TemplateEngine) bodyTmpl(_ context.Context, name, _ string, ct *compiledTemplates) *template.Template {
	bodyKey := "body:" + name
	e.mu.RLock()
	t, ok := e.cache[bodyKey]
	e.mu.RUnlock()
	if ok {
		return t
	}
	t, err := template.New(bodyKey).Funcs(templateFuncs()).Parse(ct.bodySrc)
	if err != nil {
		return nil // 编译失败：renderText 会用空模板，触发降级
	}
	e.mu.Lock()
	e.cache[bodyKey] = t
	e.mu.Unlock()
	return t
}

// invalidateCache 清空缓存（seed/update 后调用）。
func (e *TemplateEngine) invalidateCache() {
	e.mu.Lock()
	e.cache = make(map[string]*template.Template)
	e.mu.Unlock()
}

// renderText 执行模板（tmpl 为 nil 时返回空串触发降级）。
func renderText(tmpl *template.Template, data TemplateData) (string, error) {
	if tmpl == nil {
		return "", fmt.Errorf("nil template")
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// defaultNameForChannel 按 channel 取默认模板名。
func defaultNameForChannel(channel string) string {
	switch strings.ToLower(channel) {
	case "im":
		return DefaultIMTemplateName
	case "email":
		return DefaultEmailTemplateName
	case "webhook":
		return DefaultWebhookTemplateName
	default:
		return DefaultWebhookTemplateName
	}
}

// templateFuncs 模板辅助函数（upper 大写化 severity 等）。
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"upper": strings.ToUpper,
		"lower": strings.ToLower,
		"trim":  strings.TrimSpace,
	}
}

// InvalidateCache 导出缓存失效（CRUD 后由 handler 调用）。
func (e *TemplateEngine) InvalidateCache() { e.invalidateCache() }
