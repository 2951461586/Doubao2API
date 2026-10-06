package doubao

import (
	"bufio"
	"io"
	"strings"
)

// SSEEvent 是一个 Server-Sent Events 事件。
type SSEEvent struct {
	ID    string
	Event string
	Data  string
}

// ReadSSE 按 SSE 规范解析事件流。
//
// 上游会在同一事件里发送多行 data（例如文本增量中含裸换行），
// 规范要求这些行以 "\n" 拼接后作为一个整体，这里严格遵循。
func ReadSSE(r io.Reader, fn func(SSEEvent) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var (
		id, event string
		data      []string
		hasData   bool
	)
	dispatch := func() error {
		if !hasData && event == "" && id == "" {
			return nil
		}
		ev := SSEEvent{ID: id, Event: event, Data: strings.Join(data, "\n")}
		id, event, data, hasData = "", "", nil, false
		return fn(ev)
	}

	for sc.Scan() {
		line := sc.Text()
		// 去掉行尾 CR（上游同时使用 \r\n）
		line = strings.TrimSuffix(line, "\r")

		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // 注释/心跳
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")

		switch field {
		case "id":
			id = value
		case "event":
			event = value
		case "data":
			data = append(data, value)
			hasData = true
		case "retry":
			// 忽略
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return dispatch()
}
