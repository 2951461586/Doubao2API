package doubao

import (
	"encoding/json"
	"strconv"
	"strings"
)

// decodeObj 用 UseNumber 解析 JSON 对象，避免 int64 字段先变成 float64 丢精度。
func decodeObj(s string) (map[string]any, bool) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return nil, false
	}
	return obj, true
}

// blockKeyFor 返回块内容在 content 对象里的键名（block_type -> xxx_block）。
func blockKeyFor(bt int) string {
	switch bt {
	case BlockText:
		return "text_block"
	case BlockThinking:
		return "thinking_block"
	case BlockGenericTool:
		return "generic_tool_block"
	case BlockSearchQuery:
		return "search_query_block"
	case BlockSearchResult, BlockSearchResultV2:
		return "search_query_result_block"
	case BlockSearchRichMedia:
		return "search_rich_media_block"
	case BlockCode:
		return "code_block"
	case BlockGenImage:
		return "gen_image_block"
	case BlockImage:
		return "image_block"
	case BlockImageAnalysis:
		return "image_analysis_block"
	case BlockResearch:
		return "research_block"
	case BlockResearchStatus:
		return "research_status_block"
	case BlockResearchWebpage:
		return "research_webpage_block"
	case BlockFileOperation:
		return "file_operation_block"
	case BlockFile:
		return "file_block"
	case BlockArtifact, BlockArtifactOp, BlockArtifactCode:
		return "artifact_block"
	case BlockLink:
		return "link_block"
	case BlockTips:
		return "tips_block"
	case BlockReference:
		return "reference_block"
	case BlockCreation:
		return "creation_block"
	case BlockLyricsMusic:
		return "lyrics_to_songs_music_block"
	case BlockLoading:
		return "loading_block"
	}
	return ""
}

// blockTag 返回用于工具提示的短标签。
func blockTag(bt int) string {
	switch bt {
	case BlockSearchQuery:
		return "search"
	case BlockSearchResult, BlockSearchResultV2, BlockSearchRichMedia:
		return "search"
	case BlockCode:
		return "code"
	case BlockGenImage:
		return "image"
	case BlockImageAnalysis:
		return "vision"
	case BlockResearch, BlockResearchStatus, BlockResearchWebpage:
		return "research"
	case BlockFileOperation, BlockFile:
		return "file"
	case BlockArtifact, BlockArtifactOp, BlockArtifactCode:
		return "artifact"
	case BlockLink:
		return "link"
	case BlockReference:
		return "reference"
	case BlockTips:
		return "tips"
	case BlockLyricsMusic:
		return "music"
	}
	return "block"
}

// blockMap 取出 content[<key>] 子对象。
func blockMap(content map[string]any, key string) map[string]any {
	if content == nil || key == "" {
		return nil
	}
	m, _ := content[key].(map[string]any)
	return m
}

// blockText 返回块内最可能的可读文本（summary / text / title）。
func blockText(content map[string]any, key string) string {
	m := blockMap(content, key)
	if m == nil {
		// 有些块直接在 content 上放 summary/title
		m = content
	}
	if m == nil {
		return ""
	}
	return firstNonEmpty(str(m["summary"]), textOf(m["text"]), str(m["title"]), textOf(m["query"]))
}

// blockField 返回 content[<key>][<field>]。
func blockField(content map[string]any, key, field string) string {
	m := blockMap(content, key)
	if m == nil {
		return ""
	}
	return str(m[field])
}

// codeLang 把上游 code_block.type 映射为 Markdown 语言标记（尽力而为）。
func codeLang(t int) string {
	switch t {
	case 1:
		return "python"
	case 2:
		return "javascript"
	case 3:
		return "typescript"
	case 4:
		return "java"
	case 5:
		return "go"
	case 6:
		return "cpp"
	case 7:
		return "c"
	case 8:
		return "bash"
	case 9:
		return "sql"
	case 10:
		return "json"
	case 11:
		return "html"
	case 12:
		return "css"
	}
	return ""
}

// parseGenImages 解析 block_type=10010 的 gen_image_block.images。
func parseGenImages(content map[string]any) []Creation {
	gb := blockMap(content, "gen_image_block")
	if gb == nil {
		return nil
	}
	var out []Creation
	for _, it := range asSlice(gb["images"]) {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		cr := Creation{
			ID:     idOf(m["id"]),
			Type:   intOf(m["type"]),
			URI:    str(m["key"]),
			URL:    str(m["url"]),
			Width:  intOf(m["width"]),
			Height: intOf(m["height"]),
		}
		if ori, ok := m["image_ori"].(map[string]any); ok {
			cr.URL = firstNonEmpty(cr.URL, str(ori["url"]))
			cr.Width = firstInt(cr.Width, intOf(ori["width"]))
			cr.Height = firstInt(cr.Height, intOf(ori["height"]))
		}
		if cr.URL == "" {
			cr.URL = firstNonEmpty(str(m["image_url"]), str(m["thumb_url"]))
		}
		if cr.URL != "" {
			out = append(out, cr)
		}
	}
	return out
}

// parseArtifacts 解析产物块（artifact_block / artifact_op_block）。
func parseArtifacts(content map[string]any) []Artifact {
	ab := blockMap(content, "artifact_block")
	if ab == nil {
		if op := blockMap(content, "artifact_op_block"); op != nil {
			ab, _ = op["artifact"].(map[string]any)
		}
	}
	if ab == nil {
		return nil
	}
	art := Artifact{
		Title:       firstNonEmpty(str(ab["title"]), str(ab["name"])),
		ResourceID:  str(ab["resource_id"]),
		ResourceTyp: intOf(ab["resource_type"]),
		ArtifactID:  firstNonEmpty(str(ab["artifact_topic"]), str(ab["artifact_id"]), str(ab["artifact_meta_id"])),
		Status:      intOf(ab["status"]),
	}
	if art.ResourceID == "" && art.ArtifactID == "" && art.Title == "" {
		return nil
	}
	return []Artifact{art}
}

// parseMusic 解析 block_type=10075 的音乐成品块。
func parseMusic(content map[string]any) *MusicMessage {
	mb := blockMap(content, "lyrics_to_songs_music_block")
	if mb == nil {
		return nil
	}
	candidates := []map[string]any{}
	for _, k := range []string{"lyrics_to_songs_music_content", "music_content", "content"} {
		if m, ok := mb[k].(map[string]any); ok {
			candidates = append(candidates, m)
		}
	}
	candidates = append(candidates, mb)
	out := &MusicMessage{TaskNums: intOf(mb["task_nums"])}
	for _, m := range candidates {
		if out.VideoID == "" {
			out.VideoID = firstNonEmpty(str(m["video_id"]), str(m["vid"]), str(m["id"]))
		}
		if out.Title == "" {
			out.Title = firstNonEmpty(str(m["title"]), str(m["name"]))
		}
		if out.CoverURI == "" {
			out.CoverURI = firstNonEmpty(str(m["cover_uri"]), str(m["cover_url"]))
		}
		if out.Duration == 0 {
			out.Duration = floatOf(m["duration"])
		}
	}
	if out.VideoID == "" && out.Title == "" {
		return nil
	}
	return out
}

// blockSources 从搜索结果 / 链接 / 引用块中提取来源列表。
func blockSources(content map[string]any) []SearchSource {
	var out []SearchSource
	seen := map[string]bool{}
	var walk func(v any, depth int)
	walk = func(v any, depth int) {
		if depth > 4 || len(out) >= 20 {
			return
		}
		switch t := v.(type) {
		case map[string]any:
			// 命中结果数组
			for _, k := range []string{"results", "search_results", "links", "items", "pages", "citations", "text_card", "result"} {
				if child, ok := t[k]; ok {
					walk(child, depth+1)
				}
			}
			url := firstNonEmpty(str(t["url"]), str(t["link"]), str(t["source_url"]))
			title := firstNonEmpty(str(t["title"]), str(t["name"]))
			if url != "" && title != "" && !seen[url] {
				seen[url] = true
				out = append(out, SearchSource{
					Title:   title,
					URL:     url,
					Snippet: firstNonEmpty(str(t["snippet"]), str(t["summary"]), str(t["description"]), str(t["text"])),
					Site:    firstNonEmpty(str(t["site_name"]), str(t["site"]), str(t["source"])),
				})
			}
			// 搜索结果通常包在 xxx_block 里，继续向下探。
			for _, child := range t {
				switch child.(type) {
				case map[string]any, []any:
					walk(child, depth+1)
				}
			}
		case []any:
			for _, it := range t {
				walk(it, depth+1)
			}
		}
	}
	walk(content, 0)
	return out
}

// textOf 兼容 text 既可能是字符串也可能是 {text: "..."} 的情况。
func textOf(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		return str(t["text"])
	}
	return ""
}

// idOf 把 id 字段（string / json.Number / float64）安全转成十进制字符串。
func idOf(v any) string {
	switch n := v.(type) {
	case string:
		return n
	case json.Number:
		return n.String()
	case float64:
		return strconv.FormatFloat(n, 'f', 0, 64)
	case int64:
		return strconv.FormatInt(n, 10)
	case int:
		return strconv.Itoa(n)
	}
	return ""
}

func floatOf(v any) float64 {
	switch n := v.(type) {
	case json.Number:
		f, _ := n.Float64()
		return f
	case float64:
		return n
	case int:
		return float64(n)
	case string:
		f, _ := strconv.ParseFloat(n, 64)
		return f
	}
	return 0
}

func firstInt(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}

// truthy 兼容 bool / "true" / "1" / 数字形式的布尔值。
func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "1" || strings.EqualFold(t, "true")
	case json.Number:
		n, _ := t.Int64()
		return n != 0
	case float64:
		return t != 0
	}
	return false
}
