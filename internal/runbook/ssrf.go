// ssrf.go Runbook 执行器 SSRF 防护（SEC-03，FIX-2 修正 DNS rebinding）。
//
// Runbook 的 HTTP 执行器（HTTPExecutor / InternalExecutor.checkHTTP）会向
// target.Endpoint 发请求。若不校验，有 runbook 配置权限的用户可构造 Endpoint
// 指向云元数据（169.254.169.254）、内网服务、file:// 等实现 SSRF。
//
// 防护分两层：
//
//  1. 静态校验（validateEndpoint，请求前）：scheme 白名单（仅 http/https）、
//     host 非空。快速拦截明显恶意 URL（file:///etc/passwd 等）的第一道关。
//
//  2. 连接时校验（safeDialer 的 Control 回调）：在 TCP 连接真正建立前，对实际
//     解析到的 IP 做私网/保留地址判定。★ 这是防 DNS rebinding 的关键 ——
//     早期实现（FIX-2 之前）在请求前 LookupIP 校验，但 http.Client.Do 会再次解析
//     DNS，两次解析之间攻击者可改 DNS 记录（rebinding）绕过校验。
//     Control 回调在连接栈最底层拿到真实 IP，无 TOCTOU 间隙。
//
// 设计权衡：默认拒绝私网，可通过 AllowPrivate 选项放开（本地开发/同集群调用）。
package runbook

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// ErrSSRFBlocked endpoint 未通过 SSRF 校验。
var ErrSSRFBlocked = fmt.Errorf("endpoint blocked by SSRF protection")

// validateEndpoint 静态校验 endpoint URL（第一道关：scheme/host）。
// IP 校验交给 safeDialer 的 Control（第二道关，防 rebinding）。
// 返回 nil 表示静态校验通过（不代表 IP 可达，IP 校验在连接时）。
func validateEndpoint(endpoint string) error {
	if endpoint == "" {
		return fmt.Errorf("empty endpoint")
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("parse endpoint: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("%w: scheme %q not allowed (only http/https)", ErrSSRFBlocked, scheme)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("%w: empty host", ErrSSRFBlocked)
	}
	return nil
}

// newHTTPClient 构造带 SSRF 防护的 http.Client（连接时校验真实 IP，防 rebinding）。
// allowPrivate=true 时放行私网（测试/同集群场景）；生产必须 false。
func newHTTPClient(allowPrivate bool) *http.Client {
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	if !allowPrivate {
		// Control 在每个网络连接真正建立前调用，address 是实际拨号目标
		// （形如 "1.2.3.4:80"）。此处校验解析后的真实 IP，无 rebinding 间隙。
		dialer.Control = func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				host = address // 无端口的兜底（理论上 dial 总带端口）
			}
			if ip := net.ParseIP(host); ip != nil && isBlockedIP(ip) {
				return fmt.Errorf("%w: dial to private/reserved address %s", ErrSSRFBlocked, ip)
			}
			return nil
		}
	}
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext: dialer.DialContext,
		},
	}
}

// isBlockedIP 判断 IP 是否属于禁止访问的私网/保留地址段。
// 覆盖：loopback、私网、链路本地（含云元数据 169.254.169.254）、未指定。
func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
}

// endpointValidator 兼容旧引用（内部不再用 LookupIP 预校验，交给 dialer）。
type endpointValidator struct{ allowPrivate bool }

func (v *endpointValidator) validate(endpoint string) error { return validateEndpoint(endpoint) }
