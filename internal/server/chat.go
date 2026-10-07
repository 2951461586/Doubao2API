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
	"doubao2api/internal/store"
)

// chatRequest 是 OpenAI Chat Completions 的请求体（仅取所需字段）。
type chatRequest struct {
	Model          string        `json:"model"`
	Messages       []chatMessage `json:"messages"`
	Stream         bool          `json:"stream"`
	ConversationID string        `json:"conversation_id"` // 豆包原生多轮
	BotID          string        `json:"bot_id"`
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

	text, images, err := flattenMessages(req.Messages)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	if text == "" && len(images) == 0 {
		writeErr(w, http.StatusBadRequest, "消息内容为空", "invalid_request_error")
		return
	}

	upReq := doubao.ChatRequest{
		Text:           text,
		ThinkLevel:     model.ThinkLevel,
		BotID:          req.BotID,
		ConversationID: req.ConversationID,
		Images:         images,
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
		out      strings.Builder
		thinking strings.Builder
		convID   string
	)

	acct, err := s.withAccountRetry(r.Context(), timeout, func(ctx context.Context, acct *doubao.Account) error {
		return s.Up.ChatStream(ctx, acct, upReq, func(c doubao.CompletionChunk) error {
			if c.ConvID != "" {
				convID = c.ConvID
			}
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
	acct, err := s.withAccountRetry(r.Context(), timeout, func(ctx context.Context, acct *doubao.Account) error {
		return s.Up.ChatStream(ctx, acct, upReq, func(c doubao.CompletionChunk) error {
			if c.ErrorCode != 0 {
				return fmt.Errorf("上游错误 %d: %s", c.ErrorCode, c.ErrorMsg)
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

// probeAccount 用一个最小请求探测账号可用性。
func (s *Server) probeAccount(ctx context.Context, a *doubao.Account) error {
	return s.Up.ChatStream(ctx, a, doubao.ChatRequest{Text: "hi", ThinkLevel: doubao.ThinkOff}, func(c doubao.CompletionChunk) error {
		if c.ErrorCode != 0 {
			return fmt.Errorf("上游错误 %d: %s", c.ErrorCode, c.ErrorMsg)
		}
		return nil
	})
}

// flattenMessages 把 OpenAI 消息列表压平为一段提示文本，并抽出图片附件。
//
// 豆包原生按 conversation_id 维护上下文，但 OpenAI 客户端每次都带全量历史，
// 因此这里把历史合并进单轮提示，语义等价且不依赖服务端会话。
func flattenMessages(msgs []chatMessage) (string, []doubao.ImageAttachment, error) {
	var sys []string
	var turns []string
	var images []doubao.ImageAttachment

	for _, m := range msgs {
		text, imgs := extractContent(m.Content)
		images = append(images, imgs...)
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
	return sb.String(), images, nil
}

// extractContent 解析 OpenAI 的 content 字段（字符串或分片数组）。
func extractContent(v any) (string, []doubao.ImageAttachment) {
	switch c := v.(type) {
	case string:
		return c, nil
	case []any:
		var sb strings.Builder
		var imgs []doubao.ImageAttachment
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
					imgs = append(imgs, doubao.ImageAttachment{CDNURL: url, Name: "image.png", Format: "png"})
				}
			}
		}
		return sb.String(), imgs
	}
	return "", nil
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
