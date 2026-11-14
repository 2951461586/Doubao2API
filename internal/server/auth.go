package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"doubao2api/internal/secret"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// withAuth 校验 Bearer 密钥。
//
// 未签发任何密钥时进入「无密钥模式」：仅放行本机直连请求
// （首次使用的便利），除非显式开启 allow_anonymous。
// 一旦签发了密钥，则强制校验，且不再接受管理密码作为替代凭据：
// 管理凭据只能进管理面，否则一次密码泄露就等于 API 泄露。
func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.Store.HasKeys() {
			if s.Store.Settings().AllowAnonymous || s.IsLocal(r) {
				next(w, r)
				return
			}
			writeErr(w, http.StatusForbidden,
				"尚未签发 API 密钥，非本机请求已拒绝；请在控制台创建密钥，"+
					"或在设置中显式开启 allow_anonymous", "invalid_request_error")
			return
		}

		key := extractKey(r)
		if key == "" {
			writeErr(w, http.StatusUnauthorized, "缺少 API 密钥", "invalid_request_error")
			return
		}
		// 按密钥限流放在校验之后：未通过校验的请求不消耗配额，也无法借此探测密钥。
		if !s.Store.ValidKey(key) {
			writeErr(w, http.StatusUnauthorized, "API 密钥无效或已停用", "invalid_request_error")
			return
		}
		if limit := s.Store.Settings().RateLimitPerKey; limit > 0 {
			if ok, retry := s.keyLimit.Allow("k:"+secret.HashKey(key), limit, time.Minute); !ok {
				w.Header().Set("Retry-After", retryAfterSeconds(retry))
				writeErr(w, http.StatusTooManyRequests, "该密钥请求过于频繁，请稍后再试", "rate_limited")
				return
			}
		}
		next(w, r)
	}
}

// withAdminAuth 保护控制台接口。
//
// 鉴权策略：
//   - 必须先设置管理密码，否则管理面一律拒绝（503）。
//     此前「未设密码则只允许本机」的退路在有反向代理时会被绕过，
//     因此不再提供这条退路。
//   - 密码走 PBKDF2 哈希校验；同一来源 5 分钟内失败 5 次即锁定（429）。
//   - admin_loopback_only 开启时（裸机默认）只接受本机直连。
//   - 写操作（非 GET/HEAD/OPTIONS）做同源校验，阻断跨站请求伪造。
func (s *Server) withAdminAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.Store.HasAdminPassword() {
			writeErr(w, http.StatusServiceUnavailable,
				"管理接口未启用：请先设置管理密码（DOUBAO_ADMIN_PASSWORD 或控制台）", "server_error")
			return
		}

		ip := s.clientIPString(r)
		if blocked, retry := s.adminFail.Exhausted(ip, adminMaxFailures); blocked {
			w.Header().Set("Retry-After", retryAfterSeconds(retry))
			writeErr(w, http.StatusTooManyRequests, "管理鉴权失败次数过多，请稍后再试", "rate_limited")
			return
		}
		if s.Store.Settings().AdminLoopbackOnly && !s.IsLocal(r) {
			writeErr(w, http.StatusForbidden,
				"管理接口仅允许本机直连（admin_loopback_only）；如需远程管理请关闭该选项", "invalid_request_error")
			return
		}
		if !s.Store.VerifyAdminPassword(adminPasswordFrom(r)) {
			if blocked, retry := s.adminFail.Fail(ip, adminMaxFailures, adminFailWindow); blocked {
				w.Header().Set("Retry-After", retryAfterSeconds(retry))
				writeErr(w, http.StatusTooManyRequests, "管理鉴权失败次数过多，请稍后再试", "rate_limited")
				return
			}
			writeErr(w, http.StatusUnauthorized, "管理密码缺失或错误", "invalid_request_error")
			return
		}
		s.adminFail.Reset(ip)

		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if !s.adminSameOrigin(r) {
				writeErr(w, http.StatusForbidden, "跨站请求被拒绝", "invalid_request_error")
				return
			}
		}
		next(w, r)
	}
}

// adminPasswordFrom 从请求头提取管理密码。
func adminPasswordFrom(r *http.Request) string {
	if v := r.Header.Get("X-Admin-Password"); v != "" {
		return v
	}
	if h := r.Header.Get("Authorization"); h != "" {
		if v, ok := strings.CutPrefix(h, "Bearer "); ok {
			return strings.TrimSpace(v)
		}
		return strings.TrimSpace(h)
	}
	return ""
}

// secureEqual 以恒定时间比较两个字符串（先哈希，避免长度泄露）。
func secureEqual(a, b string) bool {
	ha := sha256.Sum256([]byte(a))
	hb := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

// adminSameOrigin 校验写操作来源：与请求 Host 同源，或命中显式 CORS 允许列表。
//
// 非浏览器客户端（curl / SDK）通常不带 Origin 头，直接放行。
func (s *Server) adminSameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	if cfg := strings.TrimSpace(s.Store.Settings().CORSOrigin); cfg != "" {
		for _, entry := range strings.Split(cfg, ",") {
			entry = strings.TrimSpace(entry)
			if entry != "" && !hasWildcard(entry) && strings.EqualFold(entry, origin) {
				return true
			}
		}
	}
	return false
}

// isLoopbackAddr 判断 TCP 远端地址是否为本机环回。
func isLoopbackAddr(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
