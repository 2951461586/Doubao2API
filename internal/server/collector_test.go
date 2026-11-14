package server

import (
	"errors"
	"testing"

	"doubao2api/internal/doubao"
)

// responseCollector 是阻塞式与流式两条路径共用的累积/去重层：
// 上游对同一产物会重复推送 patch，这里去重错了会表现为「同一张图出现两次」
// 或「流式有、阻塞式没有」。

// absorbOK 吸收一个增量，意外报错直接终止用例。
func absorbOK(t *testing.T, c *responseCollector, ch doubao.CompletionChunk) chunkDelta {
	t.Helper()
	d, err := c.absorb(ch)
	if err != nil {
		t.Fatalf("absorb 失败: %v", err)
	}
	return d
}

// absorbErr 吸收一个预期会报错的增量。
func absorbErr(t *testing.T, c *responseCollector, ch doubao.CompletionChunk) error {
	t.Helper()
	if _, err := c.absorb(ch); err != nil {
		return err
	}
	t.Fatalf("预期报错但成功了: %+v", ch)
	return nil
}

func TestCollectorAbsorbTextAndConv(t *testing.T) {
	c := newResponseCollector()

	d := absorbOK(t, c, doubao.CompletionChunk{ConvID: "38447714412500738"})
	if c.convID != "38447714412500738" {
		t.Errorf("convID = %q", c.convID)
	}
	if len(d.images)+len(d.sources)+len(d.artifacts) != 0 || d.music != nil {
		t.Errorf("仅会话 ID 的增量不应带产物: %+v", d)
	}

	absorbOK(t, c, doubao.CompletionChunk{Text: "你好"})
	absorbOK(t, c, doubao.CompletionChunk{Thinking: "先想"})
	if c.text() != "你好" || c.thinkingText() != "先想" {
		t.Errorf("累积 = text:%q thinking:%q", c.text(), c.thinkingText())
	}
	if c.length() != len("你好") {
		t.Errorf("length = %d, want %d", c.length(), len("你好"))
	}

	// 后续帧的会话 ID 覆盖旧值（上游可能先给临时 ID）。
	absorbOK(t, c, doubao.CompletionChunk{ConvID: "999"})
	if c.convID != "999" {
		t.Errorf("convID 未更新: %q", c.convID)
	}
}

func TestCollectorAbsorbError(t *testing.T) {
	c := newResponseCollector()
	err := absorbErr(t, c, doubao.CompletionChunk{Text: "不该保留", ErrorCode: 710020202, ErrorMsg: "限流"})
	var ue *doubao.Error
	if !errors.As(err, &ue) || ue.Code != 710020202 || ue.Msg != "限流" {
		t.Fatalf("错误类型不符: %#v", err)
	}
	if c.text() != "" {
		t.Errorf("错误帧的文本不应累积: %q", c.text())
	}
}

func TestCollectorDedupe(t *testing.T) {
	c := newResponseCollector()

	img := doubao.CompletionChunk{Creations: []doubao.Creation{{ID: "c1", URL: "https://cdn/1.png"}}}
	if d := absorbOK(t, c, img); len(d.images) != 1 {
		t.Fatalf("首个产物应下发: %+v", d)
	}
	// 同一 ID 重复推送（URL 已补全）不再下发。
	if d := absorbOK(t, c, img); len(d.images) != 0 {
		t.Fatalf("重复产物应去重: %+v", d)
	}
	if len(c.imageURLs) != 1 {
		t.Errorf("imageURLs = %+v", c.imageURLs)
	}

	src := doubao.SearchSource{Title: "官网", URL: "https://a/1"}
	// 空 URL 的来源直接跳过。
	if d := absorbOK(t, c, doubao.CompletionChunk{Sources: []doubao.SearchSource{src, {URL: ""}}}); len(d.sources) != 1 {
		t.Fatalf("来源应逐条下发且跳过空 URL: %+v", d)
	}
	if d := absorbOK(t, c, doubao.CompletionChunk{Sources: []doubao.SearchSource{src}}); len(d.sources) != 0 {
		t.Fatalf("重复来源应去重: %+v", d)
	}
	// 同 URL 不同标题算不同来源（key 是 url|title）。
	if d := absorbOK(t, c, doubao.CompletionChunk{Sources: []doubao.SearchSource{{Title: "另一个标题", URL: "https://a/1"}}}); len(d.sources) != 1 {
		t.Fatalf("同 URL 不同标题应保留: %+v", d)
	}

	art := doubao.Artifact{Title: "报告", ResourceID: "r1", ArtifactID: "a1"}
	if d := absorbOK(t, c, doubao.CompletionChunk{Artifacts: []doubao.Artifact{art}}); len(d.artifacts) != 1 {
		t.Fatalf("产物应下发: %+v", d)
	}
	if d := absorbOK(t, c, doubao.CompletionChunk{Artifacts: []doubao.Artifact{art}}); len(d.artifacts) != 0 {
		t.Fatalf("重复产物应去重: %+v", d)
	}

	music := &doubao.MusicMessage{VideoID: "v1", Title: "夏天的风"}
	if d := absorbOK(t, c, doubao.CompletionChunk{Music: music}); d.music == nil {
		t.Fatal("音乐应下发")
	}
	dup := &doubao.MusicMessage{VideoID: "v1", Title: "夏天的风"}
	if d := absorbOK(t, c, doubao.CompletionChunk{Music: dup}); d.music != nil {
		t.Fatalf("重复音乐应去重: %+v", d.music)
	}
	if len(c.music) != 1 {
		t.Errorf("music 累积 = %d, want 1", len(c.music))
	}
}

// 只带 URL 没有 ID 的产物：去重键退化为 URL，不能把后续产物一起吞掉。
func TestNewCreationURLsEmptyIDFallsBackToURL(t *testing.T) {
	got := newCreationURLs([]doubao.Creation{
		{URL: "https://cdn/1.png"},
		{URL: "https://cdn/2.png"},
		{URL: "https://cdn/1.png"}, // 同一 URL 重复推送才应去重
	}, map[string]bool{})
	if len(got) != 2 || got[0] != "https://cdn/1.png" || got[1] != "https://cdn/2.png" {
		t.Fatalf("空 ID 应按 URL 去重并保留全部产物: %+v", got)
	}

	// 有 ID 时仍按 ID 去重（同一 ID 的 URL 补全/重复都只下一次）。
	got = newCreationURLs([]doubao.Creation{
		{ID: "c1", URL: "https://cdn/1.png"},
		{ID: "c1", URL: "https://cdn/1.png"},
		{ID: "c2"},
	}, map[string]bool{})
	if len(got) != 1 || got[0] != "https://cdn/1.png" {
		t.Fatalf("按 ID 去重结果 = %+v", got)
	}
}
