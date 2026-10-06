package doubao

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// UpstreamBase 是豆包上游的推理入口。
const UpstreamBase = "https://www.doubao.com"

// ChromiumVersion 与桌面端内嵌 Chromium 对齐。
const ChromiumVersion = "147.0.7727.149"

// UserAgent 与桌面端一致。
const UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" +
	ChromiumVersion + " Safari/537.36"

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
func SecurityParams(a *Account) url.Values {
	v := url.Values{}
	set := func(k, val string) {
		if val != "" {
			v.Set(k, val)
		}
	}
	set("aid", AppID)
	set("real_aid", AppID)
	set("device_id", a.DeviceID)
	set("tea_uuid", a.DeviceID)
	set("web_id", a.WebID)
	set("device_platform", "web")
	set("language", "zh")
	set("region", "CN")
	set("sys_region", "CN")
	set("pkg_type", "release_version")
	set("version_code", VersionCode)
	set("pc_version", "2.1.7")
	set("chromium_version", ChromiumVersion)
	set("client_platform", ClientPlatform)
	set("runtime", "web")
	set("runtime_version", RuntimeVersion)
	set("samantha_web", "1")
	set("use-olympus-account", "1")
	set("fp", a.FP)
	set("web_tab_id", NewUUID())
	if a.MsToken != "" {
		v.Set("msToken", a.MsToken)
	}
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
		"option": map[string]any{
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
			"regen_query_id":         []any{},
			"edit_query_id":          []any{},
			"regen_instruction":      "",
			"no_replace_for_regen":   false,
			"message_from":           0,
			"shared_app_name":        "",
			"action_bar_skill_id":    0,
			"sse_recv_event_options": map[string]any{"support_chunk_delta": true},
			"is_ai_playground":       false,
		},
		"chat_ability": map[string]any{},
		"ext": map[string]any{
			"use_deep_think":                think,
			"fp":                            a.FP,
			"use_submit_pipeline":           "1",
			"commerce_credit_config_enable": "0",
			"sub_conv_firstmet_type":        "1",
		},
	}
}

// ChatStream 向 /chat/completion 发起流式请求，逐块回调。
//
// 回调返回错误即中止并向上传递。
func (c *Client) ChatStream(ctx context.Context, a *Account, req ChatRequest, onChunk func(CompletionChunk) error) error {
	payload := BuildPayload(a, req)
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	u := UpstreamBase + "/chat/completion?" + SecurityParams(a).Encode()
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	hreq.Header.Set("Accept", "text/event-stream")
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("User-Agent", UserAgent)
	hreq.Header.Set("Origin", UpstreamBase)
	hreq.Header.Set("Referer", UpstreamBase+"/chat/")
	hreq.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	if t := a.CSRFToken(); t != "" {
		hreq.Header.Set("x-tt-passport-csrf-token", t)
	}
	hreq.Header.Set("Cookie", CookieHeader(a.Cookies))

	resp, err := c.HTTP.Do(hreq)
	if err != nil {
		return fmt.Errorf("请求上游失败: %w", err)
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		// 上游可能直接返回 JSON 错误（如会话过期）
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		var e Error
		if json.Unmarshal(raw, &e) == nil && (e.Code != 0 || e.Msg != "") {
			return &e
		}
		return fmt.Errorf("上游返回非流式响应 (HTTP %d, %s): %s", resp.StatusCode, ct, truncate(string(raw), 300))
	}

	return parseStream(resp.Body, onChunk)
}

// parseStream 解析上游 SSE，把增量回调给调用方。
func parseStream(r io.Reader, onChunk func(CompletionChunk) error) error {
	thinkingCount := 0
	inThinking := false

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
		var obj map[string]any
		if err := json.Unmarshal([]byte(ev.Data), &obj); err != nil {
			return nil // 非 JSON 事件直接跳过
		}

		// 会话建立
		if ev.Event == "SSE_ACK" {
			if ack, ok := obj["ack_client_meta"].(map[string]any); ok {
				if cid := str(ack["conversation_id"]); cid != "" && cid != "0" {
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
					if err := onChunk(CompletionChunk{ToolInfo: t}); err != nil {
						return err
					}
				}
			case BlockGenericTool:
				gtb, _ := content["generic_tool_block"].(map[string]any)
				if title := str(gtb["title"]); title != "" {
					if err := onChunk(CompletionChunk{ToolInfo: "[tool] " + title}); err != nil {
						return err
					}
				}
			case BlockSearchResult:
				sqrb, _ := content["search_query_result_block"].(map[string]any)
				if len(sqrb) > 0 {
					if s := str(sqrb["summary"]); s != "" {
						if err := onChunk(CompletionChunk{ToolInfo: "[search] " + s}); err != nil {
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
				var inner map[string]any
				if json.Unmarshal([]byte(cs), &inner) == nil {
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
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func intOf(v any) int {
	switch n := v.(type) {
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
