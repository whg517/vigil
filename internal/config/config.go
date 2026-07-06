// Package config 定义 Vigil 的运行时配置。
//
// 配置来源优先级：环境变量 > 默认值。环境变量统一加 VIGIL_ 前缀。
// 对应 tech-stack.md §配置：环境变量 + YAML（12-Factor 风格）。
package config

import (
	"fmt"
	"time"

	"github.com/joho/godotenv"
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

	// Webhook 出口配置（能力域 14，向外部推送 incident 生命周期事件）
	Webhook Webhook `envconfig:"webhook"`

	// Ingestion 接入配置（能力域 1，限流/背压保护）
	Ingestion Ingestion `envconfig:"ingestion"`

	// Triage 分诊配置（能力域 3-4，去重/聚合窗口）
	Triage Triage `envconfig:"triage"`

	// Notification 通知通道配置（能力域 7，邮件/电话/SMS）
	Notification Notification `envconfig:"notification"`

	// Postmortem 复盘配置（能力域 12，自动起草触发档位）
	Postmortem Postmortem `envconfig:"postmortem"`

	// Credential 凭据加密托管配置（能力域 9 Runbook 执行器，T6.3）
	Credential Credential `envconfig:"credential"`

	// Retention Event/RawEvent 保留清理配置（平台化长尾，T6.2/M15）
	Retention Retention `envconfig:"retention"`
}

// Retention Event/RawEvent 保留清理配置（T6.2，能力域 15 平台化长尾）。
//
// 背景：Event 是海量不可变的原始信号（设计基线第 2 条），只追加不修改。
// 无保留策略时长期堆积会持续吃存储。本配置驱动周期性清理巡检删除超过保留期的旧
// Event/RawEvent，释放存储。
//
// 安全约束（在清理实现里保证，非配置项）：
//   - 只删「关联的 Incident 已 closed（或无关联）」的 Event——活跃处理单元引用的证据不删。
//   - 批量分页删除（每批限量），避免大事务锁表。
//
// 关闭：EventDays/RawEventDays <= 0 时对应清理不启用（永不删，向后兼容既有部署）。
type Retention struct {
	// EventDays Event 保留天数。超过此天数且关联 Incident 已 closed（或无关联）的 Event 被删。
	// <=0 表示不清理（默认 90 天，约一个季度，覆盖多数复盘/审计回溯需求）。
	EventDays int `envconfig:"event_days" default:"90"`
	// RawEventDays RawEvent 保留天数。RawEvent 是原始 payload 暂存，重放窗口过后即可清理。
	// <=0 表示不清理（默认 30 天，短于 Event——原始字节体积大且价值随时间衰减快）。
	RawEventDays int `envconfig:"raw_event_days" default:"30"`
	// Interval 清理巡检间隔。<=0 用默认 6h（低频后台任务，不与业务争资源）。
	Interval time.Duration `envconfig:"interval" default:"6h"`
	// BatchSize 单批删除上限（分页），避免大事务锁表。<=0 用默认 500。
	BatchSize int `envconfig:"batch_size" default:"500"`
}

// EffectiveInterval 返回清理巡检间隔，零值回退 6h。
func (r Retention) EffectiveInterval() time.Duration {
	if r.Interval <= 0 {
		return 6 * time.Hour
	}
	return r.Interval
}

// EffectiveBatchSize 返回单批删除上限，零值回退 500。
func (r Retention) EffectiveBatchSize() int {
	if r.BatchSize <= 0 {
		return 500
	}
	return r.BatchSize
}

// App 应用级配置。
type App struct {
	Env      string `envconfig:"env"     default:"development"` // development | production
	LogLevel string `envconfig:"log_level" default:"info"`      // debug | info | warn | error
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
// Enabled 为 true 时所有业务 API 须提供有效身份（JWT 优先，回退 X-Vigil-User-ID）；
// 为 false 时业务 API 不强制身份解析（匿名放行，仅限本地开发/测试）。
// webhook 接入/IM 回调不受此开关影响（它们用各自的 token/签名鉴权）。
//
// 安全默认（SEC-02）：默认 true（开箱即鉴权）。生产环境（App.Env=production）
// 下即使用户显式设置 false 也强制为 true——杜绝开箱即裸奔。
type Auth struct {
	Enabled bool `envconfig:"enabled" default:"true"` // 是否强制业务 API 鉴权（生产强制 true）

	// JWT 自管登录态配置（能力域 13 登录链路）。
	// JWTSecret 为空时登录链路降级（拒绝签发，提示配置缺失）。
	// ⚠️ Secret 仅从环境变量读取，绝不硬编码/提交 git；生产必须显式设置。
	JWTSecret       string        `envconfig:"jwt_secret"`                       // HMAC 签名密钥（VIGIL_AUTH_JWT_SECRET）
	AccessTokenTTL  time.Duration `envconfig:"access_token_ttl" default:"15m"`   // access token 有效期
	RefreshTokenTTL time.Duration `envconfig:"refresh_token_ttl" default:"720h"` // refresh token 有效期（30d）
}

// EffectiveAccessTokenTTL 返回 access token 有效期，零值时回退默认 15m。
// envconfig 嵌套 default 在部分场景不生效（见 .env.example 注释），故提供兜底。
func (a Auth) EffectiveAccessTokenTTL() time.Duration {
	if a.AccessTokenTTL == 0 {
		return 15 * time.Minute
	}
	return a.AccessTokenTTL
}

// EffectiveEnabled 返回鉴权是否生效（SEC-02）。
// 生产环境（App.Env=production）强制 true，杜绝显式 false 导致业务 API 裸奔。
// 配合 App.IsProduction 传入，避免 Auth 自身持有 App 引用。
//
// 用法：装配件应使用 cfg.Auth.EffectiveEnabled(cfg.App.IsProduction())
// 而非直接读 cfg.Auth.Enabled。
func (a Auth) EffectiveEnabled(isProduction bool) bool {
	if isProduction {
		return true // 生产强制鉴权，忽略用户配置
	}
	return a.Enabled
}

// EffectiveRefreshTokenTTL 返回 refresh token 有效期，零值时回退默认 720h（30d）。
func (a Auth) EffectiveRefreshTokenTTL() time.Duration {
	if a.RefreshTokenTTL == 0 {
		return 720 * time.Hour
	}
	return a.RefreshTokenTTL
}

// Webhook 出口配置（能力域 14）。
// OutURLs 为 incident 生命周期事件的订阅 URL 列表（逗号分隔）。
// 配置后，ack/resolve/escalate 等动作会推送给这些 URL。
// 为空则不推送。
type Webhook struct {
	OutURLs string `envconfig:"out_urls"` // 订阅 URL，逗号分隔
	// SigningSecret 出站签名密钥（S13）。非空时每次出站 POST 加 HMAC-SHA256 签名头
	// （X-Vigil-Signature = hex(HMAC(secret, timestamp + "." + body))）+ X-Vigil-Timestamp，
	// 接收端用同一密钥重算验源、并按时间戳容忍窗口防重放。
	// 为空则不签名（向后兼容既有订阅端）——★ 生产强烈建议配置，否则任何人可伪造出站事件投递给接收端。
	SigningSecret string `envconfig:"signing_secret"`
}

// Ingestion 接入限流/背压配置（能力域 1，PRD M1.7）。
// 保护系统不被单个告警源拖垮。无 Redis 时全部降级跳过（放行，可用性优先）。
type Ingestion struct {
	// RateLimitPerMin 单个接入点每分钟默认最大请求数。0=不限流。
	// 单个 Integration 可在 config.rate_limit 覆盖此默认值。
	RateLimitPerMin int `envconfig:"rate_limit_per_min" default:"600"`
	// BackpressureDepth 队列积压阈值，超过则接入层返回 503（payload 仍落库）。
	// 0=不检查背压。
	BackpressureDepth int `envconfig:"backpressure_depth" default:"10000"`
}

// Triage 分诊配置（能力域 3-4，C9：去重/聚合窗口可配，替代硬编码 5min）。
// 窗口过小易裂单（长风暴每窗口新建一单），过大易误聚合无关故障——按告警源特性调整。
type Triage struct {
	// DedupWindow 去重窗口：同 dedup_key 在窗口内重复直接丢弃。默认 5min。
	DedupWindow time.Duration `envconfig:"dedup_window" default:"5m"`
	// AggregateWindow 聚合窗口：同 service+severity 在窗口内并入同一 Incident。默认 5min。
	AggregateWindow time.Duration `envconfig:"aggregate_window" default:"5m"`
}

// EffectiveDedupWindow 返回去重窗口，零值回退 5min（envconfig 嵌套 default 兜底）。
func (t Triage) EffectiveDedupWindow() time.Duration {
	if t.DedupWindow <= 0 {
		return 5 * time.Minute
	}
	return t.DedupWindow
}

// EffectiveAggregateWindow 返回聚合窗口，零值回退 5min。
func (t Triage) EffectiveAggregateWindow() time.Duration {
	if t.AggregateWindow <= 0 {
		return 5 * time.Minute
	}
	return t.AggregateWindow
}

// Postmortem 复盘配置（能力域 12，M12.7 触发档位，T4.1）。
//
// 触发档位（docs/capabilities/08-postmortem.md §3）：
//   - critical：强制自动起草（无条件），不受配置影响。
//   - warning ：AutoDraftWarning 控制，默认 false（建议但不强制）。
//   - info    ：不强制，不起草。
//
// 简化说明：文档中 warning 档为「team 级可配」，但当前无 team 级复盘策略载体，
// 本轮以全局默认实现（VIGIL_POSTMORTEM_AUTO_DRAFT_WARNING）；后续可在引擎侧接入 team 配置。
type Postmortem struct {
	// AutoDraftWarning warning 级事件 resolved 是否自动起草复盘。默认 false。
	AutoDraftWarning bool `envconfig:"auto_draft_warning" default:"false"`
}

// Credential 凭据加密托管配置（能力域 9 Runbook 执行器，T6.3/S16）。
//
// EncryptionKey 为 AES-256 对称密钥，托管 Runbook 执行器访问外部平台（Ansible/Jenkins）
// 的 token 等凭据（DB 存密文，执行时解密注入）。以 base64 或 hex 编码传入（32 字节原始密钥）。
// ⚠️ 仅从环境变量读取（VIGIL_CREDENTIAL_ENCRYPTION_KEY），绝不硬编码/提交 git。
// 为空时凭据托管未启用：创建/更新凭据端点返回 503（不允许明文兜底），既有加密数据不受影响。
// 生成：`openssl rand -base64 32` 或 `openssl rand -hex 32`。
type Credential struct {
	EncryptionKey string `envconfig:"encryption_key"` // AES-256 密钥（base64/hex，32 字节），空=托管未启用
}

// Notification 通知通道配置（能力域 7，PRD M7.2/M7.3）。
// 各通道凭证缺失时降级为不发送（设计基线第 7 条）。
type Notification struct {
	SMTP  SMTP  `envconfig:"smtp"`  // 邮件通道
	Phone Voice `envconfig:"phone"` // 电话通道（占位，转发 webhook）
	SMS   Voice `envconfig:"sms"`   // 短信通道（占位，转发 webhook）
}

// SMTP 邮件服务器配置（能力域 7 M7.3）。Host 为空时邮件通道禁用。
type SMTP struct {
	Host     string `envconfig:"host"`     // SMTP 服务器（如 smtp.example.com），空=禁用
	Port     int    `envconfig:"port"`     // 端口（25/465/587），0=25
	Username string `envconfig:"username"` // 认证用户名，空=匿名
	Password string `envconfig:"password"` // 认证密码
	From     string `envconfig:"from"`     // 发件人地址，空=vigil@localhost
}

// Voice 电话/SMS 提供商配置（能力域 7 M7.2，本期占位）。
// WebhookURL 非空时，通知 POST 到此 URL，用户在端侧对接云语音 API（阿里云/腾讯云）。
// 真实云厂商对接留 docs/backlog.md，避免本期绑定具体厂商。
type Voice struct {
	WebhookURL string `envconfig:"webhook_url"` // 语音/SMS 接收端点，空=禁用
	From       string `envconfig:"from"`        // 主叫/发件标识（可选）
}

// LLM 配置（云端智谱 GLM / 本地 Ollama）。APIKey/BaseURL 缺失时 AI 功能自动降级（设计基线第 7 条）。
// ⚠️ Key 仅从环境变量读取，绝不硬编码/提交 git。
type LLM struct {
	// Provider 选择 LLM 提供方："glm"（云端，默认）| "ollama"（本地，数据不出境）。
	// VIGIL_LLM_PROVIDER。未知值回退到 glm。
	Provider string `envconfig:"provider" default:"glm"`
	APIKey   string `envconfig:"api_key"`                                                 // 智谱 API Key（VIGIL_LLM_API_KEY）
	Model    string `envconfig:"model" default:"glm-4-flash"`                             // GLM 模型，glm-4-flash 轻量低成本
	BaseURL  string `envconfig:"base_url" default:"https://open.bigmodel.cn/api/paas/v4"` // 智谱 OpenAPI 根
	// ConfidenceThreshold AI 建议产出置信度门槛（低于此值不产建议，见 capabilities/07 Q2）。
	// 默认 0.6，可经 VIGIL_LLM_CONFIDENCE_THRESHOLD 覆盖；<=0 时各引擎 Setter 内部保留默认。
	ConfidenceThreshold float32 `envconfig:"confidence_threshold" default:"0.6"`
	// Ollama 本地 Provider 子配置（Provider=="ollama" 时生效）。VIGIL_LLM_OLLAMA_*。
	Ollama LLMOllama `envconfig:"ollama"`
	// Cost LLM 成本控制（能力域 11，缓存/限流/配额）。无 Redis 时全部降级跳过。
	Cost LLMCost `envconfig:"cost"`
}

// LLMOllama 本地 Ollama Provider 配置（M11.10：隐私场景数据不出境）。
// ⚠️ EmbedModel 维度须与 pgvector 列（vector(1536)）匹配，否则相似检索降级为文本匹配。
type LLMOllama struct {
	BaseURL    string `envconfig:"base_url" default:"http://localhost:11434"` // Ollama 服务根，空=禁用
	Model      string `envconfig:"model" default:"llama3"`                    // 补全模型
	EmbedModel string `envconfig:"embed_model" default:"nomic-embed-text"`    // embedding 模型（768 维，注意 pgvector 匹配）
}

// LLMCost LLM 成本控制配置（capabilities/07 §B5 Q1）。
type LLMCost struct {
	CacheTTLSeconds int  `envconfig:"cache_ttl_seconds" default:"3600"` // Complete 缓存 TTL（秒），0=1h 默认
	DisableCache    bool `envconfig:"disable_cache"`                    // 关闭缓存（调试）
	RateLimitPerMin int  `envconfig:"rate_limit_per_min"`               // 每分钟最大请求数，0=不限流
	TokenQuota      int  `envconfig:"token_quota"`                      // token 配额（累计），0=不限额
}

// IM 能力域 8 IM 协同配置。按平台分组，凭证缺失时对应适配器 Available()==false（降级）。
// ⚠️ AppSecret 等仅从环境变量读取，绝不硬编码/提交 git。
type IM struct {
	Feishu   Feishu   `envconfig:"feishu"`
	Dingtalk Dingtalk `envconfig:"dingtalk"`
	// OncallChannel 值班群 channel 标识（飞书 chat_id / 钉钉 openConversationId）。
	// 告警卡片发送到此群。为空则 IM 通知不发送（待私聊解析完整实现）。
	OncallChannel string `envconfig:"oncall_channel"`
}

// Feishu 飞书应用凭证（能力域 8 真实接入平台之一）。
// 四要素均配置后适配器才 Available()，否则降级为不发送（设计基线第 7 条）。
type Feishu struct {
	AppID             string `envconfig:"app_id"`                                              // 应用 App ID（VIGIL_IM_FEISHU_APP_ID）
	AppSecret         string `envconfig:"app_secret"`                                          // 应用 App Secret
	VerificationToken string `envconfig:"verification_token"`                                  // 事件订阅校验 token
	EncryptKey        string `envconfig:"encrypt_key"`                                         // 事件订阅加密密钥（AES-256-CBC），空=不加密
	BaseURL           string `envconfig:"base_url" default:"https://open.feishu.cn/open-apis"` // OpenAPI 根（可换国际版域名）
}

// Dingtalk 钉钉企业内部应用凭证（能力域 8 真实接入平台之一，与飞书并列 P0）。
// AppKey+AppSecret 均配置后适配器 Available()，否则降级。
// ⚠️ AesKey/Token 仅事件订阅（回调）需要；不配事件订阅只发消息时可留空。
type Dingtalk struct {
	AppKey    string `envconfig:"app_key"`    // 企业内部应用 AppKey（VIGIL_IM_DINGTALK_APP_KEY）
	AppSecret string `envconfig:"app_secret"` // 企业内部应用 AppSecret
	RobotCode string `envconfig:"robot_code"` // 机器人编码，缺省等于 AppKey
	Token     string `envconfig:"token"`      // 事件订阅校验 token（明文校验，对应飞书 VerificationToken）
	AesKey    string `envconfig:"aes_key"`    // 事件订阅加密密钥（AES-256-CBC，base64 43 字符），空=不加密
	OapiBase  string `envconfig:"oapi_base"`  // 旧版域名，默认 https://oapi.dingtalk.com（测试可换）
	APIBase   string `envconfig:"api_base"`   // 新版域名，默认 https://api.dingtalk.com（测试可换）
}

// Load 从环境变量加载配置（前缀 VIGIL）。
// 优先从 .env 文件加载（开发便捷），再读取 OS 环境变量（生产注入）。
// .env 文件不存在时静默跳过（生产环境无 .env 是正常的）。
//
// 例：VIGIL_DB_HOST=... VIGIL_HTTP_ADDR=:9090
func Load() (*Config, error) {
	// 开发便捷：自动加载项目根目录 .env 文件。
	// 生产环境（Docker/K8s）通过 OS 环境变量注入，无 .env 文件，静默跳过。
	_ = godotenv.Load()

	var c Config
	if err := envconfig.Process("vigil", &c); err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	// development 模式自动填充 JWT secret（避免新开发者启动后登录不可用）。
	// 仅 development 生效，生产模式不填充。
	if c.App.Env == "development" && c.Auth.JWTSecret == "" {
		c.Auth.JWTSecret = "dev-jwt-secret-not-for-production"
	}

	// 生产环境安全校验（SEC-02）：强制鉴权已生效（EffectiveEnabled 保证），
	// 但鉴权链路依赖 JWT secret——secret 缺失会导致登录链路降级（拒绝签发），
	// 而强制鉴权 + 无可签发 = 系统不可用。故生产必须显式配置强 secret。
	if c.App.IsProduction() && c.Auth.JWTSecret == "" {
		return nil, fmt.Errorf("production requires VIGIL_AUTH_JWT_SECRET to be set (auth is enforced)")
	}

	return &c, nil
}
