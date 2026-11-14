package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"doubao2api/internal/store"
)

// --- 反向代理 / 可信来源 ---

func TestIsLocalRejectsUntrustedProxyHeaders(t *testing.T) {
	s := newTestServer(t)

	// 本机直连：算本机。
	if !s.IsLocal(newReq("GET", "/", "", loopbackAddr)) {
		t.Fatal("本机直连应判定为本机")
	}

	// 本机反代但未声明可信：RemoteAddr 是 127.0.0.1，但仍不能算本机。
	req := newReq("GET", "/", "", loopbackAddr)
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	if s.IsLocal(req) {
		t.Error("携带代理头且未配置可信网段时，必须按远程处理")
	}

	// 声明可信代理后，来源 IP 取自 XFF。
	s.Store.UpdateSettings(func(st *store.Settings) { st.TrustedProxies = "127.0.0.1/32" })
	if got := s.ClientIP(req).String(); got != "203.0.113.9" {
		t.Errorf("ClientIP() = %q, want 203.0.113.9", got)
	}
	if s.IsLocal(req) {
		t.Error("采信 XFF 后来源是公网地址，不应算本机")
	}

	// XFF 里是本机地址时才算本机。
	req = newReq("GET", "/", "", loopbackAddr)
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	if !s.IsLocal(req) {
		t.Error("可信代理转发本机来源应判定为本机")
	}
}

func TestParsePrefixes(t *testing.T) {
	got := parsePrefixes("127.0.0.1/32, 10.0.0.0/8 ,::1, garbage")
	if len(got) != 3 {
		t.Fatalf("应解析出 3 个网段，实际 %d: %v", len(got), got)
	}
	if got := parsePrefixes(""); got != nil {
		t.Errorf("空配置应返回 nil，实际 %v", got)
	}
}

// 无密钥模式下，带代理头的本机请求不能白嫖。
func TestNoKeyModeBlockedForProxyRequests(t *testing.T) {
	s := newTestServer(t)
	req := newReq("GET", "/v1/models", "", loopbackAddr)
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	if rec := serve(s, req); rec.Code != http.StatusForbidden {
		t.Fatalf("代理来源在无密钥模式下应 403，实际 %d", rec.Code)
	}
}

// --- 日志脱敏 ---

func TestRedact(t *testing.T) {
	cases := []string{
		"sessionid=abcdef123456; ttwid=xyz",
		"Cookie: sid_guard=abc|123|456; uid_tt=u1",
		"Authorization=Bearer sk-secret",
		"prefix passport_csrf_token=SECRET-TOKEN and more",
		"正常错误：请求上游失败",
	}
	for _, in := range cases {
		out := redact(in)
		for _, leak := range []string{"abcdef123456", "abc|123|456", "sk-secret", "SECRET-TOKEN"} {
			if strings.Contains(in, leak) && strings.Contains(out, leak) {
				t.Errorf("redact(%q) 泄露了 %q: %q", in, leak, out)
			}
		}
		if strings.ContainsAny(out, "\n\r") {
			t.Errorf("redact 输出不应含换行: %q", out)
		}
	}
	// 非敏感文本保持原样。
	if got := redact("上游返回非流式响应 (HTTP 502)"); got != "上游返回非流式响应 (HTTP 502)" {
		t.Errorf("普通文本被改写: %q", got)
	}
}

// --- 按密钥限流 ---

func TestRateLimitPerKey(t *testing.T) {
	s := newTestServer(t)
	_, plain := s.Store.AddKey("limited")
	s.Store.UpdateSettings(func(st *store.Settings) { st.RateLimitPerKey = 2 })

	call := func() int {
		req := newReq("GET", "/v1/models", "", loopbackAddr)
		req.Header.Set("Authorization", "Bearer "+plain)
		return serve(s, req).Code
	}
	if c := call(); c != http.StatusOK {
		t.Fatalf("第 1 次应通过，实际 %d", c)
	}
	if c := call(); c != http.StatusOK {
		t.Fatalf("第 2 次应通过，实际 %d", c)
	}
	rec := httptest.NewRecorder()
	req := newReq("GET", "/v1/models", "", loopbackAddr)
	req.Header.Set("Authorization", "Bearer "+plain)
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("第 3 次应 429，实际 %d", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 应带 Retry-After")
	}
}

// --- 健康检查告警 ---

func TestHealthWarnings(t *testing.T) {
	s := newTestServer(t)

	// 没有任何账号、没有密钥、没有管理密码：应当给出可行动的告警。
	rec := serve(s, newReq("GET", "/health", "", loopbackAddr))
	out := decode(t, rec)
	codes := warningCodes(out)
	for _, want := range []string{"no_api_keys", "admin_disabled"} {
		if !codes[want] {
			t.Errorf("缺少告警 %s: %v", want, codes)
		}
	}
	if rec.Code != http.StatusOK {
		t.Errorf("/health 应始终 200，实际 %d", rec.Code)
	}
	if out["degraded"] != true {
		t.Errorf("有告警时 degraded 应为 true: %v", out)
	}
	if out["started_at"] == nil || out["booted_at"] == nil {
		t.Errorf("应下发 started_at / booted_at: %v", out)
	}

	// 没有可用账号时 /readyz 必须 503。
	if rec := serve(s, newReq("GET", "/readyz", "", loopbackAddr)); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("无账号时 /readyz 应 503，实际 %d", rec.Code)
	}
	addTestAccount(t, s, "a1", "s1")
	if rec := serve(s, newReq("GET", "/readyz", "", loopbackAddr)); rec.Code != http.StatusOK {
		t.Errorf("有可用账号时 /readyz 应 200，实际 %d", rec.Code)
	}
}

func TestHealthSessionExpiryWarning(t *testing.T) {
	s := newTestServer(t)
	a := addTestAccount(t, s, "a1", "s1")
	// sid_guard: session|<创建秒>|<TTL 秒>|<...>，构造一个 3 天后到期的会话。
	a.Cookies["sid_guard"] = "sess|" + itoa(unixDaysFromNow(0)) + "|259200|flag"
	s.Store.UpsertAccount(a)
	s.Store.UpdateSettings(func(st *store.Settings) { st.SessionWarnDays = 14 })

	out := decode(t, serve(s, newReq("GET", "/health", "", loopbackAddr)))
	if !warningCodes(out)["session_expiring"] {
		t.Fatalf("3 天后到期应给出 session_expiring 告警: %v", out["warnings"])
	}
	if out["ready"] != float64(1) {
		t.Errorf("即将到期不影响 ready，实际 %v", out["ready"])
	}
}

func warningCodes(out map[string]any) map[string]bool {
	codes := map[string]bool{}
	list, _ := out["warnings"].([]any)
	for _, item := range list {
		m, _ := item.(map[string]any)
		if code, ok := m["code"].(string); ok {
			codes[code] = true
		}
	}
	return codes
}

// --- 小工具 ---

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}

// unixDaysFromNow 返回 n 天之后的 Unix 秒（n=0 表示现在）。
func unixDaysFromNow(n int) int64 {
	return time.Now().Add(time.Duration(n) * 24 * time.Hour).Unix()
}
