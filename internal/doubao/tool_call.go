package doubao

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ToolCall 是从 agent 消息里解析出的一次工具调用。
type ToolCall struct {
	// MsgID 是工具通道里的消息 id（ack / complete 用）。
	MsgID string
	// CallID 是上游给这次调用的 id（若下发）。
	CallID string
	// Name 是工具名（如 write_file / run_command）。
	Name string
	// Input 是工具入参（已归一化为 map）。
	Input map[string]any
	// Raw 是原始消息，便于排障与后续校准。
	Raw map[string]any
}

// ParseToolCall 从一条 agent 消息里解析工具调用；不是工具调用时返回 ok=false。
//
// 字段名按上游常见下发形式做容错（实测报文会在编排联调时进一步校准）：
//
//	{"msg_id":"…","type":"tool_call","tool_call":{"name":"write_file","arguments":"{…}"}}
//	{"msg_id":"…","tool_name":"write_file","tool_input":{"path":"a.txt"}}
//	{"msg_id":"…","name":"write_file","input":"{\"path\":\"a.txt\"}"}
func ParseToolCall(m map[string]any) (ToolCall, bool) {
	if m == nil {
		return ToolCall{}, false
	}
	call := ToolCall{MsgID: firstNonEmpty(str(m["msg_id"]), str(m["id"])), Raw: m}

	// 工具名：tool_call.name → function.name → tool_name → name → tool
	name := ""
	if tc, ok := m["tool_call"].(map[string]any); ok {
		name = firstNonEmpty(str(tc["name"]), str(tc["tool_name"]))
		if call.CallID == "" {
			call.CallID = firstNonEmpty(str(tc["id"]), str(tc["call_id"]), str(tc["tool_call_id"]))
		}
		if call.Input == nil {
			if in, ok := tc["arguments"].(map[string]any); ok {
				call.Input = in
			} else {
				call.Input = parseArgs(tc["arguments"])
			}
		}
	}
	if fn, ok := m["function"].(map[string]any); ok {
		if name == "" {
			name = str(fn["name"])
		}
		if call.Input == nil {
			call.Input = parseArgs(fn["arguments"])
		}
	}
	if name == "" {
		name = firstNonEmpty(str(m["tool_name"]), str(m["name"]), str(m["tool"]))
	}
	if call.CallID == "" {
		call.CallID = firstNonEmpty(str(m["tool_call_id"]), str(m["call_id"]))
	}
	if call.Input == nil {
		for _, k := range []string{"tool_input", "input", "arguments", "params", "args"} {
			if v, ok := m[k]; ok {
				call.Input = parseArgs(v)
				if call.Input != nil {
					break
				}
			}
		}
	}
	if call.Input == nil {
		call.Input = map[string]any{}
	}
	if name == "" {
		return ToolCall{}, false
	}
	call.Name = name
	return call, true
}

// parseArgs 把工具入参统一成 map：对象直接用；字符串按 JSON 解析（失败返回 nil）。
func parseArgs(v any) map[string]any {
	switch t := v.(type) {
	case map[string]any:
		return t
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return nil
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err == nil {
			return m
		}
		// 少数工具入参是裸字符串（如整段脚本），塞进 content 便于执行端处理。
		if len(s) > 0 && (s[0] == '{' || s[0] == '[') {
			return nil
		}
		return map[string]any{"content": s}
	}
	return nil
}

// ToolCallString 把入参序列化成便于回传的字符串（调试 / 日志用）。
func ToolCallString(input map[string]any) string {
	raw, err := json.Marshal(input)
	if err != nil {
		return fmt.Sprintf("%v", input)
	}
	return string(raw)
}
