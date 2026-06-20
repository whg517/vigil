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

	// LLM 配置（智谱 GLM 等，能力域 11 AI Copilot）
	LLM LLM `envconfig:"llm"`

	// IM 配置（能力域 8 IM 协同，各平台适配器凭证）
	IM IM `envconfig:"im"`

	// Auth 鉴权配置（能力域 13）
	Auth Auth `envconfig:"auth"`
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

// Auth 鉴权配置（能力域 13）。
// Enabled 为 false 时业务 API 不强制身份解析（匿名放行，渐进启用阶段默认 false）；
// 为 true 时所有业务 API 需 X-Vigil-User-ID 身份头。
// webhook 接入/IM 回调不受此开关影响（它们用各自的 token/签名鉴权）。
type Auth struct {
	Enabled bool `envconfig:"enabled" default:"false"` // 是否强制业务 API 鉴权
}

// LLM 配置（智谱 GLM）。APIKey 为空时 AI 功能自动降级（设计基线第 7 条）。
// ⚠️ Key 仅从环境变量读取，绝不硬编码/提交 git。
type LLM struct {
	APIKey  string `envconfig:"api_key"`                // 智谱 API Key（VIGIL_LLM_API_KEY）
	Model   string `envconfig:"model" default:"glm-4-flash"` // 模型，glm-4-flash 轻量低成本
	BaseURL string `envconfig:"base_url" default:"https://open.bigmodel.cn/api/paas/v4"` // 智谱 OpenAPI 根
}

// IM 能力域 8 IM 协同配置。按平台分组，凭证缺失时对应适配器 Available()==false（降级）。
// ⚠️ AppSecret 等仅从环境变量读取，绝不硬编码/提交 git。
type IM struct {
	Feishu Feishu `envconfig:"feishu"`
}

// Feishu 飞书应用凭证（能力域 8 唯一真实接入平台）。
// 四要素均配置后适配器才 Available()，否则降级为不发送（设计基线第 7 条）。
type Feishu struct {
	AppID             string `envconfig:"app_id"`              // 应用 App ID（VIGIL_IM_FEISHU_APP_ID）
	AppSecret         string `envconfig:"app_secret"`          // 应用 App Secret
	VerificationToken string `envconfig:"verification_token"`  // 事件订阅校验 token
	EncryptKey        string `envconfig:"encrypt_key"`         // 事件订阅加密密钥（AES-256-CBC），空=不加密
	BaseURL           string `envconfig:"base_url" default:"https://open.feishu.cn/open-apis"` // OpenAPI 根（可换国际版域名）
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
