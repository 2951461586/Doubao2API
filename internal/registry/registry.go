// Package registry 维护对外暴露的模型清单与别名解析。
package registry

import (
	"slices"
	"strings"

	"doubao2api/internal/doubao"
)

// DefaultID 是无法识别时回落的内置模型（快速模式）。
const DefaultID = "doubao"

// maxAliasDepth 限制自定义别名链的长度，防止环或超长链拖慢解析。
const maxAliasDepth = 8

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
// 依次尝试：精确 ID → 自定义别名（可链式）→ 内置别名 → 最长前缀/包含匹配。
// 无法识别时回落到 doubao（快速模式）。
func Resolve(name string, custom map[string]string) Model {
	n := normalize(name)
	if n == "" {
		return byID(DefaultID)
	}
	if m, ok := lookup(n); ok {
		return m
	}
	cm := normalizeCustom(custom)
	if m, ok := resolveCustom(n, cm); ok {
		return m
	}
	if target, ok := aliases[n]; ok {
		return byID(target)
	}
	if m, ok := matchLongest(n, cm); ok {
		return m
	}
	return byID(DefaultID)
}

// normalizeCustom 把自定义别名表归一化（键小写去空格、丢弃空项）。
//
// 用户在控制台里可能把别名写成大驼峰，解析时必须与请求里的模型名
// 走同一套归一化规则。
func normalizeCustom(custom map[string]string) map[string]string {
	if len(custom) == 0 {
		return nil
	}
	out := make(map[string]string, len(custom))
	for k, v := range custom {
		key := normalize(k)
		if key == "" {
			continue
		}
		out[key] = v
	}
	return out
}

// resolveCustom 解析自定义别名，支持别名指向另一个别名。
//
// 带环路保护（seen）与深度上限，避免用户配置出 a→b→a 时死循环。
func resolveCustom(n string, custom map[string]string) (Model, bool) {
	seen := map[string]bool{n: true}
	cur := n
	for i := 0; i < maxAliasDepth; i++ {
		target, ok := custom[cur]
		if !ok {
			return Model{}, false
		}
		target = normalize(target)
		if m, ok := lookup(target); ok {
			return m, true
		}
		if seen[target] {
			return Model{}, false
		}
		seen[target] = true
		cur = target
	}
	return Model{}, false
}

// matchLongest 做宽松匹配：在所有已知名字里取「最长」的包含匹配。
//
// 取最长而不是按清单顺序首个命中：否则 "doubao-think-pro" 会被 "doubao"
// 抢先命中，结果是快速模式（历史 bug）。
func matchLongest(n string, custom map[string]string) (Model, bool) {
	type candidate struct {
		key string
		m   Model
	}
	cands := make([]candidate, 0, len(Builtin)+len(aliases)+len(custom))
	for _, m := range Builtin {
		cands = append(cands, candidate{m.ID, m})
	}
	for alias, target := range aliases {
		if m, ok := lookup(target); ok {
			cands = append(cands, candidate{alias, m})
		}
	}
	names := make([]string, 0, len(custom))
	for alias := range custom {
		names = append(names, alias)
	}
	slices.Sort(names)
	for _, alias := range names {
		if m, ok := resolveCustom(alias, custom); ok {
			m.ID = alias
			m.Name = alias + " → " + m.Name
			cands = append(cands, candidate{alias, m})
		}
	}
	// 最长优先；等长时按名字字典序，保证结果稳定。
	slices.SortFunc(cands, func(a, b candidate) int {
		if len(a.key) != len(b.key) {
			return len(b.key) - len(a.key)
		}
		return strings.Compare(a.key, b.key)
	})
	for _, c := range cands {
		if strings.Contains(n, c.key) {
			return c.m, true
		}
	}
	return Model{}, false
}

// List 返回对外模型清单（含自定义别名）。
//
// 自定义别名按名字排序后追加，保证 /v1/models 的输出顺序稳定
// （map 遍历顺序随机，曾导致响应顺序抖动）。
func List(custom map[string]string) []Model {
	out := slices.Clone(Builtin)
	cm := normalizeCustom(custom)
	names := make([]string, 0, len(cm))
	for alias := range cm {
		names = append(names, alias)
	}
	slices.Sort(names)
	for _, alias := range names {
		if _, ok := lookup(alias); ok {
			continue // 与内置 ID 同名，避免清单里出现重复 ID
		}
		m, ok := resolveCustom(alias, cm)
		if !ok {
			continue
		}
		m.ID = alias
		m.Name = alias + " → " + m.Name
		out = append(out, m)
	}
	return out
}

func normalize(s string) string { return strings.TrimSpace(strings.ToLower(s)) }

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
