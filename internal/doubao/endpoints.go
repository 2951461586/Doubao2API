package doubao

import (
	"context"
	"fmt"
)

// Conversation 是一条会话（已归一化）。
type Conversation struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	BotID        string `json:"bot_id,omitempty"`
	SectionID    string `json:"last_section_id,omitempty"`
	Type         int    `json:"conversation_type,omitempty"`
	UpdateTime   int64  `json:"update_time,omitempty"`
	Pinned       bool   `json:"pinned"`
	IconURL      string `json:"icon_url,omitempty"`
	MessageIndex int    `json:"message_index,omitempty"`
}

// ConversationPage 是会话列表的一页。
type ConversationPage struct {
	Conversations []Conversation `json:"data"`
	HasMore       bool           `json:"has_more"`
}

// Conversations 拉取会话列表。
//
// 上游：POST /alice/conversation/list {index, batch_size, conversation_types}
func (c *Client) Conversations(ctx context.Context, a *Account, index, batchSize int, types []int) (*ConversationPage, error) {
	if index < 0 {
		index = 0
	}
	if batchSize <= 0 {
		batchSize = 20
	}
	if batchSize > 50 {
		batchSize = 50
	}
	body := map[string]any{"index": index, "batch_size": batchSize}
	if len(types) > 0 {
		body["conversation_types"] = types
	}
	raw, err := c.postJSON(ctx, a, "/alice/conversation/list", body)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			HasMore bool `json:"has_more"`
			List    []struct {
				ConversationID string `json:"conversation_id"`
				Name           string `json:"name"`
				BotID          string `json:"bot_id"`
				LastSectionID  string `json:"last_section_id"`
				ConversationTy int    `json:"conversation_type"`
				UpdateTime     int64  `json:"update_time"`
				Pinned         bool   `json:"pinned"`
				IconURL        string `json:"icon_url"`
				MessageIndex   int    `json:"message_index"`
			} `json:"conversation_list"`
		} `json:"data"`
	}
	if err := unmarshalJSON(raw, &resp); err != nil {
		return nil, fmt.Errorf("会话列表解析失败: %s", truncate(string(raw), 200))
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("会话列表获取失败 (code=%d): %s", resp.Code, truncate(firstNonEmpty(resp.Msg, string(raw)), 200))
	}
	out := &ConversationPage{HasMore: resp.Data.HasMore}
	for _, it := range resp.Data.List {
		out.Conversations = append(out.Conversations, Conversation{
			ID:           it.ConversationID,
			Name:         it.Name,
			BotID:        it.BotID,
			SectionID:    it.LastSectionID,
			Type:         it.ConversationTy,
			UpdateTime:   it.UpdateTime,
			Pinned:       it.Pinned,
			IconURL:      it.IconURL,
			MessageIndex: it.MessageIndex,
		})
	}
	return out, nil
}

// ConversationAction 对会话执行操作：rename / pin / unpin / archive / clear。
func (c *Client) ConversationAction(ctx context.Context, a *Account, action, conversationID, name string) error {
	if conversationID == "" {
		return fmt.Errorf("conversation_id 不能为空")
	}
	var path string
	body := map[string]any{"conversation_id": conversationID}
	switch action {
	case "rename":
		if name == "" {
			return fmt.Errorf("name 不能为空")
		}
		path = "/alice/conversation/update"
		body["name"] = name
	case "pin":
		path = "/alice/conversation/pin"
	case "unpin":
		path = "/alice/conversation/unpin"
	case "archive":
		path = "/alice/conversation/archive"
	case "clear":
		path = "/alice/conversation/clear"
	default:
		return fmt.Errorf("不支持的会话操作: %s", action)
	}
	raw, err := c.postJSON(ctx, a, path, body)
	if err != nil {
		return err
	}
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := unmarshalJSON(raw, &resp); err != nil {
		return fmt.Errorf("会话操作响应解析失败: %s", truncate(string(raw), 200))
	}
	if resp.Code != 0 {
		return fmt.Errorf("会话操作被拒绝 (code=%d): %s", resp.Code, truncate(firstNonEmpty(resp.Msg, string(raw)), 200))
	}
	return nil
}

// MusicLyrics 生成歌词。
//
// 带 genre/mood 时走 /alice/media/bigmusic/lyrics_web，否则走
// /alice/media/bigmusic/lyrics（可传 instruction_options 微调）。
func (c *Client) MusicLyrics(ctx context.Context, a *Account, prompt, genre, mood string) ([]string, error) {
	if prompt == "" {
		return nil, fmt.Errorf("prompt 不能为空")
	}
	path := "/alice/media/bigmusic/lyrics"
	body := map[string]any{"prompt": prompt}
	if genre != "" || mood != "" {
		path = "/alice/media/bigmusic/lyrics_web"
		body = map[string]any{"prompt": prompt, "genre": genre, "mood": mood}
	}
	raw, err := c.postJSON(ctx, a, path, body)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			LyricsList []string `json:"lyrics_list"`
		} `json:"data"`
	}
	if err := unmarshalJSON(raw, &resp); err != nil {
		return nil, fmt.Errorf("歌词响应解析失败: %s", truncate(string(raw), 200))
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("歌词生成失败 (code=%d): %s", resp.Code, truncate(firstNonEmpty(resp.Msg, string(raw)), 200))
	}
	if len(resp.Data.LyricsList) == 0 {
		return nil, fmt.Errorf("上游未返回歌词")
	}
	return resp.Data.LyricsList, nil
}
