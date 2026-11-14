package doubao

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// 本文件读取桌面端缓存里的「模型清单」，用于让 /v1/models 与客户端保持一致。
//
// 关键点：这份清单**不是接口下发**，而是客户端把
// `_DesktopActionBarBriefListRequestCacheDataV1` 写进了 Chromium 的
// Preferences（`saman.local_storage_for_web.frontend-request-cache`）。
// 因此只能从磁盘读，且必须容忍字段缺失 / 版本漂移。
//
// 只读、不写、不碰 Cookie：解析范围限定在 model 相关字段。

// ClientModel 是桌面端模型清单里的一项。
type ClientModel struct {
	Name              string   `json:"name"`
	ItemID            string   `json:"item_id,omitempty"`
	ModelItemKey      string   `json:"model_item_key"`
	DefaultMode       string   `json:"default_mode,omitempty"`
	AgentMode         int      `json:"agent_mode,omitempty"`
	TotalWindow       int      `json:"total_window,omitempty"`
	MemoryProfile     string   `json:"memory_profile,omitempty"`
	ReasoningDefault  int      `json:"reasoning_default,omitempty"`
	ReasoningLevels   []string `json:"reasoning_levels,omitempty"`
	ConversationModes []string `json:"conversation_modes,omitempty"`
}

// ClientModelList 是一个 profile 的模型清单快照。
type ClientModelList struct {
	Profile string `json:"profile"`
	// UserData 是 Chromium 的 User Data 目录（排查用）。
	UserData string `json:"user_data"`
	// ModeID / ModeName 是模型清单所属的模式（当前为 "3" / "工作任务"）。
	ModeID   string `json:"mode_id,omitempty"`
	ModeName string `json:"mode_name,omitempty"`
	// DefaultModelKey 是客户端默认选中的模型键。
	DefaultModelKey string `json:"default_model_key,omitempty"`
	// CapturedAt 是客户端写入缓存的时间（可能为零值）。
	CapturedAt time.Time     `json:"captured_at,omitempty"`
	Models     []ClientModel `json:"models"`
}

// cacheTimestampLayout 说明：Preferences 里的 timestamp 是毫秒。
const cacheTimestampLayout = 1000

// ReadClientModels 读取指定 profile 的客户端模型清单。
func ReadClientModels(userData, profile string) (*ClientModelList, error) {
	path := filepath.Join(userData, profile, "Preferences")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取客户端 Preferences 失败: %w", err)
	}

	var pref struct {
		Saman struct {
			LocalStorageForWeb map[string]string `json:"local_storage_for_web"`
		} `json:"saman"`
	}
	if err := json.Unmarshal(raw, &pref); err != nil {
		return nil, fmt.Errorf("Preferences 解析失败: %w", err)
	}
	cacheRaw := pref.Saman.LocalStorageForWeb["frontend-request-cache"]
	if cacheRaw == "" {
		return nil, fmt.Errorf("%s: 没有 frontend-request-cache（客户端可能尚未打开过对话页）", profile)
	}

	var cache map[string]json.RawMessage
	if err := json.Unmarshal([]byte(cacheRaw), &cache); err != nil {
		return nil, fmt.Errorf("模型缓存解析失败: %w", err)
	}
	entry, ok := cache["_DesktopActionBarBriefListRequestCacheDataV1"]
	if !ok {
		return nil, fmt.Errorf("%s: 缓存里没有模型清单（_DesktopActionBarBriefListRequestCacheDataV1）", profile)
	}

	var node struct {
		Data *struct {
			ModeSelectData json.RawMessage `json:"modeSelectData"`
		} `json:"data"`
		ModeSelectData json.RawMessage `json:"modeSelectData"`
		Timestamp      int64           `json:"timestamp"`
	}
	if err := json.Unmarshal(entry, &node); err != nil {
		return nil, fmt.Errorf("模型缓存条目解析失败: %w", err)
	}
	msdRaw := node.ModeSelectData
	if node.Data != nil && len(node.Data.ModeSelectData) > 0 {
		msdRaw = node.Data.ModeSelectData
	}
	if len(msdRaw) == 0 {
		return nil, fmt.Errorf("%s: 缓存条目里没有 modeSelectData", profile)
	}

	out := &ClientModelList{Profile: profile, UserData: userData}
	if node.Timestamp > 0 {
		out.CapturedAt = time.Unix(node.Timestamp/cacheTimestampLayout, 0)
	}
	if err := parseModeSelectData(msdRaw, out); err != nil {
		return nil, err
	}
	if len(out.Models) == 0 {
		return nil, fmt.Errorf("%s: 模型清单为空", profile)
	}
	return out, nil
}

// ReadAllClientModels 读取本机所有 profile 的模型清单。
//
// 某个 profile 读不到不算整体失败（未登录 / 未打开过对话页很常见），
// 只有全部失败时才返回错误。
func ReadAllClientModels(explicitDir string) ([]*ClientModelList, error) {
	var out []*ClientModelList
	var firstErr error
	for _, dir := range DoubaoWorkUserDataDirs(explicitDir) {
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		for _, profile := range chromiumProfiles(dir) {
			list, err := ReadClientModels(dir, profile)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			out = append(out, list)
		}
	}
	if len(out) == 0 {
		if firstErr == nil {
			firstErr = fmt.Errorf("未找到 DoubaoWork 客户端数据目录")
		}
		return nil, firstErr
	}
	return out, nil
}

// modelItemJSON 是缓存里的模型条目（字段可能缺失）。
type modelItemJSON struct {
	Name         string          `json:"name"`
	ItemID       json.RawMessage `json:"item_id"`
	ModelItemKey string          `json:"model_item_key"`
	DefaultMode  string          `json:"default_mode"`
	AgentMode    int             `json:"agent_mode"`
	ExtraParams  struct {
		TotalWindowSize string `json:"total_window_size"`
		MemoryProfile   string `json:"memory_profile"`
	} `json:"model_extra_params"`
	Reasoning struct {
		DefaultLevel int `json:"default_level"`
		OptionList   []struct {
			Level int    `json:"level"`
			Name  string `json:"name"`
		} `json:"option_list"`
	} `json:"reasoning_effort_config"`
	ConversationModes []string `json:"conversation_mode_list"`
}

// modeItemJSON 是「工作任务」这类模式的条目。
type modeItemJSON struct {
	Name         string `json:"name"`
	ModeID       string `json:"mode_id"`
	AgentMode    int    `json:"agent_mode"`
	DefaultModel struct {
		ModelItemKey string `json:"model_item_key"`
	} `json:"default_model"`
	ModelList struct {
		ItemList []modelItemJSON `json:"item_list"`
	} `json:"model_list"`
}

// modeSelectDataJSON 只取模型清单相关字段。
type modeSelectDataJSON struct {
	MenuConfV2 struct {
		ModelList struct {
			ItemList []modelItemJSON `json:"item_list"`
		} `json:"model_list"`
		ModeList struct {
			ItemList []modeItemJSON `json:"item_list"`
		} `json:"mode_list"`
	} `json:"menuConfV2"`
	ModeSelectConfig struct {
		MenuItemList []struct {
			ModelItemKey string `json:"model_item_key"`
			AgentMode    int    `json:"agent_mode"`
		} `json:"menu_item_list"`
		DefaultSelectModelItemKey string `json:"default_select_model_item_key"`
	} `json:"modeSelectConfig"`
}

// parseModeSelectData 把 modeSelectData 归一化为模型清单。
//
// 取 `menuConfV2.model_list` 为主（普通对话可选模型）；为空时退回
// `mode_list[0].model_list`。agent_mode 在 model_list 里通常缺失，
// 从 menu_item_list 里按 model_item_key 补齐。
func parseModeSelectData(raw json.RawMessage, out *ClientModelList) error {
	var msd modeSelectDataJSON
	if err := json.Unmarshal(raw, &msd); err != nil {
		return fmt.Errorf("modeSelectData 解析失败: %w", err)
	}

	agentModes := make(map[string]int, len(msd.ModeSelectConfig.MenuItemList))
	for _, m := range msd.ModeSelectConfig.MenuItemList {
		if m.ModelItemKey != "" {
			agentModes[m.ModelItemKey] = m.AgentMode
		}
	}

	items := msd.MenuConfV2.ModelList.ItemList
	if len(items) == 0 {
		for _, mode := range msd.MenuConfV2.ModeList.ItemList {
			if len(mode.ModelList.ItemList) > 0 {
				items = mode.ModelList.ItemList
				out.ModeID = mode.ModeID
				out.ModeName = mode.Name
				out.DefaultModelKey = mode.DefaultModel.ModelItemKey
				break
			}
		}
	}

	out.DefaultModelKey = firstNonEmpty(out.DefaultModelKey, msd.ModeSelectConfig.DefaultSelectModelItemKey)

	seen := make(map[string]bool, len(items))
	for _, it := range items {
		if it.ModelItemKey == "" || seen[it.ModelItemKey] {
			continue
		}
		seen[it.ModelItemKey] = true
		m := ClientModel{
			Name:              it.Name,
			ItemID:            rawScalar(it.ItemID),
			ModelItemKey:      it.ModelItemKey,
			DefaultMode:       it.DefaultMode,
			AgentMode:         it.AgentMode,
			TotalWindow:       atoiTrim(it.ExtraParams.TotalWindowSize),
			MemoryProfile:     it.ExtraParams.MemoryProfile,
			ReasoningDefault:  it.Reasoning.DefaultLevel,
			ConversationModes: it.ConversationModes,
		}
		if m.AgentMode == 0 {
			m.AgentMode = agentModes[it.ModelItemKey]
		}
		for _, opt := range it.Reasoning.OptionList {
			if opt.Name != "" {
				m.ReasoningLevels = append(m.ReasoningLevels, opt.Name)
			}
		}
		out.Models = append(out.Models, m)
	}
	return nil
}

// rawScalar 把 JSON 数字或字符串统一成字符串。
func rawScalar(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return strings.TrimSpace(string(raw))
}

// atoiTrim 解析整数字符串，失败返回 0。
func atoiTrim(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}
