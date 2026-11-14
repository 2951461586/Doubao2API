package doubao

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件用真实的 SSE 事件序列（testdata/*.sse）锁死补丁流解析行为。
//
// 这些夹具是「上游协议漂移」的第一道防线：一旦上游改了块结构或事件形状，
// 这里会先红，而不是等到线上对话变成空白。

// chunkToken 把增量压成可比较的短标记，便于在表里直观写出期望值。
func chunkToken(c CompletionChunk) string {
	switch {
	case c.ConvID != "":
		return "conv:" + c.ConvID
	case c.ErrorCode != 0:
		return fmt.Sprintf("err:%d", c.ErrorCode)
	case c.Text != "":
		return "text:" + c.Text
	case c.Thinking != "":
		return "think:" + c.Thinking
	case c.ToolInfo != "":
		return "tool:" + c.ToolInfo
	case len(c.Sources) > 0:
		s := c.Sources[0]
		return "src:" + s.Title + "|" + s.URL
	case len(c.Artifacts) > 0:
		return "art:" + c.Artifacts[0].Title
	case c.Music != nil:
		return "music:" + c.Music.Title
	case len(c.Creations) > 0:
		return "img:" + c.Creations[0].URL
	}
	return "?"
}

// parseFixture 读取夹具并返回全部增量标记。
func parseFixture(t *testing.T, name string) ([]string, error) {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("打开夹具失败: %v", err)
	}
	defer f.Close()

	var got []string
	err = parseStream(f, func(c CompletionChunk) error {
		got = append(got, chunkToken(c))
		return nil
	})
	return got, err
}

func TestParseStreamFixtures(t *testing.T) {
	cases := []struct {
		name    string
		want    []string
		wantErr string // 非空时断言错误信息包含该片段
	}{
		{
			name: "text_stream.sse",
			want: []string{
				"conv:38445411928226562",
				"text:你好",
				"text:，世界",
			},
		},
		{
			// 第 1 个思考容器内的文本算思维链，第 2 个容器之后算正文。
			name: "thinking_stream.sse",
			want: []string{
				"conv:100",
				"think:先想一下",
				"text:最终答案",
			},
		},
		{
			name: "search_stream.sse",
			want: []string{
				"conv:200",
				"tool:[search] 查询: 豆包是什么",
				"tool:[search] 豆包是字节跳动的 AI 助手",
				"src:豆包官网|https://www.doubao.com/chat/",
				"src:豆包百科|https://baike.example.com/doubao",
				"src:参考站|https://ref.example.com/a",
			},
		},
		{
			// 代码块只在 is_finish 后输出一次，重复 patch 不再重复下发。
			name: "code_stream.sse",
			want: []string{
				"conv:300",
				"text:\n```python\nprint(1)\n```\n",
			},
		},
		{
			// 同一产物被重复推送时按 URL 去重。
			name: "creation_stream.sse",
			want: []string{
				"conv:400",
				"img:https://cdn.example.com/1.png",
				"img:https://cdn.example.com/2.png",
			},
		},
		{
			name: "music_artifact_stream.sse",
			want: []string{
				"conv:500",
				"music:夏天的风",
				"art:季度报告",
			},
		},
		{
			// 上游业务错误以增量形式回传 error_code，由调用方决定是否中止。
			name: "error_stream.sse",
			want: []string{
				"conv:600",
				"err:710020202",
			},
		},
		{
			name:    "gateway_error.sse",
			wantErr: "网关错误",
		},
		{
			// 两种非 block 的增量形态：patch_value.content 为 JSON 字符串、
			// content.content_block 内嵌数组；余下为 CHUNK_DELTA。
			name: "content_patch_stream.sse",
			want: []string{
				"conv:700",
				"text:补丁文本",
				"text:-内嵌块",
				"text:多行\ndata 事件",
			},
		},
		{
			name: "tool_hints_stream.sse",
			want: []string{
				"conv:800",
				"tool:正在搜索…",
				"tool:[tool] 调用浏览器",
				"tool:[file] 读取 report.pdf",
				"tool:[block] 图片里是一只猫", // 未注册块（10012）走兜底透传
				"tool:[research] 检索到 3 篇资料",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseFixture(t, tc.name)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("错误 = %v, want 包含 %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseStream 失败: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("增量数量 = %d, want %d\n got: %q\nwant: %q", len(got), len(tc.want), got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("第 %d 个增量 = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestBlockHandlersCoverDeclaredTypes 保证「声明过的块类型」要么有处理函数，
// 要么明确落在兜底逻辑里 —— 防止新增枚举后忘记接入流式解析。
func TestBlockHandlersCoverDeclaredTypes(t *testing.T) {
	// 这些类型由兜底逻辑（summary/title 透传）处理，属于有意为之。
	intentional := map[int]bool{
		BlockImage:      true,
		BlockFile:       true,
		BlockOutline:    true,
		BlockPPT:        true,
		BlockElapsed:    true,
		BlockQuickReply: true,
	}
	for bt, h := range blockHandlers {
		if h == nil {
			t.Errorf("块类型 %d 注册了 nil 处理函数", bt)
		}
		if intentional[bt] {
			t.Errorf("块类型 %d 已注册处理函数，请从 intentional 列表移除", bt)
		}
	}
	// 抽查核心类型必须已注册。
	for _, bt := range []int{
		BlockText, BlockThinking, BlockSearchQuery, BlockSearchResult,
		BlockCode, BlockGenImage, BlockArtifact, BlockLyricsMusic, BlockCreation,
	} {
		if _, ok := blockHandlers[bt]; !ok {
			t.Errorf("块类型 %d 未注册处理函数", bt)
		}
	}
}

// TestReadSSEMultiline 验证 SSE 规范：同一事件的多行 data 以 "\n" 拼接，
// 注释行与未知字段被忽略（上游心跳会发送以 ":" 开头的行）。
func TestReadSSEMultiline(t *testing.T) {
	raw := ": heartbeat\n" +
		"event: custom\n" +
		"id: 42\n" +
		"data: line1\n" +
		"data: line2\n" +
		"retry: 1000\n" +
		"\n" +
		"data: {\"text\":\"x\"}\n" +
		"\n"

	var events []SSEEvent
	if err := ReadSSE(strings.NewReader(raw), func(ev SSEEvent) error {
		events = append(events, ev)
		return nil
	}); err != nil {
		t.Fatalf("ReadSSE 失败: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("事件数 = %d, want 2: %+v", len(events), events)
	}
	if events[0].Event != "custom" || events[0].ID != "42" || events[0].Data != "line1\nline2" {
		t.Errorf("第 1 个事件解析错误: %+v", events[0])
	}
	if events[1].Event != "" || events[1].Data != `{"text":"x"}` {
		t.Errorf("第 2 个事件解析错误: %+v", events[1])
	}
}
