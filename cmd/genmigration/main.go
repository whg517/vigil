// Command genmigration 从 ent schema 生成 postgres baseline SQL（ENG-01/FIX-4）。
//
// 用途：把 ent 的权威 schema 物化为可 review 的 DDL，作为 internal/migrate/migrations/
// 0002_baseline.sql 的内容（替代占位的 SELECT 1）。配合 CI drift 检测保证同步。
//
// 原理：连一个「空但已装 pgvector 扩展」的 postgres 库，ent Schema.WriteTo 会输出
// 把当前 schema 物化所需的全部 CREATE TABLE/INDEX（因为目标库为空，diff = 全量）。
//
// 用法：
//
//	go run ./cmd/genmigration                   # 默认连本地 vigil-postgres 的 genmig_test 库
//	go run ./cmd/genmigration -dsn "host=... dbname=empty"  # 自定义空库 DSN
//
// 前置：目标库须已 CREATE EXTENSION vector（见 internal/migrate/migrations/pre_0001_pgvector.sql）。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/kevin/vigil/ent"

	_ "github.com/lib/pq"
)

func main() {
	dsn := flag.String("dsn", "host=localhost port=5432 user=vigil password=vigil dbname=genmig_test sslmode=disable", "空库 DSN（须已装 pgvector）")
	out := flag.String("out", "internal/migrate/schema/baseline.sql", "输出 SQL 文件")
	flag.Parse()

	db, err := ent.Open("postgres", *dsn)
	if err != nil {
		log.Fatalf("open ent: %v", err)
	}
	defer func() { _ = db.Close() }()

	f, err := os.Create(*out)
	if err != nil {
		log.Fatalf("create output: %v", err)
	}
	defer func() { _ = f.Close() }()

	// mustWrite 写入失败即 fatal（生成工具，错误不可忽略）。
	mustWrite := func(s string) {
		if _, err := f.WriteString(s); err != nil {
			log.Fatalf("write output: %v", err)
		}
	}

	// 文件头注释
	mustWrite("-- 0002_baseline.sql\n")
	mustWrite("-- 完整建表 baseline（由 cmd/genmigration 从 ent schema 生成，勿手改）。\n")
	mustWrite("-- 改 ent/schema 后运行 `go run ./cmd/genmigration` 重新生成；CI 会 drift 检测。\n")
	mustWrite("-- 依赖 pre_0001_pgvector.sql 先安装 vector 扩展（Incident/Postmortem 的 embedding 列用）。\n")
	mustWrite("-- migrate.Run 优先用本文件建表；ent auto-migrate 作为兜底补充同步（处理边角差异）。\n\n")

	// WriteTo 输出 postgres 方言 DDL（目标空库 → 全量 CREATE）
	if err := db.Schema.WriteTo(context.Background(), f); err != nil {
		// 去掉 zap 之类的噪音，聚焦错误
		msg := err.Error()
		if idx := strings.Index(msg, "pq:"); idx >= 0 {
			msg = msg[idx:]
		}
		log.Fatalf("write schema: %v", msg)
	}
	fmt.Fprintf(os.Stderr, "✓ baseline written to %s\n", *out)
}
