// Package doubao 实现豆包（DoubaoWork）桌面端上游服务的逆向复刻。
//
// 上游推理入口为 POST https://www.doubao.com/chat/completion（SSE 流式），
// 鉴权依赖桌面端已登录的 Chromium Cookie。
package doubao

// 内容块类型（content_block[].block_type）
const (
	BlockText         = 10000 // 文本块（正文，也可能作为思考块子节点）
	BlockThinking     = 10040 // 思考链容器块
	BlockGenericTool  = 10024 // 通用工具块
	BlockSearchResult = 10025 // 联网搜索结果块
	BlockLoading      = 10101 // 加载中提示块
	BlockAttachment   = 10052 // 附件块（图片 / 文件）
	BlockCreation     = 2074  // 生成结果块（文生图等 creation_block）
)

// 技能类型（option.action_bar_skill_id），取值见 /samantha/skill/list 的 skill_type。
const (
	SkillImageGen = 3  // 图像生成（default_prompt: 生成一张图片:${style} ${content}）
	SkillMusicGen = 9  // 音乐生成
	SkillVideoGen = 17 // 视频生成
)

// 深度思考档位（option.need_deep_think / ext.use_deep_think）
const (
	ThinkOff    = 0 // 快速模式
	ThinkOn     = 1 // 思考模式（带思维链）
	ThinkExpert = 3 // 专家模式（深度推理）
)

// 默认 Bot。桌面端扩展 Bot，支持全部能力。
const DefaultBotID = "7338286299411103781"

// 客户端标识
const (
	// AppID 豆包主端中台 appId（桌面端为 work 变体 1044603，主端为 582478）
	AppID = "582478"
	// VersionCode 与桌面端 VERSION_CODE 对齐
	VersionCode = "20800"
	// ClientPlatform 上游识别的客户端平台
	ClientPlatform = "pc_client"
	// RuntimeVersion samantha runtime 版本
	RuntimeVersion = "3.5.4"
)

// Account 一个豆包账号（= 一份已登录的会话 Cookie + 设备指纹）。
type Account struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Source   string            `json:"source"` // auto-import / manual / qr
	Cookies  map[string]string `json:"cookies"`
	DeviceID string            `json:"device_id"`
	WebID    string            `json:"web_id"`
	FP       string            `json:"fp"`
	MsToken  string            `json:"ms_token"`

	Enabled  bool   `json:"enabled"`
	Status   string `json:"status"` // ok / error
	Note     string `json:"note"`
	LastUsed int64  `json:"last_used"`
	LastErr  string `json:"last_err"`
	Failures int    `json:"failures"`
}

// CSRFToken 返回上游要求的 x-tt-passport-csrf-token。
func (a *Account) CSRFToken() string {
	if v := a.Cookies["passport_csrf_token"]; v != "" {
		return v
	}
	return a.Cookies["passport_csrf_token_default"]
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
	Creations []Creation // 生成产物（文生图等）
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
