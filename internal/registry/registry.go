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
}

// Builtin 是内置模型清单。
//
// 豆包客户端不区分底层模型，而是通过 need_deep_think 档位切换
// 快速 / 思考 / 专家三种模式，因此这里把档位映射为独立模型 ID。
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
}

// aliases 是内置别名，指向内置模型 ID。
var aliases = map[string]string{
	"doubao-pro":      "doubao",
	"doubao-lite":     "doubao",
	"doubao-fast":     "doubao",
	"doubao-reason":   "doubao-think",
	"doubao-thinking": "doubao-think",
	"doubao-max":      "doubao-expert",
	"doubao-deep":     "doubao-expert",
	"gpt-3.5-turbo":   "doubao",
	"gpt-4":           "doubao-think",
	"gpt-4o":          "doubao",
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
