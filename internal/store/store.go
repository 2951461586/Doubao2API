// Package store 负责网关状态的持久化与账号池调度。
package store

import (
	"doubao2api/internal/account"
	"doubao2api/internal/ids"
	"doubao2api/internal/secret"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// schemaVersion 是状态文件的结构版本。
//
//	1  初版（管理密码与调用密钥明文落盘）
//	2  管理密码 PBKDF2 哈希、调用密钥只存指纹、新增安全 / 调度设置
const schemaVersion = 2

// APIKey 是网关对外签发的调用密钥。
//
// 状态文件只保存指纹（KeyHash）与展示用片段（Display），完整密钥仅在创建时
// 返回给调用方一次。此前版本把明文 Key 写进磁盘，一旦状态文件泄露即等同
// 泄露全部调用凭据。
type APIKey struct {
	// ID 是稳定句柄：控制台的启停 / 删除按 ID 定位，不再依赖密钥原文。
	ID string `json:"id"`
	// KeyHash 是 secret.HashKey 的输出，用于每次请求的常量时间校验。
	KeyHash string `json:"key_hash"`
	// Display 是给人看的片段（sk-abc123…wxyz），可用于列表展示与手工引用。
	Display string `json:"display"`
	// LegacyKey 只用于读取旧状态文件里的明文密钥并迁移，保存后即清空。
	LegacyKey string `json:"key,omitempty"`
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"created_at"`
	LastUsed  int64  `json:"last_used"`
	Requests  int64  `json:"requests"`
}

// Settings 是运行期可调参数。
type Settings struct {
	ModelAliases map[string]string `json:"model_aliases"`
	// RequestTimeout 是单次上游尝试的超时。
	RequestTimeout int `json:"request_timeout_sec"`
	// OverallTimeout 是一次客户端请求的总预算（含换号重试）。
	// 没有它时最坏耗时是 RequestTimeout × MaxRetries，客户端往往先超时。
	OverallTimeout int    `json:"overall_timeout_sec,omitempty"`
	MaxRetries     int    `json:"max_retries"`
	LogKeep        int    `json:"log_keep"`
	CORSOrigin     string `json:"cors_origin"`
	// AdminPasswordHash 是 PBKDF2 编码后的管理口令，永不下发前端。
	AdminPasswordHash string `json:"admin_password_hash,omitempty"`
	// AdminPasswordLegacy 仅用于读取旧状态文件并迁移，保存后即清空。
	AdminPasswordLegacy string `json:"admin_password,omitempty"`
	// TrustedProxies 是可信反向代理的网段（CIDR，逗号分隔）。
	//
	// 为空时任何携带 X-Forwarded-For / X-Real-IP 的请求都不再被视为本机，
	// 这是堵住「Funnel / nginx 把 RemoteAddr 变成 127.0.0.1」误放行的方法。
	TrustedProxies string `json:"trusted_proxies,omitempty"`
	// AdminLoopbackOnly 为 true 时管理接口只接受本机直连（默认）。
	AdminLoopbackOnly bool `json:"admin_loopback_only"`
	// SessionWarnDays 是凭据到期预警阈值（天）。
	SessionWarnDays int `json:"session_warn_days,omitempty"`
	// MaxConcurrentPerAccount 限制单账号并发上游请求数。
	MaxConcurrentPerAccount int `json:"max_concurrent_per_account,omitempty"`
	// RateLimitPerKey 是每把密钥每分钟的请求上限（0 表示不限）。
	RateLimitPerKey int `json:"rate_limit_per_key,omitempty"`
	// ToolsPolicy 决定收到 tools 字段时的行为：reject（默认）/ ignore。
	ToolsPolicy string `json:"tools_policy,omitempty"`
	// AutoCheckin 开启后后台每天自动为每个账号签到一次。
	AutoCheckin bool `json:"auto_checkin,omitempty"`
	// AutoClaimTasks 开启后后台自动领取「已完成 / 直接领取」的积分任务奖励。
	AutoClaimTasks bool `json:"auto_claim_tasks,omitempty"`
	// AutoDoTasks 开启后后台每天尝试执行积分任务。
	AutoDoTasks bool `json:"auto_do_tasks,omitempty"`
	// AutoTaskLimit 是每个账号每天自动尝试的任务数上限。
	AutoTaskLimit int `json:"auto_task_limit,omitempty"`
	// RetryPendingCheckin 开启后，后台每小时复查「确认中」的签到，直到上游确认。
	//
	// 默认关闭：只有在遇到上游异步发放（biz_code=3）时才产生队列项。
	RetryPendingCheckin bool `json:"retry_pending_checkin,omitempty"`
	// AllowAnonymous 允许在未签发任何密钥时接受非本机请求。
	//
	// 默认关闭：无密钥模式仅放行环回来源，避免误绑 0.0.0.0 后裸奔。
	AllowAnonymous bool `json:"allow_anonymous,omitempty"`
	// VerboseAccessLog 开启后，控制台轮询与健康检查也写入访问日志。
	VerboseAccessLog bool `json:"verbose_access_log,omitempty"`
}

// LogEntry 是一条请求日志（只记元信息，不落对话内容）。
type LogEntry struct {
	Time      int64  `json:"time"`
	Key       string `json:"key"`
	Account   string `json:"account"`
	Model     string `json:"model"`
	Stream    bool   `json:"stream"`
	Status    int    `json:"status"`
	LatencyMS int64  `json:"latency_ms"`
	PromptLen int    `json:"prompt_len"`
	OutputLen int    `json:"output_len"`
	// Kind 是本次结果的分类：ok / business / account / timeout / canceled / transport。
	// 排障时必须能区分「上游挂了」与「客户端先断开」，否则失败率无法解读。
	Kind string `json:"kind,omitempty"`
	Err  string `json:"err,omitempty"`
}

// Stats 是累计统计。
//
// FailedRequests 只统计「上游真的失败」；CanceledRequests 单独计数客户端断开，
// BusinessErrors 统计上游业务 / 配置类错误。三者混在一起时失败率毫无参考价值。
type Stats struct {
	TotalRequests    int64 `json:"total_requests"`
	FailedRequests   int64 `json:"failed_requests"`
	CanceledRequests int64 `json:"canceled_requests,omitempty"`
	BusinessErrors   int64 `json:"business_errors,omitempty"`
	TotalCharsOut    int64 `json:"total_chars_out"`
	StartedAt        int64 `json:"started_at"`
}

// Meta 是状态文件的元信息。
//
// StartedAt 容易被误读成「本次部署时间」——它其实来自状态文件首次创建。
// BootedAt 才是当前进程的启动时间。
type Meta struct {
	SchemaVersion int   `json:"schema_version"`
	CreatedAt     int64 `json:"created_at"`
	BootedAt      int64 `json:"booted_at"`
}

// State 是全部持久化状态。
type State struct {
	Meta     Meta               `json:"meta"`
	Accounts []*account.Account `json:"accounts"`
	Keys     []*APIKey          `json:"keys"`
	Settings Settings           `json:"settings"`
	Stats    Stats              `json:"stats"`
	Logs     []LogEntry         `json:"logs,omitempty"`
}

// Store 是带锁的状态容器。
//
// 并发约定（重要）：
//   - 所有导出方法都在锁内读写 s.st，绝不让内部指针逃逸；
//   - 读取接口（Accounts / Account / NextAccount）一律返回深拷贝，
//     调用方可以随意修改返回值，不会影响其它请求，也不会写入状态文件；
//   - 修改账号必须走 Mark* / Toggle / Clear 之类的方法，
//     它们在同一把写锁内完成修改，因此 Save 期间不会出现半更新状态。
type Store struct {
	mu     sync.RWMutex
	saveMu sync.Mutex
	path   string
	st     *State
	rotate int

	// keyStats 是密钥的运行时调用计数，与状态文件解耦：
	// 每个请求都要累加，不能为此去抢全局写锁。
	keyStats sync.Map // key ID -> *keyStat

	// dirty 标记「内存里有尚未落盘的变更」，由后台自动保存消费。
	dirty atomic.Bool
	// lastSnapshot 是上一次生成时间戳备份的日期（本地时区）。
	lastSnapshot string
}

// keyStat 是单把密钥的原子计数。
type keyStat struct {
	requests atomic.Int64
	lastUsed atomic.Int64
}

// DefaultSettings 返回默认设置。
func DefaultSettings() Settings {
	return Settings{
		ModelAliases:            map[string]string{},
		RequestTimeout:          300,
		OverallTimeout:          600,
		MaxRetries:              3,
		LogKeep:                 500,
		CORSOrigin:              "", // 默认仅放行本机来源；如需局域网访问，在控制台显式配置。
		AdminLoopbackOnly:       true,
		SessionWarnDays:         14,
		MaxConcurrentPerAccount: 2,
		RateLimitPerKey:         0,
		ToolsPolicy:             ToolsPolicyReject,
		// 自动签到 / 自动领取默认关闭，避免未经确认地修改账号额度。
		AutoCheckin: false,
		// 待确认签到的后台重试同样默认关闭（属于自动写操作）。
		RetryPendingCheckin: false,
		AutoClaimTasks:      false,
		AutoDoTasks:         false,
		AutoTaskLimit:       3,
		// 无密钥模式默认只放行本机；确需对外提供匿名调用时显式开启。
		AllowAnonymous: false,
	}
}

// tools 字段的处理策略。
const (
	// ToolsPolicyReject 收到 tools 时返回 422（默认）。
	//
	// 静默忽略会让 Agent 类客户端拿到「看起来成功但没调用工具」的结果，
	// 比显式报错更难排查，因此默认拒绝。
	ToolsPolicyReject = "reject"
	// ToolsPolicyIgnore 忽略 tools 字段，按普通对话处理。
	ToolsPolicyIgnore = "ignore"
)

// Open 载入（或初始化）状态文件。
func Open(path string) (*Store, error) {
	s := &Store{path: path, st: &State{Settings: DefaultSettings()}}
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(raw, s.st); err != nil {
			return nil, fmt.Errorf("状态文件解析失败: %w", err)
		}
		// 补齐缺失字段
		def := DefaultSettings()
		if s.st.Settings.ModelAliases == nil {
			s.st.Settings.ModelAliases = def.ModelAliases
		}
		if s.st.Settings.RequestTimeout == 0 {
			s.st.Settings.RequestTimeout = def.RequestTimeout
		}
		if s.st.Settings.OverallTimeout == 0 {
			s.st.Settings.OverallTimeout = def.OverallTimeout
		}
		if s.st.Settings.MaxRetries == 0 {
			s.st.Settings.MaxRetries = def.MaxRetries
		}
		if s.st.Settings.LogKeep == 0 {
			s.st.Settings.LogKeep = def.LogKeep
		}
		if s.st.Settings.SessionWarnDays == 0 {
			s.st.Settings.SessionWarnDays = def.SessionWarnDays
		}
		if s.st.Settings.MaxConcurrentPerAccount == 0 {
			s.st.Settings.MaxConcurrentPerAccount = def.MaxConcurrentPerAccount
		}
		if s.st.Settings.ToolsPolicy == "" {
			s.st.Settings.ToolsPolicy = def.ToolsPolicy
		}
		if s.st.Settings.CORSOrigin == "*" {
			// 历史状态文件里的通配符不再生效，收紧为本机来源。
			s.st.Settings.CORSOrigin = ""
		}
		if s.st.Stats.StartedAt == 0 {
			s.st.Stats.StartedAt = time.Now().Unix()
		}
		if err := s.migrateLocked(); err != nil {
			return nil, err
		}
		// 迁移旧版本导入的状态：fp 固定为 verify_<device_id>、补齐 web_tab_id 等。
		// 规则集中在 account.Normalize，避免与桌面端导入 / 手工录入出现两套逻辑。
		for _, a := range s.st.Accounts {
			if a == nil {
				continue
			}
			a.Normalize()
		}
	case errors.Is(err, os.ErrNotExist):
		s.st.Stats.StartedAt = time.Now().Unix()
	default:
		return nil, err
	}

	now := time.Now().Unix()
	if s.st.Meta.CreatedAt == 0 {
		s.st.Meta.CreatedAt = s.st.Stats.StartedAt
	}
	if s.st.Meta.CreatedAt == 0 {
		s.st.Meta.CreatedAt = now
	}
	s.st.Meta.SchemaVersion = schemaVersion
	s.st.Meta.BootedAt = now

	// 旧格式的明文密钥：指纹必须由原文算出，因此在这里一次性迁移。
	// 迁移后 LegacyKey 清空，保存时不会再写出明文。
	for _, k := range s.st.Keys {
		if k.ID == "" {
			k.ID = ids.Hex(8)
		}
		if k.KeyHash == "" && k.LegacyKey != "" {
			k.KeyHash = secret.HashKey(k.LegacyKey)
			k.Display = MaskKey(k.LegacyKey)
			k.LegacyKey = ""
		}
	}
	return s, nil
}

// migrateLocked 把旧版状态迁移到当前 schema。
//
// 管理密码：旧版明文 → PBKDF2 哈希。迁移在 Open 内完成，保存后明文字段即清空。
func (s *Store) migrateLocked() error {
	set := &s.st.Settings
	if set.AdminPasswordLegacy != "" {
		h, err := secret.HashPassword(set.AdminPasswordLegacy)
		if err != nil {
			return fmt.Errorf("迁移管理密码失败: %w", err)
		}
		set.AdminPasswordHash = h
		set.AdminPasswordLegacy = ""
	}
	if set.AdminPasswordHash == "" && set.AdminPasswordLegacy == "" {
		set.AdminPasswordHash = ""
	}
	return nil
}

// Path 返回状态文件路径。
func (s *Store) Path() string { return s.path }

// Meta 返回状态文件元信息（结构版本 / 创建时间 / 本次启动时间）。
func (s *Store) Meta() Meta {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.st.Meta
}
