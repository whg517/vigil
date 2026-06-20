// Package store 初始化数据访问客户端（PostgreSQL/ent + Redis）。
//
// 对应 architecture.md §分层：store 层封装 ent 数据访问 + Redis 客户端。
// 业务模块通过 Store 拿到 *ent.Client 与 *redis.Client。
package store

import (
	"context"
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
	return &Store{DB: db, Redis: rc}, nil
}

// openDB 打开 ent client（底层 PostgreSQL），并 ping 验证连通。
// 不在此自动迁移，迁移由 migrate 子命令/部署流程负责。
// 驱动：lib/pq（与 main.go 的 blank import 一致，ent dialect "postgres" 也用此）。
func openDB(ctx context.Context, cfg *config.Config) (*ent.Client, error) {
	db, err := ent.Open("postgres", cfg.DB.DSN())
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	// ping 验证连通性：通过查实体计数验证（兼容 ent 接口，不依赖底层 driver）
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pingViaEnt(pingCtx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return db, nil
}

// pingViaEnt 通过查一个实体计数验证连通（兼容 ent 接口，不依赖底层 driver）。
func pingViaEnt(ctx context.Context, db *ent.Client) error {
	_, err := db.User.Query().Limit(1).Count(ctx)
	return err
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
	return firstErr
}
