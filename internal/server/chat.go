package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"doubao2api/internal/doubao"
	"doubao2api/internal/registry"
	"doubao2api/internal/store"
)

// chatRequest 是 OpenAI Chat Completions 的请求体（仅取所需字段）。
type chatRequest struct {
	Model          string        `json:"model"`
	Messages       []chatMessage `json:"messages"`
	Stream         bool          `json:"stream"`
	ConversationID string        `json:"conversation_id"` // 豆包原生多轮
	BotID          string        `json:"bot_id"`
	// Skill 是本网关的扩展字段，用于显式指定上游技能
	// （对应 option.action_bar_skill_id）：名称或数值，如 "image" / "music" / "video"。
	Skill any `json:"skill"`
	// InputSkill 对应上游 ext.input_skill（技能入参 JSON 字符串）。
	// 音乐技能需要：{"lyric":"…","theme":"…","mood":"…","genre":"…","generation_type":"…"}
	InputSkill string `json:"input_skill"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// handleChatCompletions 是 /v1/chat/completions 的主入口。
func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST", "invalid_request_error")
		return
	}
	start := time.Now()

	var req chatRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	if len(req.Messages) == 0 {
		writeErr(w, http.StatusBadRequest, "messages 不能为空", "invalid_request_error")
		return
	}

	settings := s.Store.Settings()
	model := registry.Resolve(req.Model, settings.ModelAliases)

	text, images, files, err := flattenMessages(req.Messages)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	if text == "" && len(images) == 0 && len(files) == 0 {
		writeErr(w, http.StatusBadRequest, "消息内容为空", "invalid_request_error")
		return
	}

	upReq := doubao.ChatRequest{
		Text:           text,
		ThinkLevel:     model.ThinkLevel,
		BotID:          req.BotID,
		ConversationID: req.ConversationID,
		SkillID:        resolveSkill(req.Skill),
		InputSkill:     req.InputSkill,
		Images:         images,
		Files:          files,
	}

	key := extractKey(r)
	timeout := time.Duration(settings.RequestTimeout) * time.Second
	if timeout <= 0 {
		timeout = 300 * time.Second
	}

	if req.Stream {
		s.streamChat(w, r, upReq, model, key, timeout, start)
		return
	}
	s.blockingChat(w, r, upReq, model, key, timeout, start)
}

// blockingChat 收集完整回复后一次性返回。
func (s *Server) blockingChat(w http.ResponseWriter, r *http.Request, upReq doubao.ChatRequest, model registry.Model, key string, timeout time.Duration, start time.Time) {
	var (
		out          strings.Builder
		thinking     strings.Builder
		convID       string
		imageURLs    []string
		seenCreation = map[string]bool{}
	)

	acct, err := s.withAccountRetry(r.Context(), timeout, func(ctx context.Context, acct *doubao.Account) error {
		req, err := s.withUploadedAttachments(ctx, acct, upReq)
		if err != nil {
			return err
		}
		return s.Up.ChatStream(ctx, acct, req, func(c doubao.CompletionChunk) error {
			if c.ConvID != "" {
				convID = c.ConvID
			}
			if c.ErrorCode != 0 {
				return fmt.Errorf("上游错误 %d: %s", c.ErrorCode, c.ErrorMsg)
			}
			imageURLs = append(imageURLs, newCreationURLs(c.Creations, seenCreation)...)
			out.WriteString(c.Text)
			thinking.WriteString(c.Thinking)
			return nil
		})
	})
	if err != nil {
		s.record(key, acct, model.ID, false, http.StatusBadGateway, start, len(upReq.Text), 0, err)
		writeErr(w, http.StatusBadGateway, err.Error(), "upstream_error")
		return
	}

	msg := map[string]any{"role": "assistant", "content": out.String()}
	if thinking.Len() > 0 {
		msg["reasoning_content"] = thinking.String()
	}
	resp := map[string]any{
		"id":      "chatcmpl-" + doubao.RandomHex(12),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model.ID,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       msg,
			"finish_reason": "stop",
		}},
		"usage":           estimateUsage(upReq.Text, out.String()),
		"conversation_id": convID,
	}
	if len(imageURLs) > 0 {
		resp["images"] = imageURLs
	}
	s.record(key, acct, model.ID, false, http.StatusOK, start, len(upReq.Text), out.Len(), nil)
	writeJSON(w, http.StatusOK, resp)
}

// streamChat 以 OpenAI SSE 格式流式返回。
func (s *Server) streamChat(w http.ResponseWriter, r *http.Request, upReq doubao.ChatRequest, model registry.Model, key string, timeout time.Duration, start time.Time) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "服务端不支持流式响应", "server_error")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	id := "chatcmpl-" + doubao.RandomHex(12)
	created := time.Now().Unix()
	outLen := 0

	send := func(delta map[string]any, finish any) {
		chunk := map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model.ID,
			"choices": []any{map[string]any{
				"index":         0,
				"delta":         delta,
				"finish_reason": finish,
			}},
		}
		raw, err := json.Marshal(chunk)
		if err != nil {
			return
		}
		_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
		flusher.Flush()
	}

	// 首块先发 role，符合 OpenAI 约定
	send(map[string]any{"role": "assistant", "content": ""}, nil)

	var streamErr error
	seenCreation := map[string]bool{}
	acct, err := s.withAccountRetry(r.Context(), timeout, func(ctx context.Context, acct *doubao.Account) error {
		req, err := s.withUploadedAttachments(ctx, acct, upReq)
		if err != nil {
			return err
		}
		return s.Up.ChatStream(ctx, acct, req, func(c doubao.CompletionChunk) error {
			if c.ErrorCode != 0 {
				return fmt.Errorf("上游错误 %d: %s", c.ErrorCode, c.ErrorMsg)
			}
			if urls := newCreationURLs(c.Creations, seenCreation); len(urls) > 0 {
				send(map[string]any{"images": urls}, nil)
			}
			if c.ConvID != "" {
				// 通过额外的 SSE 注释块回传会话 ID，便于客户端续接多轮
				_, _ = fmt.Fprintf(w, ": conversation_id=%s\n\n", c.ConvID)
				flusher.Flush()
			}
			if c.Thinking != "" {
				send(map[string]any{"reasoning_content": c.Thinking}, nil)
			}
			if c.Text != "" {
				outLen += len(c.Text)
				send(map[string]any{"content": c.Text}, nil)
			}
			return nil
		})
	})
	if err != nil {
		streamErr = err
		send(map[string]any{"content": "\n[错误] " + err.Error()}, nil)
	}

	send(map[string]any{}, "stop")
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	flusher.Flush()

	s.record(key, acct, model.ID, true, statusOf(streamErr), start, len(upReq.Text), outLen, streamErr)
}

// withAccountRetry 选账号并在失败时换号重试。
//
// 返回本次实际使用的账号（可能为 nil），供调用方记录日志归属。
func (s *Server) withAccountRetry(ctx context.Context, timeout time.Duration, fn func(context.Context, *doubao.Account) error) (*doubao.Account, error) {
	accts := s.Store.Accounts()
	if len(accts) == 0 {
		return nil, fmt.Errorf("没有可用账号，请先在控制台导入或添加账号")
	}
	maxTries := s.Store.Settings().MaxRetries
	if maxTries <= 0 {
		maxTries = 3
	}
	if maxTries > len(accts) {
		maxTries = len(accts)
	}

	var lastErr error
	var used *doubao.Account
	for i := 0; i < maxTries; i++ {
		acct := s.Store.NextAccount()
		if acct == nil {
			break
		}
		used = acct
		cctx, cancel := context.WithTimeout(ctx, timeout)
		err := fn(cctx, acct)
		cancel()

		if err == nil {
			s.Store.MarkAccountResult(acct.ID, true, "")
			return acct, nil
		}
		lastErr = err
		s.Store.MarkAccountResult(acct.ID, false, err.Error())
		// 上下文被取消（客户端断开）时不再重试
		if ctx.Err() != nil {
			break
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("全部账号均不可用")
	}
	return used, lastErr
}

// withUploadedAttachments 确保每个图片 / 文件附件都有可用于 content_block 的上游 uri。
//
// 仅有外链（CDNURL）的图片由网关代下载；仅有原始字节（Data）的附件直接上传。
// 两者都会转存到豆包资源中心——上游不会自行抓取外部资源（已实测）。
func (s *Server) withUploadedAttachments(ctx context.Context, acct *doubao.Account, req doubao.ChatRequest) (doubao.ChatRequest, error) {
	if len(req.Images) > 0 {
		imgs := make([]doubao.ImageAttachment, len(req.Images))
		copy(imgs, req.Images)
		for i := range imgs {
			if imgs[i].URI != "" {
				continue
			}
			if len(imgs[i].Data) == 0 && imgs[i].CDNURL != "" {
				data, mime, err := doubao.FetchImage(ctx, imgs[i].CDNURL)
				if err != nil {
					return req, fmt.Errorf("获取图片失败: %w", err)
				}
				imgs[i].Data = data
				if imgs[i].Format == "" {
					imgs[i].Format = strings.TrimPrefix(mime, "image/")
				}
			}
			if len(imgs[i].Data) == 0 {
				continue
			}
			uri, err := s.Up.UploadImage(ctx, acct, imgs[i])
			if err != nil {
				return req, fmt.Errorf("图片上传失败: %w", err)
			}
			imgs[i].URI = uri
			imgs[i].Data = nil
			imgs[i].CDNURL = ""
		}
		req.Images = imgs
	}

	if len(req.Files) > 0 {
		files := make([]doubao.FileAttachment, len(req.Files))
		copy(files, req.Files)
		for i := range files {
			if files[i].URI != "" || len(files[i].Data) == 0 {
				continue
			}
			uri, err := s.Up.UploadFile(ctx, acct, files[i])
			if err != nil {
				return req, fmt.Errorf("文件上传失败: %w", err)
			}
			files[i].URI = uri
			files[i].Data = nil
		}
		req.Files = files
	}
	return req, nil
}

// imageAttachmentFromURL 把 OpenAI 的 image_url 转为待上传的图片附件。
//
// data: 内联图片直接解出字节；http(s) 外链记为 CDNURL，发送前由网关代下载。
func imageAttachmentFromURL(raw string) doubao.ImageAttachment {
	if mime, data, ok := doubao.ParseDataURL(raw); ok {
		format := strings.TrimPrefix(mime, "image/")
		if format == "" {
			format = "png"
		}
		return doubao.ImageAttachment{Name: "image." + format, Format: format, Data: data}
	}
	return doubao.ImageAttachment{Name: "image.png", Format: "png", CDNURL: raw}
}

// probeAccount 用一个最小请求探测账号可用性。
func (s *Server) probeAccount(ctx context.Context, a *doubao.Account) error {
	return s.Up.ChatStream(ctx, a, doubao.ChatRequest{Text: "hi", ThinkLevel: doubao.ThinkOff}, func(c doubao.CompletionChunk) error {
		if c.ErrorCode != 0 {
			return fmt.Errorf("上游错误 %d: %s", c.ErrorCode, c.ErrorMsg)
		}
		return nil
	})
}

// flattenMessages 把 OpenAI 消息列表压平为一段提示文本，并抽出图片 / 文件附件。
//
// 豆包原生按 conversation_id 维护上下文，但 OpenAI 客户端每次都带全量历史，
// 因此这里把历史合并进单轮提示，语义等价且不依赖服务端会话。
func flattenMessages(msgs []chatMessage) (string, []doubao.ImageAttachment, []doubao.FileAttachment, error) {
	var sys []string
	var turns []string
	var images []doubao.ImageAttachment
	var files []doubao.FileAttachment

	for _, m := range msgs {
		text, imgs, fls := extractContent(m.Content)
		images = append(images, imgs...)
		files = append(files, fls...)
		text = strings.TrimSpace(text)
		switch strings.ToLower(m.Role) {
		case "system", "developer":
			if text != "" {
				sys = append(sys, text)
			}
		case "assistant":
			if text != "" {
				turns = append(turns, "assistant: "+text)
			}
		case "user", "":
			if text != "" {
				turns = append(turns, "user: "+text)
			}
		default:
			if text != "" {
				turns = append(turns, m.Role+": "+text)
			}
		}
	}

	var sb strings.Builder
	if len(sys) > 0 {
		sb.WriteString(strings.Join(sys, "\n"))
		sb.WriteString("\n\n")
	}
	// 仅有一条 user 消息时保持原样，避免污染提示
	if len(turns) == 1 && strings.HasPrefix(turns[0], "user: ") {
		sb.WriteString(strings.TrimPrefix(turns[0], "user: "))
	} else {
		sb.WriteString(strings.Join(turns, "\n"))
	}
	return sb.String(), images, files, nil
}

// extractContent 解析 OpenAI 的 content 字段（字符串或分片数组）。
//
// 支持 text / image_url / input_image / file / input_file。
func extractContent(v any) (string, []doubao.ImageAttachment, []doubao.FileAttachment) {
	switch c := v.(type) {
	case string:
		return c, nil, nil
	case []any:
		var sb strings.Builder
		var imgs []doubao.ImageAttachment
		var files []doubao.FileAttachment
		for _, part := range c {
			pm, ok := part.(map[string]any)
			if !ok {
				continue
			}
			switch pm["type"] {
			case "text", "input_text":
				if t, ok := pm["text"].(string); ok {
					sb.WriteString(t)
				}
			case "image_url", "input_image":
				url := ""
				switch iu := pm["image_url"].(type) {
				case string:
					url = iu
				case map[string]any:
					url, _ = iu["url"].(string)
				}
				if url == "" {
					url, _ = pm["image_url"].(string)
				}
				if url != "" {
					imgs = append(imgs, imageAttachmentFromURL(url))
				}
			case "file", "input_file":
				if f, ok := fileAttachmentFromPart(pm); ok {
					files = append(files, f)
				}
			}
		}
		return sb.String(), imgs, files
	}
	return "", nil, nil
}

// fileAttachmentFromPart 解析 OpenAI 风格的文件分片，兼容两种写法：
//
//	{"type":"file","file":{"filename":"a.pdf","file_data":"data:application/pdf;base64,..."}}
//	{"type":"input_file","filename":"a.pdf","file_data":"data:application/pdf;base64,..."}
//
// file_data 也接受不带 data: 前缀的裸 base64。
func fileAttachmentFromPart(pm map[string]any) (doubao.FileAttachment, bool) {
	obj := pm
	if f, ok := pm["file"].(map[string]any); ok {
		obj = f
	}
	name := firstString(obj["filename"], obj["name"], pm["filename"], pm["name"])
	raw := firstString(obj["file_data"], obj["data"], pm["file_data"], pm["data"])
	if raw == "" {
		return doubao.FileAttachment{}, false
	}
	_, data, ok := doubao.ParseDataURL(raw)
	if !ok {
		if d, err := base64.StdEncoding.DecodeString(raw); err == nil {
			data, ok = d, true
		}
	}
	if !ok || len(data) == 0 {
		return doubao.FileAttachment{}, false
	}
	if name == "" {
		name = "file.bin"
	}
	return doubao.FileAttachment{Name: name, Size: int64(len(data)), Data: data}, true
}

// firstString 返回第一个非空字符串。
func firstString(vals ...any) string {
	for _, v := range vals {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// newCreationURLs 提取尚未出现过的生成产物 URL。
//
// 上游会分多个 patch 重复推送同一个 creation，因此按 ID 去重。
func newCreationURLs(creations []doubao.Creation, seen map[string]bool) []string {
	var urls []string
	for _, cr := range creations {
		if cr.URL == "" || seen[cr.ID] {
			continue
		}
		seen[cr.ID] = true
		urls = append(urls, cr.URL)
	}
	return urls
}

// resolveSkill 解析请求里的技能字段（名称或数值）。
//
// 取值对齐上游 SkillType（来自桌面端 bundle 的枚举，与 /samantha/skill/list 一致）：
// 3=图像生成、9=音乐生成、17=视频生成。
//
// 注意：图像生成对免费账号可用；音乐 / 视频属会员能力，实测上游会返回
// 710022004 rate limited（由 shark_admin 风控/配额判定）。
func resolveSkill(v any) int {
	switch s := v.(type) {
	case float64:
		return int(s)
	case string:
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "image", "image_gen", "imagegeneration":
			return doubao.SkillImageGen
		case "music", "music_gen", "musicgeneration":
			return doubao.SkillMusicGen
		case "video", "video_gen", "videogeneration":
			return doubao.SkillVideoGen
		}
	}
	return 0
}

// estimateUsage 给出粗略的用量估算（豆包不返回 token 数）。
func estimateUsage(prompt, out string) map[string]any {
	p := len([]rune(prompt)) / 2
	o := len([]rune(out)) / 2
	return map[string]any{
		"prompt_tokens":     p,
		"completion_tokens": o,
		"total_tokens":      p + o,
	}
}

// record 记录一次请求结果。
//
// acct 是本次实际使用的账号（可为 nil）；stream 标记是否为流式响应。
func (s *Server) record(key string, acct *doubao.Account, model string, stream bool, status int, start time.Time, inLen, outLen int, err error) {
	e := store.LogEntry{
		Time:      time.Now().Unix(),
		Key:       mask(key),
		Model:     model,
		Stream:    stream,
		Status:    status,
		LatencyMS: time.Since(start).Milliseconds(),
		PromptLen: inLen,
		OutputLen: outLen,
	}
	if acct != nil {
		e.Account = acct.ID
	}
	if err != nil {
		e.Err = err.Error()
	}
	s.Store.Record(e, outLen)
}

func statusOf(err error) int {
	if err == nil {
		return http.StatusOK
	}
	return http.StatusBadGateway
}

// contextWithTimeout 在请求上下文上叠加超时。
func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}
