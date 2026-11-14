package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"doubao2api/internal/doubao"
)

func (s *Server) handleConversations(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/conversations")
	rest = strings.Trim(rest, "/")
	if rest == "" {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "仅支持 GET", "invalid_request_error")
			return
		}
		s.listConversations(w, r)
		return
	}
	parts := strings.Split(rest, "/")
	id := strings.TrimSpace(parts[0])
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	if id == "" {
		writeErr(w, http.StatusBadRequest, "缺少 conversation id", "invalid_request_error")
		return
	}

	switch r.Method {
	case http.MethodPatch:
		action = "rename"
	case http.MethodDelete:
		action = "archive"
	case http.MethodPost:
		if action == "" {
			action = "archive"
		}
	default:
		writeErr(w, http.StatusMethodNotAllowed, "不支持的方法", "invalid_request_error")
		return
	}

	name := ""
	if action == "rename" {
		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "请求体解析失败", "invalid_request_error")
			return
		}
		name = strings.TrimSpace(body.Name)
		if name == "" {
			writeErr(w, http.StatusBadRequest, "name 不能为空", "invalid_request_error")
			return
		}
	}

	if _, ok := s.runUpstream(w, r, upstreamCall{Label: "conversations"},
		func(ctx context.Context, acct *doubao.Account) (int, error) {
			return 0, s.Up.ConversationAction(ctx, acct, action, id, name)
		}); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "action": action, "id": id})
}

// listConversations 拉取一页会话。

func (s *Server) listConversations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 {
		offset = 0
	}
	var types []int
	if raw := strings.TrimSpace(q.Get("types")); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			if n, err := strconv.Atoi(strings.TrimSpace(part)); err == nil {
				types = append(types, n)
			}
		}
	}

	var page *doubao.ConversationPage
	if _, ok := s.runUpstream(w, r, upstreamCall{
		Label:     "conversations",
		AccountID: q.Get("account_id"),
	}, func(ctx context.Context, acct *doubao.Account) (int, error) {
		p, err := s.Up.Conversations(ctx, acct, offset, limit, types)
		if err != nil {
			return 0, err
		}
		page = p
		return len(p.Conversations), nil
	}); !ok {
		return
	}
	if page == nil {
		page = &doubao.ConversationPage{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object":   "list",
		"data":     page.Conversations,
		"has_more": page.HasMore,
		"offset":   offset,
		"limit":    limit,
	})
}
