package server

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"doubao2api/internal/doubao"
)

// writeJSON 输出 JSON 响应。
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("写响应失败: %v", err)
	}
}

// writeErr 输出统一的错误结构。
func writeErr(w http.ResponseWriter, code int, msg, typ string) {
	writeJSON(w, code, map[string]any{
		"error": map[string]any{"message": msg, "type": typ, "code": code},
	})
}

// writeErrKind 按上游错误分类输出错误响应。
//
// 业务错误 → 422、账号级 → 502、超时 → 504、客户端断开 → 不写
// （连接已不在，写了也只是浪费）。调用方应先用 Kind 判断是否需要写。
func writeErrKind(w http.ResponseWriter, kind doubao.Kind, msg string) {
	if kind == doubao.KindCanceled {
		return
	}
	writeErr(w, kind.HTTPStatus(), msg, kind.ErrorType())
}

// rejectUnsupportedTools 在调用方传了 tools 时按设置决定行为。
//
// 默认拒绝：静默忽略会让 Agent 类客户端拿到「看起来成功但没调用工具」的结果，
// 比显式报错更难排查。设置 tools_policy=ignore 时才按普通对话处理。
func rejectUnsupportedTools(w http.ResponseWriter, tools, toolChoice json.RawMessage) error {
	if len(tools) == 0 && len(toolChoice) == 0 {
		return nil
	}
	writeErr(w, http.StatusUnprocessableEntity,
		"本网关暂不支持工具调用（tools / tool_choice）：请移除该字段，或把 tools_policy 设为 ignore 以忽略",
		"unsupported_parameter")
	return errUnsupportedTools
}

// errUnsupportedTools 只用于让调用方提前返回（响应已写出）。
var errUnsupportedTools = errors.New("unsupported tools")
