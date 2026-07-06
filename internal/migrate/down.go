// down.go 实现 `vigil migrate down`：逆向【有对应 down 脚本的版本化 SQL 迁移】。
//
// ★ 诚实边界（务必理解，勿被命令名误导）：
//
//	migrate down 只能可靠地逆向「版本化 SQL 迁移」（pre_*.sql / post_*.sql 这类
//	提供了 <version>.down.sql 逆向脚本的步骤）。它【绝不逆向 ent auto-migrate 的
//	实体结构变更】——ent auto-migrate 是声明式 diff，无法安全自动逆向（down 需
//	hand-tuned SQL 或备份恢复）。因此凡涉及回退实体表/列的变更，唯一可靠手段是
//	从备份恢复（scripts/restore.sh）。本命令会在开始时显式打印此警告。
package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
)

// EntDownWarning 每次执行 down（含 dry-run）都会打印的固定警告，防止误以为
// 「migrate down 能完整回滚到任意版本」。
const EntDownWarning = `⚠️  重要边界：ent auto-migrate 的 schema 变更（建表/加列/改类型）不会被本命令逆向。
    本命令只逆向【提供了 .down.sql 逆向脚本的版本化 SQL 迁移】。
    如需回退实体结构变更（回退到旧版本的表结构），请用备份恢复：scripts/restore.sh。`

// ErrMissingDownScript 目标版本缺少 down 脚本时返回（拒绝，不静默跳过）。
var ErrMissingDownScript = errors.New("version has no .down.sql script; cannot be reversed by migrate down")

// ErrDestructiveNeedsConfirm 破坏性步骤未确认/未 --force 时返回。
var ErrDestructiveNeedsConfirm = errors.New("destructive down step requires confirmation or --force")

// DownOptions 控制 down 行为。
type DownOptions struct {
	// To 目标版本：逆向所有「晚于 To」的已应用版本，保留 To 及更早的版本。
	// 为空表示只逆向【最近应用的一个版本】（单步回滚）。
	To string
	// DryRun 只规划打印将执行什么，不落库。
	DryRun bool
	// Force 跳过破坏性步骤的交互确认（自动化场景）。
	Force bool
}

// downStep 一个待逆向步骤。
type downStep struct {
	Version     string // 版本号
	Script      []byte // down 脚本内容
	Destructive bool   // 是否破坏性（删数据/结构）
}

// planDown 计算 down 执行计划（逆向顺序 = apply 逆序）。
//
// 规则：
//   - 只考虑「已应用」且「按 apply 顺序晚于 To」的版本。
//   - 逆序（后应用的先逆向）。
//   - 任一目标版本缺 down 脚本 → 整体拒绝（返回 ErrMissingDownScript），不部分执行。
//   - To 非空但不在已知/已应用版本中 → 报错（避免误操作）。
func planDown(applied map[string]bool, opts DownOptions) ([]downStep, error) {
	known, err := migrationVersions()
	if err != nil {
		return nil, err
	}

	// 已应用版本，按 apply 顺序（= known 顺序）排列。
	var appliedOrdered []string
	for _, v := range known {
		if applied[v] {
			appliedOrdered = append(appliedOrdered, v)
		}
	}
	if len(appliedOrdered) == 0 {
		return nil, nil // 无已应用版本 → 无事可做
	}

	// 校验 --to 目标：必须是已应用的已知版本（或空）。
	if opts.To != "" {
		found := false
		for _, v := range appliedOrdered {
			if v == opts.To {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("--to %q 不是一个已应用的已知版本（当前已应用: %v）", opts.To, appliedOrdered)
		}
	}

	// 选出要逆向的版本集合。
	var targets []string
	if opts.To == "" {
		// 单步：仅最近应用的一个
		targets = []string{appliedOrdered[len(appliedOrdered)-1]}
	} else {
		// 逆向所有「晚于 To」的版本（保留 To）
		for _, v := range appliedOrdered {
			if v == opts.To {
				break // To 及更早保留
			}
		}
		// 收集 To 之后的（在有序切片中 To 的索引之后）
		idx := -1
		for i, v := range appliedOrdered {
			if v == opts.To {
				idx = i
				break
			}
		}
		targets = append(targets, appliedOrdered[idx+1:]...)
	}

	// 逆序执行（后应用先逆向），并校验每个都有 down 脚本。
	steps := make([]downStep, 0, len(targets))
	for i := len(targets) - 1; i >= 0; i-- {
		v := targets[i]
		script, ok := downScriptFor(v)
		if !ok {
			return nil, fmt.Errorf("%w: %s（该版本可能是 baseline/ent 相关结构，只能靠备份恢复）", ErrMissingDownScript, v)
		}
		steps = append(steps, downStep{
			Version:     v,
			Script:      script,
			Destructive: isDestructive(script),
		})
	}
	return steps, nil
}

// Down 执行迁移回滚。confirm 用于破坏性步骤的交互确认（CLI 传入读 stdin 的函数；
// nil 表示无交互，此时破坏性步骤须 --force 否则拒绝）。所有输出写 w。
//
// 执行语义：
//   - 先打印 ent 边界警告（EntDownWarning）。
//   - 计算计划；缺 down 脚本整体拒绝。
//   - dry-run：只打印计划，不落库，直接返回。
//   - 逐步执行：每步一个事务（DELETE 版本记录 + 跑 down 脚本），失败即停（不继续后续步骤）。
func Down(ctx context.Context, sqlDB *sql.DB, opts DownOptions, w io.Writer, confirm func(prompt string) bool) error {
	e := &errWriter{w: w}

	// 1. 无条件打印边界警告（防误导，诚实第一）
	e.printf("%s\n\n", EntDownWarning)

	if err := ensureVersionTable(ctx, sqlDB); err != nil {
		return err
	}
	applied, err := getAppliedVersions(ctx, sqlDB)
	if err != nil {
		return fmt.Errorf("get applied versions: %w", err)
	}

	// 2. 规划
	steps, err := planDown(applied, opts)
	if err != nil {
		return err
	}
	if len(steps) == 0 {
		e.printf("无可逆向的已应用版本（nothing to do）。\n")
		return e.Err()
	}

	// 3. 打印计划
	e.printf("将按以下顺序逆向（后应用的先逆向）：\n")
	hasDestructive := false
	for i, s := range steps {
		tag := ""
		if s.Destructive {
			tag = "  ⚠️ 破坏性（删数据/结构）"
			hasDestructive = true
		}
		e.printf("  %d. %s%s\n", i+1, s.Version, tag)
	}
	e.printf("\n")

	// 4. dry-run：到此为止
	if opts.DryRun {
		e.printf("[dry-run] 未执行任何变更，schema_migrations 未改动。\n")
		return e.Err()
	}

	// 5. 破坏性确认
	if hasDestructive && !opts.Force {
		ok := false
		if confirm != nil {
			ok = confirm("包含破坏性步骤，确认执行回滚？输入 yes 继续: ")
		}
		if !ok {
			return ErrDestructiveNeedsConfirm
		}
	}

	// 6. 逐步执行（每步一个事务，失败即停）
	for _, s := range steps {
		if err := execDownStep(ctx, sqlDB, s); err != nil {
			return fmt.Errorf("逆向 %s 失败（已停止，之前的步骤已提交）: %w", s.Version, err)
		}
		e.printf("✓ 已逆向 %s\n", s.Version)
	}
	e.printf("\ndown 完成。\n")
	e.printf("提醒：以上仅逆向了版本化 SQL 迁移，ent 实体结构变更未被触碰（见开头警告）。\n")
	return e.Err()
}

// execDownStep 在单个事务内逆向一步：跑 down 脚本 + 删除版本记录。
// 版本记录删除用 sqlDB 的占位符方言问题：down 脚本自身用标准 SQL；
// 版本删除用 placeholder() 适配 postgres($1)/sqlite(?)。
func execDownStep(ctx context.Context, sqlDB *sql.DB, s downStep) error {
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	// 出错回滚（提交后回滚是 no-op，安全）
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ExecContext(ctx, string(s.Script)); err != nil {
		return fmt.Errorf("run down script: %w", err)
	}
	// 版本值走参数绑定（$1/?），非拼接；按方言选常量 SQL 字面量（避免 gosec G202 误报）。
	delQuery := "DELETE FROM schema_migrations WHERE version = $1"
	if placeholder(sqlDB) == "?" {
		delQuery = "DELETE FROM schema_migrations WHERE version = ?"
	}
	if _, err := tx.ExecContext(ctx, delQuery, s.Version); err != nil {
		return fmt.Errorf("delete version record: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	committed = true
	return nil
}
