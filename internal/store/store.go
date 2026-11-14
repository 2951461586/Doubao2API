// Package store 负责网关状态的持久化与账号池调度。
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"doubao2api/internal/account"
	"doubao2api/internal/ids"
)

// APIKey 是网关对外签发的调用密钥。
type APIKey struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"created_at"`
	LastUsed  int64  `json:"last_used"`
	Requests  int64  `json:"requests"`
}

// Settings 是运行期可调参数。
type Settings struct {
	ModelAliases   map[string]string `json:"model_aliases"`
	RequestTimeout int               `json:"request_timeout_sec"`
	MaxRetries     int               `json:"max_retries"`
	LogKeep        int               `json:"log_keep"`
	CORSOrigin     string            `json:"cors_origin"`
	AdminPassword  string            `json:"admin_password"`
	// AutoCheckin 开启后后台每天自动为每个账号签到一次。
	AutoCheckin bool `json:"auto_checkin,omitempty"`
	// AutoClaimTasks 开启后后台自动领取「已完成 / 直接领取」的积分任务奖励。
	AutoClaimTasks bool `json:"auto_claim_tasks,omitempty"`
	// AutoDoTasks 开启后后台每天尝试执行积分任务。
	AutoDoTasks bool `json:"auto_do_tasks,omitempty"`
	// AutoTaskLimit 是每个账号每天自动尝试的任务数上限。
	AutoTaskLimit int `json:"auto_task_limit,omitempty"`
	// AllowAnonymous 允许在未签发任何密钥时接受非本机请求。
	//
	// 默认关闭：无密钥模式仅放行环回来源，避免误绑 0.0.0.0 后裸奔。
	AllowAnonymous bool `json:"allow_anonymous,omitempty"`
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
	Err       string `json:"err,omitempty"`
}

// Stats 是累计统计。
type Stats struct {
	TotalRequests  int64 `json:"total_requests"`
	FailedRequests int64 `json:"failed_requests"`
	TotalCharsOut  int64 `json:"total_chars_out"`
	StartedAt      int64 `json:"started_at"`
}

// State 是全部持久化状态。
type State struct {
	Accounts []*account.Account `json:"accounts"`
	Keys     []*APIKey          `json:"keys"`
	Settings Settings           `json:"settings"`
	Stats    Stats              `json:"stats"`
	Logs     []LogEntry         `json:"logs"`
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
}

// DefaultSettings 返回默认设置。
func DefaultSettings() Settings {
	return Settings{
		ModelAliases:   map[string]string{},
		RequestTimeout: 300,
		MaxRetries:     3,
		LogKeep:        500,
		// 默认仅放行本机来源；如需局域网访问，在控制台显式配置。
		CORSOrigin: "",
		// 自动签到 / 自动领取默认关闭，避免未经确认地修改账号额度。
		AutoCheckin:    false,
		AutoClaimTasks: false,
		AutoDoTasks:    false,
		AutoTaskLimit:  3,
		// 无密钥模式默认只放行本机；确需对外提供匿名调用时显式开启。
		AllowAnonymous: false,
	}
}

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
		if s.st.Settings.MaxRetries == 0 {
			s.st.Settings.MaxRetries = def.MaxRetries
		}
		if s.st.Settings.LogKeep == 0 {
			s.st.Settings.LogKeep = def.LogKeep
		}
		if s.st.Settings.CORSOrigin == "*" {
			// 历史状态文件里的通配符不再生效，收紧为本机来源。
			s.st.Settings.CORSOrigin = ""
		}
		if s.st.Stats.StartedAt == 0 {
			s.st.Stats.StartedAt = time.Now().Unix()
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
	return s, nil
}

// Path 返回状态文件路径。
func (s *Store) Path() string { return s.path }

// Save 原子写回状态文件。
//
// 持读锁 marshal 是安全的：所有持久化字段的写操作都在写锁内完成，
// 读取接口返回的是深拷贝，因此这里不会与修改撞车（见 Store 的并发约定）。
func (s *Store) Save() error {
	// 串行化落盘，避免自动任务与 HTTP 请求同时写同一个 .tmp 文件。
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.RLock()
	raw, err := json.MarshalIndent(s.st, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// --- 账号 ---

// Accounts 返回账号快照（深拷贝，调用方可以随意修改）。
func (s *Store) Accounts() []*account.Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*account.Account, 0, len(s.st.Accounts))
	for _, a := range s.st.Accounts {
		out = append(out, a.Clone())
	}
	return out
}

// Account 按 ID 查找账号（深拷贝；不存在返回 nil）。
func (s *Store) Account(id string) *account.Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.accountLocked(id).Clone()
}

// accountLocked 返回内部账号指针；调用方必须已持锁且不得泄漏该指针。
func (s *Store) accountLocked(id string) *account.Account {
	for _, a := range s.st.Accounts {
		if a.ID == id {
			return a
		}
	}
	return nil
}

// UpsertAccount 按会话 ID 去重后写入账号；返回是否为新增。
//
// 入参会被拷贝一份存储，调用方后续修改自己那份不会影响状态。
func (s *Store) UpsertAccount(a *account.Account) bool {
	if a == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := a.Clone()
	for i, ex := range s.st.Accounts {
		if ex.SessionID() != "" && ex.SessionID() == cp.SessionID() {
			cp.ID = ex.ID
			// 保留调用方传入的运行期缓存（CSRF 等），其余字段整体替换。
			s.st.Accounts[i] = cp
			return false
		}
	}
	s.st.Accounts = append(s.st.Accounts, cp)
	return true
}

// RemoveAccount 删除账号。
func (s *Store) RemoveAccount(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, a := range s.st.Accounts {
		if a.ID == id {
			s.st.Accounts = append(s.st.Accounts[:i], s.st.Accounts[i+1:]...)
			return true
		}
	}
	return false
}

// NextAccount 以轮询方式挑一个可用账号。
//
// 连续失败过多的账号会被跳过；全部不可用时回退到失败最少的那个，
// 保证不会硬阻塞请求。
func (s *Store) NextAccount() *account.Account {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.st.Accounts)
	if n == 0 {
		return nil
	}
	var best *account.Account
	for i := 0; i < n; i++ {
		idx := (s.rotate + i) % n
		a := s.st.Accounts[idx]
		if !a.Enabled {
			continue
		}
		if best == nil || a.Failures < best.Failures {
			best = a
		}
		if a.Failures == 0 {
			s.rotate = (idx + 1) % n
			return a.Clone()
		}
	}
	s.rotate = (s.rotate + 1) % n
	return best.Clone()
}

// MarkAccountResult 记录一次账号调用结果。
func (s *Store) MarkAccountResult(id string, ok bool, note string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.st.Accounts {
		if a.ID != id {
			continue
		}
		a.LastUsed = time.Now().Unix()
		if ok {
			a.Failures = 0
			a.Status = "ok"
			a.LastErr = ""
			if note != "" {
				a.Note = note
			}
		} else {
			a.Failures++
			a.Status = "error"
			a.LastErr = note
		}
		return
	}
}

// ToggleAccount 切换账号启用状态；返回切换后的值。
func (s *Store) ToggleAccount(id string) (enabled bool, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.accountLocked(id)
	if a == nil {
		return false, false
	}
	a.Enabled = !a.Enabled
	return a.Enabled, true
}

// SetAccountEnabled 设置账号启用状态。
func (s *Store) SetAccountEnabled(id string, enabled bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.accountLocked(id)
	if a == nil {
		return false
	}
	a.Enabled = enabled
	return true
}

// ClearAccountErrors 清空账号的失败计数与错误状态。
func (s *Store) ClearAccountErrors(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.accountLocked(id)
	if a == nil {
		return false
	}
	a.Failures = 0
	a.Status = "ok"
	a.LastErr = ""
	return true
}

// MarkCheckin 记录一次签到结果。
func (s *Store) MarkCheckin(id, date string, reward int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.st.Accounts {
		if a.ID != id {
			continue
		}
		if date == "" {
			date = time.Now().Format("2006-01-02")
		}
		a.LastCheckinDate = date
		a.LastCheckinAt = time.Now().Unix()
		a.LastCheckinReward = reward
		return
	}
}

// MarkTaskRun 累加自动任务计数。
func (s *Store) MarkTaskRun(id, date string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.st.Accounts {
		if a.ID != id {
			continue
		}
		if date == "" {
			date = time.Now().Format("2006-01-02")
		}
		if a.LastTaskDate != date {
			a.LastTaskDate = date
			a.LastTaskCount = 0
		}
		a.LastTaskCount += n
		return
	}
}

// --- 密钥 ---

// Keys 返回密钥快照。
func (s *Store) Keys() []*APIKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*APIKey, len(s.st.Keys))
	copy(out, s.st.Keys)
	return out
}

// AddKey 新增密钥。
func (s *Store) AddKey(name string) *APIKey {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := &APIKey{
		Key:       "sk-" + ids.Hex(24),
		Name:      name,
		Enabled:   true,
		CreatedAt: time.Now().Unix(),
	}
	s.st.Keys = append(s.st.Keys, k)
	return k
}

// DeleteKey 删除密钥。
func (s *Store) DeleteKey(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, k := range s.st.Keys {
		if k.Key == key {
			s.st.Keys = append(s.st.Keys[:i], s.st.Keys[i+1:]...)
			return true
		}
	}
	return false
}

// ToggleKey 启用/停用密钥。
func (s *Store) ToggleKey(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.st.Keys {
		if k.Key == key {
			k.Enabled = !k.Enabled
			return true
		}
	}
	return false
}

// ValidKey 校验密钥并计数。
func (s *Store) ValidKey(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.st.Keys {
		if k.Key == key && k.Enabled {
			k.LastUsed = time.Now().Unix()
			k.Requests++
			return true
		}
	}
	return false
}

// HasKeys 报告是否已签发任何密钥。
func (s *Store) HasKeys() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.st.Keys) > 0
}

// --- 设置 / 统计 / 日志 ---

// Settings 返回设置快照。
func (s *Store) Settings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := s.st.Settings
	cp.ModelAliases = map[string]string{}
	for k, v := range s.st.Settings.ModelAliases {
		cp.ModelAliases[k] = v
	}
	return cp
}

// UpdateSettings 局部更新设置。
func (s *Store) UpdateSettings(fn func(*Settings)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.st.Settings)
}

// Stats 返回统计快照。
func (s *Store) Stats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.st.Stats
}

// Logs 返回最近的日志。
func (s *Store) Logs(limit int) []LogEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := len(s.st.Logs)
	if limit <= 0 || limit > n {
		limit = n
	}
	out := make([]LogEntry, limit)
	copy(out, s.st.Logs[n-limit:])
	return out
}

// Record 记录一次请求结果并更新统计。
func (s *Store) Record(e LogEntry, outChars int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Stats.TotalRequests++
	if e.Status >= 400 || e.Err != "" {
		s.st.Stats.FailedRequests++
	}
	s.st.Stats.TotalCharsOut += int64(outChars)
	s.st.Logs = append(s.st.Logs, e)
	if keep := s.st.Settings.LogKeep; keep > 0 && len(s.st.Logs) > keep {
		s.st.Logs = s.st.Logs[len(s.st.Logs)-keep:]
	}
}

// ClearLogs 清空日志。
func (s *Store) ClearLogs() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Logs = nil
}

// ClearStats 重置统计。
func (s *Store) ClearStats() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Stats = Stats{StartedAt: time.Now().Unix()}
}
