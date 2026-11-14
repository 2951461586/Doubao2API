package server

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
)

// 本文件回答一个安全问题：这次请求到底来自哪里？
//
// 仅仅看 r.RemoteAddr 是不够的。只要网关前面有任何反向代理
// （Tailscale Funnel / Serve、nginx、Caddy、宿主上的端口转发），
// RemoteAddr 都会变成 127.0.0.1，于是「仅本机」这道保护形同虚设。
//
// 规则（默认最严）：
//  1. 没有任何代理头 → 用 RemoteAddr；
//  2. 有代理头但 RemoteAddr 不在 trusted_proxies 内 → 一律视为「非本机」；
//  3. 有代理头且 RemoteAddr 是可信代理 → 采信最左的 X-Forwarded-For。
//
// 注意不采信 Forwarded 的解析（格式复杂、易被伪造），
// 只把它当作「存在代理头」的信号。

// trustCache 缓存解析后的可信代理网段，避免每请求都解析 CIDR 字符串。
type trustCache struct {
	mu   sync.Mutex
	cfg  string
	list []netip.Prefix
}

// trustedPrefixes 返回当前配置的可信代理网段。
func (s *Server) trustedPrefixes() []netip.Prefix {
	cfg := strings.TrimSpace(s.Store.Settings().TrustedProxies)
	s.trust.mu.Lock()
	defer s.trust.mu.Unlock()
	if s.trust.cfg == cfg {
		return s.trust.list
	}
	s.trust.cfg = cfg
	s.trust.list = parsePrefixes(cfg)
	return s.trust.list
}

// parsePrefixes 解析逗号分隔的 CIDR / 单 IP 列表。
//
// 非法项被忽略（配置错误不应该让整个网关起不来），
// 但调用方可以通过返回值长度判断是否全部生效。
func parsePrefixes(cfg string) []netip.Prefix {
	if cfg == "" {
		return nil
	}
	var out []netip.Prefix
	for _, raw := range strings.Split(cfg, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if strings.Contains(raw, "/") {
			if p, err := netip.ParsePrefix(raw); err == nil {
				out = append(out, p.Masked())
			}
			continue
		}
		if a, err := netip.ParseAddr(raw); err == nil {
			out = append(out, netip.PrefixFrom(a, a.BitLen()))
		}
	}
	return out
}

// proxyTrusted 报告 remote 是否在可信代理列表内。
func (s *Server) proxyTrusted(remote netip.Addr) bool {
	if !remote.IsValid() {
		return false
	}
	for _, p := range s.trustedPrefixes() {
		if p.Contains(remote) {
			return true
		}
	}
	return false
}

// HasProxyHeaders 报告请求是否携带任何反向代理来源头。
func HasProxyHeaders(r *http.Request) bool {
	return r.Header.Get("X-Forwarded-For") != "" ||
		r.Header.Get("X-Real-IP") != "" ||
		r.Header.Get("Forwarded") != "" ||
		r.Header.Get("X-Forwarded-Host") != ""
}

// ClientIP 返回本次请求的真实来源地址（不可用时返回零值）。
func (s *Server) ClientIP(r *http.Request) netip.Addr {
	remote := remoteAddrOf(r)
	if !HasProxyHeaders(r) || !s.proxyTrusted(remote) {
		return remote
	}
	if ip, ok := firstIP(r.Header.Get("X-Forwarded-For")); ok {
		return ip
	}
	if ip, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("X-Real-IP"))); err == nil {
		return ip
	}
	return remote
}

// IsLocal 判断请求是否等价于「本机直连」。
//
// 这是无密钥模式与管理面回环限制的唯一判据，因此必须保守：
// 只要来源经过代理（哪怕代理在本机），就不算本机。
func (s *Server) IsLocal(r *http.Request) bool {
	remote := remoteAddrOf(r)
	if !remote.IsValid() || !remote.IsLoopback() {
		return false
	}
	if HasProxyHeaders(r) && !s.proxyTrusted(remote) {
		// 本机反代但未声明可信 → 无法确认调用者，按远程处理。
		return false
	}
	return s.ClientIP(r).IsLoopback()
}

// remoteAddrOf 从 RemoteAddr 解析 IP，容忍缺失端口的形式。
func remoteAddrOf(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	host = strings.Trim(host, "[]")
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return ip.Unmap()
}

// firstIP 取 X-Forwarded-For 里最左侧的合法地址。
func firstIP(xff string) (netip.Addr, bool) {
	for _, part := range strings.Split(xff, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if ip, err := netip.ParseAddr(part); err == nil {
			return ip.Unmap(), true
		}
	}
	return netip.Addr{}, false
}

// clientIPString 返回来源 IP 的字符串形式（用于限流键与日志）。
func (s *Server) clientIPString(r *http.Request) string {
	if ip := s.ClientIP(r); ip.IsValid() {
		return ip.String()
	}
	return r.RemoteAddr
}
