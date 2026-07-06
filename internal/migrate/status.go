// status.go 实现 `vigil migrate status`：展示迁移版本状态，让运维一眼看清
// 「已应用哪些版本、当前版本是什么、还有哪些版本待应用」。
package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"sort"
	"time"
)

// VersionState 单个版本的状态。
type VersionState struct {
	Version   string     // 版本号（如 0002_baseline / pre_0001_pgvector）
	Applied   bool       // 是否已应用
	AppliedAt *time.Time // 应用时间（仅 Applied=true 时有值）
	HasDown   bool       // 是否提供了 down 逆向脚本
}

// StatusReport migrate status 的完整结果。
type StatusReport struct {
	// Versions 已知版本（来自嵌入的 migrations/ 目录），按 apply 顺序升序。
	Versions []VersionState
	// Current 当前版本 = 已应用版本中「按 apply 顺序」最后一个；无则为空串。
	Current string
	// Pending 待应用版本（嵌入目录里有、但 schema_migrations 未记录），升序。
	Pending []string
	// Orphaned 库里记录了但嵌入目录中已不存在的版本（老版本迁移文件被删/降级留下的记录）。
	Orphaned []string
}

// Status 采集迁移状态。只读，不修改任何数据。
//
// 数据来源：
//   - 嵌入的 migrations/ 目录 → 已知版本清单 + down 脚本存在性
//   - schema_migrations 表     → 已应用版本 + 应用时间
func Status(ctx context.Context, sqlDB *sql.DB) (*StatusReport, error) {
	if err := ensureVersionTable(ctx, sqlDB); err != nil {
		return nil, err
	}

	appliedAt, err := appliedVersionTimes(ctx, sqlDB)
	if err != nil {
		return nil, fmt.Errorf("query applied versions: %w", err)
	}

	known, err := migrationVersions()
	if err != nil {
		return nil, err
	}
	knownSet := make(map[string]bool, len(known))

	report := &StatusReport{}
	for _, v := range known {
		knownSet[v] = true
		_, has := downScriptFor(v)
		st := VersionState{Version: v, HasDown: has}
		if t, ok := appliedAt[v]; ok {
			st.Applied = true
			at := t
			st.AppliedAt = &at
			report.Current = v // known 已升序，最后一个 applied 即当前版本
		} else {
			report.Pending = append(report.Pending, v)
		}
		report.Versions = append(report.Versions, st)
	}

	// 库里记录了、但嵌入目录已无的版本（孤儿记录）——运维需要知道，避免误判。
	for v := range appliedAt {
		if !knownSet[v] {
			report.Orphaned = append(report.Orphaned, v)
		}
	}
	sort.Strings(report.Orphaned)

	return report, nil
}

// appliedVersionTimes 查询已应用版本 → 应用时间。
func appliedVersionTimes(ctx context.Context, sqlDB *sql.DB) (map[string]time.Time, error) {
	rows, err := sqlDB.QueryContext(ctx, "SELECT version, applied_at FROM schema_migrations")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]time.Time{}
	for rows.Next() {
		var (
			v  string
			at time.Time
		)
		if err := rows.Scan(&v, &at); err != nil {
			return nil, err
		}
		out[v] = at
	}
	return out, rows.Err()
}

// Render 把状态报告以人类可读文本写出（供 CLI 用）。
// 命名刻意避开 WriteTo，以免与 io.WriterTo 接口签名冲突（go vet）。
func (r *StatusReport) Render(w io.Writer) error {
	e := &errWriter{w: w}
	e.printf("迁移状态（schema_migrations）\n")
	e.printf("================================================\n")
	if r.Current == "" {
		e.printf("当前版本: <无>（尚未应用任何版本化迁移）\n")
	} else {
		e.printf("当前版本: %s\n", r.Current)
	}
	e.printf("已知版本: %d  待应用: %d\n\n", len(r.Versions), len(r.Pending))

	e.printf("%-4s %-24s %-10s %-8s %s\n", "", "版本", "状态", "可逆", "应用时间")
	e.printf("------------------------------------------------\n")
	for _, v := range r.Versions {
		mark := "○"
		state := "待应用"
		at := "-"
		if v.Applied {
			mark = "●"
			state = "已应用"
			if v.AppliedAt != nil {
				at = v.AppliedAt.Format("2006-01-02 15:04:05")
			}
		}
		reversible := "no"
		if v.HasDown {
			reversible = "yes"
		}
		e.printf("%-4s %-24s %-10s %-8s %s\n", mark, v.Version, state, reversible, at)
	}

	if len(r.Orphaned) > 0 {
		e.printf("\n⚠️  孤儿记录（schema_migrations 有记录，但嵌入迁移目录中已无对应文件）:\n")
		for _, v := range r.Orphaned {
			e.printf("    - %s\n", v)
		}
	}

	e.printf("\n注：「可逆」仅表示该版本化 SQL 迁移是否提供了 .down.sql 逆向脚本。\n")
	e.printf("    ent auto-migrate 的实体结构变更（建表/加列）不在版本表里，也不可由 migrate down 逆向，\n")
	e.printf("    如需回退实体结构变更请用备份恢复（scripts/restore.sh）。\n")
	return e.Err()
}
