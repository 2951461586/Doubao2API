package doubao

import (
	"io"
	"strings"
	"testing"
)

// FuzzReadSSE 对 SSE 分帧器做模糊测试。
//
// 这是整个网关最脆的一环：上游随时可能改事件格式（见 /health 的
// non_json_events），而分帧器必须保证「任何字节序列都不 panic、不死循环」。
func FuzzReadSSE(f *testing.F) {
	seeds := []string{
		"",
		"\n",
		"data: {\"a\":1}\n\n",
		"event: SSE_ACK\ndata: {\"ack_client_meta\":{}}\n\n",
		"data: 多行\ndata: 数据\n\n",
		": comment only\n\n",
		"id: 1\nevent: x\ndata: y\nretry: 10\n\n",
		"data:no-space\n\n",
		"\r\ndata: a\r\n\r\n",
		"data: " + strings.Repeat("x", 70000) + "\n\n",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, input string) {
		var events int
		err := ReadSSE(strings.NewReader(input), func(ev SSEEvent) error {
			events++
			if events > len(input)+1 {
				t.Fatalf("事件数异常：%d（可能有死循环）", events)
			}
			_ = ev.Data
			return nil
		})
		if err != nil && err != io.EOF {
			// 只允许解析类错误，不允许 panic（panic 会被 go test 捕获并失败）。
			_ = err
		}
	})
}
