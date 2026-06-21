// password.go 密码哈希（bcrypt，能力域 13 登录态）。
//
// 仅用于本地账号密码登录链路；IM/SSO 绑定不经过这里。
// bcrypt 自带盐（每次哈希结果不同），cost 用 DefaultCost（12）兼顾安全与性能。
package auth

import "golang.org/x/crypto/bcrypt"

// HashPassword bcrypt 哈希密码。cost 用 bcrypt.DefaultCost。
// 哈希失败理论上极少发生（仅 cost 非法），此处简单返回空串由调用方判断。
func HashPassword(pw string) string {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return ""
	}
	return string(h)
}

// VerifyPassword 校验明文与哈希。
// 空 hash（未设密码的用户）一律拒绝，避免"无密码即放行"的绕过。
func VerifyPassword(pw, hash string) bool {
	if hash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}
