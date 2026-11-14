package doubao

import (
	"strings"
	"testing"
)

// 工具调用的报文形状在不同客户端版本里出现过多种写法，这里把所有见过的形状都锁住；
// 编排联调时若发现新形状，先在这里补一条用例，再去调 ParseToolCall。
func TestParseToolCall(t *testing.T) {
	cases := []struct {
		name     string
		msg      map[string]any
		wantOK   bool
		wantName string
		wantPath string
	}{
		{
			name: "tool_call + arguments 字符串",
			msg: map[string]any{
				"msg_id": "m1", "type": "tool_call",
				"tool_call": map[string]any{"id": "c1", "name": "write_file", "arguments": `{"path":"a.txt","content":"x"}`},
			},
			wantOK: true, wantName: "write_file", wantPath: "a.txt",
		},
		{
			name: "tool_call + arguments 对象",
			msg: map[string]any{
				"msg_id":    "m2",
				"tool_call": map[string]any{"name": "read_file", "arguments": map[string]any{"path": "b.txt"}},
			},
			wantOK: true, wantName: "read_file", wantPath: "b.txt",
		},
		{
			name: "function 形式（OpenAI 风格）",
			msg: map[string]any{
				"id":       "m3",
				"function": map[string]any{"name": "list_dir", "arguments": `{"path":"."}`},
			},
			wantOK: true, wantName: "list_dir", wantPath: ".",
		},
		{
			name: "平铺 tool_name + tool_input",
			msg: map[string]any{
				"msg_id": "m4", "tool_name": "write_file",
				"tool_input": map[string]any{"path": "c.txt", "content": "y"},
			},
			wantOK: true, wantName: "write_file", wantPath: "c.txt",
		},
		{
			name: "平铺 name + input（JSON 字符串）",
			msg: map[string]any{
				"msg_id": "m5", "name": "run_command", "input": `{"command":"go version"}`,
			},
			wantOK: true, wantName: "run_command",
		},
		{
			name: "裸字符串入参 → 收到 content",
			msg: map[string]any{
				"msg_id": "m6", "name": "write_file", "input": "hello",
			},
			wantOK: true, wantName: "write_file",
		},
		{
			name:   "非工具消息（无名字）",
			msg:    map[string]any{"msg_id": "m7", "type": "text", "content": "hi"},
			wantOK: false,
		},
		{
			name:   "nil 消息",
			msg:    nil,
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call, ok := ParseToolCall(tc.msg)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if call.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", call.Name, tc.wantName)
			}
			if tc.wantPath != "" {
				if got := str(call.Input["path"]); got != tc.wantPath {
					t.Errorf("path = %q, want %q (input=%v)", got, tc.wantPath, call.Input)
				}
			}
			if call.Raw == nil {
				t.Error("Raw 应保留原始消息")
			}
		})
	}
}

// TestParseToolCallBareString 裸字符串入参进 content（脚本类工具用）。
func TestParseToolCallBareString(t *testing.T) {
	call, ok := ParseToolCall(map[string]any{"msg_id": "m", "name": "write_file", "input": "hello"})
	if !ok {
		t.Fatal("应解析成功")
	}
	if call.Input["content"] != "hello" {
		t.Errorf("content = %v, want hello", call.Input["content"])
	}
}

// TestFindStringField 覆盖 agent_id 的嵌套提取（首帧壳结构可能变）。
func TestFindStringField(t *testing.T) {
	cases := []struct {
		name string
		obj  map[string]any
		want string
	}{
		{"顶层", map[string]any{"agent_id": "a1"}, "a1"},
		{"一层嵌套", map[string]any{"msg": map[string]any{"agent_id": "a2"}}, "a2"},
		{"数组内", map[string]any{"list": []any{map[string]any{"agent_id": "a3"}}}, "a3"},
		{"没有", map[string]any{"x": "y"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := findStringField(tc.obj, "agent_id", 4); got != tc.want {
				t.Errorf("findStringField = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestToolCallString 保证入参能被稳定序列化（日志 / 排障用）。
func TestToolCallString(t *testing.T) {
	got := ToolCallString(map[string]any{"path": "a.txt"})
	if !strings.Contains(got, `"path":"a.txt"`) {
		t.Errorf("ToolCallString = %q", got)
	}
}
