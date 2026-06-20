package escalation

import (
	"testing"

	"github.com/kevin/vigil/ent/enttest"

	_ "github.com/mattn/go-sqlite3"
)

// TestEscalationTaskID 验证任务 ID 生成的稳定性与唯一性（用于幂等与取消）。
func TestEscalationTaskID(t *testing.T) {
	cases := []struct {
		incID, levelIdx, repeat int
		want                    string
	}{
		{42, 0, 0, "esc:42:0:0"},
		{42, 1, 0, "esc:42:1:0"},
		{42, 0, 2, "esc:42:0:2"},
		{1, 3, 1, "esc:1:3:1"},
	}
	for _, c := range cases {
		if got := escalationTaskID(c.incID, c.levelIdx, c.repeat); got != c.want {
			t.Errorf("escalationTaskID(%d,%d,%d): got %q, want %q", c.incID, c.levelIdx, c.repeat, got, c.want)
		}
	}
	// 唯一性：不同 (inc,level,repeat) 不撞
	seen := map[string]bool{}
	for inc := 1; inc <= 5; inc++ {
		for lv := 0; lv < 3; lv++ {
			for rp := 0; rp < 3; rp++ {
				id := escalationTaskID(inc, lv, rp)
				if seen[id] {
					t.Errorf("task id collision: %s", id)
				}
				seen[id] = true
			}
		}
	}
}

// TestNewEngine_NilDeps 验证构造函数对 nil 依赖的容错（测试场景）。
func TestNewEngine_NilDeps(t *testing.T) {
	e := NewEngine(nil, nil, nil, nil, nil)
	if e == nil {
		t.Fatal("NewEngine returned nil")
	}
	// inspector 在 redisOpt=nil 时应返回 nil
	if ins := e.inspector(); ins != nil {
		t.Errorf("inspector should be nil when redisOpt is nil")
	}
}

// TestCancelOnAck_NoRedis 验证无 Redis 时 CancelOnAck 不 panic（状态守卫兜底）。
func TestCancelOnAck_NoRedis(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:esc_test?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })

	e := NewEngine(c, nil, nil, nil, nil)
	// 无 Redis（inspector=nil），应安全返回 nil
	if err := e.CancelOnAck(nil, 1, nil, 0); err != nil {
		t.Errorf("CancelOnAck with no redis: %v", err)
	}
}
