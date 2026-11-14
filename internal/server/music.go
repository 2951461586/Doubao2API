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

	model := registry.Resolve("", s.Store.Settings().ModelAliases)

	var url string
	if _, ok := s.runUpstream(w, r, upstreamCall{Label: model.ID, InLen: len(videoID)},
		func(ctx context.Context, acct *doubao.Account) (int, error) {
			u, err := s.Up.MusicAudio(ctx, acct, videoID)
			if err != nil {
				return 0, err
			}
			url = u
			return 1, nil
		}); !ok {
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"created":  time.Now().Unix(),
		"video_id": videoID,
		"url":      url,
	})
}

type lyricsRequest struct {
	Prompt string `json:"prompt"`
	Theme  string `json:"theme"`
	Genre  string `json:"genre"`
	Mood   string `json:"mood"`
	Lyric  string `json:"lyric"`
}

// handleMusicLyrics 是 POST /v1/music/lyrics 的入口。
//
// 上游：/alice/media/bigmusic/lyrics_web（带 genre/mood）或 /lyrics（仅 prompt）。

func (s *Server) handleMusicLyrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST", "invalid_request_error")
		return
	}
	var req lyricsRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	prompt := strings.TrimSpace(firstNonEmpty(req.Prompt, req.Theme, req.Lyric))
	if prompt == "" {
		writeErr(w, http.StatusBadRequest, "prompt 不能为空", "invalid_request_error")
		return
	}

	var lines []string
	if _, ok := s.runUpstream(w, r, upstreamCall{Label: "music-lyrics", InLen: len(prompt)},
		func(ctx context.Context, acct *doubao.Account) (int, error) {
			out, err := s.Up.MusicLyrics(ctx, acct, prompt, req.Genre, req.Mood)
			if err != nil {
				return 0, err
			}
			lines = out
			return len(out), nil
		}); !ok {
		return
	}
	data := make([]map[string]any, 0, len(lines))
	for i, line := range lines {
		data = append(data, map[string]any{"index": i, "text": line})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"created": time.Now().Unix(),
		"prompt":  prompt,
		"data":    data,
	})
}
