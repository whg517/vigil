package servicesync

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/schema"
	entservice "github.com/kevin/vigil/ent/service"

	_ "github.com/mattn/go-sqlite3"
)

func newClient(t *testing.T) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:svcsync_"+t.Name()+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// mkTeamWithPolicy 建团队 + 设默认升级策略（同步前置）。
func mkTeamWithPolicy(t *testing.T, c *ent.Client, slug string) *ent.Team {
	t.Helper()
	ctx := context.Background()
	tm, err := c.Team.Create().SetName(slug).SetSlug(slug).Save(ctx)
	if err != nil {
		t.Fatalf("create team %s: %v", slug, err)
	}
	pol, err := c.EscalationPolicy.Create().
		SetName("pol-" + slug).SetRepeatTimes(0).
		SetLevels([]schema.EscalationLevel{}).SetTeamID(tm.ID).Save(ctx)
	if err != nil {
		t.Fatalf("create policy: %v", err)
	}
	if err := c.Team.UpdateOneID(tm.ID).SetDefaultEscalationPolicyID(pol.ID).Exec(ctx); err != nil {
		t.Fatalf("set default policy: %v", err)
	}
	return tm
}

// fakeSource 固定清单源（直接测 Reconcile 逻辑，免文件 I/O）。
type fakeSource struct{ items []DesiredService }

func (f fakeSource) List(context.Context) ([]DesiredService, error) { return f.items, nil }

// TestReconcile_CreatesNew 清单新条目 → 建 source=auto 服务，继承团队默认策略与标签。
func TestReconcile_CreatesNew(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()
	tm := mkTeamWithPolicy(t, c, "pay")
	src := fakeSource{items: []DesiredService{
		{Slug: "svc-a", Name: "Service A", Team: "pay", Labels: map[string]string{"env": "prod"}},
	}}
	res, err := NewSyncer(c, src, "").Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.Created != 1 || res.Updated != 0 {
		t.Fatalf("result: %+v, want Created=1", res)
	}
	svc, err := c.Service.Query().Where(entservice.SlugEQ("svc-a")).WithTeam().Only(ctx)
	if err != nil {
		t.Fatalf("query svc-a: %v", err)
	}
	if svc.Source != entservice.SourceAuto {
		t.Fatalf("source: got %q, want auto", svc.Source)
	}
	if svc.ProvisionedAt == nil {
		t.Fatalf("provisioned_at must be set")
	}
	if svc.Edges.Team == nil || svc.Edges.Team.ID != tm.ID {
		t.Fatalf("team: got %v, want %d", svc.Edges.Team, tm.ID)
	}
	if svc.Labels["env"] != "prod" {
		t.Fatalf("labels: got %v", svc.Labels)
	}
	if _, err := svc.QueryEscalationPolicy().Only(ctx); err != nil {
		t.Fatalf("escalation policy must be bound: %v", err)
	}
}

// TestReconcile_SkipsNoDefaultPolicy 团队无默认策略 → 跳过，不建服务（避免无策略静默）。
func TestReconcile_SkipsNoDefaultPolicy(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()
	// infra 团队存在但未设默认策略。
	if _, err := c.Team.Create().SetName("infra").SetSlug("infra").Save(ctx); err != nil {
		t.Fatalf("create infra: %v", err)
	}
	src := fakeSource{items: []DesiredService{{Slug: "svc-b", Team: "infra"}}}
	res, err := NewSyncer(c, src, "").Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.Created != 0 || res.Skipped != 1 {
		t.Fatalf("result: %+v, want Skipped=1", res)
	}
	if n, _ := c.Service.Query().Count(ctx); n != 0 {
		t.Fatalf("must not create service, got %d", n)
	}
}

// TestReconcile_UpdatesAutoNotManual 更新 auto 服务标签，但绝不触碰 manual。
func TestReconcile_UpdatesAutoNotManual(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()
	tm := mkTeamWithPolicy(t, c, "pay")
	// 预置：一个 manual 服务 + 一个 auto 服务（旧标签）。
	if _, err := c.Service.Create().SetName("svc-m").SetSlug("svc-m").
		SetSource(entservice.SourceManual).SetTeamID(tm.ID).
		SetLabels(map[string]string{"keep": "me"}).Save(ctx); err != nil {
		t.Fatalf("pre manual: %v", err)
	}
	if _, err := c.Service.Create().SetName("svc-x").SetSlug("svc-x").
		SetSource(entservice.SourceAuto).SetTeamID(tm.ID).
		SetLabels(map[string]string{"env": "old"}).Save(ctx); err != nil {
		t.Fatalf("pre auto: %v", err)
	}
	src := fakeSource{items: []DesiredService{
		{Slug: "svc-m", Team: "pay", Labels: map[string]string{"env": "hacked"}}, // 应被跳过
		{Slug: "svc-x", Team: "pay", Labels: map[string]string{"env": "new"}},    // 应更新
	}}
	res, err := NewSyncer(c, src, "").Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.Updated != 1 || res.Skipped != 1 {
		t.Fatalf("result: %+v, want Updated=1 Skipped=1", res)
	}
	// manual 未被覆盖。
	m, _ := c.Service.Query().Where(entservice.SlugEQ("svc-m")).Only(ctx)
	if m.Labels["keep"] != "me" || m.Labels["env"] == "hacked" {
		t.Fatalf("manual service must be untouched, got %v", m.Labels)
	}
	// auto 已更新。
	x, _ := c.Service.Query().Where(entservice.SlugEQ("svc-x")).Only(ctx)
	if x.Labels["env"] != "new" {
		t.Fatalf("auto service should update labels, got %v", x.Labels)
	}
}

// TestReconcile_DefaultTeamFallback 条目未给 team → 用兜底团队。
func TestReconcile_DefaultTeamFallback(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()
	mkTeamWithPolicy(t, c, "pay")
	src := fakeSource{items: []DesiredService{{Slug: "svc-c"}}} // 无 team
	res, err := NewSyncer(c, src, "pay").Reconcile(ctx)         // 兜底团队 pay
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.Created != 1 {
		t.Fatalf("result: %+v, want Created=1", res)
	}
}

// TestReconcile_Idempotent 同清单跑两次：第二次为 update 而非重复创建，服务数不增。
func TestReconcile_Idempotent(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()
	mkTeamWithPolicy(t, c, "pay")
	src := fakeSource{items: []DesiredService{{Slug: "svc-a", Team: "pay"}}}
	sy := NewSyncer(c, src, "")
	if _, err := sy.Reconcile(ctx); err != nil {
		t.Fatalf("first: %v", err)
	}
	res2, err := sy.Reconcile(ctx)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if res2.Created != 0 || res2.Updated != 1 {
		t.Fatalf("second run: %+v, want Updated=1 Created=0", res2)
	}
	if n, _ := c.Service.Query().Count(ctx); n != 1 {
		t.Fatalf("idempotent: got %d services, want 1", n)
	}
}

// TestFileSource_Parse FileSource 读取并解析 JSON 清单。
func TestFileSource_Parse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.json")
	if err := os.WriteFile(path, []byte(`[{"slug":"a","team":"pay","labels":{"env":"prod"}}]`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	items, err := FileSource{Path: path}.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || items[0].Slug != "a" || items[0].Labels["env"] != "prod" {
		t.Fatalf("parsed: %+v", items)
	}
}
