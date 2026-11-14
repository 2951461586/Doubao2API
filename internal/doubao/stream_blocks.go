package doubao

// blockHandler 处理单个 content_block。
//
// bt 是块类型（保留给「一个函数处理多种块」的场景），content 是该块的
// content / content_obj，finished 表示该块是否已结束（is_finish）。
type blockHandler func(st *streamState, bt int, content map[string]any, blockID string, finished bool) error

// blockHandlers 是块类型 → 处理函数的注册表。
//
// 新增块类型只需要：写一个处理函数 + 在这里注册一行；未知类型仍由
// handleUnknownBlock 兜底（带 summary/title 的透传为工具提示，避免丢信息）。
var blockHandlers = map[int]blockHandler{
	BlockThinking:        handleThinking,
	BlockText:            handleText,
	BlockLoading:         handleLoading,
	BlockGenericTool:     handleGenericTool,
	BlockSearchQuery:     handleSearchQuery,
	BlockSearchResult:    handleSearchResult,
	BlockSearchResultV2:  handleSearchResult,
	BlockSearchRichMedia: handleSearchResult,
	BlockCode:            handleCode,
	BlockGenImage:        handleGenImage,
	BlockImageAnalysis:   handleTaggedText,
	BlockResearch:        handleTaggedText,
	BlockResearchStatus:  handleTaggedText,
	BlockResearchWebpage: handleTaggedText,
	BlockFileOperation:   handleFileOperation,
	BlockArtifact:        handleArtifact,
	BlockArtifactOp:      handleArtifact,
	BlockArtifactCode:    handleArtifact,
	BlockLink:            handleSourceAndText,
	BlockReference:       handleSourceAndText,
	BlockTips:            handleSourceAndText,
	BlockLyricsMusic:     handleMusic,
	BlockCreation:        handleCreation,
}

// handleBlock 按块类型分派；未注册的类型走兜底逻辑。
func (st *streamState) handleBlock(bt int, content map[string]any, blockID string, finished bool) error {
	if h, ok := blockHandlers[bt]; ok {
		return h(st, bt, content, blockID, finished)
	}
	return handleUnknownBlock(st, bt, content)
}

// handleThinking 处理思考容器块：只计数量，文本由后续 text 块给出。
func handleThinking(st *streamState, _ int, _ map[string]any, _ string, _ bool) error {
	st.thinkingCount++
	return nil
}

// handleText 处理正文 / 思维链文本块。
func handleText(st *streamState, _ int, content map[string]any, _ string, _ bool) error {
	tb, _ := content["text_block"].(map[string]any)
	if t := str(tb["text"]); t != "" {
		return st.emitText(t)
	}
	return nil
}

// handleLoading 处理「加载中」提示块。
func handleLoading(st *streamState, _ int, content map[string]any, _ string, _ bool) error {
	lb, _ := content["loading_block"].(map[string]any)
	tl, _ := lb["text_loading"].(map[string]any)
	return st.emitTool(str(tl["text"]))
}

// handleGenericTool 处理通用工具块。
func handleGenericTool(st *streamState, _ int, content map[string]any, _ string, _ bool) error {
	if title := blockText(content, "generic_tool_block"); title != "" {
		return st.emitTool("[tool] " + title)
	}
	return nil
}

// handleSearchQuery 处理搜索词块。
func handleSearchQuery(st *streamState, _ int, content map[string]any, _ string, _ bool) error {
	if q := blockText(content, "search_query_block"); q != "" {
		return st.emitTool("[search] 查询: " + q)
	}
	return nil
}

// handleSearchResult 处理联网搜索结果块（10025 / 10005 / 10050）。
func handleSearchResult(st *streamState, _ int, content map[string]any, _ string, _ bool) error {
	if s := firstNonEmpty(
		blockField(content, "search_query_result_block", "summary"),
		blockField(content, "search_result_block", "summary"),
	); s != "" {
		if err := st.emitTool("[search] " + s); err != nil {
			return err
		}
	}
	return st.emitSources(content)
}

// handleCode 处理代码块。
//
// 代码会随 patch 不断增长，因此只在块完成时以 Markdown 围栏输出一次。
func handleCode(st *streamState, _ int, content map[string]any, blockID string, finished bool) error {
	cbk, _ := content["code_block"].(map[string]any)
	code := str(cbk["code"])
	if code == "" || !finished {
		return nil
	}
	if !st.markSeen("code:" + blockID + ":" + code) {
		return nil
	}
	lang := codeLang(intOf(cbk["type"]))
	return st.onChunk(CompletionChunk{Text: "\n```" + lang + "\n" + code + "\n```\n"})
}

// handleGenImage 处理生图过程块。
func handleGenImage(st *streamState, _ int, content map[string]any, _ string, _ bool) error {
	for _, cr := range parseGenImages(content) {
		if cr.URL == "" || !st.markSeen("img:"+cr.URL) {
			continue
		}
		if err := st.onChunk(CompletionChunk{Creations: []Creation{cr}}); err != nil {
			return err
		}
	}
	return nil
}

// handleTaggedText 处理「带标签的文本」类块（研究 / 图片理解 / 状态等）。
func handleTaggedText(st *streamState, bt int, content map[string]any, _ string, _ bool) error {
	if t := blockText(content, blockKeyFor(bt)); t != "" {
		return st.emitTool("[" + blockTag(bt) + "] " + t)
	}
	return nil
}

// handleFileOperation 处理文件操作块。
func handleFileOperation(st *streamState, _ int, content map[string]any, _ string, _ bool) error {
	t := firstNonEmpty(
		blockField(content, "file_operation_block", "title"),
		blockField(content, "file_operation_block", "summary"),
	)
	if t != "" {
		return st.emitTool("[file] " + t)
	}
	return nil
}

// handleArtifact 处理产物块（文档 / 代码 / PPT 等）。
func handleArtifact(st *streamState, _ int, content map[string]any, _ string, _ bool) error {
	for _, art := range parseArtifacts(content) {
		if art.ResourceID == "" && art.Title == "" && art.ArtifactID == "" {
			continue
		}
		if !st.markSeen("art:" + art.ResourceID + art.ArtifactID + art.Title) {
			continue
		}
		if err := st.onChunk(CompletionChunk{Artifacts: []Artifact{art}}); err != nil {
			return err
		}
	}
	return nil
}

// handleSourceAndText 处理链接 / 引用 / 提示块：既可能是来源，也可能带说明。
func handleSourceAndText(st *streamState, bt int, content map[string]any, _ string, _ bool) error {
	if err := st.emitSources(content); err != nil {
		return err
	}
	if t := blockText(content, blockKeyFor(bt)); t != "" {
		return st.emitTool("[" + blockTag(bt) + "] " + t)
	}
	return nil
}

// handleMusic 处理音乐成品块。
func handleMusic(st *streamState, _ int, content map[string]any, _ string, _ bool) error {
	m := parseMusic(content)
	if m == nil || !st.markSeen("music:"+m.VideoID+m.Title) {
		return nil
	}
	return st.onChunk(CompletionChunk{Music: m})
}

// handleCreation 处理生成结果块（文生图 / 视频产物）。
//
// 上游会分多个 patch 重复推送同一个 creation（先占位、后补 URL），
// 因此按产物 URL 去重，避免同一张图重复下发。
func handleCreation(st *streamState, _ int, content map[string]any, _ string, _ bool) error {
	for _, cr := range parseCreations(content) {
		if !st.markSeen("creation:" + cr.URL) {
			continue
		}
		if err := st.onChunk(CompletionChunk{Creations: []Creation{cr}}); err != nil {
			return err
		}
	}
	return nil
}

// handleUnknownBlock 是未知块类型的兜底。
//
// 仅在带 summary / title 时作为工具提示透传，避免上游新增块类型时静默丢信息。
func handleUnknownBlock(st *streamState, bt int, content map[string]any) error {
	if bt < 10000 {
		return nil
	}
	key := blockKeyFor(bt)
	t := firstNonEmpty(blockField(content, key, "summary"), blockField(content, key, "title"))
	if t == "" {
		return nil
	}
	return st.emitTool("[" + blockTag(bt) + "] " + t)
}

// parseCreations 解析 block_type=2074 的 creation_block。
//
// 上游会分多个 patch 推送同一个 creation：首个 patch 只有 placeholder（无 URL），
// 后续 patch 才带 image.image_ori.url，因此这里只返回已有 URL 的产物。
func parseCreations(content map[string]any) []Creation {
	cb, _ := content["creation_block"].(map[string]any)
	if cb == nil {
		return nil
	}
	var out []Creation
	for _, it := range asSlice(cb["creations"]) {
		cm, ok := it.(map[string]any)
		if !ok {
			continue
		}
		img, _ := cm["image"].(map[string]any)
		if img == nil {
			continue
		}
		cr := Creation{ID: str(cm["id"]), Type: intOf(cm["type"]), URI: str(img["key"])}
		if gd, ok := cm["gen_detail"].(map[string]any); ok {
			cr.TaskType = intOf(gd["task_type"])
		}
		if ori, ok := img["image_ori"].(map[string]any); ok {
			cr.URL = str(ori["url"])
			cr.Width = intOf(ori["width"])
			cr.Height = intOf(ori["height"])
		}
		if cr.URL == "" {
			if th, ok := img["image_thumb"].(map[string]any); ok {
				cr.URL = str(th["url"])
			}
		}
		if cr.URL != "" {
			out = append(out, cr)
		}
	}
	return out
}
