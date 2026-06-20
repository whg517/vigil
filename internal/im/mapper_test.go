package im

import (
	"context"
	"errors"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
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
