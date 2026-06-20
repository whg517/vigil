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
func openDB(ctx context.Context, cfg *config.Config) (*ent.Client, error) {
	// 先用 database/sql ping 验证连通性（轻量，不依赖 ent 内部接口）
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	sqlDB, err := sql.Open("pgx", pgxDSN(cfg))
	if err != nil {
		return nil, fmt.Errorf("open postgres (ping): %w", err)
	}
	defer sqlDB.Close()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	// 再用 ent 打开业务用 client
	db, err := ent.Open("postgres", cfg.DB.DSN())
	if err != nil {
		return nil, fmt.Errorf("open postgres (ent): %w", err)
	}
	return db, nil
}

// pgxDSN 返回 pgx 驱动的连接串（database/sql 用）。
func pgxDSN(cfg *config.Config) string {
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%d/%s?sslmode=%s",
		cfg.DB.User, cfg.DB.Password, cfg.DB.Host, cfg.DB.Port, cfg.DB.Name, cfg.DB.SSLMode,
	)
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
