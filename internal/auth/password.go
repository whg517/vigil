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

// ValidatePasswordStrength 校验新密码强度（QA 审计 C8 强制改密链路用）。
// 规则（保守，避免过度限制可用性）：
//   - 长度 ≥ 8
//   - 至少含两类字符：字母 / 数字 / 符号
//
// 返回不满足的规则说明（空串表示通过）。
func ValidatePasswordStrength(pw string) string {
	if len(pw) < 8 {
		return "password must be at least 8 characters"
	}
	hasLetter, hasDigit, hasSymbol := false, false, false
	for _, r := range pw {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			hasLetter = true
		case r >= '0' && r <= '9':
			hasDigit = true
		default:
			hasSymbol = true
		}
	}
	classes := 0
	for _, b := range []bool{hasLetter, hasDigit, hasSymbol} {
		if b {
			classes++
		}
	}
	if classes < 2 {
		return "password must contain at least two of: letters, digits, symbols"
	}
	return ""
}
