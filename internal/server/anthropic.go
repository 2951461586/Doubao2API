package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"doubao2api/internal/doubao"
	"doubao2api/internal/registry"
)

// anthropicRequest 是 Anthropic Messages API 的请求体（仅取所需字段）。
type anthropicRequest struct {
	Model         string             `json:"model"`
	MaxTokens     int                `json:"max_tokens"`
	System        json.RawMessage    `json:"system"`
	Messages      []anthropicMessage `json:"messages"`
	Stream        bool               `json:"stream"`
	Temperature   *float64           `json:"temperature"`
	TopP          *float64           `json:"top_p"`
	StopSequences []string           `json:"stop_sequences"`
	Tools         json.RawMessage    `json:"tools"`
	ToolChoice    json.RawMessage    `json:"tool_choice"`
}

type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// handleAnthropicMessages 是 POST /v1/messages 的入口。
//
// 豆包上游只有一套补丁流协议，因此这里把 Anthropic 请求先转成内部统一请求，
// 再把结果映射回 Anthropic 的 message / SSE 事件模型。
func (s *Server) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAnthropicErr(w, http.StatusMethodNotAllowed, "invalid_request_error", "仅支持 POST")
		return
	}
	start := time.Now()

	var req anthropicRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<20)).Decode(&req); err != nil {
		writeAnthropicErr(w, http.StatusBadRequest, "invalid_request_error", "请求体解析失败: "+err.Error())
		return
	}
	if len(req.Messages) == 0 {
		writeAnthropicErr(w, http.StatusBadRequest, "invalid_request_error", "messages 不能为空")
		return
	}

	settings := s.Store.Settings()
	model := registry.Resolve(req.Model, settings.ModelAliases)

	text, images, files, err := anthropicToChat(&req)
	if err != nil {
		writeAnthropicErr(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if text == "" && len(images) == 0 && len(files) == 0 {
		writeAnthropicErr(w, http.StatusBadRequest, "invalid_request_error", "消息内容为空")
		return
	}

	upReq := doubao.ChatRequest{
		Text:       text,
		ThinkLevel: model.ThinkLevel,
		Images:     images,
		Files:      files,
	}
	key := extractKey(r)
	timeout := time.Duration(settings.RequestTimeout) * time.Second
	if timeout <= 0 {
		timeout = 300 * time.Second
	}

	if req.Stream {
		s.streamAnthropic(w, r, upReq, model, key, timeout, start)
		return
	}
	s.blockingAnthropic(w, r, upReq, model, key, timeout, start)
}

// blockingAnthropic 收集完整回复后按 Anthropic message 结构返回。
func (s *Server) blockingAnthropic(w http.ResponseWriter, r *http.Request, upReq doubao.ChatRequest, model registry.Model, key string, timeout time.Duration, start time.Time) {
	var (
		out      strings.Builder
		thinking strings.Builder
	)

	acct, err := s.withAccountRetry(r.Context(), timeout, func(ctx context.Context, acct *doubao.Account) error {
		req, err := s.withUploadedAttachments(ctx, acct, upReq)
		if err != nil {
			return err
		}
		return s.Up.ChatStream(ctx, acct, req, func(c doubao.CompletionChunk) error {
			if c.ErrorCode != 0 {
				return fmt.Errorf("上游错误 %d: %s", c.ErrorCode, c.ErrorMsg)
			}
			out.WriteString(c.Text)
			thinking.WriteString(c.Thinking)
			return nil
		})
	})
	if err != nil {
		s.record(key, acct, model.ID, false, http.StatusBadGateway, start, len(upReq.Text), 0, err)
		writeAnthropicErr(w, http.StatusBadGateway, "api_error", err.Error())
		return
	}

	content := make([]map[string]any, 0, 2)
	if thinking.Len() > 0 {
		content = append(content, map[string]any{"type": "thinking", "thinking": thinking.String()})
	}
	content = append(content, map[string]any{"type": "text", "text": out.String()})

	s.record(key, acct, model.ID, false, http.StatusOK, start, len(upReq.Text), out.Len(), nil)
	writeJSON(w, http.StatusOK, map[string]any{
		"id":            "msg_" + doubao.RandomHex(12),
		"type":          "message",
		"role":          "assistant",
		"model":         model.ID,
		"content":       content,
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  estTokens(upReq.Text),
			"output_tokens": estTokens(out.String()),
		},
	})
}

// streamAnthropic 以 Anthropic SSE 事件模型流式返回。
func (s *Server) streamAnthropic(w http.ResponseWriter, r *http.Request, upReq doubao.ChatRequest, model registry.Model, key string, timeout time.Duration, start time.Time) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAnthropicErr(w, http.StatusInternalServerError, "api_error", "服务端不支持流式响应")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	msgID := "msg_" + doubao.RandomHex(12)

	send := func(event string, payload map[string]any) {
		raw, err := json.Marshal(payload)
		if err != nil {
			return
		}
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw)
		flusher.Flush()
	}

	send("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": msgID, "type": "message", "role": "assistant", "model": model.ID,
			"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": estTokens(upReq.Text), "output_tokens": 0},
		},
	})

	// 内容块按需开启：思维链块在前，正文块在后（与上游补丁流顺序一致）。
	index := -1
	kind := "" // "" / "thinking" / "text"
	openBlock := func(k string) {
		if kind == k {
			return
		}
		if kind != "" {
			send("content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
		}
		index++
		kind = k
		block := map[string]any{"type": "text", "text": ""}
		if k == "thinking" {
			block = map[string]any{"type": "thinking", "thinking": ""}
		}
		send("content_block_start", map[string]any{
			"type": "content_block_start", "index": index, "content_block": block,
		})
	}

	outLen := 0
	var streamErr error

	acct, err := s.withAccountRetry(r.Context(), timeout, func(ctx context.Context, acct *doubao.Account) error {
		req, err := s.withUploadedAttachments(ctx, acct, upReq)
		if err != nil {
			return err
		}
		return s.Up.ChatStream(ctx, acct, req, func(c doubao.CompletionChunk) error {
			if c.ErrorCode != 0 {
				return fmt.Errorf("上游错误 %d: %s", c.ErrorCode, c.ErrorMsg)
			}
			if c.Thinking != "" {
				openBlock("thinking")
				send("content_block_delta", map[string]any{
					"type": "content_block_delta", "index": index,
					"delta": map[string]any{"type": "thinking_delta", "thinking": c.Thinking},
				})
			}
			if c.Text != "" {
				openBlock("text")
				outLen += len(c.Text)
				send("content_block_delta", map[string]any{
					"type": "content_block_delta", "index": index,
					"delta": map[string]any{"type": "text_delta", "text": c.Text},
				})
			}
			return nil
		})
	})
	if err != nil {
		streamErr = err
	}

	if kind != "" {
		send("content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
	}
	if streamErr != nil {
		send("error", map[string]any{
			"type":  "error",
			"error": map[string]any{"type": "api_error", "message": streamErr.Error()},
		})
	}
	send("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": outLen / 2},
	})
	send("message_stop", map[string]any{"type": "message_stop"})

	s.record(key, acct, model.ID, true, statusOf(streamErr), start, len(upReq.Text), outLen, streamErr)
}

// --- 请求转换 ---

// anthropicToChat 把 Anthropic 请求转换为内部提示与图片附件。
//
// 复用 OpenAI 侧的压平逻辑：先把 Anthropic 内容块映射成 OpenAI 形态，
// 再交给 flattenMessages，避免两套历史压缩实现漂移。
func anthropicToChat(req *anthropicRequest) (string, []doubao.ImageAttachment, []doubao.FileAttachment, error) {
	msgs := make([]chatMessage, 0, len(req.Messages)+1)
	if sys := anthropicSystemText(req.System); sys != "" {
		msgs = append(msgs, chatMessage{Role: "system", Content: sys})
	}
	for _, m := range req.Messages {
		content, err := anthropicContent(m.Content)
		if err != nil {
			return "", nil, nil, err
		}
		msgs = append(msgs, chatMessage{Role: m.Role, Content: content})
	}
	text, images, files, err := flattenMessages(msgs)
	if err != nil {
		return "", nil, nil, err
	}
	return text, images, files, nil
}

// anthropicSystemText 提取 system 字段（字符串或文本块数组）。
func anthropicSystemText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var blocks []map[string]any
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b["type"] == "text" {
			if t, ok := b["text"].(string); ok && t != "" {
				parts = append(parts, t)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// anthropicContent 把 Anthropic 的 content 字段映射为 OpenAI 形态。
//
// 支持 text / image(base64|url) / tool_result；tool_use 与 thinking 不回流，
// 避免把上游不需要的推理内容再喂回去。
func anthropicContent(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var blocks []map[string]any
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, fmt.Errorf("content 字段格式不支持")
	}

	parts := make([]any, 0, len(blocks))
	for _, b := range blocks {
		switch b["type"] {
		case "text":
			if t, ok := b["text"].(string); ok && t != "" {
				parts = append(parts, map[string]any{"type": "text", "text": t})
			}
		case "image":
			if u := anthropicImageURL(b); u != "" {
				parts = append(parts, map[string]any{
					"type": "image_url", "image_url": map[string]any{"url": u},
				})
			}
		case "document":
			if u := anthropicDocumentDataURL(b); u != "" {
				name := firstString(b["title"], b["name"])
				if name == "" {
					name = "document.pdf"
				}
				parts = append(parts, map[string]any{
					"type": "input_file", "filename": name, "file_data": u,
				})
			}
		case "tool_result":
			// 工具结果按文本回流，保留上下文
			if t := anthropicToolResultText(b); t != "" {
				parts = append(parts, map[string]any{"type": "text", "text": t})
			}
		}
	}
	return parts, nil
}

// anthropicImageURL 把 Anthropic 图片块转成可直接交给 imageAttachmentFromURL 的地址。
//
// base64 源转成 data: URL，url 源原样透传（由网关代下载后转存上游）。
func anthropicImageURL(block map[string]any) string {
	src, ok := block["source"].(map[string]any)
	if !ok {
		return ""
	}
	switch src["type"] {
	case "base64":
		media, _ := src["media_type"].(string)
		data, _ := src["data"].(string)
		if data == "" {
			return ""
		}
		if media == "" {
			media = "image/png"
		}
		return "data:" + media + ";base64," + data
	case "url":
		u, _ := src["url"].(string)
		return u
	}
	return ""
}

// anthropicDocumentDataURL 把 Anthropic 文档块（base64 源）转成 data: URL。
func anthropicDocumentDataURL(block map[string]any) string {
	src, ok := block["source"].(map[string]any)
	if !ok || src["type"] != "base64" {
		return ""
	}
	data, _ := src["data"].(string)
	if data == "" {
		return ""
	}
	media, _ := src["media_type"].(string)
	if media == "" {
		media = "application/pdf"
	}
	return "data:" + media + ";base64," + data
}

// anthropicToolResultText 提取工具结果中的文本。
func anthropicToolResultText(block map[string]any) string {
	switch c := block["content"].(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, item := range c {
			if m, ok := item.(map[string]any); ok && m["type"] == "text" {
				if t, ok := m["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// estTokens 给出粗略的 token 估算（豆包不返回 token 数）。
func estTokens(s string) int { return len([]rune(s)) / 2 }

// writeAnthropicErr 按 Anthropic 的错误结构返回。
func writeAnthropicErr(w http.ResponseWriter, code int, typ, msg string) {
	writeJSON(w, code, map[string]any{
		"type":  "error",
		"error": map[string]any{"type": typ, "message": msg},
	})
}
