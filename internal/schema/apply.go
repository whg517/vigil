// Package schema 嵌入 atlas 版本化迁移文件，供运行时 apply。
//
// 设计：
//   - 迁移文件由 atlas migrate diff 在开发期生成（见 atlas.hcl，输出到 internal/schema/migrations）
//   - 编译期 //go:embed all:migrations 嵌入全部文件
//   - 运行时 vigil migrate 子命令解压到临时目录，调 atlas CLI apply（见 cmd/vigil/main.go）
//
// 这保持了 ADR-0031「单二进制 embed」原则：迁移文件与二进制同发同止，
// 运行环境只需 atlas CLI 二进制本身。
package schema

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed all:migrations
var embedded embed.FS

// FS 暴露嵌入的迁移文件根（含 atlas.sum + 全部 .sql）。
// 调用方可用 fs.WalkDir 遍历，或 fs.ReadFile 读单文件。
func FS() fs.FS {
	sub, err := fs.Sub(embedded, "migrations")
	if err != nil {
		// 编译期 embed 路径写死，出错只可能是开发改了目录结构忘了同步——panic 早暴露
		panic("schema: embedded migrations missing: " + err.Error())
	}
	return sub
}

// Extract 把嵌入的迁移目录解压到临时目录，返回目录路径。
// 调用方负责在用完后 os.RemoveAll 清理。
// 供 atlas CLI 用 --dir file://<path> 加载（atlas CLI 不能直接读 Go embed）。
func Extract() (string, error) {
	dir, err := os.MkdirTemp("", "vigil-migrations-")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}
	err = fs.WalkDir(FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		content, err := fs.ReadFile(FS(), path)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", path, err)
		}
		target := filepath.Join(dir, path)
		// #nosec G301 -- 临时目录权限 0755：仅本进程读写，外层 MkdirTemp 已限 0700。
		if mkErr := os.MkdirAll(filepath.Dir(target), 0o755); mkErr != nil {
			return mkErr
		}
		// #nosec G306 -- 写入临时目录供 atlas CLI 读取（同一用户），无需更严权限。
		if writeErr := os.WriteFile(target, content, 0o644); writeErr != nil {
			return writeErr
		}
		return nil
	})
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}
