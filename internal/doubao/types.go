// Package doubao 实现豆包（DoubaoWork）桌面端上游服务的逆向复刻。
//
// 上游推理入口为 POST https://www.doubao.com/chat/completion（SSE 流式），
// 鉴权依赖桌面端已登录的 Chromium Cookie。
package doubao

import "sync"

// 内容块类型（content_block[].block_type），对齐桌面端 1.2 版 bundle 的完整枚举。
const (
	BlockText         = 10000 // 文本块（正文，也可能作为思考块子节点）
	BlockThinking     = 10040 // 思考链容器块
	BlockGenericTool  = 10024 // 通用工具块
	BlockSearchResult = 10025 // 联网搜索结果块
	BlockLoading      = 10101 // 加载中提示块
	BlockAttachment   = 10052 // 附件块（图片 / 文件）
	BlockCreation     = 2074  // 生成结果块（文生图等 creation_block）

	// 新增块类型（客户端 147.0.7727.149 枚举）
	BlockSearchQuery     = 10004 // 搜索词
	BlockSearchResultV2  = 10005 // 搜索结果（results[].text_card）
	BlockCode            = 10008 // 代码块（summary / code / std_out_list）
	BlockGenImage        = 10010 // 生图过程块（gen_image_block）
	BlockResearch        = 10011 // 深度研究块
	BlockImage           = 10012 // 图片块
	BlockResearchWebpage = 10013 // 研究网页
	BlockResearchStatus  = 10014 // 研究状态
	BlockFileOperation   = 10019 // 文件操作
	BlockFile            = 10020 // 文件块
	BlockArtifactCode    = 10021 // 代码产物文件
	BlockImageAnalysis   = 10027 // 图片理解
	BlockArtifact        = 10030 // 产物（文档 / 代码 / PPT 等）
	BlockSearchRichMedia = 10050 // 富媒体搜索结果
	BlockLink            = 10051 // 链接卡片
	BlockTips            = 10053 // 提示块
	BlockReference       = 10056 // 引用块
	BlockArtifactOp      = 10058 // 产物操作
	BlockPPT             = 10059 // PPT 模板
	BlockOutline         = 10060 // 大纲
	BlockElapsed         = 10091 // 耗时
	BlockLyricsMusic     = 10075 // 音乐成品块
	BlockQuickReply      = 10080 // 快捷回复
)

// 技能类型（option.action_bar_skill_id），取值见 /samantha/skill/list 的 skill_type，
// 并与桌面端 bundle 的 SkillType 枚举对齐。
const (
	SkillCode            = 0  // Coding
	SkillReadSummary     = 1  // 阅读总结
	SkillWrite           = 2  // 创意写作
	SkillImageGen        = 3  // 图像生成（default_prompt: 生成一张图片:${style} ${content}）
	SkillSearch          = 4  // AI 搜索（default_prompt: 搜索一下: ${content}）
	SkillTranslate       = 5  // 翻译
	SkillRead            = 6  // AI 阅读
	SkillWebpage         = 7  // 网页摘要
	SkillDeepSearch      = 8  // 深度搜索
	SkillMusicGen        = 9  // 音乐生成
	SkillAcademicSearch  = 10 // 学术搜索
	SkillEdu             = 11 // 拍照搜题 / 教育
	SkillReadMulti       = 12 // 多网页阅读
	SkillCodeInterpreter = 13 // 代码解释器 / 数据分析
	SkillReferenceAsk    = 14 // 引用追问
	SkillDeepResearch    = 15 // 深度研究
	SkillCodeAssistant   = 16 // 代码助手
	SkillVideoGen        = 17 // 视频生成
	SkillAiPPT           = 18 // AI PPT
	SkillMediaASR        = 19 // 音视频理解
	SkillApiCall         = 1011
	SkillPptGen          = 5000
	SkillPodcast         = 5003
	SkillLocalFileQA     = 5005
	SkillTranslateV2     = 5007
)

// 深度思考档位（option.need_deep_think / ext.use_deep_think）
const (
	ThinkOff    = 0 // 快速模式
	ThinkOn     = 1 // 思考模式（带思维链）
	ThinkExpert = 3 // 专家模式（深度推理）
)

// 默认 Bot。桌面端扩展 Bot，支持全部能力。
const DefaultBotID = "7338286299411103781"

// 客户端标识与版本，对齐 DoubaoWork 147.0.7727.149 / 前端包 3.45.5 / 应用 2.36.15。
const (
	// AppID 是 DoubaoWork（桌面端）的中台 appId。主端 Web 为 582478。
	AppID = "1044603"
	// AppIDMain 是豆包主端 Web 的 appId（兼容旧账号 / 手工 Cookie）。
	AppIDMain = "582478"
	// VersionCode 与桌面端 VERSION_CODE 对齐
	VersionCode = "20800"
	// ClientPlatform 上游识别的客户端平台
	ClientPlatform = "pc_client"
	// PCVersion 是桌面端应用版本（UA 中 SamanthaDoubaoWork/<version>）
	PCVersion = "2.36.15"
	// RuntimeVersion 是前端 samantha 包版本（桌面端硬编码 3.45.5）
	RuntimeVersion = "3.45.5"
	// ChromiumVersion 与桌面端内嵌 Chromium 对齐
	ChromiumVersion = "147.0.7727.149"
	// DevicePlatform 是桌面端真实形态；对外 device_platform 仍兼容为 web。
	DevicePlatform = "desktop"
	// WebPlatform 对应公参 web_platform
	WebPlatform = "desktop"
	// BuildChannel 对应品牌信息 buildChannel（Windows 构建）
	BuildChannel = "win"
)

// UserAgent 与桌面端一致（Chromium UA + 应用标识，客户端用 getSamanthaClientVersion 解析后者）。
const UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" +
	ChromiumVersion + " Safari/537.36 SamanthaDoubaoWork/" + PCVersion

// Account 一个豆包账号（= 一份已登录的会话 Cookie + 设备指纹）。
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

	// CSRFTokenValue 是刷新后的 token，CSRFTokenAt 是其获取时间（Unix 秒）。
	CSRFTokenValue string     `json:"csrf_token,omitempty"`
	CSRFTokenAt    int64      `json:"csrf_token_at,omitempty"`
	csrfMu         sync.Mutex `json:"-"`

	// 每日签到状态（日期为本地时区 YYYY-MM-DD）。
	LastCheckinDate   string `json:"last_checkin_date,omitempty"`
	LastCheckinAt     int64  `json:"last_checkin_at,omitempty"`
	LastCheckinReward int64  `json:"last_checkin_reward,omitempty"`

	// 自动任务每日计数（LastTaskDate 为本地日期 YYYY-MM-DD）。
	LastTaskDate  string `json:"last_task_date,omitempty"`
	LastTaskCount int    `json:"last_task_count,omitempty"`

	Enabled  bool   `json:"enabled"`
	Status   string `json:"status"` // ok / error
	Note     string `json:"note"`
	LastUsed int64  `json:"last_used"`
	LastErr  string `json:"last_err"`
	Failures int    `json:"failures"`
}

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
	return defaultFP()
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

// CompletionChunk 是从上游 SSE 解析出的一个增量。
type CompletionChunk struct {
	Text      string // 正文增量
	Thinking  string // 思维链增量
	ToolInfo  string // 工具/搜索提示
	ConvID    string // 会话 ID（首个 SSE_ACK 给出）
	ErrorCode int
	ErrorMsg  string
	Done      bool
	Creations []Creation     // 生成产物（文生图等）
	Music     *MusicMessage  // 音乐成品（block_type=10075）
	Artifacts []Artifact     // 产物（block_type=10030）
	Sources   []SearchSource // 联网搜索结果（block_type=10005/10025/10050）
}

// MusicMessage 是音乐成品消息（lyrics_to_songs_music_block）。
type MusicMessage struct {
	VideoID  string  `json:"video_id,omitempty"`
	Title    string  `json:"title,omitempty"`
	CoverURI string  `json:"cover_uri,omitempty"`
	Duration float64 `json:"duration,omitempty"`
	TaskNums int     `json:"task_nums,omitempty"`
}

// Artifact 是上游产物块（文档 / 代码 / PPT / 网页等）。
type Artifact struct {
	Title       string `json:"title,omitempty"`
	ResourceID  string `json:"resource_id,omitempty"`
	ResourceTyp int    `json:"resource_type,omitempty"`
	ArtifactID  string `json:"artifact_id,omitempty"`
	Status      int    `json:"status,omitempty"`
}

// SearchSource 是一条联网搜索结果。
type SearchSource struct {
	Title   string `json:"title,omitempty"`
	URL     string `json:"url,omitempty"`
	Snippet string `json:"snippet,omitempty"`
	Site    string `json:"site,omitempty"`
}

// Creation 是一次生成产物（来自 block_type=2074 的 creation_block）。
type Creation struct {
	ID       string
	Type     int    // creation.type
	TaskType int    // gen_detail.task_type（1=文生图）
	URI      string // 资源 key（tos-cn-...）
	URL      string // 可访问的签名 URL
	Width    int
	Height   int
}

// ChatRequest 是网关内部统一的对话请求。
type ChatRequest struct {
	Text           string
	ThinkLevel     int
	BotID          string
	ConversationID string
	// SkillID 对应上游 option.action_bar_skill_id（技能类型，如 3=图像 9=音乐 17=视频）。
	SkillID int
	// InputSkill 对应上游 ext.input_skill（技能入参 JSON 字符串，如音乐技能的
	// {"lyric":"…","theme":"…","mood":"…","genre":"…","generation_type":"…"}）。
	InputSkill string
	// AgentMode 对应 option.agent_mode（新版客户端菜单项的 agent_mode）。
	AgentMode int
	// ModelItemKey / ModeID / SelectionMode 对应 option.conversation_init_ext / model_config。
	ModelItemKey    string
	ModeID          string
	SelectionMode   bool
	ReasoningEffort string
	// SelectedSkills 对应 ext.mention_skill_list（提及技能）。
	SelectedSkills []string
	// Ext 是调用方显式透传的 ext 字段（高级用法）。
	Ext map[string]any
	// 多模态附件
	Images []ImageAttachment
	Files  []FileAttachment
}

// ImageAttachment 图片附件。
//
// URI 非空时直接作为 block_type=10052 的 image.uri 使用；
// 否则若 Data 非空，会在发送前先上传到豆包资源中心取得 URI。
type ImageAttachment struct {
	Name   string
	URI    string
	CDNURL string
	Width  int
	Height int
	Format string
	// Data 是待上传的原始图片字节（仅当 URI 为空时使用）。
	Data []byte
}

// FileAttachment 文件附件。
//
// 与图片同理：URI 非空时直接使用，否则若 Data 非空则先上传取得 URI。
type FileAttachment struct {
	URI      string
	Name     string
	Size     int64
	FileType string
	// Data 是待上传的原始文件字节（仅当 URI 为空时使用）。
	Data []byte
}
