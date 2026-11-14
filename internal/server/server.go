// Package server 暴露 OpenAI 兼容的 HTTP 接口。
package server

import (
	"doubao2api/internal/doubao"
	"doubao2api/internal/store"
	"net/http"
	"time"
)

// Server 是网关 HTTP 服务。
type Server struct {
	Store *store.Store
	Up    *doubao.Client
	// Login 保存进行中的扫码登录会话（内存态）。
	Login *loginSessions

	// trust 缓存可信代理网段解析结果。
	trust trustCache
	// adminFail 是管理口令爆破防护（按来源 IP）。
	adminFail *limiter
	// keyLimit 是调用密钥的请求限流（按密钥指纹）。
	keyLimit *limiter
	// slots 是单账号并发闸门。
	slots *accountSlots
}

// 管理鉴权失败锁定参数。
const (
	adminMaxFailures = 5
	adminFailWindow  = 5 * time.Minute
)

// New 构造服务。
func New(st *store.Store) *Server {
	return &Server{
		Store:     st,
		Up:        doubao.NewClient(),
		Login:     newLoginSessions(),
		adminFail: newLimiter(),
		keyLimit:  newLimiter(),
		slots:     newAccountSlots(),
	}
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
	mux.HandleFunc("/readyz", s.handleReady)

	// 推理
	mux.HandleFunc("/v1/models", s.withAuth(s.handleModels))
	mux.HandleFunc("/v1/chat/completions", s.withAuth(s.handleChatCompletions))
	mux.HandleFunc("/v1/messages", s.withAuth(s.handleAnthropicMessages))
	mux.HandleFunc("/v1/images/generations", s.withAuth(s.handleImageGenerations))
	mux.HandleFunc("/v1/videos/generations", s.withAuth(s.handleVideoGenerations))
	mux.HandleFunc("/v1/music/audio", s.withAuth(s.handleMusicAudio))
	mux.HandleFunc("/v1/music/lyrics", s.withAuth(s.handleMusicLyrics))
	mux.HandleFunc("/v1/skills", s.withAuth(s.handleSkills))
	mux.HandleFunc("/v1/capabilities", s.withAuth(s.handleCapabilities))
	mux.HandleFunc("/v1/conversations", s.withAuth(s.handleConversations))
	mux.HandleFunc("/v1/conversations/", s.withAuth(s.handleConversations))

	// 积分 / 签到 / 激励任务
	mux.HandleFunc("/v1/credits", s.withAuth(s.handleCredits))
	mux.HandleFunc("/v1/credits/history", s.withAuth(s.handleCreditHistory))
	mux.HandleFunc("/v1/checkin", s.withAuth(s.handleCheckin))
	mux.HandleFunc("/v1/tasks", s.withAuth(s.handleTasks))
	mux.HandleFunc("/v1/tasks/claim", s.withAuth(s.handleTaskClaim))
	mux.HandleFunc("/v1/tasks/claim-completed", s.withAuth(s.handleTaskClaimCompleted))
	mux.HandleFunc("/v1/tasks/run", s.withAuth(s.handleTaskRun))

	// 控制台 API（统一走管理鉴权 + 同源校验）
	mux.HandleFunc("/admin/api/state", s.withAdminAuth(s.handleAdminState))
	mux.HandleFunc("/admin/api/accounts", s.withAdminAuth(s.handleAdminAccounts))
	mux.HandleFunc("/admin/api/keys", s.withAdminAuth(s.handleAdminKeys))
	mux.HandleFunc("/admin/api/logs", s.withAdminAuth(s.handleAdminLogs))
	mux.HandleFunc("/admin/api/stats", s.withAdminAuth(s.handleAdminStats))
	mux.HandleFunc("/admin/api/settings", s.withAdminAuth(s.handleAdminSettings))
	mux.HandleFunc("/admin/api/checkin", s.withAdminAuth(s.handleAdminCheckin))
	mux.HandleFunc("/admin/api/checkin/retry", s.withAdminAuth(s.handleAdminCheckinRetry))
	mux.HandleFunc("/admin/api/login/qrcode", s.withAdminAuth(s.handleLoginQRCode))
	mux.HandleFunc("/admin/api/login/qrcode/poll", s.withAdminAuth(s.handleLoginQRCodePoll))
	mux.HandleFunc("/admin/api/state/snapshot", s.withAdminAuth(s.handleAdminStateSnapshot))
	mux.HandleFunc("/admin/api/state/export", s.withAdminAuth(s.handleAdminStateExport))
	mux.HandleFunc("/admin/api/state/import", s.withAdminAuth(s.handleAdminStateImport))

	// 控制台图标：浏览器自动请求，无需鉴权。
	mux.HandleFunc("/favicon.ico", serveFavicon)
	mux.HandleFunc("/apple-touch-icon.png", serveAppleTouchIcon)

	mux.HandleFunc("/", s.handleRoot)

	return s.withAccessLog(s.withCORS(mux))
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
