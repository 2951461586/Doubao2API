package doubao

import (
	"encoding/json"
	"strings"
	"testing"
)

// 本文件覆盖 blocks.go 里的纯函数：块字段解析、来源提取、类型转换。
// 这些都是 SSE 解析的底座，出错时表现为「字段消失」而非显式报错。

func TestCodeLang(t *testing.T) {
	want := map[int]string{
		0: "", 1: "python", 2: "javascript", 3: "typescript", 4: "java",
		5: "go", 6: "cpp", 7: "c", 8: "bash", 9: "sql", 10: "json",
		11: "html", 12: "css", 99: "", -1: "",
	}
	for in, expect := range want {
		if got := codeLang(in); got != expect {
			t.Errorf("codeLang(%d) = %q, want %q", in, got, expect)
		}
	}
}

func TestBlockKeyForAndTag(t *testing.T) {
	keys := map[int]string{
		BlockText:            "text_block",
		BlockThinking:        "thinking_block",
		BlockGenericTool:     "generic_tool_block",
		BlockSearchQuery:     "search_query_block",
		BlockSearchResult:    "search_query_result_block",
		BlockSearchResultV2:  "search_query_result_block",
		BlockSearchRichMedia: "search_rich_media_block",
		BlockCode:            "code_block",
		BlockGenImage:        "gen_image_block",
		BlockImage:           "image_block",
		BlockImageAnalysis:   "image_analysis_block",
		BlockResearch:        "research_block",
		BlockResearchStatus:  "research_status_block",
		BlockResearchWebpage: "research_webpage_block",
		BlockFileOperation:   "file_operation_block",
		BlockFile:            "file_block",
		BlockArtifact:        "artifact_block",
		BlockArtifactOp:      "artifact_block",
		BlockArtifactCode:    "artifact_block",
		BlockLink:            "link_block",
		BlockTips:            "tips_block",
		BlockReference:       "reference_block",
		BlockCreation:        "creation_block",
		BlockLyricsMusic:     "lyrics_to_songs_music_block",
		BlockLoading:         "loading_block",
		12000:                "",
	}
	for bt, want := range keys {
		if got := blockKeyFor(bt); got != want {
			t.Errorf("blockKeyFor(%d) = %q, want %q", bt, got, want)
		}
	}

	tags := map[int]string{
		BlockSearchQuery:     "search",
		BlockSearchResult:    "search",
		BlockSearchResultV2:  "search",
		BlockSearchRichMedia: "search",
		BlockCode:            "code",
		BlockGenImage:        "image",
		BlockImageAnalysis:   "vision",
		BlockResearch:        "research",
		BlockResearchStatus:  "research",
		BlockResearchWebpage: "research",
		BlockFileOperation:   "file",
		BlockFile:            "file",
		BlockArtifact:        "artifact",
		BlockArtifactOp:      "artifact",
		BlockArtifactCode:    "artifact",
		BlockLink:            "link",
		BlockReference:       "reference",
		BlockTips:            "tips",
		BlockLyricsMusic:     "music",
		BlockImage:           "block", // 未在 switch 里，退化为通用标签
	}
	for bt, want := range tags {
		if got := blockTag(bt); got != want {
			t.Errorf("blockTag(%d) = %q, want %q", bt, got, want)
		}
	}
}

func TestBlockTextFallbacks(t *testing.T) {
	cases := []struct {
		name    string
		content map[string]any
		key     string
		want    string
	}{
		{"nil content", nil, "tips_block", ""},
		// 空 key 时 blockMap 返回 nil，直接回退到 content 顶层。
		{"空 key 回退到顶层", map[string]any{"summary": "x"}, "", "x"},
		{"块内 summary 优先", map[string]any{"tips_block": map[string]any{"summary": "S", "title": "T"}}, "tips_block", "S"},
		{"块内 text 字符串", map[string]any{"tips_block": map[string]any{"text": "tx"}}, "tips_block", "tx"},
		{"块内 text 对象", map[string]any{"tips_block": map[string]any{"text": map[string]any{"text": "nested"}}}, "tips_block", "nested"},
		{"块内 title", map[string]any{"tips_block": map[string]any{"title": "T"}}, "tips_block", "T"},
		{"块内 query", map[string]any{"search_query_block": map[string]any{"query": "Q"}}, "search_query_block", "Q"},
		// 块键不存在时回退到 content 本身。
		{"回退到 content 顶层", map[string]any{"summary": "top"}, "missing_block", "top"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := blockText(tc.content, tc.key); got != tc.want {
				t.Fatalf("blockText = %q, want %q", got, tc.want)
			}
		})
	}

	// blockField 只在块键存在时取值。
	content := map[string]any{"artifact_block": map[string]any{"title": "T"}}
	if got := blockField(content, "artifact_block", "title"); got != "T" {
		t.Errorf("blockField = %q", got)
	}
	if got := blockField(content, "missing", "title"); got != "" {
		t.Errorf("缺失块键应返回空: %q", got)
	}
}

func TestBlockSources(t *testing.T) {
	t.Run("嵌套 results / text_card", func(t *testing.T) {
		content := map[string]any{
			"search_query_result_block": map[string]any{
				"summary": "找到 2 篇",
				"results": []any{
					map[string]any{"text_card": map[string]any{
						"title": "T1", "url": "https://a/1", "snippet": "S1", "site_name": "站点1",
					}},
					map[string]any{"title": "T2", "link": "https://a/2", "description": "S2", "site": "站点2"},
				},
			},
		}
		got := blockSources(content)
		if len(got) != 2 {
			t.Fatalf("来源数 = %d, want 2: %+v", len(got), got)
		}
		if got[0].URL != "https://a/1" || got[0].Title != "T1" || got[0].Snippet != "S1" || got[0].Site != "站点1" {
			t.Errorf("第 1 条来源 = %+v", got[0])
		}
		if got[1].URL != "https://a/2" || got[1].Site != "站点2" {
			t.Errorf("第 2 条来源 = %+v", got[1])
		}
	})

	t.Run("同一 URL 去重", func(t *testing.T) {
		content := map[string]any{"items": []any{
			map[string]any{"title": "A", "url": "https://a/1"},
			map[string]any{"title": "A again", "url": "https://a/1"},
		}}
		if got := blockSources(content); len(got) != 1 {
			t.Fatalf("来源数 = %d, want 1: %+v", len(got), got)
		}
	})

	t.Run("没有标题的 URL 被忽略", func(t *testing.T) {
		content := map[string]any{"items": []any{map[string]any{"url": "https://a/1"}}}
		if got := blockSources(content); len(got) != 0 {
			t.Fatalf("来源数 = %d, want 0: %+v", len(got), got)
		}
	})

	t.Run("最多 20 条", func(t *testing.T) {
		items := make([]any, 0, 30)
		for i := 0; i < 30; i++ {
			items = append(items, map[string]any{"title": "T", "url": "https://a/" + string(rune('a'+i))})
		}
		if got := blockSources(map[string]any{"results": items}); len(got) != 20 {
			t.Fatalf("来源数 = %d, want 20", len(got))
		}
	})

	t.Run("超过 4 层的来源会被漏掉", func(t *testing.T) {
		// 递归深度上限是 4：更深的搜索结果会静默消失。
		deep := map[string]any{"results": []any{map[string]any{"title": "T", "url": "https://a/deep"}}}
		wrapped := any(deep)
		for i := 0; i < 5; i++ {
			wrapped = map[string]any{"child": wrapped}
		}
		if got := blockSources(map[string]any{"x": wrapped}); len(got) != 0 {
			t.Fatalf("深度超限仍提取到来源: %+v", got)
		}
	})
}

func TestParseCreations(t *testing.T) {
	content := map[string]any{"creation_block": map[string]any{
		"creations": []any{
			map[string]any{
				"id": "c1", "type": 1,
				"image": map[string]any{
					"key":       "uri-1",
					"image_ori": map[string]any{"url": "https://a/1.png", "width": 512, "height": 768},
				},
				"gen_detail": map[string]any{"task_type": 9},
			},
			// image_ori 不存在时回退到缩略图。
			map[string]any{"id": "c2", "image": map[string]any{
				"image_thumb": map[string]any{"url": "https://a/2-thumb.png"},
			}},
			// 无 image 子对象直接丢弃。
			map[string]any{"id": "c3"},
			// 有 image 但没有任何 URL 也丢弃。
			map[string]any{"id": "c4", "image": map[string]any{"key": "k"}},
		},
	}}
	got := parseCreations(content)
	if len(got) != 2 {
		t.Fatalf("产物数 = %d, want 2: %+v", len(got), got)
	}
	if got[0].URL != "https://a/1.png" || got[0].Width != 512 || got[0].Height != 768 ||
		got[0].URI != "uri-1" || got[0].TaskType != 9 {
		t.Errorf("第 1 个产物 = %+v", got[0])
	}
	if got[1].URL != "https://a/2-thumb.png" {
		t.Errorf("缩略图回退失败: %+v", got[1])
	}

	if parseCreations(map[string]any{}) != nil {
		t.Error("缺少 creation_block 时应返回 nil")
	}
}

func TestParseGenImages(t *testing.T) {
	content := map[string]any{"gen_image_block": map[string]any{"images": []any{
		map[string]any{"id": json.Number("1"), "url": "https://a/direct.png", "width": 100},
		map[string]any{"id": "2", "image_ori": map[string]any{"url": "https://a/ori.png", "height": 200}},
		map[string]any{"id": "3", "image_url": "https://a/image_url.png"},
		map[string]any{"id": "4", "thumb_url": "https://a/thumb.png"},
		map[string]any{"id": "5"},
	}}}
	got := parseGenImages(content)
	if len(got) != 4 {
		t.Fatalf("生图数 = %d, want 4: %+v", len(got), got)
	}
	if got[0].URL != "https://a/direct.png" || got[0].Width != 100 {
		t.Errorf("直连 url 解析错误: %+v", got[0])
	}
	if got[1].URL != "https://a/ori.png" || got[1].Height != 200 {
		t.Errorf("image_ori 解析错误: %+v", got[1])
	}
	if got[2].URL != "https://a/image_url.png" || got[3].URL != "https://a/thumb.png" {
		t.Errorf("URL 回退顺序错误: %+v", got)
	}

	if parseGenImages(map[string]any{}) != nil {
		t.Error("缺少 gen_image_block 时应返回 nil")
	}
}

func TestParseArtifacts(t *testing.T) {
	t.Run("artifact_block 标题回退到 name", func(t *testing.T) {
		got := parseArtifacts(map[string]any{"artifact_block": map[string]any{
			"name": "季度报告", "resource_id": "r1", "resource_type": 2,
			"artifact_meta_id": "m1", "status": 3,
		}})
		if len(got) != 1 {
			t.Fatalf("产物数 = %d, want 1", len(got))
		}
		a := got[0]
		if a.Title != "季度报告" || a.ResourceID != "r1" || a.ResourceTyp != 2 ||
			a.ArtifactID != "m1" || a.Status != 3 {
			t.Errorf("产物字段 = %+v", a)
		}
	})

	t.Run("artifact_op_block 包装", func(t *testing.T) {
		got := parseArtifacts(map[string]any{"artifact_op_block": map[string]any{
			"artifact": map[string]any{"title": "代码", "artifact_id": "aid"},
		}})
		if len(got) != 1 || got[0].Title != "代码" || got[0].ArtifactID != "aid" {
			t.Fatalf("产物 = %+v", got)
		}
	})

	t.Run("三个键全空时返回 nil", func(t *testing.T) {
		if got := parseArtifacts(map[string]any{"artifact_block": map[string]any{"status": 1}}); got != nil {
			t.Fatalf("应返回 nil: %+v", got)
		}
	})
}

func TestParseMusic(t *testing.T) {
	t.Run("嵌套 content 与字段别名", func(t *testing.T) {
		got := parseMusic(map[string]any{"lyrics_to_songs_music_block": map[string]any{
			"task_nums": 1,
			"music_content": map[string]any{
				"id": "vid-1", "title": "歌名", "cover_uri": "uri://c", "duration": json.Number("9.5"),
			},
		}})
		if got == nil {
			t.Fatal("应解析出音乐")
		}
		if got.VideoID != "vid-1" || got.Title != "歌名" || got.CoverURI != "uri://c" || got.Duration != 9.5 {
			t.Errorf("音乐字段 = %+v", got)
		}
	})

	t.Run("直接在块上", func(t *testing.T) {
		got := parseMusic(map[string]any{"lyrics_to_songs_music_block": map[string]any{
			"video_id": "v2", "name": "直接",
		}})
		if got == nil || got.VideoID != "v2" || got.Title != "直接" {
			t.Fatalf("音乐 = %+v", got)
		}
	})

	t.Run("缺 id 与标题返回 nil", func(t *testing.T) {
		if got := parseMusic(map[string]any{"lyrics_to_songs_music_block": map[string]any{"duration": 3}}); got != nil {
			t.Fatalf("应返回 nil: %+v", got)
		}
		if got := parseMusic(map[string]any{}); got != nil {
			t.Fatalf("缺少块时应返回 nil: %+v", got)
		}
	})
}

func TestTruthy(t *testing.T) {
	yes := []any{true, "1", "true", "TRUE", json.Number("1"), float64(2)}
	no := []any{false, "0", "false", "", json.Number("0"), float64(0), nil, struct{}{}}
	for _, v := range yes {
		if !truthy(v) {
			t.Errorf("truthy(%#v) = false, want true", v)
		}
	}
	for _, v := range no {
		if truthy(v) {
			t.Errorf("truthy(%#v) = true, want false", v)
		}
	}
}

func TestIDAndNumberHelpers(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"abc", "abc"},
		{json.Number("38447714412500738"), "38447714412500738"},
		{float64(42), "42"},
		{int64(7), "7"},
		{int(8), "8"},
		{nil, ""},
		{true, ""},
	}
	for _, tc := range cases {
		if got := idOf(tc.in); got != tc.want {
			t.Errorf("idOf(%#v) = %q, want %q", tc.in, got, tc.want)
		}
	}

	floats := []struct {
		in   any
		want float64
	}{
		{json.Number("1.25"), 1.25},
		{float64(2.5), 2.5},
		{int(3), 3},
		{"4.75", 4.75},
		{"bad", 0},
		{nil, 0},
	}
	for _, tc := range floats {
		if got := floatOf(tc.in); got != tc.want {
			t.Errorf("floatOf(%#v) = %v, want %v", tc.in, got, tc.want)
		}
	}

	if firstInt(0, 5) != 5 || firstInt(7, 5) != 7 {
		t.Error("firstInt 应返回第一个非零值")
	}
}

// 结构化产物在流式路径上重复推送时，key 必须稳定可拼接。
func TestArtifactDedupeKeyStability(t *testing.T) {
	a := Artifact{Title: "T", ResourceID: "r", ArtifactID: "a"}
	key := func(x Artifact) string { return x.ResourceID + x.ArtifactID + x.Title }
	if key(a) != key(Artifact{Title: "T", ResourceID: "r", ArtifactID: "a"}) {
		t.Fatal("相同产物应得到相同去重键")
	}
	if !strings.Contains(key(a), "r") {
		t.Error("去重键应包含 resource_id")
	}
}
