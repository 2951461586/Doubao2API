package server

import (
	"net/http"

	"doubao2api/internal/registry"
)

// --- 基础端点 ---

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	custom := s.Store.Settings().ModelAliases
	models := registry.List(custom)
	data := make([]map[string]any, 0, len(models))
	for _, m := range models {
		data = append(data, map[string]any{
			"id":       m.ID,
			"object":   "model",
			"created":  registry.CreatedAt,
			"owned_by": "doubao",
			"doubao": map[string]any{
				"name":        m.Name,
				"think_level": m.ThinkLevel,
				"skill":       m.Skill,
				"context":     m.Context,
				"description": m.Desc,
				// 工作模式模型（agent_mode=1）会带出上游 model_item_key。
				"model_item_key": m.ModelItemKey,
				"agent_mode":     m.AgentMode,
				// 客户端为该模型提供的推理档位（仅展示；实际强度由 reasoning_effort 决定）。
				"reasoning_levels": m.ReasoningLevels,
			},
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

// handleRoot 返回控制台单页；其余未知路径返回 404。
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeErr(w, http.StatusNotFound, "未找到该路径", "invalid_request_error")
		return
	}
	serveConsole(w, r)
}
