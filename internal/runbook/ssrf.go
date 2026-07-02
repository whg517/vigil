// ssrf.go Runbook 执行器 SSRF 防护（SEC-03）。
//
// Runbook 的 HTTP 执行器（HTTPExecutor / InternalExecutor.checkHTTP）会向
// target.Endpoint 发请求。若不校验，有 runbook 配置权限的用户可构造 Endpoint
// 指向云元数据（169.254.169.254）、内网服务（localhost/10.x/172.16.x/192.168.x）、
// file:// 等实现 SSRF。
//
// validateEndpoint 在发请求前校验目标 URL：
//   - 只允许 http/https scheme（禁 file/data/gopher/dict 等）
//   - 禁止指向私网/保留地址（loopback/私网/链路本地/云元数据）
//   - 解析 DNS 后再校验 IP（防 DNS rebinding 与域名指向内网）
//
// 设计权衡：默认拒绝私网，可通过 AllowPrivateEndpoints 选项放开（如本地开发/同集群调用）。
package runbook

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ErrSSRFBlocked endpoint 未通过 SSRF 校验。
var ErrSSRFBlocked = errors.New("endpoint blocked by SSRF protection")

// ssrfValidator 共享校验器（默认禁私网）。HTTPExecutor/InternalExecutor 复用。
var ssrfValidator = &endpointValidator{allowPrivate: false}

// validateEndpoint 校验 endpoint URL 是否安全（防 SSRF）。
// 返回 nil 表示安全可请求；ErrSSRFBlocked 表示被拒。
func validateEndpoint(endpoint string) error {
	return ssrfValidator.validate(endpoint)
}

// endpointValidator endpoint SSRF 校验器。
type endpointValidator struct {
	allowPrivate bool // 是否允许私网/loopback（本地开发场景可放开）
}

// validate 校验单个 endpoint URL。
func (v *endpointValidator) validate(endpoint string) error {
	if endpoint == "" {
		return fmt.Errorf("empty endpoint")
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("parse endpoint: %w", err)
	}
	// 1. scheme 白名单
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("%w: scheme %q not allowed (only http/https)", ErrSSRFBlocked, scheme)
	}
	// 2. 解析 host（可能是域名或 IP）
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: empty host", ErrSSRFBlocked)
	}
	// 3. 解析 DNS（若 host 是域名）拿到所有 IP，逐一校验
	ips, err := net.LookupIP(host)
	if err != nil {
		// DNS 解析失败：可能是 IP 字面量，直接解析
		ip := net.ParseIP(host)
		if ip == nil {
			return fmt.Errorf("%w: cannot resolve host %q", ErrSSRFBlocked, host)
		}
		ips = []net.IP{ip}
	}
	if v.allowPrivate {
		return nil // 开发模式放行
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return fmt.Errorf("%w: host %q resolves to private/reserved address %s", ErrSSRFBlocked, host, ip)
		}
	}
	return nil
}

// isBlockedIP 判断 IP 是否属于禁止访问的私网/保留地址段。
// 覆盖：
//   - loopback：127.0.0.0/8、::1
//   - 私网：10.0.0.0/8、172.16.0.0/12、192.168.0.0/16、fc00::/7（IPv6 私网）
//   - 链路本地：169.254.0.0/16（含云元数据 169.254.169.254）、fe80::/10
//   - 未指定：0.0.0.0、::
func isBlockedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return true
	}
	return false
}
