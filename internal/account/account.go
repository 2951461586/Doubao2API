// Package account 定义豆包账号模型：持久化字段 + 进程内运行时状态。
//
// 为什么单独成包：
//   - 持久化层（internal/store）只应依赖「账号是什么」，不应依赖
//     「上游怎么握手」，否则状态文件格式会被逆向结论反向绑定；
//   - 账号需要在多处被复制（HTTP handler、后台任务、重试循环），
//     因此必须有一个语义明确的 Clone 与不会被复制的运行时状态。
//
// 字段分为两层：
//   - 导出字段 = 持久化字段，全部落盘（见 internal/store）；
//   - runtime = 进程内缓存（CSRF token / tab id），不落盘、
//     由同一账号的所有副本共享，自带互斥锁。
package account

import (
	"maps"
	"sync"
	"time"

	"doubao2api/internal/ids"
)

// 账号身份相关的客户端常量。
//
// 这些值同时是上游公参的来源，协议包（internal/doubao）会重新导出，
// 因此两处名字保持一致。
const (
	// AppID 是 DoubaoWork（桌面端）的中台 appId。主端 Web 为 582478。
	AppID = "1044603"
	// PCVersion 是桌面端应用版本（UA 中 SamanthaDoubaoWork/<version>）。
	PCVersion = "2.36.15"
	// RuntimeVersion 是前端 samantha 包版本（桌面端硬编码 3.45.5）。
	RuntimeVersion = "3.45.5"
	// ChromiumVersion 与桌面端内嵌 Chromium 对齐。
	ChromiumVersion = "147.0.7727.149"
	// BuildChannel 对应品牌信息 buildChannel（Windows 构建）。
	BuildChannel = "win"
)

// UserAgent 与桌面端一致（Chromium UA + 应用标识，客户端用
// getSamanthaClientVersion 解析后者）。
const UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" +
	ChromiumVersion + " Safari/537.36 SamanthaDoubaoWork/" + PCVersion

// csrfTTL 是 CSRF token 的本地缓存时长。
//
// 桌面端在访问 passport 相关 URL 时会主动刷新；实测 token 有效期远长于
// 10 分钟，这里取 30 分钟，既避免每次请求都多一跳，又能在过期后自动恢复。
const csrfTTL = 30 * time.Minute

// Account 一个豆包账号（= 一份已登录的会话 Cookie + 设备指纹）。
//
// 约定：所有导出字段都是持久化字段，可以安全地按值读取；
// 修改它们必须经过 store 提供的方法（store 内部持锁），
// 拿到副本直接改不会被持久化，也不会影响别的请求。
type Account struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Source   string            `json:"source"` // auto-import / manual / qr
	Cookies  map[string]string `json:"cookies"`
	DeviceID string            `json:"device_id"`
	WebID    string            `json:"web_id"`
	IID      string            `json:"iid,omitempty"`
	FP       string            `json:"fp"`
	MsToken  string            `json:"ms_token"`
	// AppID 允许按账号覆盖中台 appId（work=1044603 / 主端=582478）。
	AppID string `json:"app_id,omitempty"`
	// 客户端版本信息（导入时从 Local Storage / brand info 读取，缺省用常量）。
	AppVersion     string `json:"app_version,omitempty"`
	PCVersion      string `json:"pc_version,omitempty"`
	RuntimeVersion string `json:"runtime_version,omitempty"`
	Channel        string `json:"channel,omitempty"`
	// WebTabID 是每个账号固定的 web_tab_id（客户端每个 tab 固定一个）。
	WebTabID string `json:"web_tab_id,omitempty"`

	// 每日签到状态（日期为本地时区 YYYY-MM-DD）。
	LastCheckinDate   string `json:"last_checkin_date,omitempty"`
	LastCheckinAt     int64  `json:"last_checkin_at,omitempty"`
	LastCheckinReward int64  `json:"last_checkin_reward,omitempty"`

	// 自动任务每日计数（LastTaskDate 为本地日期 YYYY-MM-DD）。
	LastTaskDate  string `json:"last_task_date,omitempty"`
	LastTaskCount int    `json:"last_task_count,omitempty"`

	// 待确认的签到：上游回「领取结果确认中」(biz_code=3) 时记录，
	// 由后台重试队列每小时复查一次。日期跨天后视为作废。
	PendingCheckinDate  string `json:"pending_checkin_date,omitempty"`
	PendingCheckinAt    int64  `json:"pending_checkin_at,omitempty"`
	PendingCheckinTries int    `json:"pending_checkin_tries,omitempty"`

	Enabled  bool   `json:"enabled"`
	Status   string `json:"status"` // ok / error
	Note     string `json:"note"`
	LastUsed int64  `json:"last_used"`
	LastErr  string `json:"last_err"`
	Failures int    `json:"failures"`

	// rt 是进程内运行时状态（未导出 → 不落盘），由副本共享。
	rt *runtime
}

// runtime 是账号的进程内状态：刷新得到的 CSRF token 与临时 tab id。
//
// 它被同一账号的所有 Account 副本共享，因此必须自带互斥锁，
// 且不得写入任何持久化字段。
type runtime struct {
	mu       sync.Mutex
	csrf     string
	csrfAt   time.Time
	fallback string // 仅当账号没有持久化 WebTabID 时使用
}

// rtOf 返回账号的运行时状态，必要时惰性创建。
//
// 同一个 Account 值不应被多个 goroutine 同时使用（store 每次返回副本，
// 副本之间共享同一个 runtime），因此这里的惰性写入是安全的。
func (a *Account) rtOf() *runtime {
	if a.rt == nil {
		a.rt = &runtime{}
	}
	return a.rt
}

// Clone 返回账号的深拷贝。
//
// Cookies 会复制一份，避免调用方误改共享 map；运行时状态（CSRF 缓存）
// 则按指针共享，这样副本之间不会反复刷新同一个 token。
func (a *Account) Clone() *Account {
	if a == nil {
		return nil
	}
	// 先确保原账号有运行时状态，再让副本共享同一个指针；
	// 否则原账号 rt 为 nil 时副本会各拿一份，CSRF 缓存形同虚设。
	rt := a.rtOf()
	cp := *a // 复制全部持久化字段
	cp.rt = rt
	cp.Cookies = maps.Clone(a.Cookies)
	return &cp
}

// Normalize 补齐 / 迁移设备指纹相关字段（幂等）。
//
// 客户端 1.2 起 fp 固定为 verify_<device_id>，web_tab_id 每个账号固定一个；
// 旧版本状态文件与手工录入都需要经过这里。
func (a *Account) Normalize() {
	if a.DeviceID == "" {
		a.DeviceID = ids.Digits(15)
	}
	if a.WebID == "" {
		a.WebID = ids.Digits(19)
	}
	// 覆盖历史随机 fp：必须与 device_id 对齐，否则风控概率升高。
	a.FP = "verify_" + a.DeviceID
	if a.WebTabID == "" {
		a.WebTabID = ids.UUID()
	}
	if a.PCVersion == "" && a.AppVersion != "" {
		a.PCVersion = a.AppVersion
	}
	a.rtOf()
}

// --- 运行时状态：CSRF token ---

// CSRFToken 返回上游要求的 x-tt-passport-csrf-token。
//
// 优先使用 /passport/safe/csrf_token/ 刷新得到的缓存值，其次回退到 Cookie。
func (a *Account) CSRFToken() string {
	if v := a.cachedCSRF(); v != "" {
		return v
	}
	if v := a.Cookies["passport_csrf_token"]; v != "" {
		return v
	}
	return a.Cookies["passport_csrf_token_default"]
}

// SetCSRF 写入刷新得到的 token（仅驻留内存）。
func (a *Account) SetCSRF(token string) {
	if token == "" {
		return
	}
	rt := a.rtOf()
	rt.mu.Lock()
	rt.csrf = token
	rt.csrfAt = time.Now()
	rt.mu.Unlock()
}

// NeedsCSRFRefresh 报告当前是否需要刷新。
func (a *Account) NeedsCSRFRefresh() bool {
	rt := a.rtOf()
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.csrf != "" {
		// 已经刷新过：过期就再刷一次。
		return time.Since(rt.csrfAt) > csrfTTL
	}
	// 从未刷新过时，Cookie 里的 token 仍可用就不必预刷新。
	return a.Cookies["passport_csrf_token"] == "" && a.Cookies["passport_csrf_token_default"] == ""
}

// cachedCSRF 返回未过期的缓存 token（空串表示需要刷新）。
func (a *Account) cachedCSRF() string {
	rt := a.rtOf()
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.csrf == "" || time.Since(rt.csrfAt) > csrfTTL {
		return ""
	}
	return rt.csrf
}

// TabID 返回账号固定的 web_tab_id。
//
// 优先用持久化值；缺失时生成一次并缓存在运行时状态里，
// 保证同一进程内同一账号的取值稳定。
func (a *Account) TabID() string {
	if a.WebTabID != "" {
		return a.WebTabID
	}
	rt := a.rtOf()
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.fallback == "" {
		rt.fallback = ids.UUID()
	}
	return rt.fallback
}

// --- 派生值（缺省回落到客户端常量） ---

// AppIDValue 返回账号使用的中台 appId。
func (a *Account) AppIDValue() string {
	if a.AppID != "" {
		return a.AppID
	}
	return AppID
}

// PCVersionValue 返回 pc_version（UA 中的应用版本）。
func (a *Account) PCVersionValue() string {
	if a.PCVersion != "" {
		return a.PCVersion
	}
	if a.AppVersion != "" {
		return a.AppVersion
	}
	return PCVersion
}

// RuntimeVersionValue 返回 runtime_version（前端包版本）。
func (a *Account) RuntimeVersionValue() string {
	if a.RuntimeVersion != "" {
		return a.RuntimeVersion
	}
	return RuntimeVersion
}

// ChannelValue 返回 channel 公参。
func (a *Account) ChannelValue() string {
	if a.Channel != "" {
		return a.Channel
	}
	return BuildChannel
}

// FPValue 返回指纹参数：客户端固定用 verify_<device_id>。
func (a *Account) FPValue() string {
	if a.DeviceID != "" {
		return "verify_" + a.DeviceID
	}
	if a.FP != "" {
		return a.FP
	}
	return "verify_" + ids.Hex(5) + "_" + ids.Hex(5) + "_" + ids.Hex(4) +
		"_" + ids.Hex(4) + "_" + ids.Hex(4) + "_" + ids.Hex(10)
}

// UAValue 返回带应用标识的 User-Agent。
func (a *Account) UAValue() string {
	v := a.PCVersionValue()
	if v == PCVersion {
		return UserAgent
	}
	return "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" +
		ChromiumVersion + " Safari/537.36 SamanthaDoubaoWork/" + v
}

// SessionID 返回用于展示/排障的会话标识。
func (a *Account) SessionID() string { return a.Cookies["sessionid"] }
