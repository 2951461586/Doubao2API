package doubao

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Canvas 操作类型（来自桌面端 bundle 的枚举 690231）。
const (
	CanvasTypeGenImage = 10
	CanvasTypeGenVideo = 50
)

// 生成类型（枚举 87790 的 cm）。
const (
	GenerateTypeCreate     = 1
	GenerateTypeRegenerate = 2
	GenerateTypeGenerate   = 3
)

// 任务状态（枚举 87790 的 hY）。
const (
	TaskPending   = 1
	TaskRunning   = 2
	TaskSucceeded = 3
	TaskFailed    = 4
)

// 视频默认参数（上游在缺省时会自行选择模型，实测落到 seedance_v2.0）。
const (
	videoDefaultDuration = 15
	videoDefaultRatio    = "16:9"
)

// VideoRequest 是一次文生视频请求。
type VideoRequest struct {
	Prompt   string
	Model    string // 留空由上游选择（实测 seedance_v2.0）
	Duration int    // 秒
	Ratio    string // 如 16:9 / 9:16 / 1:1
}

// VideoResult 是一次视频生成的结果。
type VideoResult struct {
	RequestID string
	Artifact  string
	VID       string
	URL       string
	Duration  float64
	Width     int
	Height    int
}

// canvasOperation 是 exec_req.operation。
type canvasOperation struct {
	CanvasType   int            `json:"canvas_type"`
	GenerateType int            `json:"generate_type"`
	Prompt       string         `json:"prompt,omitempty"`
	UseModel     string         `json:"use_model,omitempty"`
	VideoParam   map[string]any `json:"video_param,omitempty"`
}

// canvasExecReq 是 /creativity/canvas/exec 的 exec_req。
type canvasExecReq struct {
	RequestID string          `json:"request_id"`
	Operation canvasOperation `json:"operation"`
}

// canvasExecResp 是 /creativity/canvas/exec 的响应。
type canvasExecResp struct {
	Code int `json:"code"`
	Msg  string
	Data struct {
		CanvasID     string `json:"canvas_id"`
		CanvasSubID  string `json:"canvas_sub_id"`
		RequestID    string `json:"request_id"`
		TaskStatus   int    `json:"task_status"`
		ReqOperation struct {
			CanvasType   int            `json:"canvas_type"`
			GenerateType int            `json:"generate_type"`
			Prompt       string         `json:"prompt"`
			UseModel     string         `json:"use_model"`
			VideoParam   map[string]any `json:"video_param"`
		} `json:"req_operation"`
		MessageInfo map[string]any `json:"message_info"`
		Result      struct {
			Artifacts []struct {
				ArtifactID string `json:"artifact_id"`
				Video      struct {
					DownloadURL string  `json:"download_url"`
					Duration    float64 `json:"duration"`
					Width       int     `json:"width"`
					Height      int     `json:"height"`
					VID         string  `json:"vid"`
				} `json:"video"`
			} `json:"artifacts"`
		} `json:"result"`
	} `json:"data"`
}

// GenerateVideo 提交文生视频任务并轮询到完成。
//
// 上游没有独立的视频 REST 接口，链路是「创意画布」的执行接口：
//
//	POST /creativity/canvas/exec  {exec_req:{request_id, operation:{canvas_type:50, ...}}}
//
// 该接口按 request_id 幂等：重复提交同一 exec_req 会返回同一任务的当前状态，
// 因此轮询即「原样重发」，直到 task_status=3（SUCCEEDED）后从
// data.result.artifacts[0].video 取结果。onProgress 会收到每次观察到的状态。
func (c *Client) GenerateVideo(ctx context.Context, a *Account, req VideoRequest, onProgress func(status int)) (*VideoResult, error) {
	prompt := req.Prompt
	if prompt == "" {
		return nil, fmt.Errorf("prompt 不能为空")
	}
	duration := req.Duration
	if duration <= 0 {
		duration = videoDefaultDuration
	}
	ratio := req.Ratio
	if ratio == "" {
		ratio = videoDefaultRatio
	}

	exec := canvasExecReq{
		RequestID: RandomHex(8),
		Operation: canvasOperation{
			CanvasType:   CanvasTypeGenVideo,
			GenerateType: GenerateTypeCreate,
			Prompt:       prompt,
			UseModel:     req.Model,
			VideoParam:   map[string]any{"duration": duration, "ratio": ratio},
		},
	}

	const maxPolls = 120 // 120 × 5s = 10 分钟上限
	var last *canvasExecResp
	for i := 0; i < maxPolls; i++ {
		resp, err := c.canvasExec(ctx, a, exec)
		if err != nil {
			return nil, err
		}
		last = resp
		status := resp.Data.TaskStatus
		if onProgress != nil {
			onProgress(status)
		}
		switch status {
		case TaskSucceeded:
			if len(resp.Data.Result.Artifacts) == 0 {
				return nil, fmt.Errorf("任务成功但未返回产物")
			}
			art := resp.Data.Result.Artifacts[0]
			return &VideoResult{
				RequestID: resp.Data.RequestID,
				Artifact:  art.ArtifactID,
				VID:       art.Video.VID,
				URL:       art.Video.DownloadURL,
				Duration:  art.Video.Duration,
				Width:     art.Video.Width,
				Height:    art.Video.Height,
			}, nil
		case TaskFailed:
			return nil, fmt.Errorf("上游任务失败 (task_status=%d)", status)
		}
		if err := sleepCtx(ctx, 5*time.Second); err != nil {
			return nil, err
		}
	}
	if last != nil {
		return nil, fmt.Errorf("视频生成超时（最后状态 %d）", last.Data.TaskStatus)
	}
	return nil, fmt.Errorf("视频生成超时")
}

// canvasExec 调用创意画布执行接口。
func (c *Client) canvasExec(ctx context.Context, a *Account, exec canvasExecReq) (*canvasExecResp, error) {
	payload := map[string]any{"exec_req": exec, "canvas_id_str": ""}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	u := UpstreamBase + "/creativity/canvas/exec?" + SecurityParams(a).Encode()
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	setUpstreamHeaders(hreq, a)

	resp, err := c.HTTP.Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("请求画布执行失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("画布执行 HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var out canvasExecResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("画布执行响应解析失败: %s", truncate(string(raw), 200))
	}
	if out.Code != 0 {
		return nil, fmt.Errorf("画布执行被拒绝 (code=%d): %s", out.Code, truncate(out.Msg, 200))
	}
	return &out, nil
}

// sleepCtx 可被 context 取消的等待。
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
