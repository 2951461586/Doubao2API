package doubao

import (
	"encoding/json"
	"fmt"
	"io"
)

// 本文件把上游 SSE 事件流还原成增量（CompletionChunk）。
//
// 上游不返回纯文本，而是一串 content_block 补丁；事件信封的解析留在这里，
// 具体块类型的处理在 stream_blocks.go 的注册表里，新增块类型只需加一个
// 处理函数 + 一行注册，不必再改动主循环。
//
//go:generate true

// streamState 是解析一条 SSE 流期间的运行状态。
type streamState struct {
	onChunk func(CompletionChunk) error

	// thinkingCount 记录已出现的思考容器块数量：
	// 第 1 个容器内的文本算思维链，之后算正文（上游的补丁流约定）。
	thinkingCount int

	seen     map[string]bool // 结构化产物去重（代码 / 来源 / 图片 / 音乐 / 产物）
	seenTool map[string]bool // 工具提示去重
}

// parseStream 解析上游 SSE，把增量回调给调用方。
func parseStream(r io.Reader, onChunk func(CompletionChunk) error) error {
	st := &streamState{
		onChunk:  onChunk,
		seen:     map[string]bool{},
		seenTool: map[string]bool{},
	}
	return ReadSSE(r, st.handleEvent)
}

// inThinking 报告当前增量是否属于思维链。
func (st *streamState) inThinking() bool { return st.thinkingCount == 1 }

// emitText 按当前阶段把文本归入正文或思维链。
func (st *streamState) emitText(text string) error {
	ch := CompletionChunk{}
	if st.inThinking() {
		ch.Thinking = text
	} else {
		ch.Text = text
	}
	return st.onChunk(ch)
}

// emitTool 输出一条工具提示（同文去重）。
func (st *streamState) emitTool(text string) error {
	if text == "" || st.seenTool[text] {
		return nil
	}
	st.seenTool[text] = true
	return st.onChunk(CompletionChunk{ToolInfo: text})
}

// markSeen 记录一个去重键，返回它是否是首次出现。
func (st *streamState) markSeen(key string) bool {
	if st.seen[key] {
		return false
	}
	st.seen[key] = true
	return true
}

// emitSources 输出来源列表（按 url + title 去重）。
func (st *streamState) emitSources(content map[string]any) error {
	for _, src := range blockSources(content) {
		if src.URL == "" {
			continue
		}
		if !st.markSeen("src:" + src.URL + "|" + src.Title) {
			continue
		}
		if err := st.onChunk(CompletionChunk{Sources: []SearchSource{src}}); err != nil {
			return err
		}
	}
	return nil
}

// handleEvent 处理一个 SSE 事件。
func (st *streamState) handleEvent(ev SSEEvent) error {
	if ev.Event == "gateway-error" {
		var e Error
		if json.Unmarshal([]byte(ev.Data), &e) == nil {
			return fmt.Errorf("网关错误: %s", firstNonEmpty(e.Text, e.Msg, ev.Data))
		}
		return fmt.Errorf("网关错误: %s", truncate(ev.Data, 200))
	}
	if ev.Data == "" {
		return nil
	}
	obj, ok := decodeObj(ev.Data)
	if !ok {
		return nil // 非 JSON 事件直接跳过
	}

	// 会话建立
	if ev.Event == "SSE_ACK" {
		ack, _ := obj["ack_client_meta"].(map[string]any)
		if cid := idOf(ack["conversation_id"]); cid != "" && cid != "0" {
			return st.onChunk(CompletionChunk{ConvID: cid})
		}
		return nil
	}

	// 业务错误
	if ev.Event == "STREAM_ERROR" || obj["error_code"] != nil {
		if code := intOf(obj["error_code"]); code != 0 {
			return st.onChunk(CompletionChunk{ErrorCode: code, ErrorMsg: str(obj["error_msg"])})
		}
	}

	// CHUNK_DELTA：紧凑的 {"text": "..."}
	if t, ok := obj["text"].(string); ok && t != "" && obj["error_code"] == nil {
		return st.emitText(t)
	}

	for _, cb := range iterBlocks(obj) {
		content, _ := cb["content"].(map[string]any)
		if content == nil {
			// 部分响应使用 content_obj
			content, _ = cb["content_obj"].(map[string]any)
		}
		err := st.handleBlock(intOf(cb["block_type"]), content, idOf(cb["block_id"]), truthy(cb["is_finish"]))
		if err != nil {
			return err
		}
	}
	return st.handleContentPatches(obj)
}

// handleContentPatches 处理 patch_op[].patch_value.content 为 JSON 字符串的新格式增量。
func (st *streamState) handleContentPatches(obj map[string]any) error {
	for _, po := range asSlice(obj["patch_op"]) {
		pm, _ := po.(map[string]any)
		pv, _ := pm["patch_value"].(map[string]any)
		cs, ok := pv["content"].(string)
		if !ok || cs == "" {
			continue
		}
		inner, ok := decodeObj(cs)
		if !ok {
			continue
		}
		if t := str(inner["text"]); t != "" {
			if err := st.emitText(t); err != nil {
				return err
			}
		}
	}
	return nil
}

// iterBlocks 从事件对象中取出全部 content_block。
func iterBlocks(obj map[string]any) []map[string]any {
	var out []map[string]any
	for _, po := range asSlice(obj["patch_op"]) {
		pm, _ := po.(map[string]any)
		pv, _ := pm["patch_value"].(map[string]any)
		for _, cb := range asSlice(pv["content_block"]) {
			if m, ok := cb.(map[string]any); ok {
				out = append(out, m)
			}
		}
	}
	if dc, ok := obj["content"].(map[string]any); ok {
		for _, cb := range asSlice(dc["content_block"]) {
			if m, ok := cb.(map[string]any); ok {
				out = append(out, m)
			}
		}
	}
	return out
}
