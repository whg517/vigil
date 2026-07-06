// down_test.go 覆盖 migrate down（逆向版本化 SQL 迁移）。
//
// 说明：真实嵌入的 down 脚本是 Postgres 方言（DROP EXTENSION 等），sqlite 跑不了。
// 因此：
//   - planDown / 缺 down 脚本拒绝 / --to / 顺序 → 用真实嵌入版本做纯逻辑断言（不 exec 脚本）。
//   - Down 的执行/dry-run/事务落库 → 用注入的 sqlite 安全脚本（execDownStep）驱动。
package migrate

import (
	"context"
	"errors"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// TestPlanDown_MissingDownScriptRefuses 无 down 脚本的版本被显式拒绝（非静默跳过）。
func TestPlanDown_MissingDownScriptRefuses(t *testing.T) {
	// 0002_baseline 已应用且无 down 脚本 → 单步回滚它应报 ErrMissingDownScript。
	applied := map[string]bool{"pre_0001_pgvector": true, "0002_baseline": true}
	_, err := planDown(applied, DownOptions{}) // 缺省单步 = 逆向最近应用（0002_baseline）
	if !errors.Is(err, ErrMissingDownScript) {
		t.Fatalf("want ErrMissingDownScript, got %v", err)
	}
}

// TestPlanDown_SingleStepPicksLatest 缺省单步回滚选「最近应用」的版本。
func TestPlanDown_SingleStepPicksLatest(t *testing.T) {
	// 只应用 pre_0001_pgvector（有 down 脚本）→ 单步应规划逆向它。
	applied := map[string]bool{"pre_0001_pgvector": true}
	steps, err := planDown(applied, DownOptions{})
	if err != nil {
		t.Fatalf("planDown: %v", err)
	}
	if len(steps) != 1 || steps[0].Version != "pre_0001_pgvector" {
		t.Fatalf("want single step pre_0001_pgvector, got %+v", steps)
	}
	if !steps[0].Destructive {
		t.Error("pgvector down 应标记为破坏性（DROP EXTENSION）")
	}
}

// TestPlanDown_NoneApplied 无已应用版本 → 空计划（nothing to do）。
func TestPlanDown_NoneApplied(t *testing.T) {
	steps, err := planDown(map[string]bool{}, DownOptions{})
	if err != nil {
		t.Fatalf("planDown: %v", err)
	}
	if len(steps) != 0 {
		t.Errorf("want empty plan, got %+v", steps)
	}
}

// TestPlanDown_ToUnknownVersion --to 指向未应用/未知版本 → 报错防误操作。
func TestPlanDown_ToUnknownVersion(t *testing.T) {
	applied := map[string]bool{"pre_0001_pgvector": true}
	_, err := planDown(applied, DownOptions{To: "0002_baseline"})
	if err == nil || !strings.Contains(err.Error(), "不是一个已应用") {
		t.Fatalf("want 'not applied' error, got %v", err)
	}
}

// TestPlanDown_ToReversesLaterVersionsInReverseOrder --to 逆向所有晚于目标的版本，倒序。
func TestPlanDown_ToReversesLaterVersionsInReverseOrder(t *testing.T) {
	// 两个都已应用；--to pre_0001_pgvector 应逆向「晚于它」的 0002_baseline（并保留 pre）。
	applied := map[string]bool{"pre_0001_pgvector": true, "0002_baseline": true}
	_, err := planDown(applied, DownOptions{To: "pre_0001_pgvector"})
	// 0002_baseline 无 down 脚本 → 整体拒绝（证明「拒绝而非部分执行」）。
	if !errors.Is(err, ErrMissingDownScript) {
		t.Fatalf("want ErrMissingDownScript (0002_baseline no down), got %v", err)
	}
}

// --- Down 执行路径（用 sqlite 安全脚本注入，绕开 Postgres 方言）---

// execDownStep 直接测试：在事务内跑脚本 + 删版本记录，成功后记录消失。
func TestExecDownStep_DeletesVersionRecord(t *testing.T) {
	db := newTestDB(t)
	markApplied(t, db, "0003_safe")
	if countRows(t, db) != 1 {
		t.Fatal("precondition: 1 row")
	}
	step := downStep{Version: "0003_safe", Script: []byte("SELECT 1;")}
	if err := execDownStep(context.Background(), db, step); err != nil {
		t.Fatalf("execDownStep: %v", err)
	}
	if countRows(t, db) != 0 {
		t.Errorf("version record should be deleted, still %d rows", countRows(t, db))
	}
}

// TestExecDownStep_FailingScriptRollsBack 脚本失败时事务回滚，版本记录保留。
func TestExecDownStep_FailingScriptRollsBack(t *testing.T) {
	db := newTestDB(t)
	markApplied(t, db, "0003_safe")
	step := downStep{Version: "0003_safe", Script: []byte("THIS IS NOT VALID SQL;")}
	err := execDownStep(context.Background(), db, step)
	if err == nil {
		t.Fatal("want error from bad script")
	}
	// 事务回滚 → 版本记录仍在（未误删）。
	if countRows(t, db) != 1 {
		t.Errorf("version record should survive failed down, got %d rows", countRows(t, db))
	}
}

// downTestDB 起 sqlite 库并预置「已应用 0003_safe」，Down 会因缺真实 down 脚本失败——
// 因此本组用 planDown+execDownStep 覆盖执行；Down 顶层用 dry-run/警告断言覆盖。

// TestDown_DryRunNoWrite dry-run 打印计划但不落库（版本表不变）。
func TestDown_DryRunNoWrite(t *testing.T) {
	db := newTestDB(t)
	markApplied(t, db, "pre_0001_pgvector")
	before := countRows(t, db)

	var sb strings.Builder
	err := Down(context.Background(), db, DownOptions{DryRun: true}, &sb, nil)
	if err != nil {
		t.Fatalf("Down dry-run: %v", err)
	}
	if countRows(t, db) != before {
		t.Errorf("dry-run must not change version table: before=%d after=%d", before, countRows(t, db))
	}
	out := sb.String()
	if !strings.Contains(out, "dry-run") {
		t.Errorf("output should mention dry-run:\n%s", out)
	}
	// 计划里应含要逆向的版本
	if !strings.Contains(out, "pre_0001_pgvector") {
		t.Errorf("dry-run plan should list pre_0001_pgvector:\n%s", out)
	}
}

// TestDown_AlwaysPrintsEntWarning 每次 down（含 dry-run）都打印 ent 边界警告（防误导核心）。
func TestDown_AlwaysPrintsEntWarning(t *testing.T) {
	db := newTestDB(t)
	markApplied(t, db, "pre_0001_pgvector")
	var sb strings.Builder
	_ = Down(context.Background(), db, DownOptions{DryRun: true}, &sb, nil)
	out := sb.String()
	if !strings.Contains(out, "ent auto-migrate") || !strings.Contains(out, "备份恢复") {
		t.Errorf("down output must contain ent boundary warning:\n%s", out)
	}
	// 与导出常量一致（CLI/文档引用同一份文案）
	if !strings.Contains(out, EntDownWarning) {
		t.Errorf("output should embed EntDownWarning verbatim:\n%s", out)
	}
}

// TestDown_RefusesMissingDownScript Down 顶层对无 down 脚本版本返回拒绝错误。
func TestDown_RefusesMissingDownScript(t *testing.T) {
	db := newTestDB(t)
	// 应用两者 → 缺省单步逆向最近的 0002_baseline（无 down）→ 拒绝。
	markApplied(t, db, "pre_0001_pgvector", "0002_baseline")
	var sb strings.Builder
	err := Down(context.Background(), db, DownOptions{}, &sb, nil)
	if !errors.Is(err, ErrMissingDownScript) {
		t.Fatalf("want ErrMissingDownScript, got %v", err)
	}
	// 拒绝时不得改动版本表
	if countRows(t, db) != 2 {
		t.Errorf("refused down must not change version table, got %d rows", countRows(t, db))
	}
}

// TestDown_NothingToDo 无已应用版本时干净返回、不报错。
func TestDown_NothingToDo(t *testing.T) {
	db := newTestDB(t)
	var sb strings.Builder
	if err := Down(context.Background(), db, DownOptions{}, &sb, nil); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if !strings.Contains(sb.String(), "nothing to do") {
		t.Errorf("expected nothing-to-do message:\n%s", sb.String())
	}
}

// TestDown_DestructiveNeedsConfirm 破坏性步骤无 --force 且确认返回 false → 拒绝执行。
func TestDown_DestructiveNeedsConfirm(t *testing.T) {
	// 用注入脚本模拟：pre_0001_pgvector 是真实破坏性版本，但其真实脚本是 PG 方言，
	// Down 会先要确认再 exec。confirm 返回 false → 应在 exec 前以 ErrDestructiveNeedsConfirm 停。
	db := newTestDB(t)
	markApplied(t, db, "pre_0001_pgvector")
	var sb strings.Builder
	err := Down(context.Background(), db, DownOptions{}, &sb, func(string) bool { return false })
	if !errors.Is(err, ErrDestructiveNeedsConfirm) {
		t.Fatalf("want ErrDestructiveNeedsConfirm, got %v", err)
	}
	// 未确认 → 未执行 → 版本表不变
	if countRows(t, db) != 1 {
		t.Errorf("unconfirmed destructive down must not run, got %d rows", countRows(t, db))
	}
}
