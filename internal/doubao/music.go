package doubao

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// 音乐相关常量。
//
// 音乐消息的 content_type（桌面端 ContentType 枚举）：
//
//	70 LyricsToSongMusic  音乐（成品）
//	71 LyricsToSongLyric  歌词
//	72 LyricsToSongsMusic 多首成品
const (
	ContentTypeLyricsToSongMusic  = 70
	ContentTypeLyricsToSongLyric  = 71
	ContentTypeLyricsToSongsMusic = 72
)

// MusicInputSkill 是音乐技能的 ext.input_skill 结构
// （来自桌面端 musicSkill 协议转换插件）。
type MusicInputSkill struct {
	Lyric          string `json:"lyric"`
	Theme          string `json:"theme"`
	Mood           string `json:"mood"`
	Genre          string `json:"genre"`
	Gender         string `json:"gender"`
	GenerationType string `json:"generation_type"`
}

// JSON 序列化为上游 ext.input_skill 所需的字符串。
func (m MusicInputSkill) JSON() string {
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(b)
}

// MusicAudio 用 music 消息里的 video_id 换取可播放 / 下载的音频地址。
//
// 音乐成品以 content_type=70 的消息回传，其 content_obj 携带 video_id；
// 客户端随后调用 /alice/media/bigmusic/get_video 取得真实地址
// （桌面端渲染器里的 fetchMusicAudioUrl 即此调用）。
func (c *Client) MusicAudio(ctx context.Context, a *Account, videoID string) (string, error) {
	if videoID == "" {
		return "", fmt.Errorf("video_id 不能为空")
	}
	body, err := json.Marshal(map[string]any{"video_id": videoID})
	if err != nil {
		return "", err
	}
	u := UpstreamBase + "/alice/media/bigmusic/get_video?" + SecurityParams(a).Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	setUpstreamHeaders(req, a)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("获取音乐地址失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("获取音乐地址 HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("获取音乐地址响应解析失败: %s", truncate(string(raw), 200))
	}
	if out.Code != 0 {
		return "", fmt.Errorf("获取音乐地址被拒绝 (code=%d): %s", out.Code, truncate(out.Msg, 200))
	}
	if out.Data.URL == "" {
		return "", fmt.Errorf("未返回音频地址")
	}
	return out.Data.URL, nil
}
