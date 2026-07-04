// jwt.go JWT 签发与校验（能力域 13 §登录态）。
//
// 自管 JWT：后端 HMAC 签发，前端 localStorage 存。
// access token 短期（默认 15m），refresh token 长期（默认 30d）。
// 算法 HS256，Claims 含 userID/username/token_type/exp。
//
// 安全要点：
//   - secret 为空时签发/校验一律返回错误（降级保护，拒绝在无密钥下运行登录链路）
//   - 强制 HMAC 签名方法，拒绝其他算法（防 alg=none 注入）
//   - access/refresh 用 token_type 区分，refresh 不能当 access 用
package auth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// TokenType access / refresh，写进 Claims 防止 refresh token 被当作 access 使用。
type TokenType string

const (
	TokenTypeAccess  TokenType = "access"
	TokenTypeRefresh TokenType = "refresh"
)

// Claims JWT 声明。嵌入 RegisteredClaims 获得 exp/iat/sub 等标准字段。
type Claims struct {
	UserID    int       `json:"uid"`
	Username  string    `json:"usr"`
	TokenType TokenType `json:"typ"`
	// TokenVersion 签发时用户的 token_version 快照（T0.4 改密令牌吊销）。
	// 鉴权时与库中当前 token_version 比对，不一致视为已吊销——改密后所有旧 token 立即失效。
	TokenVersion int `json:"tv"`
	jwt.RegisteredClaims
}

// JWTSigner JWT 签发/校验器（无状态，持有 HMAC 密钥）。
// 用结构体而非包级全局变量，便于测试隔离与多实例配置。
type JWTSigner struct {
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
}

// NewJWTSigner 构造签发器。secret 为空时签发/校验返回错误（降级保护）。
func NewJWTSigner(secret string, accessTTL, refreshTTL time.Duration) *JWTSigner {
	return &JWTSigner{secret: []byte(secret), accessTTL: accessTTL, refreshTTL: refreshTTL}
}

// Available 是否可签发（secret 已配置）。
func (s *JWTSigner) Available() bool { return len(s.secret) > 0 }

// GenerateAccessToken 签发 access token（含 username，用于业务展示）。
// tokenVersion 为签发时用户的 token_version 快照（改密令牌吊销依据，T0.4）。
func (s *JWTSigner) GenerateAccessToken(userID int, username string, tokenVersion int) (string, error) {
	if !s.Available() {
		return "", errors.New("jwt secret not configured")
	}
	now := time.Now()
	claims := &Claims{
		UserID:       userID,
		Username:     username,
		TokenType:    TokenTypeAccess,
		TokenVersion: tokenVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(s.accessTTL)),
			IssuedAt:  jwt.NewNumericDate(now),
			Subject:   username,
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString(s.secret)
}

// GenerateRefreshToken 签发 refresh token（仅用于换发 access，不含 username）。
// tokenVersion 同 access：改密后旧 refresh 也失效，杜绝凭旧 refresh 换新 access 的旁路。
func (s *JWTSigner) GenerateRefreshToken(userID int, tokenVersion int) (string, error) {
	if !s.Available() {
		return "", errors.New("jwt secret not configured")
	}
	now := time.Now()
	claims := &Claims{
		UserID:       userID,
		TokenType:    TokenTypeRefresh,
		TokenVersion: tokenVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(s.refreshTTL)),
			IssuedAt:  jwt.NewNumericDate(now),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString(s.secret)
}

// ParseToken 校验签名+过期，返回 Claims。
// 任何失败（过期/篡改/格式错/签名方法不符）均返回 error。
func (s *JWTSigner) ParseToken(tokenStr string) (*Claims, error) {
	if !s.Available() {
		return nil, errors.New("jwt secret not configured")
	}
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		// 强制 HMAC，拒绝其他签名方法（防 alg=none / RS256 伪造）
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return s.secret, nil
	})
	if err != nil {
		return nil, err
	}
	return claims, nil
}
