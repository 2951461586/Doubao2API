package server

import (
	"fmt"
	"strings"

	"doubao2api/internal/doubao"
)

// responseCollector 汇总一次对话产生的文本与结构化产物。
//
// 上游对同一个产物会分多个 patch 重复推送（creation / 来源 / 产物 / 音乐），
// 因此这里统一按 ID 或 URL 去重。阻塞式端点读最终状态，流式端点读增量，
// 两条路径共用同一份去重与累积逻辑（此前是两份几乎相同的代码）。
type responseCollector struct {
	out      strings.Builder
	thinking strings.Builder
	convID   string

	imageURLs []string
	sources   []doubao.SearchSource
	artifacts []doubao.Artifact
	music     []*doubao.MusicMessage

	seenCreation map[string]bool
	seenSource   map[string]bool
	seenArtifact map[string]bool
	seenMusic    map[string]bool
}

// chunkDelta 是本次 absorb 新产生的结构化产物。
type chunkDelta struct {
	images    []string
	sources   []doubao.SearchSource
	artifacts []doubao.Artifact
	music     *doubao.MusicMessage
}

// newResponseCollector 构造一个空的收集器。
func newResponseCollector() *responseCollector {
	return &responseCollector{
		seenCreation: map[string]bool{},
		seenSource:   map[string]bool{},
		seenArtifact: map[string]bool{},
		seenMusic:    map[string]bool{},
	}
}

// absorb 吸收一个上游增量，返回本次新增的结构化产物。
//
// 上游以 error_code 表示业务错误，这里转成 error 交由调用方中止。
func (c *responseCollector) absorb(ch doubao.CompletionChunk) (chunkDelta, error) {
	if ch.ConvID != "" {
		c.convID = ch.ConvID
	}
	if ch.ErrorCode != 0 {
		return chunkDelta{}, fmt.Errorf("上游错误 %d: %s", ch.ErrorCode, ch.ErrorMsg)
	}

	var d chunkDelta
	if imgs := newCreationURLs(ch.Creations, c.seenCreation); len(imgs) > 0 {
		c.imageURLs = append(c.imageURLs, imgs...)
		d.images = imgs
	}
	for _, src := range ch.Sources {
		key := src.URL + "|" + src.Title
		if src.URL == "" || c.seenSource[key] {
			continue
		}
		c.seenSource[key] = true
		c.sources = append(c.sources, src)
		d.sources = append(d.sources, src)
	}
	for _, art := range ch.Artifacts {
		key := art.ResourceID + art.ArtifactID + art.Title
		if c.seenArtifact[key] {
			continue
		}
		c.seenArtifact[key] = true
		c.artifacts = append(c.artifacts, art)
		d.artifacts = append(d.artifacts, art)
	}
	if ch.Music != nil {
		key := ch.Music.VideoID + ch.Music.Title
		if !c.seenMusic[key] {
			c.seenMusic[key] = true
			c.music = append(c.music, ch.Music)
			d.music = ch.Music
		}
	}
	c.out.WriteString(ch.Text)
	c.thinking.WriteString(ch.Thinking)
	return d, nil
}

// text 返回已累积的正文。
func (c *responseCollector) text() string { return c.out.String() }

// thinkingText 返回已累积的思维链。
func (c *responseCollector) thinkingText() string { return c.thinking.String() }

// length 返回已累积正文的字节数（用于日志 / usage 估算）。
func (c *responseCollector) length() int { return c.out.Len() }
