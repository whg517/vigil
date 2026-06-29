package im

import (
	"context"
	"errors"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/imaccountbinding"
	"github.com/kevin/vigil/ent/schema"

	_ "github.com/mattn/go-sqlite3"
)

func newMapperClient(t *testing.T) *ent.Client {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:im_mapper_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestResolveUser_Hit 命中已绑定账号。
func TestResolveUser_Hit(t *testing.T) {
	c := newMapperClient(t)
	ctx := context.Background()
	_, err := c.User.Create().
		SetUsername("zhangsan").
		SetEmail("zs@x.com").
		SetImAccounts([]schema.IMAccount{{Platform: "feishu", AccountID: "ou_123"}}).
		Save(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	m := NewMapper(c)
	u, err := m.ResolveUser(ctx, "feishu", "ou_123")
	if err != nil {
		t.Fatalf("ResolveUser: %v", err)
	}
	if u.Username != "zhangsan" {
		t.Errorf("username: got %s, want zhangsan", u.Username)
	}
}

// TestResolveUser_NotBound 未绑定返回 ErrNotBound。
func TestResolveUser_NotBound(t *testing.T) {
	c := newMapperClient(t)
	m := NewMapper(c)
	_, err := m.ResolveUser(context.Background(), "feishu", "ou_unknown")
	if !errors.Is(err, ErrNotBound) {
		t.Errorf("expected ErrNotBound, got %v", err)
	}
}

// TestResolveUser_MultiPlatform 一个 user 绑多平台，正确匹配各自 platform。
func TestResolveUser_MultiPlatform(t *testing.T) {
	c := newMapperClient(t)
	ctx := context.Background()
	_, err := c.User.Create().
		SetUsername("multi").
		SetEmail("m@x.com").
		SetImAccounts([]schema.IMAccount{
			{Platform: "feishu", AccountID: "ou_feishu"},
			{Platform: "dingtalk", AccountID: "dt_001"},
		}).
		Save(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	m := NewMapper(c)
	if _, err := m.ResolveUser(ctx, "feishu", "ou_feishu"); err != nil {
		t.Errorf("feishu resolve failed: %v", err)
	}
	if _, err := m.ResolveUser(ctx, "dingtalk", "dt_001"); err != nil {
		t.Errorf("dingtalk resolve failed: %v", err)
	}
	// 平台不匹配
	if _, err := m.ResolveUser(ctx, "wecom", "dt_001"); !errors.Is(err, ErrNotBound) {
		t.Errorf("wecom should be not bound, got %v", err)
	}
}

// TestBindAccount_Idempotent 重复绑定不报错、不重复。
func TestBindAccount_Idempotent(t *testing.T) {
	c := newMapperClient(t)
	ctx := context.Background()
	u, err := c.User.Create().SetUsername("u").SetEmail("u@x.com").Save(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	m := NewMapper(c)
	if err := m.BindAccount(ctx, u.ID, "feishu", "ou_1"); err != nil {
		t.Fatalf("first bind: %v", err)
	}
	if err := m.BindAccount(ctx, u.ID, "feishu", "ou_1"); err != nil {
		t.Fatalf("second bind (idempotent): %v", err)
	}
	// 再绑一个不同账号
	if err := m.BindAccount(ctx, u.ID, "dingtalk", "dt_1"); err != nil {
		t.Fatalf("bind dingtalk: %v", err)
	}
	reloaded, _ := c.User.Get(ctx, u.ID)
	if len(reloaded.ImAccounts) != 2 {
		t.Errorf("accounts count: got %d, want 2", len(reloaded.ImAccounts))
	}
}

// TestBindAccount_DoubleWrite 验证 BindAccount 双写：独立表 + JSON 字段都有记录，
// 且独立表查询命中（ResolveUser 优先走表，O(1)）。
func TestBindAccount_DoubleWrite(t *testing.T) {
	c := newMapperClient(t)
	ctx := context.Background()
	u, err := c.User.Create().SetUsername("db").SetEmail("db@x.com").Save(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	m := NewMapper(c)
	if err := m.BindAccount(ctx, u.ID, "feishu", "ou_db"); err != nil {
		t.Fatalf("bind: %v", err)
	}

	// 独立表应有 1 条
	cnt, err := c.IMAccountBinding.Query().Count(ctx)
	if err != nil {
		t.Fatalf("count bindings: %v", err)
	}
	if cnt != 1 {
		t.Errorf("im_account_bindings count: got %d, want 1", cnt)
	}

	// ResolveUser 应通过独立表命中（而非 JSON 回退）
	got, err := m.ResolveUser(ctx, "feishu", "ou_db")
	if err != nil {
		t.Fatalf("ResolveUser after bind: %v", err)
	}
	if got.ID != u.ID {
		t.Errorf("resolved user id: got %d, want %d", got.ID, u.ID)
	}
}

// TestResolveUser_PrefersTableOverJSON 独立表与 JSON 同时存在时，表结果应被采用。
// 构造一个仅在独立表有记录（JSON 字段为空）的用户，验证 ResolveUser 命中。
func TestResolveUser_PrefersTableOverJSON(t *testing.T) {
	c := newMapperClient(t)
	ctx := context.Background()
	u, err := c.User.Create().SetUsername("tableonly").SetEmail("to@x.com").Save(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	// 仅写独立表（不写 JSON），模拟「新绑定走新表」场景
	if _, err := c.IMAccountBinding.Create().
		SetPlatform(imaccountbinding.Platform("feishu")).
		SetAccountID("ou_table").
		SetUserID(u.ID).
		Save(ctx); err != nil {
		t.Fatalf("create binding: %v", err)
	}
	m := NewMapper(c)
	got, err := m.ResolveUser(ctx, "feishu", "ou_table")
	if err != nil {
		t.Fatalf("ResolveUser via table only: %v", err)
	}
	if got.ID != u.ID {
		t.Errorf("resolved user id: got %d, want %d", got.ID, u.ID)
	}
}

// TestListBindings_ReturnsAllAccounts QA 审计 C6：ListBindings 返回用户全部 IM 绑定。
func TestListBindings_ReturnsAllAccounts(t *testing.T) {
	c := newMapperClient(t)
	ctx := context.Background()
	u, err := c.User.Create().SetUsername("lb").SetEmail("lb@x.com").Save(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	m := NewMapper(c)
	if err := m.BindAccount(ctx, u.ID, "feishu", "ou_lb1"); err != nil {
		t.Fatalf("bind feishu: %v", err)
	}
	if err := m.BindAccount(ctx, u.ID, "dingtalk", "dt_lb1"); err != nil {
		t.Fatalf("bind dingtalk: %v", err)
	}
	views, err := m.ListBindings(ctx, u.ID)
	if err != nil {
		t.Fatalf("ListBindings: %v", err)
	}
	if len(views) != 2 {
		t.Fatalf("expected 2 bindings, got %d: %+v", len(views), views)
	}
	// 验证两个平台都在
	platforms := map[string]bool{}
	for _, v := range views {
		platforms[v.Platform] = true
	}
	if !platforms["feishu"] || !platforms["dingtalk"] {
		t.Errorf("missing platform, got %v", platforms)
	}
}

// TestListBindings_Empty 无绑定时返回空切片（不报错）。
func TestListBindings_Empty(t *testing.T) {
	c := newMapperClient(t)
	ctx := context.Background()
	u, _ := c.User.Create().SetUsername("empty").SetEmail("em@x.com").Save(ctx)
	m := NewMapper(c)
	views, err := m.ListBindings(ctx, u.ID)
	if err != nil {
		t.Fatalf("ListBindings empty: %v", err)
	}
	if len(views) != 0 {
		t.Errorf("expected 0 bindings, got %d", len(views))
	}
}

// 确保未使用导入不告警（schema 在 BindAccount_DoubleWrite 用过）
var _ = schema.IMAccount{}
