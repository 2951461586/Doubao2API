package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"doubao2api/internal/doubao"
	"doubao2api/internal/registry"
	"doubao2api/internal/store"
)

// 请求来源：默认本机环回，非本机用 remote 显式指定。
const (
	loopbackAddr = "127.0.0.1:54321"
	remoteAddr   = "203.0.113.9:54321"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("打开状态文件失败: %v", err)
	}
	return New(st)
}

func newReq(method, path, body, remote string) *http.Request {
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	req.RemoteAddr = remote
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func serve(s *Server, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func addTestAccount(t *testing.T, s *Server, id, session string) *doubao.Account {
	t.Helper()
	a := &doubao.Account{
		ID: id, Name: "acct-" + id, Enabled: true, Status: "ok",
		DeviceID: "dev-" + id, FP: "verify_dev-" + id, WebTabID: "tab-" + id,
		Cookies: map[string]string{"sessionid": session},
	}
	s.Store.UpsertAccount(a)
	return a
}

// decode 把响应体解析为 map，便于断言 JSON 结构。
func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是合法 JSON: %v\n%s", err, rec.Body.String())
	}
	return out
}

// --- 无密钥模式 / 密钥鉴权 ---

func TestNoKeyModeLoopbackOnly(t *testing.T) {
	s := newTestServer(t)

	if rec := serve(s, newReq("GET", "/v1/models", "", loopbackAddr)); rec.Code != http.StatusOK {
		t.Fatalf("本机请求应放行，实际 %d: %s", rec.Code, rec.Body.String())
	}

	rec := serve(s, newReq("GET", "/v1/models", "", remoteAddr))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("无密钥模式下非本机请求应返回 403，实际 %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "allow_anonymous") {
		t.Errorf("拒绝信息应提示 allow_anonymous 逃生开关，实际: %s", body)
	}
	if !strings.Contains(body, "密钥") {
		t.Errorf("拒绝信息应提示去控制台创建密钥，实际: %s", body)
	}

	// 显式开启匿名后才放行非本机请求。
	s.Store.UpdateSettings(func(st *store.Settings) { st.AllowAnonymous = true })
	if rec := serve(s, newReq("GET", "/v1/models", "", remoteAddr)); rec.Code != http.StatusOK {
		t.Errorf("开启 allow_anonymous 后应放行，实际 %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAPIKeyEnforcement(t *testing.T) {
	s := newTestServer(t)
	k := s.Store.AddKey("test")

	cases := []struct {
		name   string
		header func(*http.Request)
		want   int
	}{
		{"无凭据", func(*http.Request) {}, http.StatusUnauthorized},
		{"Bearer 有效", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+k.Key)
		}, http.StatusOK},
		{"x-api-key 有效", func(r *http.Request) {
			r.Header.Set("x-api-key", k.Key)
		}, http.StatusOK},
		{"裸密钥", func(r *http.Request) {
			r.Header.Set("Authorization", k.Key)
		}, http.StatusOK},
		{"错误密钥", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer sk-wrong")
		}, http.StatusUnauthorized},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := newReq("GET", "/v1/models", "", loopbackAddr)
			c.header(req)
			if rec := serve(s, req); rec.Code != c.want {
				t.Errorf("状态码 = %d, want %d: %s", rec.Code, c.want, rec.Body.String())
			}
		})
	}

	// 签发密钥后，非本机来源也必须有密钥。
	if rec := serve(s, newReq("GET", "/v1/models", "", remoteAddr)); rec.Code != http.StatusUnauthorized {
		t.Errorf("远端无密钥应 401，实际 %d", rec.Code)
	}

	// 管理密码可代用为调用凭据（控制台便利）。
	s.Store.UpdateSettings(func(st *store.Settings) { st.AdminPassword = "pw-123" })
	req := newReq("GET", "/v1/models", "", loopbackAddr)
	req.Header.Set("Authorization", "Bearer pw-123")
	if rec := serve(s, req); rec.Code != http.StatusOK {
		t.Errorf("管理密码应可用于 /v1/*，实际 %d", rec.Code)
	}

	// 停用后即使带对密钥也拒绝。
	s.Store.ToggleKey(k.Key)
	req = newReq("GET", "/v1/models", "", loopbackAddr)
	req.Header.Set("Authorization", "Bearer "+k.Key)
	if rec := serve(s, req); rec.Code != http.StatusUnauthorized {
		t.Errorf("停用密钥应 401，实际 %d", rec.Code)
	}
}

func TestAdminAuth(t *testing.T) {
	s := newTestServer(t)

	if rec := serve(s, newReq("GET", "/admin/api/stats", "", loopbackAddr)); rec.Code != http.StatusOK {
		t.Fatalf("未设密码时本机应可访问，实际 %d", rec.Code)
	}
	if rec := serve(s, newReq("GET", "/admin/api/stats", "", remoteAddr)); rec.Code != http.StatusForbidden {
		t.Fatalf("未设密码时远端应 403，实际 %d", rec.Code)
	}

	s.Store.UpdateSettings(func(st *store.Settings) { st.AdminPassword = "s3cret" })
	if rec := serve(s, newReq("GET", "/admin/api/stats", "", remoteAddr)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("设密码后无凭据应 401，实际 %d", rec.Code)
	}
	if rec := serve(s, newReq("GET", "/admin/api/stats", "", loopbackAddr)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("设密码后本机也需要密码，实际 %d", rec.Code)
	}

	for name, set := range map[string]func(*http.Request){
		"X-Admin-Password": func(r *http.Request) { r.Header.Set("X-Admin-Password", "s3cret") },
		"Bearer":           func(r *http.Request) { r.Header.Set("Authorization", "Bearer s3cret") },
	} {
		req := newReq("GET", "/admin/api/stats", "", remoteAddr)
		set(req)
		if rec := serve(s, req); rec.Code != http.StatusOK {
			t.Errorf("%s 方式应通过，实际 %d", name, rec.Code)
		}
	}

	req := newReq("GET", "/admin/api/stats", "", remoteAddr)
	req.Header.Set("X-Admin-Password", "wrong")
	if rec := serve(s, req); rec.Code != http.StatusUnauthorized {
		t.Errorf("错误密码应 401，实际 %d", rec.Code)
	}
}

func TestAdminWriteSameOrigin(t *testing.T) {
	s := newTestServer(t)
	body := `{"log_keep":200}`

	// 非浏览器客户端（无 Origin）直接放行。
	req := newReq("POST", "/admin/api/settings", body, loopbackAddr)
	if rec := serve(s, req); rec.Code != http.StatusOK {
		t.Fatalf("无 Origin 的写操作应放行，实际 %d: %s", rec.Code, rec.Body.String())
	}

	// 同源放行。
	req = newReq("POST", "/admin/api/settings", body, loopbackAddr)
	req.Header.Set("Origin", "http://"+req.Host)
	if rec := serve(s, req); rec.Code != http.StatusOK {
		t.Errorf("同源写操作应放行，实际 %d: %s", rec.Code, rec.Body.String())
	}

	// 跨站阻断（CSRF）。
	req = newReq("POST", "/admin/api/settings", body, loopbackAddr)
	req.Header.Set("Origin", "http://evil.example")
	if rec := serve(s, req); rec.Code != http.StatusForbidden {
		t.Errorf("跨站写操作应 403，实际 %d: %s", rec.Code, rec.Body.String())
	}

	// 只读请求不做同源校验。
	req = newReq("GET", "/admin/api/stats", "", loopbackAddr)
	req.Header.Set("Origin", "http://evil.example")
	if rec := serve(s, req); rec.Code != http.StatusOK {
		t.Errorf("GET 不做同源校验，实际 %d", rec.Code)
	}
}

// --- CORS ---

func TestCORSOrigin(t *testing.T) {
	s := newTestServer(t)

	allowed := map[string]string{
		"http://127.0.0.1:5173":  "http://127.0.0.1:5173",
		"https://localhost":      "https://localhost",
		"http://[::1]:8080":      "http://[::1]:8080",
		"http://localhost:10086": "http://localhost:10086",
	}
	for origin, want := range allowed {
		if got := s.corsOrigin(origin); got != want {
			t.Errorf("corsOrigin(%q) = %q, want %q", origin, got, want)
		}
	}

	for _, origin := range []string{
		"", "null", "https://evil.example", "file://x", "%%%", "http://10.0.0.5:8080",
	} {
		if got := s.corsOrigin(origin); got != "" {
			t.Errorf("corsOrigin(%q) = %q, want 空（默认只放行本机）", origin, got)
		}
	}

	// 显式允许列表：命中即回显配置值，未命中不放行。
	s.Store.UpdateSettings(func(st *store.Settings) {
		st.CORSOrigin = "https://app.example, http://10.0.0.5:8080"
	})
	if got := s.corsOrigin("https://app.example"); got != "https://app.example" {
		t.Errorf("配置来源应放行，实际 %q", got)
	}
	if got := s.corsOrigin("https://APP.EXAMPLE"); got != "https://app.example" {
		t.Errorf("配置来源匹配应大小写不敏感，实际 %q", got)
	}
	if got := s.corsOrigin("http://10.0.0.5:8080"); got != "http://10.0.0.5:8080" {
		t.Errorf("配置来源应放行，实际 %q", got)
	}
	if got := s.corsOrigin("http://127.0.0.1:5173"); got != "" {
		t.Errorf("配置了允许列表后不应回落到本机默认，实际 %q", got)
	}

	// 通配符配置不生效。
	s.Store.UpdateSettings(func(st *store.Settings) { st.CORSOrigin = "*" })
	for _, origin := range []string{"https://evil.example", "http://127.0.0.1:5173"} {
		if got := s.corsOrigin(origin); got != "" {
			t.Errorf("通配符来源不应放行 %q，实际 %q", origin, got)
		}
	}
}

func TestWriteCORSHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	writeCORSHeaders(rec, "http://127.0.0.1:5173")
	h := rec.Header()
	if got := h.Get("Access-Control-Allow-Origin"); got != "http://127.0.0.1:5173" {
		t.Errorf("Allow-Origin = %q", got)
	}
	if got := h.Get("Vary"); got != "Origin" {
		t.Errorf("Vary = %q, want Origin", got)
	}
	if got := h.Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("不应发送 Allow-Credentials，实际 %q", got)
	}
	for _, name := range []string{"Authorization", "Content-Type", "x-api-key"} {
		if !strings.Contains(h.Get("Access-Control-Allow-Headers"), name) {
			t.Errorf("Allow-Headers 应包含 %s，实际 %q", name, h.Get("Access-Control-Allow-Headers"))
		}
	}

	for _, bad := range []string{"", "*", "http://*.example"} {
		rec := httptest.NewRecorder()
		writeCORSHeaders(rec, bad)
		if len(rec.Header()) != 0 {
			t.Errorf("%q 不应写入任何 CORS 头，实际 %v", bad, rec.Header())
		}
	}
}

func TestCORSPreflight(t *testing.T) {
	s := newTestServer(t)
	req := newReq("OPTIONS", "/v1/chat/completions", "", loopbackAddr)
	req.Header.Set("Origin", "http://127.0.0.1:5173")
	rec := serve(s, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("预检请求应返回 204，实际 %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://127.0.0.1:5173" {
		t.Errorf("预检应带上 Allow-Origin，实际 %q", got)
	}
}

// --- 基础端点 ---

func TestHandleModels(t *testing.T) {
	s := newTestServer(t)
	s.Store.UpdateSettings(func(st *store.Settings) {
		st.ModelAliases = map[string]string{"my-gpt": "doubao"}
	})
	body := decode(t, serve(s, newReq("GET", "/v1/models", "", loopbackAddr)))

	if body["object"] != "list" {
		t.Errorf("object = %v, want list", body["object"])
	}
	data, _ := body["data"].([]any)
	if len(data) < len(registry.Builtin) {
		t.Errorf("模型数量 = %d, 少于内置模型数 %d", len(data), len(registry.Builtin))
	}
	ids := map[string]bool{}
	for _, item := range data {
		m, _ := item.(map[string]any)
		id, _ := m["id"].(string)
		ids[id] = true
		if _, ok := m["doubao"].(map[string]any); !ok {
			t.Errorf("模型 %q 缺少 doubao 扩展字段", id)
		}
	}
	for _, want := range []string{"doubao", "doubao-think", "doubao-expert", "doubao-draw", "my-gpt"} {
		if !ids[want] {
			t.Errorf("模型清单缺少 %q", want)
		}
	}
}

func TestHandleHealth(t *testing.T) {
	s := newTestServer(t)
	if body := decode(t, serve(s, newReq("GET", "/health", "", loopbackAddr))); body["status"] != "ok" {
		t.Errorf("status = %v, want ok", body["status"])
	}

	addTestAccount(t, s, "a1", "sess-1")
	for _, path := range []string{"/health", "/healthz"} {
		body := decode(t, serve(s, newReq("GET", path, "", loopbackAddr)))
		if body["accounts"] != float64(1) || body["ready"] != float64(1) {
			t.Errorf("%s 账号统计不对: %v", path, body)
		}
		if _, ok := body["non_json_events"]; !ok {
			t.Errorf("%s 应暴露 non_json_events: %v", path, body)
		}
	}

	req := newReq("GET", "/ping", "", loopbackAddr)
	if rec := serve(s, req); rec.Body.String() != "pong" {
		t.Errorf("/ping = %q, want pong", rec.Body.String())
	}
}

func TestHandleRoot(t *testing.T) {
	s := newTestServer(t)

	rec := serve(s, newReq("GET", "/", "", loopbackAddr))
	if rec.Code != http.StatusOK {
		t.Fatalf("控制台应可访问，实际 %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q", ct)
	}
	for k, want := range map[string]string{
		"Cache-Control":          "no-store",
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if body := rec.Body.String(); !strings.Contains(body, "<html") {
		t.Error("控制台响应应为 HTML 单页")
	}

	rec = serve(s, newReq("GET", "/v1/not-exist", "", loopbackAddr))
	if rec.Code != http.StatusNotFound {
		t.Errorf("未知路径应 404，实际 %d", rec.Code)
	}
	if e, ok := decode(t, rec)["error"].(map[string]any); !ok || e["message"] == "" {
		t.Errorf("404 应返回统一错误结构，实际: %s", rec.Body.String())
	}
}

// --- 控制台 API ---

func TestAdminStateRedactsSecrets(t *testing.T) {
	s := newTestServer(t)
	const session = "abcdefghijklmnopqrstuvwxyz0123456789"
	addTestAccount(t, s, "a1", session)
	k := s.Store.AddKey("console")

	rec := serve(s, newReq("GET", "/admin/api/state", "", loopbackAddr))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态接口失败: %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, session) {
		t.Error("状态接口绝不能下发明文会话 Cookie")
	}
	if strings.Contains(body, k.Key) {
		t.Error("状态接口绝不能下发明文 API 密钥")
	}

	out := decode(t, rec)
	accts, _ := out["accounts"].([]any)
	if len(accts) != 1 {
		t.Fatalf("账号数 = %d, want 1", len(accts))
	}
	acct, _ := accts[0].(map[string]any)
	if acct["cookie_count"] != float64(1) {
		t.Errorf("cookie_count = %v, want 1", acct["cookie_count"])
	}
	if acct["session"] != mask(session) {
		t.Errorf("session 应脱敏为 %q，实际 %v", mask(session), acct["session"])
	}

	settings, _ := out["settings"].(map[string]any)
	if _, leaked := settings["admin_password"]; leaked {
		t.Error("设置里不应下发管理密码字段")
	}
	if _, ok := settings["admin_password_set"]; !ok {
		t.Error("应下发 admin_password_set 标记")
	}
	if _, ok := settings["allow_anonymous"]; !ok {
		t.Error("应下发 allow_anonymous，供控制台展示")
	}
}

func TestAdminKeysCRUD(t *testing.T) {
	s := newTestServer(t)

	rec := serve(s, newReq("POST", "/admin/api/keys", `{"name":"ci"}`, loopbackAddr))
	if rec.Code != http.StatusOK {
		t.Fatalf("创建密钥失败: %d: %s", rec.Code, rec.Body.String())
	}
	full, _ := decode(t, rec)["key"].(string)
	if !strings.HasPrefix(full, "sk-") {
		t.Fatalf("密钥格式不对: %q", full)
	}

	// 列表只回脱敏值。
	rec = serve(s, newReq("GET", "/admin/api/keys", "", loopbackAddr))
	if body := rec.Body.String(); strings.Contains(body, full) {
		t.Error("列表接口不应回传完整密钥")
	}
	keys, _ := decode(t, rec)["keys"].([]any)
	if len(keys) != 1 || keys[0].(map[string]any)["key"] != mask(full) {
		t.Errorf("列表应返回脱敏密钥 %q，实际 %v", mask(full), keys)
	}

	// 删除：支持用完整密钥或脱敏值定位。
	rec = serve(s, newReq("DELETE", "/admin/api/keys?key="+mask(full), "", loopbackAddr))
	if rec.Code != http.StatusOK {
		t.Fatalf("删除失败: %d", rec.Code)
	}
	if s.Store.HasKeys() {
		t.Error("删除后不应再有密钥")
	}
}

func TestAdminSettingsUpdate(t *testing.T) {
	s := newTestServer(t)

	body := `{"allow_anonymous":true,"log_keep":20,"cors_origin":"http://127.0.0.1:3000","admin_password":"  pw  "}`
	rec := serve(s, newReq("POST", "/admin/api/settings", body, loopbackAddr))
	if rec.Code != http.StatusOK {
		t.Fatalf("更新设置失败: %d: %s", rec.Code, rec.Body.String())
	}
	st := s.Store.Settings()
	if !st.AllowAnonymous {
		t.Error("allow_anonymous 应被写入")
	}
	if st.LogKeep != 20 || st.CORSOrigin != "http://127.0.0.1:3000" {
		t.Errorf("设置未更新: %+v", st)
	}
	if st.AdminPassword != "pw" {
		t.Errorf("管理密码应去除首尾空格，实际 %q", st.AdminPassword)
	}

	// GET 返回脱敏设置。
	rec = serve(s, newReq("GET", "/admin/api/settings", "", loopbackAddr))
	// 设置了密码后需要带密码。
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("设置密码后 GET 也应鉴权，实际 %d", rec.Code)
	}

	req := newReq("GET", "/admin/api/settings", "", loopbackAddr)
	req.Header.Set("X-Admin-Password", "pw")
	rec = serve(s, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("带密码应可读取设置，实际 %d", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, `"pw"`) {
		t.Error("设置接口不应下发明文密码")
	}
}

func TestAdminAccountsManualAndRemove(t *testing.T) {
	s := newTestServer(t)
	cookies := "sessionid=abc123456789; ttwid=tt-1; passport_csrf_token=csrf-1"

	rec := serve(s, newReq("POST", "/admin/api/accounts",
		`{"action":"manual","name":"手工","cookies":"`+cookies+`","device_id":"dev-9"}`, loopbackAddr))
	if rec.Code != http.StatusOK {
		t.Fatalf("手工录入失败: %d: %s", rec.Code, rec.Body.String())
	}
	out := decode(t, rec)
	if out["new"] != true {
		t.Errorf("首次录入应标记 new=true，实际 %v", out["new"])
	}
	id, _ := out["id"].(string)
	a := s.Store.Account(id)
	if a == nil {
		t.Fatal("账号未写入")
	}
	if a.Source != "manual" || a.FP != "verify_dev-9" || a.Cookies["sessionid"] != "abc123456789" {
		t.Errorf("账号字段不对: %+v", a)
	}

	// 同 sessionid 再次录入应为更新。
	out = decode(t, serve(s, newReq("POST", "/admin/api/accounts",
		`{"action":"manual","name":"改名","cookies":"`+cookies+`"}`, loopbackAddr)))
	if out["new"] != false {
		t.Error("重复录入应标记 new=false")
	}
	if got := len(s.Store.Accounts()); got != 1 {
		t.Errorf("账号数 = %d, want 1（按 sessionid 去重）", got)
	}

	// 删除。
	out = decode(t, serve(s, newReq("POST", "/admin/api/accounts",
		`{"action":"remove","id":"`+id+`"}`, loopbackAddr)))
	if out["ok"] != true {
		t.Errorf("删除应返回 ok=true，实际 %v", out)
	}
	if len(s.Store.Accounts()) != 0 {
		t.Error("账号未被删除")
	}

	// 未知操作。
	rec = serve(s, newReq("POST", "/admin/api/accounts", `{"action":"nope"}`, loopbackAddr))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("未知操作应 400，实际 %d", rec.Code)
	}

	// 错误方法。
	rec = serve(s, newReq("GET", "/admin/api/accounts", "", loopbackAddr))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET 应 405，实际 %d", rec.Code)
	}
}

func TestAdminLogsAndStats(t *testing.T) {
	s := newTestServer(t)
	s.Store.Record(store.LogEntry{Status: 200, Model: "doubao"}, 10)
	s.Store.Record(store.LogEntry{Status: 502, Err: "boom"}, 0)

	out := decode(t, serve(s, newReq("GET", "/admin/api/logs?limit=1", "", loopbackAddr)))
	logs, _ := out["logs"].([]any)
	if len(logs) != 1 {
		t.Fatalf("limit 未生效，实际 %d 条", len(logs))
	}
	if logs[0].(map[string]any)["status"] != float64(502) {
		t.Errorf("应返回最新一条日志，实际 %v", logs[0])
	}

	stats := decode(t, serve(s, newReq("GET", "/admin/api/stats", "", loopbackAddr)))
	if stats["total_requests"] != float64(2) || stats["failed_requests"] != float64(1) {
		t.Errorf("统计不对: %v", stats)
	}

	serve(s, newReq("DELETE", "/admin/api/logs", "", loopbackAddr))
	if got := len(s.Store.Logs(0)); got != 0 {
		t.Errorf("清空日志后仍有 %d 条", got)
	}
	serve(s, newReq("DELETE", "/admin/api/stats", "", loopbackAddr))
	if got := s.Store.Stats().TotalRequests; got != 0 {
		t.Errorf("重置统计后 TotalRequests = %d", got)
	}
}

// --- 工具函数 ---

func TestMask(t *testing.T) {
	cases := map[string]string{
		"":                       "",
		"a":                      "a***",
		"sk-1234567890":          "sk-123...7890",
		"sk-12345678901":         "sk-123...8901",
		"sk-abcdefghijklmnopqrs": "sk-abc...pqrs",
	}
	for in, want := range cases {
		if got := mask(in); got != want {
			t.Errorf("mask(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSecureEqual(t *testing.T) {
	if !secureEqual("abc", "abc") {
		t.Error("相同字符串应相等")
	}
	if secureEqual("abc", "abd") {
		t.Error("不同字符串不应相等")
	}
	if secureEqual("abc", "abc ") {
		t.Error("长度不同不应相等")
	}
	if !secureEqual("", "") {
		t.Error("空串应相等")
	}
}

func TestIsLoopbackAddr(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:1000":    true,
		"[::1]:1000":        true,
		"127.0.0.1":         true,
		"::1":               true,
		"192.168.1.10:1000": false,
		"203.0.113.9:80":    false,
		"":                  false,
		"not-an-ip:80":      false,
	}
	for in, want := range cases {
		if got := isLoopbackAddr(in); got != want {
			t.Errorf("isLoopbackAddr(%q) = %v, want %v", in, got, want)
		}
	}
	// 空 RemoteAddr 视为非本机（默认拒绝）。
	if isLoopbackAddr("") {
		t.Error("空来源必须按非本机处理")
	}
}

func TestExtractKey(t *testing.T) {
	cases := []struct {
		header, value, want string
	}{
		{"Authorization", "Bearer sk-1", "sk-1"},
		{"Authorization", "sk-2", "sk-2"},
		{"x-api-key", "sk-3", "sk-3"},
	}
	for _, c := range cases {
		req := httptest.NewRequest("GET", "/v1/models", nil)
		req.Header.Set(c.header, c.value)
		if got := extractKey(req); got != c.want {
			t.Errorf("%s: extractKey() = %q, want %q", c.header, got, c.want)
		}
	}
	if got := extractKey(httptest.NewRequest("GET", "/v1/models", nil)); got != "" {
		t.Errorf("无凭据时 extractKey() = %q, want 空", got)
	}

	// Authorization 优先于 x-api-key。
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-auth")
	req.Header.Set("x-api-key", "sk-x")
	if got := extractKey(req); got != "sk-auth" {
		t.Errorf("应优先 Authorization，实际 %q", got)
	}
}

func TestResolveKeyRef(t *testing.T) {
	s := newTestServer(t)
	k := s.Store.AddKey("ref")

	if got := s.resolveKeyRef(k.Key); got != k.Key {
		t.Errorf("完整密钥应解析成功，实际 %q", got)
	}
	if got := s.resolveKeyRef(mask(k.Key)); got != k.Key {
		t.Errorf("脱敏密钥应解析成功，实际 %q", got)
	}
	if got := s.resolveKeyRef("sk-nope"); got != "sk-nope" {
		t.Errorf("未知引用应原样透传（删除 / 停用时自然成为空操作），实际 %q", got)
	}
}

func TestStatusOf(t *testing.T) {
	if got := statusOf(nil); got != http.StatusOK {
		t.Errorf("statusOf(nil) = %d, want 200", got)
	}
	if got := statusOf(io.EOF); got != http.StatusBadGateway {
		t.Errorf("statusOf(err) = %d, want 502", got)
	}
}
