package server

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
)

// 本文件是控制台的状态备份 / 导出 / 导入端点。
//
// 背景（issues.md #9）：全部账号 Cookie、密钥与日志都在一份 JSON 里，
// 主机上只有这一份，误删或一次失败的导入就无法恢复。
//
// 端点：
//
//	POST /admin/api/state/snapshot   立即生成带时间戳的副本
//	GET  /admin/api/state/export     下载可迁移的状态（不含请求日志）
//	POST /admin/api/state/import?... 导入状态（?merge=1 只合并账号，?confirm=1 必需）
//
// 导入是危险操作：必须显式带 confirm=1，且默认保留当前管理密码，
// 避免用一份旧导出把管理入口锁死。

// handleAdminStateSnapshot 立即生成一份备份。
func (s *Server) handleAdminStateSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST", "invalid_request_error")
		return
	}
	name, err := s.Store.Snapshot()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "生成备份失败: "+err.Error(), "server_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": name})
}

// handleAdminStateExport 导出状态。
//
// 响应带 Content-Disposition，浏览器会直接下载；内容含账号 Cookie，
// 因此要求管理鉴权（路由层已保证）。
func (s *Server) handleAdminStateExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 GET", "invalid_request_error")
		return
	}
	raw, err := s.Store.Export()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "导出失败: "+err.Error(), "server_error")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="doubao2api-export.json"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

// handleAdminStateImport 导入状态。
func (s *Server) handleAdminStateImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST", "invalid_request_error")
		return
	}
	q := r.URL.Query()
	if q.Get("confirm") != "1" {
		writeErr(w, http.StatusBadRequest,
			"导入会覆盖账号与密钥，请确认后带上 confirm=1（merge=1 仅合并账号）", "invalid_request_error")
		return
	}
	merge := q.Get("merge") == "1"

	raw, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "读取请求体失败: "+err.Error(), "invalid_request_error")
		return
	}
	added, updated, err := s.Store.Import(raw, merge)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	if err := s.Store.Save(); err != nil {
		writeErr(w, http.StatusInternalServerError, "导入后保存失败: "+err.Error(), "server_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "added": added, "updated": updated, "merge": merge,
	})
}

// backupFiles 返回备份目录下的文件名（供控制台展示，不含内容）。
func (s *Server) backupFiles(limit int) []map[string]any {
	dir := filepath.Join(filepath.Dir(s.Store.Path()), "backups")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, map[string]any{
			"name": e.Name(), "size": info.Size(), "modified": info.ModTime().Unix(),
		})
	}
	// 新的在前。
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// atoiOr 解析整数，失败时返回默认值。
func atoiOr(raw string, def int) int {
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return n
}
