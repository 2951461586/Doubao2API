package doubao

import (
	"errors"
	"strings"
	"testing"
)

// 本文件直接压 ReadSSE 的分帧边界：流式解析的绝大多数「回答空白」故障，
// 根因都在这层（行尾 CR、多行 data、注释心跳、超长行、回调提前中止）。

func collectEvents(t *testing.T, raw string) ([]SSEEvent, error) {
	t.Helper()
	var got []SSEEvent
	err := ReadSSE(strings.NewReader(raw), func(ev SSEEvent) error {
		got = append(got, ev)
		return nil
	})
	return got, err
}

func TestReadSSEFraming(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []SSEEvent
	}{
		{
			name: "空流",
			raw:  "",
			want: nil,
		},
		{
			name: "仅注释与心跳不产生事件",
			raw:  ": ping\n: ping\n\n",
			want: nil,
		},
		{
			name: "CRLF 行尾",
			raw:  "event: chunk\r\ndata: {\"text\":\"a\"}\r\n\r\n",
			want: []SSEEvent{{Event: "chunk", Data: `{"text":"a"}`}},
		},
		{
			name: "多行 data 以换行拼接",
			raw:  "data: a\ndata: b\ndata: c\n\n",
			want: []SSEEvent{{Data: "a\nb\nc"}},
		},
		{
			name: "冒号后无空格仍取到值",
			raw:  "data:{\"text\":\"a\"}\n\n",
			want: []SSEEvent{{Data: `{"text":"a"}`}},
		},
		{
			name: "无冒号的行按空值字段处理",
			raw:  "data\n\n",
			want: []SSEEvent{{Data: ""}},
		},
		{
			name: "只有 event 没有 data 也派发",
			raw:  "event: SSE_ACK\n\n",
			want: []SSEEvent{{Event: "SSE_ACK"}},
		},
		{
			name: "未知字段与 retry 被忽略",
			raw:  "retry: 3000\nfoo: bar\ndata: x\n\n",
			want: []SSEEvent{{Data: "x"}},
		},
		{
			name: "EOF 前未以空行结尾的事件仍派发",
			raw:  "event: tail\ndata: last",
			want: []SSEEvent{{Event: "tail", Data: "last"}},
		},
		{
			name: "多事件按顺序",
			raw:  "data: 1\n\ndata: 2\n\n",
			want: []SSEEvent{{Data: "1"}, {Data: "2"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := collectEvents(t, tc.raw)
			if err != nil {
				t.Fatalf("ReadSSE 失败: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("事件数 = %d, want %d: %+v", len(got), len(tc.want), got)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("第 %d 个事件 = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// 回调返回错误时必须立即停止读取，且错误原样上抛。
func TestReadSSEStopsOnCallbackError(t *testing.T) {
	stop := errors.New("stop here")
	seen := 0
	err := ReadSSE(strings.NewReader("data: 1\n\ndata: 2\n\ndata: 3\n\n"), func(SSEEvent) error {
		seen++
		return stop
	})
	if !errors.Is(err, stop) {
		t.Fatalf("错误 = %v, want %v", err, stop)
	}
	if seen != 1 {
		t.Errorf("回调在错误后仍被调用，次数 = %d, want 1", seen)
	}
}

// 底层读取错误必须上报，不能当成正常 EOF。
type failReader struct {
	data string
	done bool
}

func (r *failReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, errors.New("connection reset")
	}
	r.done = true
	return copy(p, r.data), nil
}

func TestReadSSEReadError(t *testing.T) {
	var n int
	err := ReadSSE(&failReader{data: "data: x\n"}, func(SSEEvent) error {
		n++
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("应上报底层读取错误，实际: %v", err)
	}
}

// 单行超过 4MB 缓冲上限时必须报错，而不是截断成半个 JSON 静默丢事件。
func TestReadSSEOversizedLine(t *testing.T) {
	huge := "data: " + strings.Repeat("x", 4<<20+16) + "\n\n"
	err := ReadSSE(strings.NewReader(huge), func(SSEEvent) error { return nil })
	if err == nil {
		t.Fatal("超长行应返回错误")
	}
	if !strings.Contains(err.Error(), "token too long") {
		t.Errorf("错误应为 token too long，实际: %v", err)
	}
}

// io.Reader 直接返回 EOF 时应无错误、无事件。
func TestReadSSEEmptyReader(t *testing.T) {
	got, err := collectEvents(t, "")
	if err != nil || len(got) != 0 {
		t.Fatalf("空输入: got=%+v err=%v", got, err)
	}
}
