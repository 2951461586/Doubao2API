package registry

import (
	"strings"
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
		"doubao-pro":        "doubao-2.1-pro",
		"doubao-lite":       "doubao-2.1-lite",
		"doubao-auto":       "auto",
		"doubao-reason":     "doubao-think",
		"doubao-max":        "doubao-expert",
		"gpt-4o":            "doubao",
		"claude-3-5-sonnet": "doubao-think",
		"doubao-image":      "doubao-draw",
		"doubao-slides":     "doubao-ppt",
		"glm":               "glm-5.3",
		"glm-flash":         "glm-5.3-flash",
		"kimi-fast":         "kimi-k3.1-pro-fast",
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

// TestModelCatalogConsistency 是「模型名实相符」的守卫。
//
// 线上教训（issues.md #6）：deepseek-v4-pro 的展示名写着 V4.1，但上游键是
// person-deepseek-v4-pro-ga-260813（GA 版）。用户按名字选模型、按名字计费，
// 名实不符时很难察觉。这里强制「名字里的版本号」与「键里的版本号」一致。
func TestModelCatalogConsistency(t *testing.T) {
	seen := map[string]bool{}
	for _, m := range Builtin {
		if m.ID == "" {
			t.Errorf("模型缺少 ID: %+v", m)
		}
		if seen[m.ID] {
			t.Errorf("模型 ID 重复: %s", m.ID)
		}
		seen[m.ID] = true
		if m.ThinkLevel < 0 || m.ThinkLevel > 3 {
			t.Errorf("%s: think_level 越界: %d", m.ID, m.ThinkLevel)
		}
		if m.ModelItemKey == "" {
			continue
		}
		// 只有工作模式模型带 model_item_key。
		if m.AgentMode != 1 {
			t.Errorf("%s: 带 model_item_key 但不是工作模式（agent_mode=%d）", m.ID, m.AgentMode)
		}
		nameV41 := strings.Contains(m.Name, "V4.1")
		keyV41 := strings.Contains(m.ModelItemKey, "v4-1")
		if nameV41 != keyV41 {
			t.Errorf("%s: 名称 %q 与上游键 %q 的版本号不一致", m.ID, m.Name, m.ModelItemKey)
		}
	}
}

// TestModelCreatedAtStable 保证 /v1/models 的 created 可缓存。
func TestModelCreatedAtStable(t *testing.T) {
	if CreatedAt <= 0 {
		t.Fatalf("CreatedAt 必须是一个固定的正数时间戳，实际 %d", CreatedAt)
	}
}

// TestRegistryCoversClientModels 锁定「与桌面端模型清单对齐」。
//
// 数据来源：桌面端 Preferences 里的
// `_DesktopActionBarBriefListRequestCacheDataV1.data.modeSelectData`
// （2026-11-14 从本机 Profile 2/3 缓存读取，见 `doubao2bao/internal/doubao/client_models.go`）。
//
// 客户端升级后清单可能变化：用 `doubao2api-probe -client-models` 做差异比对，
// 确认后再更新这里的期望值与 Builtin。
func TestRegistryCoversClientModels(t *testing.T) {
	want := []struct {
		name    string
		key     string
		context int
	}{
		{"自动", "9", 256000},
		{"豆包 2.1 Lite", "seed-lite-7b", 256000},
		{"豆包 2.1 Pro", "5", 256000},
		{"DeepSeek V4.1 Flash", "person-deepseek-v4-1-flash-261017", 300000},
		{"Kimi K3.1 Pro", "person-kimi-k3-1-pro-261111", 300000},
		{"Kimi K3.1 Pro 高速", "person-kimi-k3-1-pro-highspeed-261111", 300000},
		{"Kimi K3", "person-kimi-k3-261111", 300000},
		{"GLM 5.3", "person-glm-5-3-261111", 300000},
		{"GLM 5.3 Flash", "person-glm-5-3-flash-261111", 300000},
		{"DeepSeek V4 Pro", "person-deepseek-v4-pro-ga-260813", 300000},
	}

	byKey := map[string]Model{}
	for _, m := range Builtin {
		if m.ModelItemKey != "" {
			if _, dup := byKey[m.ModelItemKey]; dup {
				t.Errorf("model_item_key 重复: %s", m.ModelItemKey)
			}
			byKey[m.ModelItemKey] = m
		}
	}

	for _, w := range want {
		m, ok := byKey[w.key]
		if !ok {
			t.Errorf("缺少客户端模型 %q（key=%s）", w.name, w.key)
			continue
		}
		if m.Name != w.name {
			t.Errorf("%s: 名称 = %q, want %q", w.key, m.Name, w.name)
		}
		if m.Context != w.context {
			t.Errorf("%s: 上下文 = %d, want %d", w.key, m.Context, w.context)
		}
		if m.AgentMode != 1 {
			t.Errorf("%s: 工作模式模型 agent_mode 必须为 1", w.key)
		}
		if len(m.ReasoningLevels) == 0 {
			t.Errorf("%s: 应带上客户端推理档位", w.key)
		}
	}
}
