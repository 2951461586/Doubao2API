package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"doubao2api/internal/doubao"
)

// --- OpenAI 请求压平 ---

func TestFlattenMessagesSingleUser(t *testing.T) {
	text, imgs, files, err := flattenMessages([]chatMessage{
		{Role: "user", Content: "你好"},
	})
	if err != nil {
		t.Fatalf("flattenMessages() 错误: %v", err)
	}
	if text != "你好" {
		t.Errorf("单条 user 消息应保持原样，实际 %q", text)
	}
	if len(imgs) != 0 || len(files) != 0 {
		t.Errorf("纯文本不应产生附件: images=%d files=%d", len(imgs), len(files))
	}
}

func TestFlattenMessagesMultiTurn(t *testing.T) {
	text, _, _, err := flattenMessages([]chatMessage{
		{Role: "system", Content: "你是助手"},
		{Role: "developer", Content: "保持简洁"},
		{Role: "user", Content: "第一问"},
		{Role: "assistant", Content: "第一答"},
		{Role: "user", Content: "   "}, // 空白消息应跳过
		{Role: "tool", Content: "工具输出"},
	})
	if err != nil {
		t.Fatalf("flattenMessages() 错误: %v", err)
	}
	want := "你是助手\n保持简洁\n\nuser: 第一问\nassistant: 第一答\ntool: 工具输出"
	if text != want {
		t.Errorf("压平结果不对:\n got: %q\nwant: %q", text, want)
	}
}

func TestFlattenMessagesEmptyRole(t *testing.T) {
	text, _, _, _ := flattenMessages([]chatMessage{
		{Role: "", Content: "无角色"},
		{Role: "user", Content: "有角色"},
	})
	if !strings.Contains(text, "user: 无角色") {
		t.Errorf("空 role 应按 user 处理，实际 %q", text)
	}
}

func TestExtractContentString(t *testing.T) {
	text, imgs, files := extractContent("纯文本")
	if text != "纯文本" || imgs != nil || files != nil {
		t.Errorf("字符串内容解析不对: %q %v %v", text, imgs, files)
	}
}

func TestExtractContentParts(t *testing.T) {
	parts := []any{
		map[string]any{"type": "text", "text": "看图"},
		map[string]any{"type": "input_text", "text": "和文件"},
		map[string]any{"type": "image_url", "image_url": map[string]any{
			"url": "data:image/png;base64,aGk=",
		}},
		map[string]any{"type": "input_image", "image_url": "https://cdn.example/a.png"},
		map[string]any{"type": "file", "file": map[string]any{
			"filename": "a.pdf", "file_data": "data:application/pdf;base64,aGk=",
		}},
		map[string]any{"type": "input_file", "filename": "b.txt", "file_data": "aGk="},
		map[string]any{"type": "unknown"}, // 忽略
		"不是对象",                            // 忽略
	}
	text, imgs, files := extractContent(parts)
	if text != "看图和文件" {
		t.Errorf("文本解析 = %q, want 看图和文件", text)
	}
	if len(imgs) != 2 {
		t.Fatalf("图片数 = %d, want 2", len(imgs))
	}
	if imgs[0].Name != "image.png" || imgs[0].Format != "png" || string(imgs[0].Data) != "hi" {
		t.Errorf("data URL 图片解析不对: %+v", imgs[0])
	}
	if imgs[1].CDNURL != "https://cdn.example/a.png" || imgs[1].Data != nil {
		t.Errorf("远程图片应保留 URL: %+v", imgs[1])
	}
	if len(files) != 2 {
		t.Fatalf("文件数 = %d, want 2", len(files))
	}
	if files[0].Name != "a.pdf" || string(files[0].Data) != "hi" || files[0].Size != 2 {
		t.Errorf("嵌套 file 分片解析不对: %+v", files[0])
	}
	if files[1].Name != "b.txt" || string(files[1].Data) != "hi" {
		t.Errorf("裸 base64 分片解析不对: %+v", files[1])
	}
}

func TestExtractContentIgnoresGarbage(t *testing.T) {
	for _, in := range []any{nil, 42, map[string]any{"type": "text"}, []any{}} {
		text, imgs, files := extractContent(in)
		if text != "" || len(imgs) != 0 || len(files) != 0 {
			t.Errorf("extractContent(%v) 应返回空，实际 %q %v %v", in, text, imgs, files)
		}
	}

	// 缺 file_data 的分片不应产生附件。
	if _, _, ok := extractContent([]any{map[string]any{
		"type": "file", "file": map[string]any{"filename": "x.pdf"},
	}}); len(ok) != 0 {
		t.Error("缺 file_data 时不应生成文件附件")
	}
}

func TestImageAttachmentFromURL(t *testing.T) {
	img := imageAttachmentFromURL("data:image/jpeg;base64,aGk=")
	if img.Format != "jpeg" || img.Name != "image.jpeg" || string(img.Data) != "hi" {
		t.Errorf("data URL 解析不对: %+v", img)
	}

	img = imageAttachmentFromURL("https://cdn.example/x.webp")
	if img.CDNURL != "https://cdn.example/x.webp" || img.Format != "png" {
		t.Errorf("远程 URL 应原样透传: %+v", img)
	}
}

func TestNewCreationURLs(t *testing.T) {
	creations := []doubao.Creation{
		{ID: "c1", URL: "https://a/1.png"},
		{ID: "c1", URL: "https://a/1.png"}, // 重复 ID 应去重
		{ID: "c2", URL: ""},                // 无 URL 跳过
		{ID: "c3", URL: "https://a/3.png"},
	}
	seen := map[string]bool{}
	got := newCreationURLs(creations, seen)
	if len(got) != 2 || got[0] != "https://a/1.png" || got[1] != "https://a/3.png" {
		t.Errorf("产物去重结果不对: %v", got)
	}
	// 同一 seen 再次调用时不应重复返回。
	if again := newCreationURLs(creations, seen); len(again) != 0 {
		t.Errorf("已见过的产物不应重复返回: %v", again)
	}
}

// --- 技能 / 档位 / 用量 ---

func TestResolveSkill(t *testing.T) {
	cases := []struct {
		in   any
		want int
	}{
		{"image", doubao.SkillImageGen},
		{"draw", doubao.SkillImageGen},
		{"IMAGE", doubao.SkillImageGen},
		{float64(doubao.SkillMusicGen), doubao.SkillMusicGen}, // JSON 数字解出来是 float64
		{float64(17), 17},
		{"", 0},
		{"nope", 0},
		{nil, 0},
	}
	for _, c := range cases {
		if got := resolveSkill(c.in); got != c.want {
			t.Errorf("resolveSkill(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestThinkLevelFromEffort(t *testing.T) {
	off, on, expert := doubao.ThinkOff, doubao.ThinkOn, doubao.ThinkExpert
	cases := []struct {
		in       string
		fallback int
		want     int
	}{
		{"minimal", expert, off},
		{"low", expert, off},
		{"none", expert, off},
		{"fast", expert, off},
		{"0", expert, off},
		{"medium", off, on},
		{"auto", off, on},
		{"1", off, on},
		{" HIGH ", off, expert},
		{"xhigh", off, expert},
		{"max", off, expert},
		{"expert", off, expert},
		{"3", off, expert},
		{"未知值", on, on}, // 回落
		{"", expert, expert},
	}
	for _, c := range cases {
		if got := thinkLevelFromEffort(c.in, c.fallback); got != c.want {
			t.Errorf("thinkLevelFromEffort(%q, %d) = %d, want %d", c.in, c.fallback, got, c.want)
		}
	}
}

func TestEstimateUsage(t *testing.T) {
	u := estimateUsage("一二三四五六", "七八")
	// 中文按 2 字符 ≈ 1 token 估算
	if u["prompt_tokens"] != 3 || u["completion_tokens"] != 1 || u["total_tokens"] != 4 {
		t.Errorf("用量估算不对: %v", u)
	}
	if got := estTokens("abcdef"); got != 3 {
		t.Errorf("estTokens() = %d, want 3", got)
	}
}

func TestImageSkillPrompt(t *testing.T) {
	cases := []struct {
		prompt, style, size, want string
	}{
		{"一只猫", "", "1024x1024", "生成一张图片: 1:1 一只猫"},
		{"一只猫", "水彩", "", "生成一张图片: 水彩 一只猫"},
		{"一只猫", "  ", "1792x1024", "生成一张图片: 16:9 一只猫"},
		{"一只猫", "", "999x999", "生成一张图片: 一只猫"},
	}
	for _, c := range cases {
		if got := imageSkillPrompt(c.prompt, c.style, c.size); got != c.want {
			t.Errorf("imageSkillPrompt(%q,%q,%q) = %q, want %q", c.prompt, c.style, c.size, got, c.want)
		}
	}
}

func TestRatioFromSize(t *testing.T) {
	cases := map[string]string{
		"1792x1024": "16:9",
		"1536x1024": "16:9",
		"16:9":      "16:9",
		"1024x1792": "9:16",
		"9:16":      "9:16",
		"1024x1024": "1:1",
		"512x512":   "1:1",
		"weird":     "",
		"":          "",
	}
	for in, want := range cases {
		if got := ratioFromSize(in); got != want {
			t.Errorf("ratioFromSize(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- Anthropic 转换 ---

func TestAnthropicToChat(t *testing.T) {
	raw := `{
	  "model": "doubao-think",
	  "system": [{"type":"text","text":"你是助手"},{"type":"text","text":"简洁回答"}],
	  "messages": [
	    {"role":"user","content":[
	      {"type":"text","text":"看图"},
	      {"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGk="}}
	    ]},
	    {"role":"assistant","content":"收到"},
	    {"role":"user","content":[{"type":"text","text":"谢谢"}]}
	  ]
	}`
	var req anthropicRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("请求解析失败: %v", err)
	}
	text, imgs, files, err := anthropicToChat(&req)
	if err != nil {
		t.Fatalf("anthropicToChat() 错误: %v", err)
	}
	want := "你是助手\n简洁回答\n\nuser: 看图\nassistant: 收到\nuser: 谢谢"
	if text != want {
		t.Errorf("转换结果不对:\n got: %q\nwant: %q", text, want)
	}
	if len(imgs) != 1 || string(imgs[0].Data) != "hi" {
		t.Errorf("图片转换不对: %+v", imgs)
	}
	if len(files) != 0 {
		t.Errorf("不应产生文件附件: %+v", files)
	}
}

func TestAnthropicToChatPlainString(t *testing.T) {
	var req anthropicRequest
	if err := json.Unmarshal([]byte(`{
	  "system": "系统提示",
	  "messages": [{"role":"user","content":"单轮问题"}]
	}`), &req); err != nil {
		t.Fatal(err)
	}
	text, _, _, err := anthropicToChat(&req)
	if err != nil {
		t.Fatal(err)
	}
	if text != "系统提示\n\n单轮问题" {
		t.Errorf("text = %q", text)
	}
}

func TestAnthropicToChatDocumentAndToolResult(t *testing.T) {
	var req anthropicRequest
	if err := json.Unmarshal([]byte(`{
	  "messages": [{"role":"user","content":[
	    {"type":"document","title":"报告.pdf","source":{"type":"base64","media_type":"application/pdf","data":"aGk="}},
	    {"type":"tool_result","content":[{"type":"text","text":"工具结果"},{"type":"text","text":"第二段"}]},
	    {"type":"tool_use","id":"t1","name":"x"},
	    {"type":"thinking","thinking":"内部推理"}
	  ]}]
	}`), &req); err != nil {
		t.Fatal(err)
	}
	text, _, files, err := anthropicToChat(&req)
	if err != nil {
		t.Fatalf("anthropicToChat() 错误: %v", err)
	}
	if len(files) != 1 || files[0].Name != "报告.pdf" || string(files[0].Data) != "hi" {
		t.Errorf("文档转换不对: %+v", files)
	}
	if !strings.Contains(text, "工具结果\n第二段") {
		t.Errorf("工具结果应按文本回流，实际 %q", text)
	}
	if strings.Contains(text, "内部推理") {
		t.Errorf("thinking 块不应回流，实际 %q", text)
	}
}

func TestAnthropicContentUnsupported(t *testing.T) {
	if _, err := anthropicContent(json.RawMessage(`42`)); err == nil {
		t.Error("非字符串 / 非数组的 content 应报错")
	}
	if _, err := anthropicContent(json.RawMessage(`{"oops":1}`)); err == nil {
		t.Error("对象形态的 content 应报错")
	}
	if v, err := anthropicContent(nil); err != nil || v != "" {
		t.Errorf("空 content 应返回空串，实际 %v %v", v, err)
	}
}

func TestAnthropicSystemText(t *testing.T) {
	if got := anthropicSystemText(nil); got != "" {
		t.Errorf("空 system 应返回空串，实际 %q", got)
	}
	if got := anthropicSystemText(json.RawMessage(`"  你好  "`)); got != "你好" {
		t.Errorf("字符串 system 应去空格，实际 %q", got)
	}
	if got := anthropicSystemText(json.RawMessage(`[{"type":"text","text":"A"},{"type":"image"}]`)); got != "A" {
		t.Errorf("数组 system 应只取文本块，实际 %q", got)
	}
}

func TestAnthropicImageURL(t *testing.T) {
	got := anthropicImageURL(map[string]any{"source": map[string]any{
		"type": "base64", "data": "aGk=",
	}})
	if got != "data:image/png;base64,aGk=" {
		t.Errorf("缺省 media_type 应补 image/png，实际 %q", got)
	}
	got = anthropicImageURL(map[string]any{"source": map[string]any{
		"type": "url", "url": "https://cdn.example/x.png",
	}})
	if got != "https://cdn.example/x.png" {
		t.Errorf("url 源应原样透传，实际 %q", got)
	}
	for _, bad := range []map[string]any{
		{},
		{"source": map[string]any{"type": "base64"}},
		{"source": map[string]any{"type": "weird"}},
	} {
		if got := anthropicImageURL(bad); got != "" {
			t.Errorf("无效图片块应返回空，实际 %q", got)
		}
	}
}

func TestAnthropicDocumentDataURL(t *testing.T) {
	got := anthropicDocumentDataURL(map[string]any{"source": map[string]any{
		"type": "base64", "data": "aGk=",
	}})
	if got != "data:application/pdf;base64,aGk=" {
		t.Errorf("缺省 media_type 应补 application/pdf，实际 %q", got)
	}
	if got := anthropicDocumentDataURL(map[string]any{"source": map[string]any{
		"type": "text", "data": "aGk=",
	}}); got != "" {
		t.Errorf("非 base64 源应返回空，实际 %q", got)
	}
}

func TestAnthropicToolResultText(t *testing.T) {
	if got := anthropicToolResultText(map[string]any{"content": "纯文本"}); got != "纯文本" {
		t.Errorf("字符串内容 = %q", got)
	}
	blocks := []any{
		map[string]any{"type": "text", "text": "A"},
		map[string]any{"type": "image"},
		map[string]any{"type": "text", "text": "B"},
	}
	if got := anthropicToolResultText(map[string]any{"content": blocks}); got != "A\nB" {
		t.Errorf("块内容 = %q", got)
	}
	if got := anthropicToolResultText(map[string]any{}); got != "" {
		t.Errorf("缺 content 应返回空，实际 %q", got)
	}
}

// --- 响应辅助函数 ---

func TestWriteErrShape(t *testing.T) {
	rec := httptest.NewRecorder()
	writeErr(rec, http.StatusBadRequest, "参数不对", "invalid_request_error")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("状态码 = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
	var body struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    int    `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if body.Error.Message != "参数不对" || body.Error.Type != "invalid_request_error" ||
		body.Error.Code != http.StatusBadRequest {
		t.Errorf("错误结构不对: %+v", body.Error)
	}
}

func TestWriteAnthropicErrShape(t *testing.T) {
	rec := httptest.NewRecorder()
	writeAnthropicErr(rec, http.StatusBadRequest, "invalid_request_error", "bad")

	var body struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if body.Type != "error" || body.Error.Type != "invalid_request_error" || body.Error.Message != "bad" {
		t.Errorf("Anthropic 错误结构不对: %+v", body)
	}
}
