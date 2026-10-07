package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"doubao2api/internal/doubao"
	"doubao2api/internal/registry"
)

// musicAudioRequest 是音乐音频地址解析请求。
type musicAudioRequest struct {
	VideoID string `json:"video_id"`
}

// handleMusicAudio 是 POST /v1/music/audio 的入口。
//
// 音乐成品以 `content_type=70`（LyricsToSongMusic）的消息回传，其 `content_obj`
// 携带 `video_id`；本接口用该 id 换取可播放 / 下载的音频地址
// （上游 `/alice/media/bigmusic/get_video`，实测该接口可用）。
//
// 注意：本网关**不代生成音乐**。上游把音乐技能（`skill=9`）放在会员配额门禁之后，
// 实测固定返回 `710022004 rate limited`（`shark_admin`），因此网关无法自行取得
// `video_id`；有配额的账号可自行从聊天流里拿到 `video_id` 再调用本接口。
func (s *Server) handleMusicAudio(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST", "invalid_request_error")
		return
	}
	start := time.Now()

	var req musicAudioRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	videoID := strings.TrimSpace(req.VideoID)
	if videoID == "" {
		writeErr(w, http.StatusBadRequest, "video_id 不能为空", "invalid_request_error")
		return
	}

	settings := s.Store.Settings()
	model := registry.Resolve("", settings.ModelAliases)
	key := extractKey(r)
	timeout := time.Duration(settings.RequestTimeout) * time.Second
	if timeout <= 0 {
		timeout = 300 * time.Second
	}

	var url string
	acct, err := s.withAccountRetry(r.Context(), timeout, func(ctx context.Context, acct *doubao.Account) error {
		u, err := s.Up.MusicAudio(ctx, acct, videoID)
		if err != nil {
			return err
		}
		url = u
		return nil
	})
	if err != nil {
		s.record(key, acct, model.ID, false, http.StatusBadGateway, start, len(videoID), 0, err)
		writeErr(w, http.StatusBadGateway, err.Error(), "upstream_error")
		return
	}

	s.record(key, acct, model.ID, false, http.StatusOK, start, len(videoID), 1, nil)
	writeJSON(w, http.StatusOK, map[string]any{
		"created":  time.Now().Unix(),
		"video_id": videoID,
		"url":      url,
	})
}
