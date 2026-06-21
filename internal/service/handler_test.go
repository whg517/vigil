package service

import (
	"context"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"

	_ "github.com/mattn/go-sqlite3"
)

// newTestClient sqlite 内存库。
func newTestClient(t *testing.T) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:svc_test_"+t.Name()+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestServiceCRUD 服务目录 CRUD 全流程（直接调 db，验证 ent 操作链路完整）。
// handler 是薄封装，逻辑全在 db 操作；此处覆盖 CRUD 链路即可。
func TestServiceCRUD(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	// 先建 team（service 归属 team）
	team, err := c.Team.Create().SetName("t").SetSlug("t").Save(ctx)
	if err != nil {
		t.Fatalf("create team: %v", err)
	}
	// create
	svc, err := c.Service.Create().
		SetName("payment-api").
		SetSlug("payment").
		SetTeamID(team.ID).
		SetLabels(map[string]string{"env": "prod"}).
		Save(ctx)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	if svc.Slug != "payment" {
		t.Errorf("slug: got %q", svc.Slug)
	}
	// list
	all, _ := c.Service.Query().All(ctx)
	if len(all) != 1 {
		t.Errorf("list: got %d, want 1", len(all))
	}
	// get
	got, _ := c.Service.Get(ctx, svc.ID)
	if got.Name != "payment-api" {
		t.Errorf("get name: %q", got.Name)
	}
	// update
	updated, err := c.Service.UpdateOneID(svc.ID).SetName("payment-v2").Save(ctx)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Name != "payment-v2" {
		t.Errorf("update name: %q", updated.Name)
	}
	// delete
	if err := c.Service.DeleteOneID(svc.ID).Exec(ctx); err != nil {
		t.Fatalf("delete: %v", err)
	}
	count, _ := c.Service.Query().Count(ctx)
	if count != 0 {
		t.Errorf("delete 后应 0 条，got %d", count)
	}
}
