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
	set("web_tab_id", a.webTabID())
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

// parseStream 解析上游 SSE，把增量回调给调用方。
func parseStream(r io.Reader, onChunk func(CompletionChunk) error) error {
	thinkingCount := 0
	inThinking := false
	seen := map[string]bool{}
	seenTool := map[string]bool{}
	emitTool := func(text string) error {
		if text == "" || seenTool[text] {
			return nil
		}
		seenTool[text] = true
		return onChunk(CompletionChunk{ToolInfo: text})
	}

	return ReadSSE(r, func(ev SSEEvent) error {
		if ev.Event == "gateway-error" {
			var e Error
			if json.Unmarshal([]byte(ev.Data), &e) == nil {
				return fmt.Errorf("网关错误: %s", firstNonEmpty(e.Text, e.Msg, ev.Data))
			}
			return fmt.Errorf("网关错误: %s", truncate(ev.Data, 200))
		}
		if ev.Data == "" {
			return nil
		}
		obj, ok := decodeObj(ev.Data)
		if !ok {
			return nil // 非 JSON 事件直接跳过
		}

		// 会话建立
		if ev.Event == "SSE_ACK" {
			if ack, ok := obj["ack_client_meta"].(map[string]any); ok {
				if cid := idOf(ack["conversation_id"]); cid != "" && cid != "0" {
					if err := onChunk(CompletionChunk{ConvID: cid}); err != nil {
						return err
					}
				}
			}
			return nil
		}

		// 业务错误
		if ev.Event == "STREAM_ERROR" || obj["error_code"] != nil {
			code := intOf(obj["error_code"])
			if code != 0 {
				if err := onChunk(CompletionChunk{ErrorCode: code, ErrorMsg: str(obj["error_msg"])}); err != nil {
					return err
				}
				return nil
			}
		}

		// CHUNK_DELTA：紧凑的 {"text": "..."}
		if t, ok := obj["text"].(string); ok && t != "" && obj["error_code"] == nil {
			ch := CompletionChunk{}
			if inThinking {
				ch.Thinking = t
			} else {
				ch.Text = t
			}
			return onChunk(ch)
		}

		// content_block 数组（STREAM_MSG_NOTIFY / STREAM_CHUNK）
		for _, cb := range iterBlocks(obj) {
			bt := intOf(cb["block_type"])
			content, _ := cb["content"].(map[string]any)
			if content == nil {
				// 部分响应使用 content_obj
				content, _ = cb["content_obj"].(map[string]any)
			}
			blockID := idOf(cb["block_id"])
			finished := truthy(cb["is_finish"])

			switch bt {
			case BlockThinking:
				thinkingCount++
				inThinking = thinkingCount == 1
			case BlockText:
				tb, _ := content["text_block"].(map[string]any)
				if t := str(tb["text"]); t != "" {
					ch := CompletionChunk{}
					if inThinking {
						ch.Thinking = t
					} else {
						ch.Text = t
					}
					if err := onChunk(ch); err != nil {
						return err
					}
				}
			case BlockLoading:
				lb, _ := content["loading_block"].(map[string]any)
				tl, _ := lb["text_loading"].(map[string]any)
				if t := str(tl["text"]); t != "" {
					if err := emitTool(t); err != nil {
						return err
					}
				}
			case BlockGenericTool:
				if title := blockText(content, "generic_tool_block"); title != "" {
					if err := emitTool("[tool] " + title); err != nil {
						return err
					}
				}
			case BlockSearchQuery:
				if q := blockText(content, "search_query_block"); q != "" {
					if err := emitTool("[search] 查询: " + q); err != nil {
						return err
					}
				}
			case BlockSearchResult, BlockSearchResultV2, BlockSearchRichMedia:
				if s := firstNonEmpty(blockField(content, "search_query_result_block", "summary"), blockField(content, "search_result_block", "summary")); s != "" {
					if err := emitTool("[search] " + s); err != nil {
						return err
					}
				}
				for _, src := range blockSources(content) {
					key := "src:" + src.URL + "|" + src.Title
					if src.URL == "" || seen[key] {
						continue
					}
					seen[key] = true
					if err := onChunk(CompletionChunk{Sources: []SearchSource{src}}); err != nil {
						return err
					}
				}
			case BlockCode:
				cbk, _ := content["code_block"].(map[string]any)
				code := str(cbk["code"])
				if code == "" {
					break
				}
				// 代码会随 patch 不断增长；仅在块已完成时输出一次。
				if !finished {
					break
				}
				key := "code:" + blockID + ":" + code
				if seen[key] {
					break
				}
				seen[key] = true
				lang := codeLang(intOf(cbk["type"]))
				if err := onChunk(CompletionChunk{Text: "\n```" + lang + "\n" + code + "\n```\n"}); err != nil {
					return err
				}
			case BlockGenImage:
				for _, cr := range parseGenImages(content) {
					key := "img:" + cr.URL
					if cr.URL == "" || seen[key] {
						continue
					}
					seen[key] = true
					if err := onChunk(CompletionChunk{Creations: []Creation{cr}}); err != nil {
						return err
					}
				}
			case BlockImageAnalysis, BlockResearch, BlockResearchStatus, BlockResearchWebpage:
				if t := blockText(content, blockKeyFor(bt)); t != "" {
					if err := emitTool("[" + blockTag(bt) + "] " + t); err != nil {
						return err
					}
				}
			case BlockFileOperation:
				if t := firstNonEmpty(blockField(content, "file_operation_block", "title"), blockField(content, "file_operation_block", "summary")); t != "" {
					if err := emitTool("[file] " + t); err != nil {
						return err
					}
				}
			case BlockArtifact, BlockArtifactOp, BlockArtifactCode:
				for _, art := range parseArtifacts(content) {
					key := "art:" + art.ResourceID + art.ArtifactID + art.Title
					if seen[key] || (art.ResourceID == "" && art.Title == "" && art.ArtifactID == "") {
						continue
					}
					seen[key] = true
					if err := onChunk(CompletionChunk{Artifacts: []Artifact{art}}); err != nil {
						return err
					}
				}
			case BlockLink, BlockReference, BlockTips:
				for _, src := range blockSources(content) {
					key := "src:" + src.URL + "|" + src.Title
					if src.URL == "" || seen[key] {
						continue
					}
					seen[key] = true
					if err := onChunk(CompletionChunk{Sources: []SearchSource{src}}); err != nil {
						return err
					}
				}
				if t := blockText(content, blockKeyFor(bt)); t != "" {
					if err := emitTool("[" + blockTag(bt) + "] " + t); err != nil {
						return err
					}
				}
			case BlockLyricsMusic:
				if m := parseMusic(content); m != nil {
					key := "music:" + m.VideoID + m.Title
					if !seen[key] {
						seen[key] = true
						if err := onChunk(CompletionChunk{Music: m}); err != nil {
							return err
						}
					}
				}
			case BlockCreation:
				for _, cr := range parseCreations(content) {
					if err := onChunk(CompletionChunk{Creations: []Creation{cr}}); err != nil {
						return err
					}
				}
			default:
				// 未知块：仅在带 summary/title 时作为工具提示透传，避免丢信息。
				if bt >= 10000 {
					if t := firstNonEmpty(blockField(content, blockKeyFor(bt), "summary"), blockField(content, blockKeyFor(bt), "title")); t != "" {
						if err := emitTool("[" + blockTag(bt) + "] " + t); err != nil {
							return err
						}
					}
				}
			}
		}

		// patch_op[].patch_value.content 为 JSON 字符串的新格式
		for _, po := range asSlice(obj["patch_op"]) {
			pm, _ := po.(map[string]any)
			pv, _ := pm["patch_value"].(map[string]any)
			if cs, ok := pv["content"].(string); ok && cs != "" {
				if inner, ok := decodeObj(cs); ok {
					if t := str(inner["text"]); t != "" {
						ch := CompletionChunk{}
						if inThinking {
							ch.Thinking = t
						} else {
							ch.Text = t
						}
						if err := onChunk(ch); err != nil {
							return err
						}
					}
				}
			}
		}
		return nil
	})
}

// parseCreations 解析 block_type=2074 的 creation_block。
//
// 上游会分多个 patch 推送同一个 creation：首个 patch 只有 placeholder（无 URL），
// 后续 patch 才带 image.image_ori.url，因此这里只返回已有 URL 的产物。
func parseCreations(content map[string]any) []Creation {
	cb, _ := content["creation_block"].(map[string]any)
	if cb == nil {
		return nil
	}
	var out []Creation
	for _, it := range asSlice(cb["creations"]) {
		cm, ok := it.(map[string]any)
		if !ok {
			continue
		}
		img, _ := cm["image"].(map[string]any)
		if img == nil {
			continue
		}
		cr := Creation{ID: str(cm["id"]), Type: intOf(cm["type"]), URI: str(img["key"])}
		if gd, ok := cm["gen_detail"].(map[string]any); ok {
			cr.TaskType = intOf(gd["task_type"])
		}
		if ori, ok := img["image_ori"].(map[string]any); ok {
			cr.URL = str(ori["url"])
			cr.Width = intOf(ori["width"])
			cr.Height = intOf(ori["height"])
		}
		if cr.URL == "" {
			if th, ok := img["image_thumb"].(map[string]any); ok {
				cr.URL = str(th["url"])
			}
		}
		if cr.URL != "" {
			out = append(out, cr)
		}
	}
	return out
}

// iterBlocks 从事件对象中取出全部 content_block。
func iterBlocks(obj map[string]any) []map[string]any {
	var out []map[string]any
	for _, po := range asSlice(obj["patch_op"]) {
		pm, _ := po.(map[string]any)
		pv, _ := pm["patch_value"].(map[string]any)
		for _, cb := range asSlice(pv["content_block"]) {
			if m, ok := cb.(map[string]any); ok {
				out = append(out, m)
			}
		}
	}
	if dc, ok := obj["content"].(map[string]any); ok {
		for _, cb := range asSlice(dc["content_block"]) {
			if m, ok := cb.(map[string]any); ok {
				out = append(out, m)
			}
		}
	}
	return out
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
