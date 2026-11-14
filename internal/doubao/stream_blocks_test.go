package doubao

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// 本文件锁死 streamState 的分派 / 去重 / 状态机行为：
// 块注册表之外的兜底、思维链边界、错误事件、非 JSON 容错。

// ev 拼一个 SSE 事件。
func ev(event, data string) string {
	return "event: " + event + "\ndata: " + data + "\n\n"
}

// patchBlock 把单个 content_block 包进上游补丁信封。
func patchBlock(bt int, content string, extra string) string {
	if extra != "" {
		extra = "," + extra
	}
	return `{"patch_op":[{"patch_value":{"content_block":[{"block_type":` +
		fmt.Sprintf("%d", bt) + `,"content":` + content + extra + `}]}}]}`
}

func textPatch(bt int, text string) string {
	return patchBlock(bt, `{"text_block":{"text":`+quote(text)+`}}`, "")
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func collectStream(t *testing.T, sse string) ([]CompletionChunk, error) {
	t.Helper()
	var got []CompletionChunk
	err := parseStream(strings.NewReader(sse), func(c CompletionChunk) error {
		got = append(got, c)
		return nil
	})
	return got, err
}

// mustCollect 收集全部增量，解析失败直接终止用例。
func mustCollect(t *testing.T, sse string) []CompletionChunk {
	t.Helper()
	got, err := collectStream(t, sse)
	if err != nil {
		t.Fatalf("parseStream 失败: %v", err)
	}
	return got
}

func chunkTokens(chs []CompletionChunk) []string {
	out := make([]string, len(chs))
	for i, c := range chs {
		out[i] = chunkToken(c)
	}
	return out
}

// 同一句工具提示重复推送时只下一次。
func TestStreamToolDedupe(t *testing.T) {
	loading := `{"loading_block":{"text_loading":{"text":"正在搜索…"}}}`
	sse := ev("STREAM_CHUNK", patchBlock(BlockLoading, loading, "")) +
		ev("STREAM_CHUNK", patchBlock(BlockLoading, loading, ""))

	got, err := collectStream(t, sse)
	if err != nil {
		t.Fatal(err)
	}
	if tokens := chunkTokens(got); len(tokens) != 1 || tokens[0] != "tool:正在搜索…" {
		t.Fatalf("增量 = %q, want 单条去重后的工具提示", tokens)
	}
}

// 上游约定：第 1 个思考容器内的文本算思维链，第 2 个之后一直算正文。
// 这是一条隐式协议，改动会让 reasoning_content / content 互换，故锁死。
func TestStreamThinkingBoundary(t *testing.T) {
	sse := ev("STREAM_CHUNK", patchBlock(BlockThinking, `{}`, "")) +
		ev("STREAM_CHUNK", textPatch(BlockText, "A")) +
		ev("STREAM_CHUNK", patchBlock(BlockThinking, `{}`, "")) +
		ev("STREAM_CHUNK", textPatch(BlockText, "B")) +
		ev("STREAM_CHUNK", patchBlock(BlockThinking, `{}`, "")) +
		ev("STREAM_CHUNK", textPatch(BlockText, "C"))

	got, err := collectStream(t, sse)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"think:A", "text:B", "text:C"}
	if tokens := chunkTokens(got); strings.Join(tokens, ",") != strings.Join(want, ",") {
		t.Fatalf("增量 = %q, want %q", tokens, want)
	}
}

// 未注册块：小于 10000 的未知类型静默丢弃；>=10000 且带 summary/title 时透传。
func TestStreamUnknownBlockFallback(t *testing.T) {
	cases := []struct {
		name string
		bt   int
		body string
		want []string
	}{
		{"低编号未知块静默丢弃", 9999, `{"whatever_block":{"title":"x"}}`, nil},
		{"高编号未知块无标题静默丢弃", 12000, `{}`, nil},
		// 未声明类型没有约定键名，兜底看 content 顶层。
		{"未声明类型顶层 summary 透传", 12000, `{"summary":"进度 50%"}`, []string{"tool:[block] 进度 50%"}},
		{"未声明类型顶层 title 透传", 12000, `{"title":"未知块"}`, []string{"tool:[block] 未知块"}},
		{"未声明类型深层字段仍取不到", 12000, `{"whatever_block":{"summary":"进度 50%"}}`, nil},
		// BlockImage(10012) 未注册处理函数，落到兜底，标签为通用 block。
		{"图片块兜底透传", BlockImage, `{"image_block":{"title":"一只猫"}}`, []string{"tool:[block] 一只猫"}},
		{"通用工具块有专属标签", BlockGenericTool, `{"generic_tool_block":{"summary":"调用浏览器"}}`, []string{"tool:[tool] 调用浏览器"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := collectStream(t, ev("STREAM_CHUNK", patchBlock(tc.bt, tc.body, "")))
			if err != nil {
				t.Fatal(err)
			}
			if tokens := chunkTokens(got); strings.Join(tokens, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("增量 = %q, want %q", tokens, tc.want)
			}
		})
	}
}

// 部分响应把块内容放在 content_obj 而非 content。
func TestStreamContentObjFallback(t *testing.T) {
	sse := ev("STREAM_CHUNK",
		`{"patch_op":[{"patch_value":{"content_block":[{"block_type":10000,"content_obj":{"text_block":{"text":"来自 content_obj"}}}]}}]}`)
	got, err := collectStream(t, sse)
	if err != nil {
		t.Fatal(err)
	}
	if tokens := chunkTokens(got); len(tokens) != 1 || tokens[0] != "text:来自 content_obj" {
		t.Fatalf("增量 = %q", tokens)
	}
}

// 代码块只在 is_finish 后输出一次，并带语言围栏。
func TestStreamCodeBlock(t *testing.T) {
	base := `{"code_block":{"code":"print(1)","type":1}}`
	notFinished := ev("STREAM_CHUNK", patchBlock(BlockCode, base, `"is_finish":false`))
	finished := ev("STREAM_CHUNK", patchBlock(BlockCode, base, `"is_finish":true`))

	if got, err := collectStream(t, notFinished); err != nil || len(got) != 0 {
		t.Fatalf("未完成代码块不应输出: got=%q err=%v", chunkTokens(got), err)
	}

	got, err := collectStream(t, finished+finished)
	if err != nil {
		t.Fatal(err)
	}
	if tokens := chunkTokens(got); len(tokens) != 1 || tokens[0] != "text:\n```python\nprint(1)\n```\n" {
		t.Fatalf("增量 = %q", tokens)
	}

	// 语言映射：type=5 → go，未知 type 退化为空标记。
	goBlock := ev("STREAM_CHUNK", patchBlock(BlockCode, `{"code_block":{"code":"x","type":5}}`, `"is_finish":true`))
	if got := mustCollect(t, goBlock); len(got) != 1 || got[0].Text != "\n```go\nx\n```\n" {
		t.Errorf("go 围栏 = %q", got[0].Text)
	}
	unknown := ev("STREAM_CHUNK", patchBlock(BlockCode, `{"code_block":{"code":"x","type":99}}`, `"is_finish":true`))
	if got := mustCollect(t, unknown); len(got) != 1 || got[0].Text != "\n```\nx\n```\n" {
		t.Errorf("未知语言围栏 = %q", got[0].Text)
	}
}

// SSE_ACK：conversation_id="0" 表示尚未建立会话，不能当会话 ID 下发。
func TestStreamAckZeroConversation(t *testing.T) {
	zero := ev("SSE_ACK", `{"ack_client_meta":{"conversation_id":"0"}}`)
	if got, err := collectStream(t, zero); err != nil || len(got) != 0 {
		t.Fatalf("conversation_id=0 不应产生增量: %q err=%v", chunkTokens(got), err)
	}

	ok := ev("SSE_ACK", `{"ack_client_meta":{"conversation_id":38447714412500738}}`)
	got, err := collectStream(t, ok)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ConvID != "38447714412500738" {
		t.Fatalf("会话 ID 解析错误: %+v", got)
	}
}

// agent_id 只上报一次，且允许出现在嵌套对象里。
func TestStreamAgentIDOnce(t *testing.T) {
	sse := ev("STREAM_MSG_NOTIFY", `{"agent_id":"ag-1"}`) +
		ev("STREAM_MSG_NOTIFY", `{"a":{"b":{"c":{"agent_id":"ag-2"}}}}`) +
		ev("STREAM_MSG_NOTIFY", `{"agent_id":"ag-3"}`)

	got, err := collectStream(t, sse)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].AgentID != "ag-1" {
		t.Fatalf("agent_id 增量 = %+v, want 仅首帧 ag-1", got)
	}
}

// 错误事件：error_code=0 视为无错误；非 0 转成 ErrorCode 增量；
// 带 error_code 的 text 字段不能当正文（否则错误文案会混进回答）。
func TestStreamErrorEvents(t *testing.T) {
	t.Run("STREAM_ERROR code=0 静默", func(t *testing.T) {
		got, err := collectStream(t, ev("STREAM_ERROR", `{"error_code":0,"error_msg":""}`))
		if err != nil || len(got) != 0 {
			t.Fatalf("got=%q err=%v", chunkTokens(got), err)
		}
	})
	t.Run("错误码转为增量", func(t *testing.T) {
		got, err := collectStream(t, ev("STREAM_ERROR", `{"error_code":710020202,"error_msg":"限流"}`))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].ErrorCode != 710020202 || got[0].ErrorMsg != "限流" {
			t.Fatalf("增量 = %+v", got)
		}
	})
	t.Run("带 error_code 的 text 不算正文", func(t *testing.T) {
		sse := ev("STREAM_CHUNK", `{"text":"伪装成正文的错误","error_code":710020202}`)
		got, err := collectStream(t, sse)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].ErrorCode != 710020202 || got[0].Text != "" {
			t.Fatalf("增量 = %+v", got)
		}
	})
	t.Run("网关错误携带 msg", func(t *testing.T) {
		got, err := collectStream(t, ev("gateway-error", `{"msg":"bad gateway"}`))
		if err == nil || !strings.Contains(err.Error(), "bad gateway") {
			t.Fatalf("错误 = %v", err)
		}
		if len(got) != 0 {
			t.Errorf("网关错误不应产生增量: %+v", got)
		}
	})
	t.Run("网关错误非 JSON 时回退原文", func(t *testing.T) {
		got, err := collectStream(t, ev("gateway-error", `plain text`))
		if err == nil || !strings.Contains(err.Error(), "plain text") {
			t.Fatalf("错误 = %v", err)
		}
		if len(got) != 0 {
			t.Errorf("网关错误不应产生增量: %+v", got)
		}
	})
}

// 非 JSON data、非法 patched JSON 都应静默跳过，而不是中断整条流。
func TestStreamNonJSONTolerance(t *testing.T) {
	sse := ev("STREAM_CHUNK", `not-json`) +
		ev("STREAM_CHUNK", `{"patch_op":[{"patch_value":{"content":"not-json"}}]}`) +
		ev("STREAM_CHUNK", `{"patch_op":"unexpected-shape"}`) +
		ev("STREAM_CHUNK", `{"patch_op":[{"patch_value":{"content":"{\"text\":\"补丁\"}"}}]}`)

	got, err := collectStream(t, sse)
	if err != nil {
		t.Fatal(err)
	}
	if tokens := chunkTokens(got); len(tokens) != 1 || tokens[0] != "text:补丁" {
		t.Fatalf("增量 = %q", tokens)
	}
}

// 生图过程块：URL 可能来自 url / image_url / thumb_url / image_ori。
func TestStreamGenImageFallbacks(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"image_ori", `{"gen_image_block":{"images":[{"id":1,"image_ori":{"url":"https://a/ori.png","width":10,"height":20}}]}}`, []string{"img:https://a/ori.png"}},
		{"image_url", `{"gen_image_block":{"images":[{"id":2,"image_url":"https://a/plain.png"}]}}`, []string{"img:https://a/plain.png"}},
		{"thumb_url", `{"gen_image_block":{"images":[{"id":3,"thumb_url":"https://a/thumb.png"}]}}`, []string{"img:https://a/thumb.png"}},
		{"无 URL 丢弃", `{"gen_image_block":{"images":[{"id":4}]}}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := collectStream(t, ev("STREAM_CHUNK", patchBlock(BlockGenImage, tc.body, "")))
			if err != nil {
				t.Fatal(err)
			}
			if tokens := chunkTokens(got); strings.Join(tokens, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("增量 = %q, want %q", tokens, tc.want)
			}
			if len(got) == 1 && got[0].Creations[0].Width == 0 && tc.name == "image_ori" {
				t.Error("image_ori 的宽高未解析")
			}
		})
	}

	// 同一 URL 在多个 patch 中重复出现时只下一次。
	body := `{"gen_image_block":{"images":[{"id":1,"image_url":"https://a/1.png"}]}}`
	sse := ev("STREAM_CHUNK", patchBlock(BlockGenImage, body, "")) +
		ev("STREAM_CHUNK", patchBlock(BlockGenImage, body, ""))
	if got := mustCollect(t, sse); len(got) != 1 {
		t.Errorf("重复生图未去重: %q", chunkTokens(got))
	}
}

// creation_block 先推占位（无 URL）、后续 patch 才补 URL：占位不应下发。
func TestStreamCreationPlaceholderThenURL(t *testing.T) {
	placeholder := `{"creation_block":{"creations":[{"id":"c1","image":{"key":"k1"}}]}}`
	withURL := `{"creation_block":{"creations":[{"id":"c1","image":{"key":"k1","image_ori":{"url":"https://cdn/1.png"}}}]}}`
	sse := ev("STREAM_CHUNK", patchBlock(BlockCreation, placeholder, "")) +
		ev("STREAM_CHUNK", patchBlock(BlockCreation, withURL, "")) +
		ev("STREAM_CHUNK", patchBlock(BlockCreation, withURL, ""))

	got, err := collectStream(t, sse)
	if err != nil {
		t.Fatal(err)
	}
	if tokens := chunkTokens(got); len(tokens) != 1 || tokens[0] != "img:https://cdn/1.png" {
		t.Fatalf("增量 = %q", tokens)
	}
}

// 产物块的两种形状：artifact_block 与 artifact_op_block.artifact。
func TestStreamArtifactShapes(t *testing.T) {
	cases := []struct {
		name string
		bt   int
		body string
		want string
	}{
		{"artifact_block", BlockArtifact, `{"artifact_block":{"title":"报告","resource_id":"r1"}}`, "art:报告"},
		{"artifact_op_block", BlockArtifactOp, `{"artifact_op_block":{"artifact":{"name":"代码产物","artifact_topic":"a1"}}}`, "art:代码产物"},
		{"artifact_code", BlockArtifactCode, `{"artifact_block":{"artifact_id":"aid","resource_id":"r2"}}`, "art:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := collectStream(t, ev("STREAM_CHUNK", patchBlock(tc.bt, tc.body, "")))
			if err != nil {
				t.Fatal(err)
			}
			if tokens := chunkTokens(got); len(tokens) != 1 || tokens[0] != tc.want {
				t.Fatalf("增量 = %q, want %q", tokens, tc.want)
			}
		})
	}
}

// 音乐块内容可能嵌在 lyrics_to_songs_music_content / music_content，
// 字段名也有 vid / name / cover_url 等别名。
func TestStreamMusicNested(t *testing.T) {
	body := `{"lyrics_to_songs_music_block":{"task_nums":2,"lyrics_to_songs_music_content":{"vid":"v1","name":"夏天的风","cover_url":"https://c/1.jpg","duration":12.5}}}`
	got, err := collectStream(t, ev("STREAM_CHUNK", patchBlock(BlockLyricsMusic, body, "")))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Music == nil {
		t.Fatalf("增量 = %+v", got)
	}
	m := got[0].Music
	if m.VideoID != "v1" || m.Title != "夏天的风" || m.CoverURI != "https://c/1.jpg" || m.Duration != 12.5 || m.TaskNums != 2 {
		t.Errorf("音乐字段解析错误: %+v", m)
	}

	// 既无 video_id 也无标题时不下发空音乐。
	empty := `{"lyrics_to_songs_music_block":{"task_nums":1}}`
	if got := mustCollect(t, ev("STREAM_CHUNK", patchBlock(BlockLyricsMusic, empty, ""))); len(got) != 0 {
		t.Errorf("空音乐块不应下发: %+v", got)
	}
}

// 来源与工具提示的混合块：先出来源，再出说明。
func TestStreamSourceAndText(t *testing.T) {
	body := `{"tips_block":{"summary":"参考了 2 篇","results":[{"title":"T1","url":"https://a/1"},{"title":"T2","url":"https://a/2"}]}}`
	got, err := collectStream(t, ev("STREAM_CHUNK", patchBlock(BlockTips, body, "")))
	if err != nil {
		t.Fatal(err)
	}
	tokens := chunkTokens(got)
	want := []string{"src:T1|https://a/1", "src:T2|https://a/2", "tool:[tips] 参考了 2 篇"}
	if strings.Join(tokens, ",") != strings.Join(want, ",") {
		t.Fatalf("增量 = %q, want %q", tokens, want)
	}
}

// 回调返回错误时必须中断解析，避免在客户端已断开时继续烧上游。
func TestStreamCallbackErrorStops(t *testing.T) {
	sse := ev("STREAM_CHUNK", textPatch(BlockText, "a")) +
		ev("STREAM_CHUNK", textPatch(BlockText, "b"))
	calls := 0
	errSentinel := fmt.Errorf("client gone")
	err := parseStream(strings.NewReader(sse), func(CompletionChunk) error {
		calls++
		return errSentinel
	})
	if err != errSentinel {
		t.Fatalf("错误 = %v, want 回调错误", err)
	}
	if calls != 1 {
		t.Errorf("回调次数 = %d, want 1", calls)
	}
}

// 各块处理函数的「空内容」分支：不能因为字段缺失就 panic 或下发空增量。
func TestStreamEmptyBlockContent(t *testing.T) {
	cases := []struct {
		name string
		bt   int
		body string
		fin  bool
	}{
		{"文本块无 text_block", BlockText, `{}`, false},
		{"文本块 text 为空", BlockText, `{"text_block":{"text":""}}`, false},
		{"代码块无 code", BlockCode, `{"code_block":{"type":1}}`, true},
		{"产物块三个键全空", BlockArtifact, `{"artifact_block":{"status":1}}`, false},
		{"通用工具块无标题", BlockGenericTool, `{"generic_tool_block":{}}`, false},
		{"搜索词块无查询", BlockSearchQuery, `{"search_query_block":{}}`, false},
		{"文件操作块无标题", BlockFileOperation, `{"file_operation_block":{}}`, false},
		{"生图块无 images", BlockGenImage, `{"gen_image_block":{}}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			extra := ""
			if tc.fin {
				extra = `"is_finish":true`
			}
			got, err := collectStream(t, ev("STREAM_CHUNK", patchBlock(tc.bt, tc.body, extra)))
			if err != nil {
				t.Fatalf("空内容不应报错: %v", err)
			}
			if len(got) != 0 {
				t.Fatalf("空内容不应下发增量: %q", chunkTokens(got))
			}
		})
	}
}

// 来源在流式解析层也要去重（同一 URL+title 只下一次）。
func TestStreamSourcesDeduped(t *testing.T) {
	body := `{"tips_block":{"results":[{"title":"T1","url":"https://a/1"},{"title":"T1","url":"https://a/1"}]}}`
	sse := ev("STREAM_CHUNK", patchBlock(BlockTips, body, "")) +
		ev("STREAM_CHUNK", patchBlock(BlockTips, body, ""))

	got := mustCollect(t, sse)
	if tokens := chunkTokens(got); len(tokens) != 1 || tokens[0] != "src:T1|https://a/1" {
		t.Fatalf("增量 = %q, want 单条去重来源", tokens)
	}
}

// 文件操作块优先 title，其次 summary。
func TestStreamFileOperationFallback(t *testing.T) {
	title := `{"file_operation_block":{"title":"打开 a.pdf","summary":"S"}}`
	if got := mustCollect(t, ev("STREAM_CHUNK", patchBlock(BlockFileOperation, title, ""))); len(got) != 1 || got[0].ToolInfo != "[file] 打开 a.pdf" {
		t.Errorf("title 优先失败: %q", chunkTokens(got))
	}
	summary := `{"file_operation_block":{"summary":"读取中"}}`
	if got := mustCollect(t, ev("STREAM_CHUNK", patchBlock(BlockFileOperation, summary, ""))); len(got) != 1 || got[0].ToolInfo != "[file] 读取中" {
		t.Errorf("summary 回退失败: %q", chunkTokens(got))
	}
}

// 非 JSON 事件必须计数（/health 依赖它发现协议漂移），且不影响正常增量。
func TestParseStreamCountsNonJSONEvents(t *testing.T) {
	var stats ParseStats
	sse := ev("STREAM_CHUNK", "not-json") +
		ev("STREAM_CHUNK", textPatch(BlockText, "ok")) +
		ev("STREAM_CHUNK", "also-bad")

	var got []CompletionChunk
	err := parseStreamWithStats(strings.NewReader(sse), &stats, func(c CompletionChunk) error {
		got = append(got, c)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := stats.NonJSONEvents.Load(); n != 2 {
		t.Errorf("NonJSONEvents = %d, want 2", n)
	}
	if tokens := chunkTokens(got); len(tokens) != 1 || tokens[0] != "text:ok" {
		t.Fatalf("正常增量被影响: %q", tokens)
	}

	// stats 为 nil 时（旧路径）只跳过、不 panic。
	if err := parseStream(strings.NewReader(ev("STREAM_CHUNK", "not-json")), func(CompletionChunk) error { return nil }); err != nil {
		t.Fatalf("nil stats 不应报错: %v", err)
	}
}
