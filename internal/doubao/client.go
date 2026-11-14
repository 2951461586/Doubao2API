package doubao

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// UpstreamBase 是豆包上游的推理入口。
const UpstreamBase = "https://www.doubao.com"

// Error 是上游返回的业务错误。
type Error struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Text string `json:"message"`
}

func (e *Error) Error() string {
	m := e.Msg
	if m == "" {
		m = e.Text
	}
	return fmt.Sprintf("上游错误 %d: %s", e.Code, m)
}

// Client 是豆包上游客户端。
type Client struct {
	HTTP *http.Client
}

// NewClient 构造上游客户端。
func NewClient() *Client {
	return &Client{
		HTTP: &http.Client{
			// 流式请求不做整体超时，靠 ctx 控制
			Transport: &http.Transport{
				MaxIdleConns:        64,
				MaxIdleConnsPerHost: 32,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// SecurityParams 构造上游要求的公参。
//
// 对齐桌面端 3.45.5 的 getCommonParams：
//   - aid/real_aid 用 work 变体 1044603；
//   - device_platform 仍为 web（后端历史包袱），另发 doubao_device_platform=desktop；
//   - fp 固定为 verify_<device_id>；
//   - 增加 iid / doubao_pc_version / channel / web_platform。
func SecurityParams(a *Account) url.Values {
	v := url.Values{}
	set := func(k, val string) {
		if val != "" {
			v.Set(k, val)
		}
	}
	aid := a.AppIDValue()
	set("aid", aid)
	set("real_aid", aid)
	set("device_id", a.DeviceID)
	set("tea_uuid", a.DeviceID)
	set("web_id", a.WebID)
	set("iid", a.IID)
	set("device_platform", "web")
	set("doubao_device_platform", DevicePlatform)
	set("web_platform", WebPlatform)
	set("language", "zh")
	set("region", "CN")
	set("sys_region", "CN")
	set("pkg_type", "release_version")
	set("version_code", VersionCode)
	set("pc_version", a.PCVersionValue())
	set("doubao_pc_version", a.PCVersionValue())
	set("chromium_version", ChromiumVersion)
	set("client_platform", ClientPlatform)
	set("channel", a.ChannelValue())
	set("runtime", "web")
	set("runtime_version", a.RuntimeVersionValue())
	set("samantha_web", "1")
	set("use-olympus-account", "1")
	set("fp", a.FPValue())
	set("web_tab_id", a.TabID())
	if a.MsToken != "" {
		v.Set("msToken", a.MsToken)
	}
	return v
}

// csrfParams 返回刷新 CSRF token 的查询参数。
//
// 桌面端在该接口上显式把 real_aid 置空；带 real_aid 会被判为「非法应用」(error_code=22)。
func csrfParams(a *Account) url.Values {
	v := SecurityParams(a)
	v.Del("real_aid")
	v.Del("msToken")
	return v
}

// textBlock 构造一个文本内容块。
func textBlock(text string) map[string]any {
	return map[string]any{
		"block_type": BlockText,
		"content": map[string]any{
			"text_block": map[string]any{
				"text":          text,
				"icon_url":      "",
				"icon_url_dark": "",
				"summary":       "",
			},
			"pc_event_block": "",
		},
		"block_id":      NewUUID(),
		"parent_id":     "",
		"meta_info":     []any{},
		"extra_info":    map[string]any{},
		"append_fields": []any{},
		"is_finish":     true,
		"patch_type":    2,
	}
}

// fileBlock 构造文件附件块（block_type=10052，attachment type=3）。
func fileBlock(files []FileAttachment) map[string]any {
	atts := make([]any, 0, len(files))
	for _, f := range files {
		atts = append(atts, map[string]any{
			"type":       3,
			"identifier": NewUUID(),
			"file": map[string]any{
				"uri":       f.URI,
				"url":       "",
				"file_type": 0,
				"name":      f.Name,
				"size":      f.Size,
			},
			"parse_state":   1,
			"review_state":  1,
			"upload_status": 1,
			"progress":      100,
			"src":           "",
		})
	}
	return map[string]any{
		"block_type": BlockAttachment,
		"content": map[string]any{
			"attachment_block": map[string]any{"attachments": atts},
			"pc_event_block":   "",
		},
		"block_id":      NewUUID(),
		"parent_id":     "",
		"meta_info":     []any{},
		"append_fields": []any{},
	}
}

// imageBlock 构造图片附件块（block_type=10052，attachment type=1）。
func imageBlock(imgs []ImageAttachment) map[string]any {
	atts := make([]any, 0, len(imgs))
	for _, im := range imgs {
		w, h := im.Width, im.Height
		if w <= 0 {
			w = 64
		}
		if h <= 0 {
			h = 64
		}
		format := im.Format
		if format == "" {
			format = "png"
		}
		name := im.Name
		if name == "" {
			name = "image.png"
		}
		atts = append(atts, map[string]any{
			"type":       1,
			"identifier": NewUUID(),
			"image": map[string]any{
				"name": name,
				"uri":  im.URI,
				"image_ori": map[string]any{
					"url":         im.CDNURL,
					"width":       w,
					"height":      h,
					"format":      format,
					"url_formats": map[string]any{},
				},
			},
			"parse_state":  0,
			"review_state": 0,
		})
	}
	return map[string]any{
		"block_type": BlockAttachment,
		"content": map[string]any{
			"attachment_block": map[string]any{"attachments": atts},
			"pc_event_block":   "",
		},
		"block_id":      NewUUID(),
		"parent_id":     "",
		"meta_info":     []any{},
		"append_fields": []any{},
		"is_finish":     true,
		"patch_type":    2,
	}
}

// BuildPayload 构造 /chat/completion 的请求体。
func BuildPayload(a *Account, req ChatRequest) map[string]any {
	botID := req.BotID
	if botID == "" {
		botID = DefaultBotID
	}
	blocks := make([]any, 0, 4)
	if len(req.Files) > 0 {
		blocks = append(blocks, fileBlock(req.Files))
	}
	if len(req.Images) > 0 {
		blocks = append(blocks, imageBlock(req.Images))
	}
	blocks = append(blocks, textBlock(req.Text))

	think := strconv.Itoa(req.ThinkLevel)

	localConv := "local_" + RandomHex(8)
	convID := req.ConversationID
	if convID == "" {
		convID = "0"
	}

	ext := map[string]any{
		"use_deep_think":                think,
		"fp":                            a.FPValue(),
		"use_submit_pipeline":           "1",
		"commerce_credit_config_enable": "0",
		"sub_conv_firstmet_type":        "1",
	}
	if req.InputSkill != "" {
		ext["input_skill"] = req.InputSkill
	}
	if len(req.SelectedSkills) > 0 {
		ext["mention_skill_list"] = req.SelectedSkills
	}
	// 调用方显式透传的 ext 字段（高级用法，覆盖同名默认值）。
	for k, val := range req.Ext {
		if k == "" || val == nil {
			continue
		}
		ext[k] = val
	}

	option := map[string]any{
		"send_message_scene": "",
		"create_time_ms":     0,
		"collect_id":         "",
		"is_audio":           false,
		// 必须为 true：置 false 会导致上游不返回思维链（已实测）
		"answer_with_suggest":      true,
		"tts_switch":               false,
		"need_deep_think":          req.ThinkLevel,
		"click_clear_context":      false,
		"from_suggest":             false,
		"is_regen":                 false,
		"is_replace":               false,
		"is_from_click_option":     false,
		"is_from_click_softlink":   false,
		"is_select_text":           false,
		"disable_sse_cache":        false,
		"select_text_action":       "",
		"resend_for_regen":         false,
		"scene_type":               0,
		"unique_key":               NewUUID(),
		"start_seq":                0,
		"need_create_conversation": true,
		"conversation_init_option": map[string]any{
			"need_ack_conversation": true,
		},
		"regen_query_id":       []any{},
		"edit_query_id":        []any{},
		"regen_instruction":    "",
		"no_replace_for_regen": false,
		"message_from":         0,
		"shared_app_name":      "",
		"action_bar_skill_id":  req.SkillID,
		"sse_recv_event_options": map[string]any{
			"support_chunk_delta": true,
		},
		"is_ai_playground": false,
	}
	if req.AgentMode > 0 {
		option["agent_mode"] = req.AgentMode
	}
	if req.SupportLazyFetchStream {
		option["support_lazy_fetch_stream"] = true
	}

	// conversation_init_ext / model_config / aggregate_params 是新版客户端的
	// 模型选择 + 推理强度参数；仅在调用方显式传入时下发，且不发送空键
	// （带空 model_item_key 会被上游判为 common invalid param）。
	initExt := map[string]any{}
	if req.ModelItemKey != "" {
		initExt["model_item_key"] = req.ModelItemKey
	}
	if req.ModeID != "" {
		initExt["mode_id"] = req.ModeID
	}
	if req.SelectionMode {
		initExt["selection_mode"] = true
	}
	if len(initExt) > 0 {
		option["conversation_init_ext"] = initExt
	}

	modelConfig := map[string]any{}
	if req.ModelItemKey != "" {
		modelConfig["model_item_key"] = req.ModelItemKey
		if req.ReasoningEffort != "" {
			modelConfig["reasoning_effort"] = req.ReasoningEffort
		}
	}
	if len(modelConfig) > 0 {
		option["model_config"] = modelConfig
	}

	agg := map[string]any{}
	if req.ModelItemKey != "" {
		agg["model_item_key"] = req.ModelItemKey
		if req.ReasoningEffort != "" {
			agg["reasoning_effort"] = req.ReasoningEffort
		}
	}
	if req.ModeID != "" {
		agg["mode_id"] = req.ModeID
	}
	if len(agg) > 0 {
		option["aggregate_params"] = agg
	}

	return map[string]any{
		"client_meta": map[string]any{
			"local_conversation_id": localConv,
			"conversation_id":       convID,
			"bot_id":                botID,
			"last_section_id":       "",
			"last_message_index":    0,
		},
		"messages": []any{
			map[string]any{
				"local_message_id": NewUUID(),
				"content_block":    blocks,
				"message_status":   0,
			},
		},
		"option":       option,
		"chat_ability": map[string]any{},
		"ext":          ext,
	}
}

// ChatStream 向 /chat/completion 发起流式请求，逐块回调。
//
// 回调返回错误即中止并向上传递。
// 请求前会确保 CSRF token 可用；若上游以登录/CSRF 类错误拒绝且尚未输出任何内容，
// 会自动强制刷新 token 并重试一次。
func (c *Client) ChatStream(ctx context.Context, a *Account, req ChatRequest, onChunk func(CompletionChunk) error) error {
	payload := BuildPayload(a, req)
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	emitted := false
	wrapped := func(ch CompletionChunk) error {
		emitted = true
		return onChunk(ch)
	}

	if err := c.ensureCSRF(ctx, a, false); err != nil {
		// 预刷新失败不阻断：Cookie 里的 token 往往仍可用。
		_ = err
	}

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			if _, err := c.RefreshCSRF(ctx, a); err != nil {
				return lastErr
			}
		}
		err := c.chatOnce(ctx, a, body, wrapped)
		if err == nil {
			return nil
		}
		lastErr = err
		// 已输出内容则不再重试，避免重复；非鉴权错误也不重试。
		if emitted || !looksLikeAuthError(err.Error()) {
			return err
		}
	}
	return lastErr
}

// chatOnce 执行一次 /chat/completion 请求。
func (c *Client) chatOnce(ctx context.Context, a *Account, body []byte, onChunk func(CompletionChunk) error) error {
	params := SecurityParams(a)
	params.Set("tz_name", tzName())
	u := UpstreamBase + "/chat/completion?" + params.Encode()
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	hreq.Header.Set("Accept", "text/event-stream")
	setUpstreamHeaders(hreq, a)

	resp, err := c.HTTP.Do(hreq)
	if err != nil {
		return fmt.Errorf("请求上游失败: %w", err)
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		// 上游可能直接返回 JSON 错误（如会话过期、CSRF 失效）
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		var e Error
		if json.Unmarshal(raw, &e) == nil && (e.Code != 0 || e.Msg != "") {
			return &e
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return fmt.Errorf("上游拒绝访问 (HTTP %d): %s", resp.StatusCode, truncate(string(raw), 300))
		}
		return fmt.Errorf("上游返回非流式响应 (HTTP %d, %s): %s", resp.StatusCode, ct, truncate(string(raw), 300))
	}

	return parseStream(resp.Body, onChunk)
}

// tzName 返回 IANA 时区名（客户端在 query 里传 tz_name）。
func tzName() string {
	if tz := os.Getenv("TZ"); tz != "" {
		return tz
	}
	if name := time.Now().Location().String(); name != "Local" && name != "" {
		return name
	}
	return "Asia/Shanghai"
}

// CookieHeader 把 Cookie map 拼成请求头值。
func CookieHeader(cookies map[string]string) string {
	var sb strings.Builder
	for k, v := range cookies {
		if v == "" {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteString("; ")
		}
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(v)
	}
	return sb.String()
}

func asSlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}

func str(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	}
	return ""
}

func intOf(v any) int {
	switch n := v.(type) {
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	case float64:
		return int(n)
	case int:
		return n
	case string:
		i, _ := strconv.Atoi(n)
		return i
	}
	return 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
