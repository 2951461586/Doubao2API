package server

import (
	"net/http"
	"time"

	"doubao2api/internal/account"
	"doubao2api/internal/registry"
)

// 本文件集中健康检查与「可行动的告警」。
//
// 设计原则：
//   - /health 永远 200（进程活着），附带 warnings 供人看；
//   - /readyz 只回答「现在能不能服务」（有可用账号），不可用时 503；
//   - /ping 极简探针，不查任何状态。
//
// 这样负载均衡 / 容器健康检查不会因为「有个 Cookie 快过期」就把实例摘掉，
// 而人又能从同一个响应里看到需要处理的事。

// handleHealth 返回进程健康与实际告警。
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	accts := s.Store.Accounts()
	ready := 0
	for _, a := range accts {
		if a.Enabled && a.Status == "ok" {
			ready++
		}
	}
	warnings := s.healthWarnings(accts)
	meta := s.Store.Meta()
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"degraded": len(warnings) > 0,
		"accounts": len(accts),
		"ready":    ready,
		"models":   len(registry.Builtin),
		// 非 JSON SSE 事件数：持续增长说明上游协议漂移，
		// 这些事件会被跳过（详见 doubao.ParseStats）。
		"non_json_events":   s.Up.NonJSONEvents(),
		"started_at":        meta.CreatedAt,
		"booted_at":         meta.BootedAt,
		"session_warn_days": s.Store.Settings().SessionWarnDays,
		"warnings":          warnings,
	})
}

// handleReady 只回答「现在能否服务」。
//
// 没有可用账号时返回 503：此时任何 /v1/* 请求都只会失败，
// 继续以 200 上报健康会让上游一直打进来。
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	accts := s.Store.Accounts()
	ready := 0
	for _, a := range accts {
		if a.Enabled && a.Status == "ok" {
			ready++
		}
	}
	if ready == 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "unavailable", "ready": 0, "accounts": len(accts),
			"reason": "没有可用账号",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ready", "ready": ready, "accounts": len(accts),
	})
}

// healthWarnings 汇总当前需要人工处理的事项。
//
// 每条都是「看了就能动手」的：哪个账号、还剩几天、该怎么做。
func (s *Server) healthWarnings(accts []*account.Account) []map[string]any {
	now := time.Now()
	set := s.Store.Settings()
	out := make([]map[string]any, 0, 4)

	for _, a := range accts {
		if !a.Enabled {
			continue
		}
		if days, ok := a.DaysUntilExpiry(now); ok {
			exp, _ := a.SessionExpiry()
			switch {
			case days < 0:
				out = append(out, map[string]any{
					"code": "session_expired", "account_id": a.ID, "account": a.Name,
					"expires_at": exp.Unix(), "days_left": days,
					"message": "会话 Cookie 已过期，请重新登录并导入",
				})
			case days <= set.SessionWarnDays:
				out = append(out, map[string]any{
					"code": "session_expiring", "account_id": a.ID, "account": a.Name,
					"expires_at": exp.Unix(), "days_left": days,
					"message": "会话 Cookie 即将过期，请提前重新登录并导入",
				})
			}
		}
		if a.PendingCheckinDate != "" && a.PendingCheckinDate != todayString() {
			out = append(out, map[string]any{
				"code": "checkin_pending_stale", "account_id": a.ID, "account": a.Name,
				"date": a.PendingCheckinDate, "attempts": a.PendingCheckinTries,
				"message": "有跨天未确认的签到；开启 retry_pending_checkin 才会自动复查",
			})
		}
	}

	if !s.Store.HasKeys() {
		out = append(out, map[string]any{
			"code":    "no_api_keys",
			"message": "尚未签发 API 密钥：/v1/* 仅放行本机直连（或 allow_anonymous）",
		})
	}
	if !s.Store.HasAdminPassword() {
		out = append(out, map[string]any{
			"code":    "admin_disabled",
			"message": "未设置管理密码，管理接口当前拒绝全部请求",
		})
	} else if !set.AdminLoopbackOnly {
		out = append(out, map[string]any{
			"code":    "admin_remote_access",
			"message": "管理接口允许非本机访问：请确认前面是可信代理，并设置 trusted_proxies",
		})
	}
	if set.CORSOrigin == "" {
		out = append(out, map[string]any{
			"code":    "cors_same_origin_only",
			"message": "未配置 CORS 来源：浏览器跨域直连会被拦截（curl / SDK 不受影响）",
		})
	}
	return out
}
