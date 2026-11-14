package server

import (
	"context"
	"net/http"

	"doubao2api/internal/doubao"
	"doubao2api/internal/registry"
)

func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 GET", "invalid_request_error")
		return
	}
	var list *doubao.SkillList
	if _, ok := s.runUpstream(w, r, upstreamCall{Label: "skills"},
		func(ctx context.Context, acct *doubao.Account) (int, error) {
			l, err := s.Up.Skills(ctx, acct)
			if err != nil {
				return 0, err
			}
			list = l
			return len(l.Skills), nil
		}); !ok {
		return
	}

	// 上游返回的技能 -> 可读信息
	type upstreamSkill struct {
		doubao.Skill
	}
	up := map[int]upstreamSkill{}
	if list != nil {
		for _, sk := range list.Skills {
			up[sk.SkillType] = upstreamSkill{sk}
		}
	}

	modelBySkill := map[int]string{}
	for _, m := range registry.Builtin {
		if m.Skill != 0 {
			if _, ok := modelBySkill[m.Skill]; !ok {
				modelBySkill[m.Skill] = m.ID
			}
		}
	}

	data := make([]map[string]any, 0, len(doubao.SkillMetas)+len(up))
	seen := map[int]bool{}
	for _, meta := range doubao.SkillMetas {
		seen[meta.Type] = true
		item := map[string]any{
			"skill_type": meta.Type,
			"name":       meta.Name,
			"aliases":    meta.Aliases,
			"model":      modelBySkill[meta.Type],
		}
		if meta.Desc != "" {
			item["description"] = meta.Desc
		}
		if meta.DefaultPrompt != "" {
			item["default_prompt"] = meta.DefaultPrompt
		}
		if sk, ok := up[meta.Type]; ok {
			if sk.Name != "" {
				item["name"] = sk.Name
			}
			if sk.ShowNameShort != "" {
				item["short_name"] = sk.ShowNameShort
			}
			if sk.Description != "" {
				item["description"] = sk.Description
			}
			if sk.DefaultPrompt != "" {
				item["default_prompt"] = sk.DefaultPrompt
			}
		}
		data = append(data, item)
	}
	// 上游独有的技能也一并暴露
	for t, sk := range up {
		if seen[t] {
			continue
		}
		data = append(data, map[string]any{
			"skill_type":     t,
			"name":           sk.Name,
			"short_name":     sk.ShowNameShort,
			"description":    sk.Description,
			"default_prompt": sk.DefaultPrompt,
			"model":          modelBySkill[t],
		})
	}

	resp := map[string]any{"object": "list", "data": data}
	if list != nil {
		resp["input_skill_types"] = list.Input
		resp["sidebar_skill_types"] = list.Sidebar
		resp["hide_in_lp"] = list.HideInLP
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleConversations 处理会话列表与单会话操作。
//
//	GET    /v1/conversations?limit=20&offset=0&types=3
//	PATCH  /v1/conversations/{id}          {"name":"..."}
//	DELETE /v1/conversations/{id}          归档
//	POST   /v1/conversations/{id}/pin|unpin|archive|clear

func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	models := registry.List(s.Store.Settings().ModelAliases)
	skills := make([]map[string]any, 0, len(doubao.SkillMetas))
	for _, m := range doubao.SkillMetas {
		skills = append(skills, map[string]any{
			"skill_type":     m.Type,
			"name":           m.Name,
			"aliases":        m.Aliases,
			"default_prompt": m.DefaultPrompt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "capabilities",
		"upstream": map[string]any{
			"app_id":          doubao.AppID,
			"version_code":    doubao.VersionCode,
			"pc_version":      doubao.PCVersion,
			"runtime_version": doubao.RuntimeVersion,
			"chromium":        doubao.ChromiumVersion,
		},
		"models": models,
		"skills": skills,
		"rewards": map[string]any{
			"campaign_group":  doubao.IncentiveCampaignGroupV3,
			"auto_checkin":    s.Store.Settings().AutoCheckin,
			"auto_claim":      s.Store.Settings().AutoClaimTasks,
			"auto_do_tasks":   s.Store.Settings().AutoDoTasks,
			"auto_task_limit": s.Store.Settings().AutoTaskLimit,
		},
		"endpoints": []string{
			"/v1/chat/completions", "/v1/messages", "/v1/models", "/v1/skills",
			"/v1/conversations", "/v1/images/generations", "/v1/videos/generations",
			"/v1/music/lyrics", "/v1/music/audio", "/v1/capabilities",
			"/v1/credits", "/v1/credits/history", "/v1/checkin",
			"/v1/tasks", "/v1/tasks/claim", "/v1/tasks/claim-completed", "/v1/tasks/run",
		},
	})
}
