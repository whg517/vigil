// Package config 定义 Vigil 的运行时配置。
//
// 配置来源优先级：环境变量 > 默认值。环境变量统一加 VIGIL_ 前缀。
// 对应 tech-stack.md §配置：环境变量 + YAML（12-Factor 风格）。
package config

import (
	"fmt"

	"github.com/kelseyhightower/envconfig"
)

// Config Vigil 全局配置。
type Config struct {
	// App 应用级配置
	App App `envconfig:"app"`

	// HTTP API 服务配置
	HTTP HTTP `envconfig:"http"`

	// DB PostgreSQL 配置
	DB DB `envconfig:"db"`

	// Redis 配置（缓存 + 队列 + 锁）
	Redis Redis `envconfig:"redis"`

	// Asynq 异步任务配置（基于 Redis）
	Asynq Asynq `envconfig:"asynq"`
}

// App 应用级配置。
type App struct {
	Env     string `envconfig:"env"     default:"development"` // development | production
	LogLevel string `envconfig:"log_level" default:"info"` // debug | info | warn | error
}

// IsProduction 是否生产环境。
func (a App) IsProduction() bool { return a.Env == "production" }

// HTTP API 服务配置。
type HTTP struct {
	Addr string `envconfig:"addr" default:":8080"` // 监听地址
}

// DB PostgreSQL 配置。
type DB struct {
	Host     string `envconfig:"host"     default:"localhost"`
	Port     int    `envconfig:"port"     default:"5432"`
	User     string `envconfig:"user"     default:"vigil"`
	Password string `envconfig:"password" default:"vigil"`
	Name     string `envconfig:"name"     default:"vigil"`
	SSLMode  string `envconfig:"ssl_mode" default:"disable"` // disable | require | verify-full
}

// DSN 拼接 PostgreSQL 连接串。
func (d DB) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		d.Host, d.Port, d.User, d.Password, d.Name, d.SSLMode,
	)
}

// Redis 配置。
type Redis struct {
	Addr     string `envconfig:"addr"     default:"localhost:6379"`
	Password string `envconfig:"password" default:""`
	DB       int    `envconfig:"db"       default:"0"`
}

// Asynq 异步任务配置。
type Asynq struct {
	Concurrency int `envconfig:"concurrency" default:"10"` // worker 并发数
}

// Load 从环境变量加载配置（前缀 VIGIL）。
// 例：VIGIL_DB_HOST=... VIGIL_HTTP_ADDR=:9090
func Load() (*Config, error) {
	var c Config
	if err := envconfig.Process("vigil", &c); err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return &c, nil
}
