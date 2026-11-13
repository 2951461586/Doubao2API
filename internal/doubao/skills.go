package doubao

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// unmarshalJSON 是带 UseNumber 的通用解码入口（保留 int64 精度）。
func unmarshalJSON(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(v)
}

// postJSON 调用一个上游 JSON 接口，返回原始响应体。
//
// 自动附加公参与公共请求头（含 Agw-Js-Conv / CSRF / Cookie）；
// 非 200 会返回带响应片段的错误。
func (c *Client) postJSON(ctx context.Context, a *Account, path string, body any) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(raw)
	}
	u := UpstreamBase + path
	if q := SecurityParams(a).Encode(); q != "" {
		u += "?" + q
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, rd)
	if err != nil {
		return nil, err
	}
	setUpstreamHeaders(req, a)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求上游失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("上游 HTTP %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	return raw, nil
}

// SkillMeta 是本地维护的技能元数据（名称 / 别名 / 默认提示词模板）。
//
// default_prompt 取自上游 /samantha/skill/list（只有部分技能带模板），
// ${content} 用用户输入替换，${style} 用风格 / 比例替换。
type SkillMeta struct {
	Type          int      `json:"skill_type"`
	Name          string   `json:"name"`
	DefaultPrompt string   `json:"default_prompt,omitempty"`
	Aliases       []string `json:"aliases,omitempty"`
	Desc          string   `json:"description,omitempty"`
}

// SkillMetas 是内置技能清单（对齐桌面端 SkillType 枚举与 /samantha/skill/list）。
var SkillMetas = []SkillMeta{
	{Type: SkillCode, Name: "Coding", Aliases: []string{"code", "coding", "code_assistant"}, Desc: "编程问答与代码生成"},
	{Type: SkillReadSummary, Name: "阅读总结", Aliases: []string{"read", "read_summary", "summary"}, DefaultPrompt: "阅读总结以下 ${content}", Desc: "阅读网页 / 文档并总结"},
	{Type: SkillWrite, Name: "创意写作", Aliases: []string{"write", "writing", "creative_writing"}, Desc: "创意写作与内容生成"},
	{Type: SkillImageGen, Name: "图像生成", Aliases: []string{"image", "image_gen", "draw", "txt2img"}, DefaultPrompt: "生成一张图片:${style} ${content}", Desc: "文生图（Seedream）"},
	{Type: SkillSearch, Name: "AI 搜索", Aliases: []string{"search", "ai_search"}, DefaultPrompt: "搜索一下: ${content}", Desc: "联网搜索并总结"},
	{Type: SkillTranslate, Name: "翻译", Aliases: []string{"translate", "translation"}, Desc: "多语言翻译"},
	{Type: SkillRead, Name: "AI 阅读", Aliases: []string{"ai_read", "document_read"}, Desc: "PDF / 文档阅读"},
	{Type: SkillWebpage, Name: "网页摘要", Aliases: []string{"webpage", "web_summary"}, Desc: "网页摘要"},
	{Type: SkillDeepSearch, Name: "深度搜索", Aliases: []string{"deep_search", "deepsearch"}, Desc: "多轮深度检索"},
	{Type: SkillMusicGen, Name: "音乐生成", Aliases: []string{"music", "music_gen"}, Desc: "歌词 / 歌曲生成"},
	{Type: SkillAcademicSearch, Name: "学术搜索", Aliases: []string{"academic", "paper_search"}, Desc: "论文 / 学术检索"},
	{Type: SkillEdu, Name: "拍照搜题", Aliases: []string{"edu", "photo_search"}, Desc: "题目识别与讲解"},
	{Type: SkillReadMulti, Name: "多网页阅读", Aliases: []string{"read_multi"}, Desc: "多网页聚合阅读"},
	{Type: SkillCodeInterpreter, Name: "代码解释器", Aliases: []string{"code_interpreter", "data_analysis", "python"}, Desc: "数据分析与代码执行"},
	{Type: SkillReferenceAsk, Name: "引用追问", Aliases: []string{"reference_ask"}, Desc: "基于引用内容追问"},
	{Type: SkillDeepResearch, Name: "深度研究", Aliases: []string{"deep_research", "research"}, Desc: "深度研究报告"},
	{Type: SkillCodeAssistant, Name: "代码助手", Aliases: []string{"code_assistant_agent"}, Desc: "代码工程助手"},
	{Type: SkillVideoGen, Name: "视频生成", Aliases: []string{"video", "video_gen"}, Desc: "文生视频（Seedance）"},
	{Type: SkillAiPPT, Name: "AI PPT", Aliases: []string{"ppt", "ai_ppt", "slides"}, Desc: "PPT 生成"},
	{Type: SkillMediaASR, Name: "音视频理解", Aliases: []string{"asr", "media_understanding"}, Desc: "音视频转写与理解"},
	{Type: SkillApiCall, Name: "API 调用", Aliases: []string{"api"}, Desc: "API 调用技能"},
	{Type: SkillPptGen, Name: "PPT 生成", Aliases: []string{"ppt_gen"}, Desc: "PPT 生成（新）"},
	{Type: SkillPodcast, Name: "AI 播客", Aliases: []string{"podcast"}, Desc: "播客生成"},
	{Type: SkillLocalFileQA, Name: "本地文件问答", Aliases: []string{"local_file"}, Desc: "本地文件问答"},
	{Type: SkillTranslateV2, Name: "翻译（新版）", Aliases: []string{"translate_v2"}, Desc: "翻译技能（新版）"},
}

// LookupSkillMeta 按类型或名称 / 别名查找技能元数据。
func LookupSkillMeta(v any) (SkillMeta, bool) {
	switch t := v.(type) {
	case int:
		return skillMetaByType(t)
	case float64:
		return skillMetaByType(int(t))
	case string:
		s := strings.ToLower(strings.TrimSpace(t))
		if s == "" {
			return SkillMeta{}, false
		}
		for _, m := range SkillMetas {
			if strings.ToLower(m.Name) == s {
				return m, true
			}
			for _, a := range m.Aliases {
				if a == s {
					return m, true
				}
			}
		}
	}
	return SkillMeta{}, false
}

func skillMetaByType(t int) (SkillMeta, bool) {
	for _, m := range SkillMetas {
		if m.Type == t {
			return m, true
		}
	}
	return SkillMeta{}, false
}

// SkillName 返回技能名称（未知时返回 "skill:<type>"）。
func SkillName(t int) string {
	if m, ok := skillMetaByType(t); ok {
		return m.Name
	}
	return fmt.Sprintf("skill:%d", t)
}

// WrapSkillPrompt 套用技能的默认提示词模板。
//
// 桌面端只有部分技能带 default_prompt（如图像生成、AI 搜索、阅读总结）；
// 没有模板或提示词已经是模板形态时原样返回。
func WrapSkillPrompt(skillType int, prompt, style string) string {
	m, ok := skillMetaByType(skillType)
	if !ok || m.DefaultPrompt == "" {
		return prompt
	}
	tpl := m.DefaultPrompt
	// 已带模板前缀（如 "生成一张图片:"）时不重复包裹。
	if idx := strings.Index(tpl, "${content}"); idx >= 0 {
		prefix := strings.ReplaceAll(tpl[:idx], "${style}", "")
		if prefix = strings.TrimSpace(prefix); prefix != "" && strings.Contains(prompt, prefix) {
			return prompt
		}
	}
	out := strings.ReplaceAll(tpl, "${style}", strings.TrimSpace(style))
	out = strings.ReplaceAll(out, "${content}", prompt)
	return strings.TrimSpace(out)
}

// Skill 是 /samantha/skill/list 返回的单个技能。
type Skill struct {
	SkillType     int    `json:"skill_type"`
	Name          string `json:"name,omitempty"`
	ShowNameShort string `json:"show_name_short,omitempty"`
	Description   string `json:"description,omitempty"`
	DefaultPrompt string `json:"default_prompt,omitempty"`
}

// SkillList 是 /samantha/skill/list 的归一化结果。
type SkillList struct {
	Skills     []Skill `json:"skills"`
	Input      []int   `json:"input_skill_types"`
	Onboarding []int   `json:"onboarding_skill_types"`
	Sidebar    []int   `json:"sidebar_skill_types"`
	HideInLP   bool    `json:"hide_in_lp"`
}

// Skills 拉取上游技能清单。
func (c *Client) Skills(ctx context.Context, a *Account) (*SkillList, error) {
	body := map[string]any{
		"desktop_channel": a.ChannelValue(),
		"source_channel":  "sidebar",
	}
	raw, err := c.postJSON(ctx, a, "/samantha/skill/list", body)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			HideInLP bool `json:"hide_in_lp"`
			Input    []struct {
				SkillType int `json:"skill_type"`
			} `json:"input_skill"`
			Onboarding []struct {
				SkillType int `json:"skill_type"`
			} `json:"onboarding_skill"`
			Sidebar []struct {
				SkillType int `json:"skill_type"`
			} `json:"sidebar_skill"`
			Skills []Skill `json:"skills"`
		} `json:"data"`
	}
	if err := unmarshalJSON(raw, &resp); err != nil {
		return nil, fmt.Errorf("技能清单解析失败: %s", truncate(string(raw), 200))
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("技能清单获取失败 (code=%d): %s", resp.Code, truncate(firstNonEmpty(resp.Msg, string(raw)), 200))
	}
	out := &SkillList{HideInLP: resp.Data.HideInLP, Skills: resp.Data.Skills}
	for _, it := range resp.Data.Input {
		out.Input = append(out.Input, it.SkillType)
	}
	for _, it := range resp.Data.Onboarding {
		out.Onboarding = append(out.Onboarding, it.SkillType)
	}
	for _, it := range resp.Data.Sidebar {
		out.Sidebar = append(out.Sidebar, it.SkillType)
	}
	return out, nil
}
