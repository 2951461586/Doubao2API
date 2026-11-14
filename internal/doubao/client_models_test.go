package doubao

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// fixture 构造一个最小但结构真实的 Preferences 文件。
//
// 结构取自桌面端实际写法：
//
//	Preferences.saman.local_storage_for_web["frontend-request-cache"]
//	  = "{\"_DesktopActionBarBriefListRequestCacheDataV1\":{\"data\":{\"modeSelectData\":{…}}}}"
func writePrefsFixture(t *testing.T, dir, profile string, models []map[string]any, modeModels []map[string]any) {
	t.Helper()
	msd := map[string]any{
		"menuConfV2": map[string]any{
			"model_list": map[string]any{"item_list": models},
			"mode_list": map[string]any{
				"item_list": []any{map[string]any{
					"name": "工作任务", "mode_id": "3", "agent_mode": 1,
					"default_model": map[string]any{"model_item_key": "9"},
					"model_list":    map[string]any{"item_list": modeModels},
				}},
			},
		},
		"modeSelectConfig": map[string]any{
			"default_select_model_item_key": "9",
			"menu_item_list": []any{
				map[string]any{"model_item_key": "9", "agent_mode": 1},
				map[string]any{"model_item_key": "person-glm-5-3-261111", "agent_mode": 1},
			},
		},
	}
	entry := map[string]any{
		"data":      map[string]any{"modeSelectData": msd},
		"timestamp": int64(1794569858793),
	}
	cache := map[string]any{"_DesktopActionBarBriefListRequestCacheDataV1": entry}
	raw, err := json.Marshal(cache)
	if err != nil {
		t.Fatal(err)
	}
	prefs := map[string]any{
		"saman": map[string]any{
			"local_storage_for_web": map[string]any{"frontend-request-cache": string(raw)},
		},
	}
	raw, err = json.Marshal(prefs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, profile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, profile, "Preferences"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func modelItem(name, key, window string) map[string]any {
	return map[string]any{
		"name":           name,
		"item_id":        10001,
		"model_item_key": key,
		"default_mode":   "3",
		"model_extra_params": map[string]any{
			"total_window_size": window,
			"memory_profile":    "300k_230",
		},
		"reasoning_effort_config": map[string]any{
			"default_level": 5,
			"option_list": []any{
				map[string]any{"level": 3, "name": "低"},
				map[string]any{"level": 4, "name": "中"},
				map[string]any{"level": 5, "name": "高"},
			},
		},
		"conversation_mode_list": []string{"office", "creation"},
	}
}

func TestReadClientModels(t *testing.T) {
	dir := t.TempDir()
	models := []map[string]any{
		modelItem("自动", "9", "256000"),
		modelItem("GLM 5.3", "person-glm-5-3-261111", "300000"),
	}
	writePrefsFixture(t, dir, "Default", models, models)

	list, err := ReadClientModels(dir, "Default")
	if err != nil {
		t.Fatalf("ReadClientModels: %v", err)
	}
	if list.Profile != "Default" || len(list.Models) != 2 {
		t.Fatalf("清单不符: %+v", list)
	}
	if list.ModeID != "" {
		// model_list 存在时优先用它，mode 元信息可缺省。
		t.Logf("mode_id = %q", list.ModeID)
	}
	if list.DefaultModelKey != "9" {
		t.Errorf("默认模型键 = %q, want 9", list.DefaultModelKey)
	}
	if list.CapturedAt.Unix() != 1794569858 {
		t.Errorf("缓存时间 = %v", list.CapturedAt)
	}

	auto := list.Models[0]
	if auto.Name != "自动" || auto.ModelItemKey != "9" || auto.TotalWindow != 256000 {
		t.Errorf("自动模型解析不符: %+v", auto)
	}
	// agent_mode 在 model_list 里缺失，应从 menu_item_list 补齐。
	if auto.AgentMode != 1 {
		t.Errorf("agent_mode 应从 menu_item_list 补齐，实际 %d", auto.AgentMode)
	}
	if len(auto.ReasoningLevels) != 3 || auto.ReasoningLevels[2] != "高" {
		t.Errorf("推理档位解析不符: %v", auto.ReasoningLevels)
	}
	// item_id 数字应归一化为字符串。
	if auto.ItemID != "10001" {
		t.Errorf("item_id = %q, want 10001", auto.ItemID)
	}
	if len(auto.ConversationModes) != 2 {
		t.Errorf("会话模式解析不符: %v", auto.ConversationModes)
	}
}

func TestReadClientModelsFallsBackToModeList(t *testing.T) {
	dir := t.TempDir()
	models := []map[string]any{modelItem("Kimi K3", "person-kimi-k3-261111", "300000")}
	// model_list 为空 → 应退回 mode_list[0].model_list。
	writePrefsFixture(t, dir, "Profile 2", nil, models)

	list, err := ReadClientModels(dir, "Profile 2")
	if err != nil {
		t.Fatalf("ReadClientModels: %v", err)
	}
	if len(list.Models) != 1 || list.Models[0].ModelItemKey != "person-kimi-k3-261111" {
		t.Fatalf("退回 mode_list 失败: %+v", list.Models)
	}
	if list.ModeID != "3" || list.ModeName != "工作任务" {
		t.Errorf("模式元信息 = %q/%q", list.ModeID, list.ModeName)
	}
}

func TestReadClientModelsErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadClientModels(dir, "Default"); err == nil {
		t.Error("缺少 Preferences 应报错")
	}
	// Preferences 存在但没有缓存。
	if err := os.MkdirAll(filepath.Join(dir, "Default"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Default", "Preferences"), []byte(`{"saman":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadClientModels(dir, "Default"); err == nil {
		t.Error("没有 frontend-request-cache 应报错")
	}
}
