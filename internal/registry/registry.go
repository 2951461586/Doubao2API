// Package registry 维护对外暴露的模型清单与别名解析。
package registry

import (
	"strings"

	"doubao2api/internal/doubao"
)

// Model 是一个对外可用的模型。
type Model struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ThinkLevel int    `json:"think_level"`
	Context    int    `json:"context"`
	Desc       string `json:"description"`
	// Skill 非 0 时，选中该模型会自动套用对应技能（option.action_bar_skill_id）。
	Skill int `json:"skill,omitempty"`
}

// Builtin 是内置模型清单。
//
// 豆包客户端不区分底层模型，而是通过 need_deep_think 档位切换
// 快速 / 思考 / 专家三种模式，因此这里把档位映射为独立模型 ID；
// 另提供一组「技能模型」，选中即自动带上 action_bar_skill_id。
var Builtin = []Model{
	{
		ID: "doubao", Name: "豆包·快速", ThinkLevel: doubao.ThinkOff,
		Context: 128000, Desc: "默认模式，响应最快，无思维链",
	},
	{
		ID: "doubao-think", Name: "豆包·思考", ThinkLevel: doubao.ThinkOn,
		Context: 128000, Desc: "思考模式，返回思维链（reasoning_content）",
	},
	{
		ID: "doubao-expert", Name: "豆包·专家", ThinkLevel: doubao.ThinkExpert,
		Context: 256000, Desc: "专家模式，深度推理，适合复杂任务",
	},
	// 技能模型（对齐桌面端 SkillType）
	{ID: "doubao-draw", Name: "豆包·绘画", ThinkLevel: doubao.ThinkOff, Skill: doubao.SkillImageGen, Context: 32000, Desc: "图像生成技能（Seedream）"},
	{ID: "doubao-search", Name: "豆包·搜索", ThinkLevel: doubao.ThinkOn, Skill: doubao.SkillSearch, Context: 128000, Desc: "AI 搜索技能（联网）"},
	{ID: "doubao-write", Name: "豆包·写作", ThinkLevel: doubao.ThinkOn, Skill: doubao.SkillWrite, Context: 128000, Desc: "创意写作技能"},
	{ID: "doubao-translate", Name: "豆包·翻译", ThinkLevel: doubao.ThinkOff, Skill: doubao.SkillTranslate, Context: 64000, Desc: "翻译技能"},
	{ID: "doubao-read", Name: "豆包·阅读", ThinkLevel: doubao.ThinkOff, Skill: doubao.SkillRead, Context: 256000, Desc: "文档 / PDF 阅读技能"},
	{ID: "doubao-code", Name: "豆包·代码", ThinkLevel: doubao.ThinkOn, Skill: doubao.SkillCode, Context: 128000, Desc: "Coding 技能"},
	{ID: "doubao-data", Name: "豆包·数据分析", ThinkLevel: doubao.ThinkOn, Skill: doubao.SkillCodeInterpreter, Context: 128000, Desc: "代码解释器 / 数据分析技能"},
	{ID: "doubao-deep-search", Name: "豆包·深度搜索", ThinkLevel: doubao.ThinkExpert, Skill: doubao.SkillDeepSearch, Context: 256000, Desc: "多轮深度检索技能"},
	{ID: "doubao-research", Name: "豆包·深度研究", ThinkLevel: doubao.ThinkExpert, Skill: doubao.SkillDeepResearch, Context: 256000, Desc: "深度研究报告技能"},
	{ID: "doubao-ppt", Name: "豆包·PPT", ThinkLevel: doubao.ThinkOff, Skill: doubao.SkillAiPPT, Context: 64000, Desc: "AI PPT 技能"},
	{ID: "doubao-music", Name: "豆包·音乐", ThinkLevel: doubao.ThinkOff, Skill: doubao.SkillMusicGen, Context: 32000, Desc: "音乐生成技能（需账号配额）"},
	{ID: "doubao-video", Name: "豆包·视频", ThinkLevel: doubao.ThinkOff, Skill: doubao.SkillVideoGen, Context: 32000, Desc: "视频生成技能（需账号配额）"},
}

// aliases 是内置别名，指向内置模型 ID。
var aliases = map[string]string{
	"doubao-pro":        "doubao",
	"doubao-lite":       "doubao",
	"doubao-fast":       "doubao",
	"doubao-reason":     "doubao-think",
	"doubao-thinking":   "doubao-think",
	"doubao-max":        "doubao-expert",
	"doubao-deep":       "doubao-expert",
	"gpt-3.5-turbo":     "doubao",
	"gpt-4":             "doubao-think",
	"gpt-4o":            "doubao",
	"gpt-4o-mini":       "doubao",
	"claude-3-5-sonnet": "doubao-think",
	"gemini-pro":        "doubao",
	"doubao-image":      "doubao-draw",
	"doubao-txt2img":    "doubao-draw",
	"doubao-web":        "doubao-search",
	"doubao-coding":     "doubao-code",
	"doubao-python":     "doubao-data",
	"doubao-slides":     "doubao-ppt",
}

// Resolve 把用户传入的模型名解析为内置模型。
//
// 依次尝试：精确 ID → 自定义别名 → 内置别名 → 前缀/包含匹配。
// 无法识别时回落到 doubao（快速模式）。
func Resolve(name string, custom map[string]string) Model {
	n := strings.TrimSpace(strings.ToLower(name))
	if n == "" {
		return byID("doubao")
	}
	if m, ok := lookup(n); ok {
		return m
	}
	if target, ok := custom[n]; ok {
		if m, ok := lookup(strings.ToLower(target)); ok {
			return m
		}
	}
	if target, ok := aliases[n]; ok {
		return byID(target)
	}
	// 宽松匹配：任意包含 doubao 的名称
	for _, m := range Builtin {
		if strings.Contains(n, m.ID) {
			return m
		}
	}
	return byID("doubao")
}

func lookup(n string) (Model, bool) {
	for _, m := range Builtin {
		if m.ID == n {
			return m, true
		}
	}
	return Model{}, false
}

func byID(id string) Model {
	m, _ := lookup(id)
	return m
}

// List 返回对外模型清单（含自定义别名）。
func List(custom map[string]string) []Model {
	out := make([]Model, len(Builtin))
	copy(out, Builtin)
	for alias, target := range custom {
		if m, ok := lookup(strings.ToLower(target)); ok {
			m.ID = alias
			m.Name = alias + " → " + m.Name
			out = append(out, m)
		}
	}
	return out
}
