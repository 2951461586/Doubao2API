package registry

import (
	"testing"

	"doubao2api/internal/doubao"
)

func TestBuiltinIDsUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, m := range Builtin {
		if m.ID == "" {
			t.Fatal("内置模型 ID 不能为空")
		}
		if seen[m.ID] {
			t.Fatalf("内置模型 ID 重复: %s", m.ID)
		}
		seen[m.ID] = true
	}
}

// TestAliasTargetsExist 保证内置别名表不会指向已被删除的模型。
func TestAliasTargetsExist(t *testing.T) {
	for alias, target := range aliases {
		if _, ok := lookup(target); !ok {
			t.Errorf("别名 %q 指向不存在的模型 %q", alias, target)
		}
	}
}

// TestResolveExact 精确 ID 优先于一切其它规则。
func TestResolveExact(t *testing.T) {
	for _, want := range Builtin {
		if got := Resolve(want.ID, nil); got.ID != want.ID {
			t.Errorf("Resolve(%q) = %q, want %q", want.ID, got.ID, want.ID)
		}
	}
	// 大小写 / 前后空格归一化
	if got := Resolve("  Doubao-Expert  ", nil); got.ID != "doubao-expert" {
		t.Errorf("大小写与空格应归一化，实际 %q", got.ID)
	}
}

func TestResolveBuiltinAlias(t *testing.T) {
	cases := map[string]string{
		"doubao-pro":        "doubao",
		"doubao-reason":     "doubao-think",
		"doubao-max":        "doubao-expert",
		"gpt-4o":            "doubao",
		"claude-3-5-sonnet": "doubao-think",
		"doubao-image":      "doubao-draw",
		"doubao-slides":     "doubao-ppt",
	}
	for in, want := range cases {
		if got := Resolve(in, nil); got.ID != want {
			t.Errorf("Resolve(%q) = %q, want %q", in, got.ID, want)
		}
	}
}

func TestResolveCustomAlias(t *testing.T) {
	custom := map[string]string{
		"my-model":   "doubao-expert",
		"MY-ALIAS":   "doubao-draw",
		"chain-a":    "chain-b",
		"chain-b":    "doubao-think",
		"cycle-a":    "cycle-b",
		"cycle-b":    "cycle-a",
		"broken":     "no-such-model",
		"self":       "self",
		"doubao-ppt": "doubao-code", // 与内置 ID 同名：精确 ID 优先
	}
	cases := map[string]string{
		"my-model":   "doubao-expert",
		"my-alias":   "doubao-draw",
		"chain-a":    "doubao-think", // 别名链
		"cycle-a":    DefaultID,      // 环路保护 → 回落
		"broken":     DefaultID,      // 指向不存在的模型 → 回落
		"self":       DefaultID,      // 自指 → 回落
		"doubao-ppt": "doubao-ppt",   // 精确 ID 优先于自定义别名
	}
	for in, want := range cases {
		if got := Resolve(in, custom); got.ID != want {
			t.Errorf("Resolve(%q, custom) = %q, want %q", in, got.ID, want)
		}
	}
}

// TestResolveLongestMatch 锁死最长匹配：短 ID 不得抢先命中。
//
// 历史 bug：宽松匹配按 Builtin 顺序取首个 strings.Contains，
// "doubao-think-pro" 会命中 "doubao"（快速模式）。
func TestResolveLongestMatch(t *testing.T) {
	cases := map[string]string{
		"doubao-think-pro":     "doubao-think",
		"doubao-expert-latest": "doubao-expert",
		"doubao-deep-search-x": "doubao-deep-search",
		"openai/doubao-video":  "doubao-video",
		"doubao-image-v2":      "doubao-draw", // 经内置别名 doubao-image
		"whatever-doubao":      "doubao",
	}
	for in, want := range cases {
		if got := Resolve(in, nil); got.ID != want {
			t.Errorf("Resolve(%q) = %q, want %q（最长匹配）", in, got.ID, want)
		}
	}
}

func TestResolveFallback(t *testing.T) {
	for _, in := range []string{"", "   ", "totally-unknown-model"} {
		if got := Resolve(in, nil); got.ID != DefaultID {
			t.Errorf("Resolve(%q) = %q, want %q", in, got.ID, DefaultID)
		}
	}
	if got := Resolve("unknown", map[string]string{"other": "doubao-think"}); got.ID != DefaultID {
		t.Errorf("未知模型应回落 %q，实际 %q", DefaultID, got.ID)
	}
}

// TestResolveSkillModels 技能模型必须带出对应 skill id（透传链路的起点）。
func TestResolveSkillModels(t *testing.T) {
	for _, m := range Builtin {
		got := Resolve(m.ID, nil)
		if got.Skill != m.Skill {
			t.Errorf("Resolve(%q).Skill = %d, want %d", m.ID, got.Skill, m.Skill)
		}
	}
	if got := Resolve("doubao-draw", nil); got.Skill != doubao.SkillImageGen {
		t.Errorf("doubao-draw 应触发图像生成技能，实际 %d", got.Skill)
	}
}

// TestListStableAndDeduped 验证 /v1/models 的输出顺序稳定且无重复 ID。
func TestListStableAndDeduped(t *testing.T) {
	custom := map[string]string{
		"zeta":       "doubao-think",
		"alpha":      "doubao",
		"doubao-ppt": "doubao-code", // 与内置同名 → 过滤
		"mid":        "doubao-expert",
	}
	first := List(custom)
	for i := 0; i < 20; i++ {
		if got := List(custom); len(got) != len(first) {
			t.Fatalf("List 长度不稳定: %d vs %d", len(got), len(first))
		} else {
			for j := range got {
				if got[j].ID != first[j].ID {
					t.Fatalf("List 顺序不稳定：第 %d 项 %q vs %q", j, got[j].ID, first[j].ID)
				}
			}
		}
	}

	seen := map[string]bool{}
	for _, m := range first {
		if seen[m.ID] {
			t.Errorf("List 出现重复模型 ID: %s", m.ID)
		}
		seen[m.ID] = true
	}
	if !seen["alpha"] || !seen["mid"] || !seen["zeta"] {
		t.Errorf("自定义别名缺失: %v", seen)
	}

	// 自定义别名追加在内置清单之后，并按名字排序。
	tail := first[len(Builtin):]
	want := []string{"alpha", "mid", "zeta"}
	if len(tail) != len(want) {
		t.Fatalf("自定义别名数量 = %d, want %d", len(tail), len(want))
	}
	for i := range want {
		if tail[i].ID != want[i] {
			t.Errorf("第 %d 个自定义别名 = %q, want %q", i, tail[i].ID, want[i])
		}
	}
	if got := Resolve(tail[0].ID, custom); got.ID != "doubao" {
		t.Errorf("清单中的别名应可解析回内置模型，实际 %q", got.ID)
	}
}

func TestListWithoutCustom(t *testing.T) {
	got := List(nil)
	if len(got) != len(Builtin) {
		t.Fatalf("List(nil) 长度 = %d, want %d", len(got), len(Builtin))
	}
	// 返回副本：修改结果不应污染 Builtin。
	got[0].ID = "mutated"
	if Builtin[0].ID == "mutated" {
		t.Error("List 应返回 Builtin 的副本")
	}
}
