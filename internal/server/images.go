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

// imageGenRequest 是 OpenAI Images API 的请求体（仅取所需字段）。
type imageGenRequest struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	N              int    `json:"n"`
	Size           string `json:"size"`
	ResponseFormat string `json:"response_format"`
	Style          string `json:"style"`
}

// handleImageGenerations 是 POST /v1/images/generations 的入口。
//
// 豆包没有独立的文生图 REST 接口（/samantha/cozeplugin/txt2img 实测返回
// no permission），真正的链路是「图像生成」技能（skill_type=3）：
// 按技能自带的模板把 prompt 包成一条对话，上游以 block_type=2074 的
// creation_block 回传图片（实测 2048x2048）。
func (s *Server) handleImageGenerations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST", "invalid_request_error")
		return
	}
	start := time.Now()

	var req imageGenRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		writeErr(w, http.StatusBadRequest, "prompt 不能为空", "invalid_request_error")
		return
	}
	n := req.N
	if n <= 0 {
		n = 1
	}
	if n > 4 {
		n = 4
	}

	settings := s.Store.Settings()
	model := registry.Resolve(req.Model, settings.ModelAliases)
	key := extractKey(r)
	timeout := time.Duration(settings.RequestTimeout) * time.Second
	if timeout <= 0 {
		timeout = 300 * time.Second
	}

	upReq := doubao.ChatRequest{
		Text:       imageSkillPrompt(prompt, req.Style, req.Size),
		ThinkLevel: model.ThinkLevel,
		SkillID:    doubao.SkillImageGen,
	}

	seen := map[string]bool{}
	var urls []string
	acct, err := s.withAccountRetry(r.Context(), timeout, func(ctx context.Context, acct *doubao.Account) error {
		return s.Up.ChatStream(ctx, acct, upReq, func(c doubao.CompletionChunk) error {
			if c.ErrorCode != 0 {
				return fmt.Errorf("上游错误 %d: %s", c.ErrorCode, c.ErrorMsg)
			}
			for _, cr := range c.Creations {
				if cr.URL == "" || seen[cr.ID] {
					continue
				}
				seen[cr.ID] = true
				urls = append(urls, cr.URL)
			}
			return nil
		})
	})
	if err != nil {
		s.record(key, acct, model.ID, false, http.StatusBadGateway, start, len(prompt), 0, err)
		writeErr(w, http.StatusBadGateway, err.Error(), "upstream_error")
		return
	}
	if len(urls) == 0 {
		err := fmt.Errorf("上游未返回图片")
		s.record(key, acct, model.ID, false, http.StatusBadGateway, start, len(prompt), 0, err)
		writeErr(w, http.StatusBadGateway,
			"上游未返回图片（该账号可能无图像生成权限，或提示词未触发图像技能）", "upstream_error")
		return
	}
	if len(urls) > n {
		urls = urls[:n]
	}

	data := make([]map[string]any, 0, len(urls))
	for _, u := range urls {
		if req.ResponseFormat == "b64_json" {
			if b64, ferr := fetchAsBase64(r.Context(), u); ferr == nil {
				data = append(data, map[string]any{"b64_json": b64})
				continue
			}
		}
		data = append(data, map[string]any{"url": u})
	}

	s.record(key, acct, model.ID, false, http.StatusOK, start, len(prompt), len(urls), nil)
	writeJSON(w, http.StatusOK, map[string]any{
		"created": time.Now().Unix(),
		"data":    data,
	})
}

// imageSkillPrompt 套用桌面端「图像生成」技能的默认模板
// （/samantha/skill/list 中 skill_type=3 的 default_prompt 为
// "生成一张图片:${style} ${content}"）。
func imageSkillPrompt(prompt, style, size string) string {
	slot := strings.TrimSpace(style)
	if slot == "" {
		slot = ratioFromSize(size)
	}
	if slot == "" {
		return "生成一张图片: " + prompt
	}
	return "生成一张图片: " + slot + " " + prompt
}

// ratioFromSize 把 OpenAI 的 size 映射为豆包支持的宽高比（尽力而为）。
func ratioFromSize(size string) string {
	switch strings.ToLower(strings.TrimSpace(size)) {
	case "1792x1024", "1536x1024", "16:9":
		return "16:9"
	case "1024x1792", "1024x1536", "9:16":
		return "9:16"
	case "1024x1024", "512x512", "256x256", "2048x2048", "1:1":
		return "1:1"
	}
	return ""
}

// fetchAsBase64 取回生成结果并转为 base64（response_format=b64_json 时使用）。
func fetchAsBase64(ctx context.Context, url string) (string, error) {
	data, _, err := doubao.FetchImage(ctx, url)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}
