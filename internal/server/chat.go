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

	// 新版客户端参数：推理强度 / 模型项 / 智能体模式 / 提及技能。
	ReasoningEffort string   `json:"reasoning_effort"`
	AgentMode       int      `json:"agent_mode"`
	ModelItemKey    string   `json:"model_item_key"`
	ModeID          string   `json:"mode_id"`
	SelectionMode   bool     `json:"selection_mode"`
	SelectedSkills  []string `json:"selected_skills"`
	// Ext 透传到上游 ext（高级用法）。
	Ext map[string]any `json:"ext"`
	// Doubao 兼容嵌套写法：{"doubao":{"agent_mode":…}}。
	Doubao *doubaoExtras `json:"doubao"`
}

// doubaoExtras 是 doubao 命名空间下的扩展参数。
type doubaoExtras struct {
	ReasoningEffort string         `json:"reasoning_effort"`
	AgentMode       int            `json:"agent_mode"`
	ModelItemKey    string         `json:"model_item_key"`
	ModeID          string         `json:"mode_id"`
	SelectionMode   bool           `json:"selection_mode"`
	SelectedSkills  []string       `json:"selected_skills"`
	Ext             map[string]any `json:"ext"`
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

	// 兼容 doubao 命名空间写法。
	extras := req
	if req.Doubao != nil {
		if extras.ReasoningEffort == "" {
			extras.ReasoningEffort = req.Doubao.ReasoningEffort
		}
		if extras.AgentMode == 0 {
			extras.AgentMode = req.Doubao.AgentMode
		}
		if extras.ModelItemKey == "" {
			extras.ModelItemKey = req.Doubao.ModelItemKey
		}
		if extras.ModeID == "" {
			extras.ModeID = req.Doubao.ModeID
		}
		if !extras.SelectionMode {
			extras.SelectionMode = req.Doubao.SelectionMode
		}
		if len(extras.SelectedSkills) == 0 {
			extras.SelectedSkills = req.Doubao.SelectedSkills
		}
		if len(extras.Ext) == 0 {
			extras.Ext = req.Doubao.Ext
		}
	}

	// 技能：显式 skill 优先，否则用模型自带的技能（如 doubao-draw）。
	skillID := resolveSkill(req.Skill)
	if skillID == 0 {
		skillID = model.Skill
	}
	if skillID != 0 {
		text = doubao.WrapSkillPrompt(skillID, text, "")
	}

	// 推理强度：OpenAI 的 reasoning_effort 映射到上游 think 档位。
	thinkLevel := model.ThinkLevel
	if v := strings.TrimSpace(extras.ReasoningEffort); v != "" {
		thinkLevel = thinkLevelFromEffort(v, thinkLevel)
	}

	// 工作模式模型（registry 里带 ModelItemKey 的）：模型选择优先于请求字段。
	modelItemKey := extras.ModelItemKey
	if model.ModelItemKey != "" {
		modelItemKey = model.ModelItemKey
	}

	upReq := doubao.ChatRequest{
		Text:            text,
		ThinkLevel:      thinkLevel,
		AgentMode:       resolveAgentMode(model, extras),
		BotID:           req.BotID,
		ConversationID:  req.ConversationID,
		SkillID:         skillID,
		InputSkill:      req.InputSkill,
		ModelItemKey:    modelItemKey,
		ModeID:          extras.ModeID,
		SelectionMode:   extras.SelectionMode,
		ReasoningEffort: extras.ReasoningEffort,
		SelectedSkills:  extras.SelectedSkills,
		Ext:             extras.Ext,
		Images:          images,
		Files:           files,
	}

	key := extractKey(r)
	timeout := s.requestTimeout()

	if req.Stream {
		s.streamChat(w, r, upReq, model, key, timeout, start)
		return
	}
	s.blockingChat(w, r, upReq, model, key, timeout, start)
}

// blockingChat 收集完整回复后一次性返回。
func (s *Server) blockingChat(w http.ResponseWriter, r *http.Request, upReq doubao.ChatRequest, model registry.Model, key string, timeout time.Duration, start time.Time) {
	col := newResponseCollector()
	acct, err := s.withAccountRetry(r.Context(), timeout, func(ctx context.Context, acct *doubao.Account) error {
		req, err := s.withUploadedAttachments(ctx, acct, upReq)
		if err != nil {
			return err
		}
		return s.Up.ChatStream(ctx, acct, req, func(c doubao.CompletionChunk) error {
			_, err := col.absorb(c)
			return err
		})
	})
	if err != nil {
		s.record(key, acct, model.ID, false, http.StatusBadGateway, start, len(upReq.Text), 0, err)
		writeErr(w, http.StatusBadGateway, err.Error(), "upstream_error")
		return
	}

	msg := map[string]any{"role": "assistant", "content": col.text()}
	if t := col.thinkingText(); t != "" {
		msg["reasoning_content"] = t
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
		"usage":           estimateUsage(upReq.Text, col.text()),
		"conversation_id": col.convID,
	}
	if len(col.imageURLs) > 0 {
		resp["images"] = col.imageURLs
	}
	if len(col.sources) > 0 {
		resp["sources"] = col.sources
	}
	if len(col.artifacts) > 0 {
		resp["artifacts"] = col.artifacts
	}
	if len(col.music) > 0 {
		resp["music"] = col.music
	}
	s.record(key, acct, model.ID, false, http.StatusOK, start, len(upReq.Text), col.length(), nil)
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

	// writeChunk 写一帧确切的 chunk JSON。
	writeChunk := func(chunk map[string]any) {
		raw, err := json.Marshal(chunk)
		if err != nil {
			return
		}
		_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
		flusher.Flush()
	}

	send := func(delta map[string]any, finish any) {
		writeChunk(map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model.ID,
			"choices": []any{map[string]any{
				"index":         0,
				"delta":         delta,
				"finish_reason": finish,
			}},
		})
	}

	// sendError 写一帧结构化错误：finish_reason=error + 顶层 error 字段。
	//
	// 不能把错误文案当成正文 delta 下发——SDK 会当正常回复收下，
	// 并在 finish_reason=stop 时认为请求成功。
	sendError := func(err error) {
		writeChunk(map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model.ID,
			"choices": []any{map[string]any{
				"index":         0,
				"delta":         map[string]any{},
				"finish_reason": "error",
			}},
			"error": map[string]any{
				"type":    "upstream_error",
				"message": err.Error(),
			},
		})
	}

	// 首块先发 role，符合 OpenAI 约定
	send(map[string]any{"role": "assistant", "content": ""}, nil)

	var streamErr error
	col := newResponseCollector()
	acct, err := s.withAccountRetry(r.Context(), timeout, func(ctx context.Context, acct *doubao.Account) error {
		req, err := s.withUploadedAttachments(ctx, acct, upReq)
		if err != nil {
			return err
		}
		return s.Up.ChatStream(ctx, acct, req, func(c doubao.CompletionChunk) error {
			d, err := col.absorb(c)
			if err != nil {
				return err
			}
			// 结构化产物逐条下发，便于客户端边收边渲染。
			if len(d.images) > 0 {
				send(map[string]any{"images": d.images}, nil)
			}
			for _, src := range d.sources {
				send(map[string]any{"sources": []doubao.SearchSource{src}}, nil)
			}
			for _, art := range d.artifacts {
				send(map[string]any{"artifacts": []doubao.Artifact{art}}, nil)
			}
			if d.music != nil {
				send(map[string]any{"music": d.music}, nil)
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
		sendError(err)
	} else {
		send(map[string]any{}, "stop")
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	flusher.Flush()

	s.record(key, acct, model.ID, true, statusOf(streamErr), start, len(upReq.Text), outLen, streamErr)
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
			return &doubao.Error{Code: c.ErrorCode, Msg: c.ErrorMsg}
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
// 上游会分多个 patch 重复推送同一个 creation，因此按 ID 去重；
// 少数情况下 creation 不带 id，此时退化为按 URL 去重，
// 避免空 ID 把后续所有产物一起吞掉。
func newCreationURLs(creations []doubao.Creation, seen map[string]bool) []string {
	var urls []string
	for _, cr := range creations {
		if cr.URL == "" {
			continue
		}
		key := cr.ID
		if key == "" {
			key = "url:" + cr.URL
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		urls = append(urls, cr.URL)
	}
	return urls
}

// resolveAgentMode 决定是否按工作模式下发：模型自带的 AgentMode 优先
// （工作模式模型必须为 1），其次才看请求里的 agent_mode / doubao.agent_mode。
func resolveAgentMode(model registry.Model, extras chatRequest) int {
	if model.AgentMode != 0 {
		return model.AgentMode
	}
	return extras.AgentMode
}

// resolveSkill 解析请求里的技能字段（名称 / 别名 / 数值）。
func resolveSkill(v any) int {
	if m, ok := doubao.LookupSkillMeta(v); ok {
		return m.Type
	}
	return 0
}

// thinkLevelFromEffort 把 OpenAI reasoning_effort 映射为上游档位。
func thinkLevelFromEffort(v string, fallback int) int {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "none", "minimal", "low", "0", "fast":
		return doubao.ThinkOff
	case "medium", "auto", "default", "1", "think":
		return doubao.ThinkOn
	case "high", "xhigh", "max", "3", "expert":
		return doubao.ThinkExpert
	}
	return fallback
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
