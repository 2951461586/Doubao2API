package server

import (
	"doubao2api/internal/doubao"
	"doubao2api/internal/registry"
	"doubao2api/internal/store"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// sanitizedSettings 返回可下发给前端的设置（管理密码不外泄）。
func sanitizedSettings(st store.Settings) map[string]any {
	return map[string]any{
		"model_aliases":              st.ModelAliases,
		"request_timeout_sec":        st.RequestTimeout,
		"overall_timeout_sec":        st.OverallTimeout,
		"max_retries":                st.MaxRetries,
		"log_keep":                   st.LogKeep,
		"cors_origin":                st.CORSOrigin,
		"admin_password_set":         st.AdminPasswordHash != "",
		"trusted_proxies":            st.TrustedProxies,
		"admin_loopback_only":        st.AdminLoopbackOnly,
		"session_warn_days":          st.SessionWarnDays,
		"max_concurrent_per_account": st.MaxConcurrentPerAccount,
		"rate_limit_per_key":         st.RateLimitPerKey,
		"tools_policy":               st.ToolsPolicy,
		"auto_checkin":               st.AutoCheckin,
		"auto_claim_tasks":           st.AutoClaimTasks,
		"auto_do_tasks":              st.AutoDoTasks,
		"retry_pending_checkin":      st.RetryPendingCheckin,
		"auto_task_limit":            st.AutoTaskLimit,
		"allow_anonymous":            st.AllowAnonymous,
	}
}

// resolveKeyRef 把 ID / 展示片段 / 明文密钥解析为密钥 ID。
//
// 删除、启停都按 ID 进行：状态文件里已没有明文密钥，控制台也只拿到展示片段。
func (s *Server) resolveKeyRef(ref string) string {
	if k := s.Store.KeyByRef(ref); k != nil {
		return k.ID
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

// --- 控制台 API ---

func (s *Server) handleAdminState(w http.ResponseWriter, r *http.Request) {
	accts := s.Store.Accounts()
	// 脱敏：只暴露会话标识与状态，不回传完整 Cookie
	safe := make([]map[string]any, 0, len(accts))
	now := time.Now()
	for _, a := range accts {
		entry := map[string]any{
			"id": a.ID, "name": a.Name, "source": a.Source,
			"session": mask(a.SessionID()), "enabled": a.Enabled,
			"status": a.Status, "note": a.Note, "last_err": a.LastErr,
			"failures": a.Failures, "last_used": a.LastUsed,
			"device_id": a.DeviceID, "cookie_count": len(a.Cookies),
			"iid": a.IID, "channel": a.ChannelValue(),
			"app_version": a.PCVersionValue(), "web_tab_id": a.WebTabID,
			"last_checkin_date": a.LastCheckinDate, "last_checkin_at": a.LastCheckinAt,
			"last_checkin_reward": a.LastCheckinReward,
			"last_task_date":      a.LastTaskDate, "last_task_count": a.LastTaskCount,
			"pending_checkin_date":  a.PendingCheckinDate,
			"pending_checkin_at":    a.PendingCheckinAt,
			"pending_checkin_tries": a.PendingCheckinTries,
		}
		// 凭据到期：控制台据此显示「剩余 N 天」，避免 issues.md #4 那种被动过期。
		if exp, ok := a.SessionExpiry(); ok {
			entry["session_expires_at"] = exp.Unix()
			if days, ok := a.DaysUntilExpiry(now); ok {
				entry["session_days_left"] = days
			}
		}
		if a.CooldownUntil > 0 {
			entry["cooldown_until"] = a.CooldownUntil
		}
		safe = append(safe, entry)
	}
	keys := s.Store.Keys()
	safeKeys := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		safeKeys = append(safeKeys, map[string]any{
			"id": k.ID, "key": k.Display, "name": k.Name, "enabled": k.Enabled,
			"created_at": k.CreatedAt, "last_used": k.LastUsed, "requests": k.Requests,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"accounts": safe,
		"keys":     safeKeys,
		"models":   registry.List(s.Store.Settings().ModelAliases),
		"settings": sanitizedSettings(s.Store.Settings()),
		"stats":    s.Store.Stats(),
		"meta":     s.Store.Meta(),
		"backups":  s.backupFiles(5),
	})
}

func (s *Server) handleAdminAccounts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST", "invalid_request_error")
		return
	}
	var body struct {
		Action   string `json:"action"`
		ID       string `json:"id"`
		Dir      string `json:"dir"`
		Cookies  string `json:"cookies"`
		Name     string `json:"name"`
		DeviceID string `json:"device_id"`
		WebID    string `json:"web_id"`
		IID      string `json:"iid"`
		AppID    string `json:"app_id"`
		Channel  string `json:"channel"`
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
		// 可选：用真实设备指纹覆盖随机占位值（推荐，降低风控概率）。
		if body.DeviceID != "" {
			acct.DeviceID = body.DeviceID
			acct.FP = "verify_" + body.DeviceID
		}
		if body.WebID != "" {
			acct.WebID = body.WebID
		}
		if body.IID != "" {
			acct.IID = body.IID
		}
		if body.AppID != "" {
			acct.AppID = body.AppID
		}
		if body.Channel != "" {
			acct.Channel = body.Channel
		}
		isNew := s.Store.UpsertAccount(acct)
		_ = s.Store.Save()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "new": isNew, "id": acct.ID})
	case "remove":
		ok := s.Store.RemoveAccount(body.ID)
		_ = s.Store.Save()
		writeJSON(w, http.StatusOK, map[string]any{"ok": ok})
	case "toggle":
		enabled, ok := s.Store.ToggleAccount(body.ID)
		if ok {
			_ = s.Store.Save()
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "enabled": enabled})
	case "clear-errors":
		ok := s.Store.ClearAccountErrors(body.ID)
		if ok {
			_ = s.Store.Save()
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": ok})
	default:
		writeErr(w, http.StatusBadRequest, "未知操作: "+body.Action, "invalid_request_error")
	}
}

func (s *Server) handleAdminKeys(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// 状态文件里只有指纹：列表只展示展示片段（sk-abc123…wxyz）与 ID。
		keys := s.Store.Keys()
		safe := make([]map[string]any, 0, len(keys))
		for _, k := range keys {
			safe = append(safe, map[string]any{
				"id": k.ID, "key": k.Display, "name": k.Name, "enabled": k.Enabled,
				"created_at": k.CreatedAt, "last_used": k.LastUsed, "requests": k.Requests,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"keys": safe})
	case http.MethodPost:
		var body struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
		k, plain := s.Store.AddKey(body.Name)
		_ = s.Store.Save()
		// 明文只在这里返回一次，之后只能通过重新签发获得。
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "key": plain, "id": k.ID, "display": k.Display,
		})
	case http.MethodDelete:
		q := r.URL.Query()
		if ref := q.Get("id"); ref != "" {
			_ = s.Store.DeleteKey(s.resolveKeyRef(ref))
		}
		if ref := q.Get("toggle"); ref != "" {
			_ = s.Store.ToggleKey(s.resolveKeyRef(ref))
		}
		// 兼容旧控制台的 ?key=<展示片段> 写法。
		if ref := q.Get("key"); ref != "" {
			_ = s.Store.DeleteKey(s.resolveKeyRef(ref))
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
		ModelAliases            map[string]string `json:"model_aliases"`
		RequestTimeout          *int              `json:"request_timeout_sec"`
		OverallTimeout          *int              `json:"overall_timeout_sec"`
		MaxRetries              *int              `json:"max_retries"`
		LogKeep                 *int              `json:"log_keep"`
		CORSOrigin              *string           `json:"cors_origin"`
		AdminPassword           *string           `json:"admin_password"`
		TrustedProxies          *string           `json:"trusted_proxies"`
		AdminLoopbackOnly       *bool             `json:"admin_loopback_only"`
		SessionWarnDays         *int              `json:"session_warn_days"`
		MaxConcurrentPerAccount *int              `json:"max_concurrent_per_account"`
		RateLimitPerKey         *int              `json:"rate_limit_per_key"`
		ToolsPolicy             *string           `json:"tools_policy"`
		AutoCheckin             *bool             `json:"auto_checkin"`
		AutoClaimTasks          *bool             `json:"auto_claim_tasks"`
		AutoDoTasks             *bool             `json:"auto_do_tasks"`
		RetryPendingCheckin     *bool             `json:"retry_pending_checkin"`
		AutoTaskLimit           *int              `json:"auto_task_limit"`
		AllowAnonymous          *bool             `json:"allow_anonymous"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败", "invalid_request_error")
		return
	}

	// 管理密码只保存哈希，因此不走 UpdateSettings 的通用路径。
	if body.AdminPassword != nil {
		if err := s.Store.SetAdminPassword(strings.TrimSpace(*body.AdminPassword)); err != nil {
			writeErr(w, http.StatusBadRequest, "设置管理密码失败: "+err.Error(), "invalid_request_error")
			return
		}
	}

	s.Store.UpdateSettings(func(st *store.Settings) {
		if body.ModelAliases != nil {
			st.ModelAliases = body.ModelAliases
		}
		if body.RequestTimeout != nil {
			st.RequestTimeout = *body.RequestTimeout
		}
		if body.OverallTimeout != nil {
			st.OverallTimeout = *body.OverallTimeout
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
		if body.TrustedProxies != nil {
			st.TrustedProxies = strings.TrimSpace(*body.TrustedProxies)
		}
		if body.AdminLoopbackOnly != nil {
			st.AdminLoopbackOnly = *body.AdminLoopbackOnly
		}
		if body.SessionWarnDays != nil {
			st.SessionWarnDays = *body.SessionWarnDays
		}
		if body.MaxConcurrentPerAccount != nil {
			st.MaxConcurrentPerAccount = *body.MaxConcurrentPerAccount
		}
		if body.RateLimitPerKey != nil {
			st.RateLimitPerKey = *body.RateLimitPerKey
		}
		if body.ToolsPolicy != nil {
			st.ToolsPolicy = *body.ToolsPolicy
		}
		if body.AutoCheckin != nil {
			st.AutoCheckin = *body.AutoCheckin
		}
		if body.AutoClaimTasks != nil {
			st.AutoClaimTasks = *body.AutoClaimTasks
		}
		if body.AutoDoTasks != nil {
			st.AutoDoTasks = *body.AutoDoTasks
		}
		if body.RetryPendingCheckin != nil {
			st.RetryPendingCheckin = *body.RetryPendingCheckin
		}
		if body.AutoTaskLimit != nil {
			st.AutoTaskLimit = *body.AutoTaskLimit
		}
		if body.AllowAnonymous != nil {
			st.AllowAnonymous = *body.AllowAnonymous
		}
	})
	_ = s.Store.Save()
	s.TriggerAutoRewards()
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
