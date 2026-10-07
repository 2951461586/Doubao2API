// Package server 暴露 OpenAI 兼容的 HTTP 接口。
package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"doubao2api/internal/doubao"
	"doubao2api/internal/registry"
	"doubao2api/internal/store"
)

// Server 是网关 HTTP 服务。
type Server struct {
	Store *store.Store
	Up    *doubao.Client
	// Login 保存进行中的扫码登录会话（内存态）。
	Login *loginSessions
}

// New 构造服务。
func New(st *store.Store) *Server {
	return &Server{Store: st, Up: doubao.NewClient(), Login: newLoginSessions()}
}

// Handler 返回完整路由。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// 探活
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("pong"))
	})
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/healthz", s.handleHealth)

	// 推理
	mux.HandleFunc("/v1/models", s.withAuth(s.handleModels))
	mux.HandleFunc("/v1/chat/completions", s.withAuth(s.handleChatCompletions))
	mux.HandleFunc("/v1/messages", s.withAuth(s.handleAnthropicMessages))
	mux.HandleFunc("/v1/images/generations", s.withAuth(s.handleImageGenerations))

	// 控制台 API（统一走管理鉴权 + 同源校验）
	mux.HandleFunc("/admin/api/state", s.withAdminAuth(s.handleAdminState))
	mux.HandleFunc("/admin/api/accounts", s.withAdminAuth(s.handleAdminAccounts))
	mux.HandleFunc("/admin/api/keys", s.withAdminAuth(s.handleAdminKeys))
	mux.HandleFunc("/admin/api/logs", s.withAdminAuth(s.handleAdminLogs))
	mux.HandleFunc("/admin/api/stats", s.withAdminAuth(s.handleAdminStats))
	mux.HandleFunc("/admin/api/settings", s.withAdminAuth(s.handleAdminSettings))
	mux.HandleFunc("/admin/api/checkin", s.withAdminAuth(s.handleAdminCheckin))
	mux.HandleFunc("/admin/api/login/qrcode", s.withAdminAuth(s.handleLoginQRCode))
	mux.HandleFunc("/admin/api/login/qrcode/poll", s.withAdminAuth(s.handleLoginQRCodePoll))

	mux.HandleFunc("/", s.handleRoot)

	return s.withCORS(mux)
}

// withCORS 处理跨域。
//
// 默认只放行本机来源（localhost / 127.0.0.1 / [::1]，任意端口）；
// 需局域网访问时在设置里显式列出逗号分隔的来源。不提供通配符。
func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowed := s.corsOrigin(r.Header.Get("Origin"))
		writeCORSHeaders(w, allowed)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// writeCORSHeaders 仅在来源通过校验时写入跨域响应头。
//
// allowed 为空串时不写任何 CORS 头（浏览器按同源策略拦截）；
// 含通配符的来源一律拒绝（本网关从不允许任意来源）；
// 从不发送 Access-Control-Allow-Credentials，因此不存在凭据泄露面。
func writeCORSHeaders(w http.ResponseWriter, allowed string) {
	if allowed == "" || hasWildcard(allowed) {
		return
	}
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", allowed)
	h.Set("Vary", "Origin")
	h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, x-api-key")
	h.Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
}

// corsOrigin 返回应当回给浏览器的 Allow-Origin 值，空串表示不放行。
//
// 不直接回显请求头：显式允许列表命中时返回值来自配置；
// 默认本机场景下由已校验的 scheme/host/port 重新拼接。
func (s *Server) corsOrigin(raw string) string {
	if raw == "" || raw == "null" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}

	// 1) 显式允许列表（逗号分隔，值来自控制台设置）
	if cfg := strings.TrimSpace(s.Store.Settings().CORSOrigin); cfg != "" {
		for _, entry := range strings.Split(cfg, ",") {
			entry = strings.TrimSpace(entry)
			if entry != "" && !hasWildcard(entry) && strings.EqualFold(entry, raw) {
				return entry
			}
		}
		return ""
	}

	// 2) 默认仅本机：主机名必须在环回集合内
	if !isLoopbackHost(u.Hostname()) {
		return ""
	}
	if port := u.Port(); port != "" {
		return u.Scheme + "://" + net.JoinHostPort(u.Hostname(), port)
	}
	return u.Scheme + "://" + u.Hostname()
}

// hasWildcard 报告字符串是否含通配符字符（用于拒绝任意来源）。
func hasWildcard(s string) bool { return strings.IndexByte(s, '*') >= 0 }

// isLoopbackHost 判断主机名是否为本机环回地址。
func isLoopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// withAuth 校验 Bearer 密钥。
//
// 未签发任何密钥时，允许无密钥访问（首次使用的便利）；
// 一旦签发了密钥，则强制校验。
func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.Store.HasKeys() {
			next(w, r)
			return
		}
		key := extractKey(r)
		if key == "" {
			writeErr(w, http.StatusUnauthorized, "缺少 API 密钥", "invalid_request_error")
			return
		}
		if !s.Store.ValidKey(key) {
			writeErr(w, http.StatusUnauthorized, "API 密钥无效或已停用", "invalid_request_error")
			return
		}
		next(w, r)
	}
}

// withAdminAuth 保护控制台接口。
//
// 鉴权策略：
//   - 已设置管理密码：必须通过 X-Admin-Password 或 Authorization: Bearer 提供；
//   - 未设置管理密码：仅允许来自本机环回的请求（避免局域网内裸奔）。
//
// 另外对写操作（非 GET/HEAD/OPTIONS）做同源校验，阻断跨站请求伪造。
func (s *Server) withAdminAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if pw := s.Store.Settings().AdminPassword; pw != "" {
			if !secureEqual(adminPasswordFrom(r), pw) {
				writeErr(w, http.StatusUnauthorized, "管理密码缺失或错误", "invalid_request_error")
				return
			}
		} else if !isLoopbackAddr(r.RemoteAddr) {
			writeErr(w, http.StatusForbidden, "未设置管理密码，管理接口仅允许本机访问", "invalid_request_error")
			return
		}

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

// sanitizedSettings 返回可下发给前端的设置（管理密码不外泄）。
func sanitizedSettings(st store.Settings) map[string]any {
	return map[string]any{
		"model_aliases":       st.ModelAliases,
		"request_timeout_sec": st.RequestTimeout,
		"max_retries":         st.MaxRetries,
		"log_keep":            st.LogKeep,
		"cors_origin":         st.CORSOrigin,
		"admin_password_set":  st.AdminPassword != "",
	}
}

// resolveKeyRef 把完整密钥或脱敏密钥解析为完整密钥。
func (s *Server) resolveKeyRef(ref string) string {
	for _, k := range s.Store.Keys() {
		if k.Key == ref || mask(k.Key) == ref {
			return k.Key
		}
	}
	return ref
}

func extractKey(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if v, ok := strings.CutPrefix(h, "Bearer "); ok {
			return strings.TrimSpace(v)
		}
		return strings.TrimSpace(h)
	}
	if v := r.Header.Get("x-api-key"); v != "" {
		return strings.TrimSpace(v)
	}
	return ""
}

// --- 基础端点 ---

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	accts := s.Store.Accounts()
	ok := 0
	for _, a := range accts {
		if a.Enabled && a.Status == "ok" {
			ok++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"accounts": len(accts),
		"ready":    ok,
		"models":   len(registry.Builtin),
	})
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	custom := s.Store.Settings().ModelAliases
	models := registry.List(custom)
	data := make([]map[string]any, 0, len(models))
	now := time.Now().Unix()
	for _, m := range models {
		data = append(data, map[string]any{
			"id":       m.ID,
			"object":   "model",
			"created":  now,
			"owned_by": "doubao",
			"doubao": map[string]any{
				"name":        m.Name,
				"think_level": m.ThinkLevel,
				"context":     m.Context,
				"description": m.Desc,
			},
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

// handleRoot 返回控制台单页；其余未知路径返回 404。
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeErr(w, http.StatusNotFound, "未找到该路径", "invalid_request_error")
		return
	}
	serveConsole(w, r)
}

// --- 控制台 API ---

func (s *Server) handleAdminState(w http.ResponseWriter, r *http.Request) {
	accts := s.Store.Accounts()
	// 脱敏：只暴露会话标识与状态，不回传完整 Cookie
	safe := make([]map[string]any, 0, len(accts))
	for _, a := range accts {
		safe = append(safe, map[string]any{
			"id": a.ID, "name": a.Name, "source": a.Source,
			"session": mask(a.SessionID()), "enabled": a.Enabled,
			"status": a.Status, "note": a.Note, "last_err": a.LastErr,
			"failures": a.Failures, "last_used": a.LastUsed,
			"device_id": a.DeviceID, "cookie_count": len(a.Cookies),
		})
	}
	keys := s.Store.Keys()
	safeKeys := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		safeKeys = append(safeKeys, map[string]any{
			"key": mask(k.Key), "name": k.Name, "enabled": k.Enabled,
			"created_at": k.CreatedAt, "last_used": k.LastUsed, "requests": k.Requests,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"accounts": safe,
		"keys":     safeKeys,
		"models":   registry.List(s.Store.Settings().ModelAliases),
		"settings": sanitizedSettings(s.Store.Settings()),
		"stats":    s.Store.Stats(),
	})
}

func (s *Server) handleAdminAccounts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST", "invalid_request_error")
		return
	}
	var body struct {
		Action  string `json:"action"`
		ID      string `json:"id"`
		Dir     string `json:"dir"`
		Cookies string `json:"cookies"`
		Name    string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败", "invalid_request_error")
		return
	}

	switch body.Action {
	case "import":
		results, err := doubao.ImportAllFromDesktop(body.Dir)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "导入失败: "+err.Error(), "invalid_request_error")
			return
		}
		imported := make([]map[string]any, 0, len(results))
		for _, res := range results {
			isNew := s.Store.UpsertAccount(res.Account)
			imported = append(imported, map[string]any{
				"id": res.Account.ID, "name": res.Account.Name,
				"profile": res.Profile, "dir": res.Dir,
				"cookies": res.Cookies, "new": isNew,
			})
		}
		_ = s.Store.Save()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": len(imported), "imported": imported})
	case "manual":
		acct, err := doubao.AccountFromCookieString(body.Cookies, body.Name)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
			return
		}
		acct.Source = "manual"
		isNew := s.Store.UpsertAccount(acct)
		_ = s.Store.Save()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "new": isNew, "id": acct.ID})
	case "remove":
		ok := s.Store.RemoveAccount(body.ID)
		_ = s.Store.Save()
		writeJSON(w, http.StatusOK, map[string]any{"ok": ok})
	case "toggle":
		if a := s.Store.Account(body.ID); a != nil {
			a.Enabled = !a.Enabled
			_ = s.Store.Save()
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "clear-errors":
		if a := s.Store.Account(body.ID); a != nil {
			a.Failures = 0
			a.Status = "ok"
			a.LastErr = ""
			_ = s.Store.Save()
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeErr(w, http.StatusBadRequest, "未知操作: "+body.Action, "invalid_request_error")
	}
}

func (s *Server) handleAdminKeys(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// 脱敏下发：完整密钥只在创建时返回一次
		keys := s.Store.Keys()
		safe := make([]map[string]any, 0, len(keys))
		for _, k := range keys {
			safe = append(safe, map[string]any{
				"key": mask(k.Key), "name": k.Name, "enabled": k.Enabled,
				"created_at": k.CreatedAt, "last_used": k.LastUsed, "requests": k.Requests,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"keys": safe})
	case http.MethodPost:
		var body struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		k := s.Store.AddKey(body.Name)
		_ = s.Store.Save()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "key": k.Key})
	case http.MethodDelete:
		q := r.URL.Query()
		if ref := q.Get("key"); ref != "" {
			_ = s.Store.DeleteKey(s.resolveKeyRef(ref))
		}
		if ref := q.Get("toggle"); ref != "" {
			_ = s.Store.ToggleKey(s.resolveKeyRef(ref))
		}
		_ = s.Store.Save()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "不支持的方法", "invalid_request_error")
	}
}

func (s *Server) handleAdminLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		s.Store.ClearLogs()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	writeJSON(w, http.StatusOK, map[string]any{"logs": s.Store.Logs(limit)})
}

func (s *Server) handleAdminStats(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		s.Store.ClearStats()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	writeJSON(w, http.StatusOK, s.Store.Stats())
}

func (s *Server) handleAdminSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusOK, sanitizedSettings(s.Store.Settings()))
		return
	}
	var body struct {
		ModelAliases   map[string]string `json:"model_aliases"`
		RequestTimeout *int              `json:"request_timeout_sec"`
		MaxRetries     *int              `json:"max_retries"`
		LogKeep        *int              `json:"log_keep"`
		CORSOrigin     *string           `json:"cors_origin"`
		AdminPassword  *string           `json:"admin_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败", "invalid_request_error")
		return
	}
	s.Store.UpdateSettings(func(st *store.Settings) {
		if body.ModelAliases != nil {
			st.ModelAliases = body.ModelAliases
		}
		if body.RequestTimeout != nil {
			st.RequestTimeout = *body.RequestTimeout
		}
		if body.MaxRetries != nil {
			st.MaxRetries = *body.MaxRetries
		}
		if body.LogKeep != nil {
			st.LogKeep = *body.LogKeep
		}
		if body.CORSOrigin != nil {
			st.CORSOrigin = *body.CORSOrigin
		}
		if body.AdminPassword != nil {
			st.AdminPassword = strings.TrimSpace(*body.AdminPassword)
		}
	})
	_ = s.Store.Save()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "settings": sanitizedSettings(s.Store.Settings())})
}

// handleAdminCheckin 对全部（或指定）账号做一次连通性探测。
func (s *Server) handleAdminCheckin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	targets := s.Store.Accounts()
	if body.ID != "" {
		targets = nil
		if a := s.Store.Account(body.ID); a != nil {
			targets = append(targets, a)
		}
	}
	results := make([]map[string]any, 0, len(targets))
	for _, a := range targets {
		ctx, cancel := contextWithTimeout(r, 60*time.Second)
		err := s.probeAccount(ctx, a)
		cancel()
		ok := err == nil
		note := ""
		if err != nil {
			note = err.Error()
		}
		s.Store.MarkAccountResult(a.ID, ok, note)
		results = append(results, map[string]any{
			"id": a.ID, "name": a.Name, "ok": ok, "err": note,
		})
	}
	_ = s.Store.Save()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "results": results})
}

// --- 工具函数 ---

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("写响应失败: %v", err)
	}
}

func writeErr(w http.ResponseWriter, code int, msg, typ string) {
	writeJSON(w, code, map[string]any{
		"error": map[string]any{"message": msg, "type": typ, "code": code},
	})
}

// mask 对敏感串做脱敏，仅保留首尾片段。
func mask(s string) string {
	if len(s) <= 10 {
		if s == "" {
			return ""
		}
		return s[:1] + "***"
	}
	return s[:6] + "..." + s[len(s)-4:]
}
