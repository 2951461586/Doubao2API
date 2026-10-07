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

// videoGenRequest 是文生视频请求体。
type videoGenRequest struct {
	Model    string `json:"model"`
	Prompt   string `json:"prompt"`
	Duration int    `json:"duration"` // 秒
	Ratio    string `json:"ratio"`    // 16:9 / 9:16 / 1:1
}

// videoGenTimeout 是文生视频的最小超时（生成实测约 2 分钟，留足余量）。
const videoGenTimeout = 15 * time.Minute

// handleVideoGenerations 是 POST /v1/videos/generations 的入口。
//
// 豆包的文生视频不在聊天技能链路上（`skill=17` 会走「参数确认卡」并受会员配额限制，
// 实测返回 710022004 rate limited），而是走「创意画布」执行接口：
//
//	POST /creativity/canvas/exec  canvas_type=50 (GenVideo)
//
// 该接口按 request_id 幂等，重复提交即轮询，完成后返回
// result.artifacts[0].video（download_url / vid / 时长 / 分辨率）。
func (s *Server) handleVideoGenerations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST", "invalid_request_error")
		return
	}
	start := time.Now()

	var req videoGenRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		writeErr(w, http.StatusBadRequest, "prompt 不能为空", "invalid_request_error")
		return
	}

	settings := s.Store.Settings()
	model := registry.Resolve(req.Model, settings.ModelAliases)
	key := extractKey(r)

	// 视频生成远慢于对话，单独给足超时
	timeout := time.Duration(settings.RequestTimeout) * time.Second
	if timeout < videoGenTimeout {
		timeout = videoGenTimeout
	}

	upReq := doubao.VideoRequest{
		Prompt:   prompt,
		Duration: req.Duration,
		Ratio:    req.Ratio,
	}

	var result *doubao.VideoResult
	acct, err := s.withAccountRetry(r.Context(), timeout, func(ctx context.Context, acct *doubao.Account) error {
		res, err := s.Up.GenerateVideo(ctx, acct, upReq, nil)
		if err != nil {
			return err
		}
		result = res
		return nil
	})
	if err != nil {
		s.record(key, acct, model.ID, false, http.StatusBadGateway, start, len(prompt), 0, err)
		writeErr(w, http.StatusBadGateway, err.Error(), "upstream_error")
		return
	}
	if result == nil || result.URL == "" {
		err := fmt.Errorf("上游未返回视频地址")
		s.record(key, acct, model.ID, false, http.StatusBadGateway, start, len(prompt), 0, err)
		writeErr(w, http.StatusBadGateway, err.Error(), "upstream_error")
		return
	}

	s.record(key, acct, model.ID, false, http.StatusOK, start, len(prompt), 1, nil)
	writeJSON(w, http.StatusOK, map[string]any{
		"created": time.Now().Unix(),
		"data": []any{map[string]any{
			"url":      result.URL,
			"vid":      result.VID,
			"duration": result.Duration,
			"width":    result.Width,
			"height":   result.Height,
		}},
	})
}
