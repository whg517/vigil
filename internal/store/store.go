// Package store 初始化数据访问客户端（PostgreSQL/ent + Redis）。
//
// 对应 architecture.md §分层：store 层封装 ent 数据访问 + Redis 客户端。
// 业务模块通过 Store 拿到 *ent.Client 与 *redis.Client。
package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/internal/config"

	"github.com/redis/go-redis/v9"
)

// Store 聚合所有数据访问客户端。
type Store struct {
	DB    *ent.Client
	Redis *redis.Client
	// SQL 原生 *sql.DB（pgvector 等需要 raw SQL 的场景用，由 New 打开）。
	// nil 时表示未启用 raw SQL（部分高级功能降级）。
	SQL *sql.DB
}

// New 创建并连通 store（含连通性 ping）。
func New(ctx context.Context, cfg *config.Config) (*Store, error) {
	db, err := openDB(ctx, cfg)
	if err != nil {
		return nil, err
	}
	rc, err := openRedis(ctx, cfg)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	// 原生 *sql.DB：供 pgvector 相似检索等 raw SQL 场景用。
	sqlDB, err := sql.Open("postgres", cfg.DB.DSN())
	if err != nil {
		_ = db.Close()
		_ = rc.Close()
		return nil, fmt.Errorf("open raw sql db: %w", err)
	}
	sqlDB.SetMaxOpenConns(10)
	// BE-02：用 SELECT 1 轻量探活（替代旧的 User.Count 全表扫描）。
	// 启动期验证连通即可，无需遍历数据。
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		_ = db.Close()
		_ = rc.Close()
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &Store{DB: db, Redis: rc, SQL: sqlDB}, nil
}

// openDB 打开 ent client（底层 PostgreSQL）。
// 不在此自动迁移或 ping（连通性由 store.New 统一用 sql.DB.PingContext 探活）。
// 驱动：lib/pq（与 main.go 的 blank import 一致，ent dialect "postgres" 也用此）。
func openDB(ctx context.Context, cfg *config.Config) (*ent.Client, error) {
	db, err := ent.Open("postgres", cfg.DB.DSN())
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	return db, nil
}

// openRedis 打开 Redis client。
func openRedis(ctx context.Context, cfg *config.Config) (*redis.Client, error) {
	rc := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := rc.Ping(pingCtx).Err(); err != nil {
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return rc, nil
}

// Close 关闭所有客户端（优雅退出时调用）。
func (s *Store) Close() error {
	var firstErr error
	if err := s.DB.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := s.Redis.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	if s.SQL != nil {
		if err := s.SQL.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
