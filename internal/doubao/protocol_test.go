package doubao

import (
	"encoding/json"
	"strings"
	"testing"
)

func testAccount() *Account {
	return &Account{
		DeviceID:       "2064247378718569",
		WebID:          "7600000000000000000",
		IID:            "3876257098650236",
		CSRFTokenValue: "csrf-from-refresh",
		Cookies: map[string]string{
			"sessionid": "sid",
		},
	}
}

func TestSecurityParamsMatchDesktop(t *testing.T) {
	a := testAccount()
	v := SecurityParams(a)

	want := map[string]string{
		"aid":                    AppID,
		"real_aid":               AppID,
		"device_platform":        "web",
		"doubao_device_platform": DevicePlatform,
		"web_platform":           WebPlatform,
		"pc_version":             PCVersion,
		"doubao_pc_version":      PCVersion,
		"runtime_version":        RuntimeVersion,
		"channel":                BuildChannel,
		"fp":                     "verify_" + a.DeviceID,
		"iid":                    a.IID,
		"device_id":              a.DeviceID,
		"client_platform":        ClientPlatform,
		"version_code":           VersionCode,
	}
	for k, expected := range want {
		if got := v.Get(k); got != expected {
			t.Errorf("%s = %q, want %q", k, got, expected)
		}
	}
	if v.Get("web_tab_id") == "" {
		t.Error("web_tab_id 不应为空")
	}
	if v.Get("web_tab_id") != SecurityParams(a).Get("web_tab_id") {
		t.Error("同一账号的 web_tab_id 必须稳定")
	}
}

func TestCSRFParamsDropRealAid(t *testing.T) {
	v := csrfParams(testAccount())
	if v.Get("real_aid") != "" {
		t.Error("CSRF 接口必须去掉 real_aid")
	}
	if v.Get("aid") != AppID {
		t.Error("CSRF 接口应保留 aid")
	}
}

func TestBuildPayloadOption(t *testing.T) {
	a := testAccount()
	p := BuildPayload(a, ChatRequest{Text: "hi", ThinkLevel: 1, SkillID: 3})
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"action_bar_skill_id":3`, `"need_deep_think":1`, `"support_chunk_delta":true`} {
		if !strings.Contains(s, want) {
			t.Errorf("payload 缺少 %s: %s", want, s)
		}
	}
	if strings.Contains(s, `"model_config"`) {
		t.Error("未传 model_item_key 时不应下发 model_config")
	}
	if strings.Contains(s, `"aggregate_params"`) {
		t.Error("未传新版参数时不应下发 aggregate_params")
	}
}

func TestBuildPayloadModelConfig(t *testing.T) {
	a := testAccount()
	p := BuildPayload(a, ChatRequest{
		Text:            "hi",
		ModelItemKey:    "doubao-pro",
		ReasoningEffort: "high",
		AgentMode:       2,
		SelectedSkills:  []string{"image"},
		Ext:             map[string]any{"custom": "1"},
	})
	b, _ := json.Marshal(p)
	s := string(b)
	for _, want := range []string{`"model_item_key":"doubao-pro"`, `"reasoning_effort":"high"`, `"agent_mode":2`, `"mention_skill_list":["image"]`, `"custom":"1"`} {
		if !strings.Contains(s, want) {
			t.Errorf("payload 缺少 %s", want)
		}
	}
}

func TestWrapSkillPrompt(t *testing.T) {
	if got := WrapSkillPrompt(SkillImageGen, "一只猫", ""); got != "生成一张图片: 一只猫" {
		t.Errorf("image prompt = %q", got)
	}
	// 已带模板前缀时不重复包裹
	if got := WrapSkillPrompt(SkillImageGen, "生成一张图片: 一只猫", ""); got != "生成一张图片: 一只猫" {
		t.Errorf("重复包裹: %q", got)
	}
	if got := WrapSkillPrompt(SkillTranslate, "hello", ""); got != "hello" {
		t.Errorf("无模板技能不应改写: %q", got)
	}
}

func TestParseStreamNewBlocks(t *testing.T) {
	sse := strings.Join([]string{
		`event: SSE_ACK`,
		`data: {"ack_client_meta":{"conversation_id":"38447714412500738"}}`,
		``,
		`event: STREAM_CHUNK`,
		`data: {"patch_op":[{"patch_value":{"content_block":[{"block_type":10005,"content":{"search_query_result_block":{"summary":"found 2","results":[{"text_card":{"title":"T1","url":"https://a.example/1","snippet":"S1"}}]}}}]}}]}`,
		``,
		`event: STREAM_CHUNK`,
		`data: {"patch_op":[{"patch_value":{"content_block":[{"block_type":10075,"content":{"lyrics_to_songs_music_block":{"task_nums":1,"lyrics_to_songs_music_content":{"video_id":"v123","title":"Summer"}}}}]}}]}`,
		``,
		`event: STREAM_CHUNK`,
		`data: {"patch_op":[{"patch_value":{"content_block":[{"block_type":10030,"content":{"artifact_block":{"title":"Report","resource_id":"r1","resource_type":2,"artifact_topic":"a1"}}}]}}]}`,
		``,
	}, "\n")

	var got struct {
		conv    string
		sources []SearchSource
		music   *MusicMessage
		arts    []Artifact
	}
	if err := parseStream(strings.NewReader(sse), func(c CompletionChunk) error {
		if c.ConvID != "" {
			got.conv = c.ConvID
		}
		got.sources = append(got.sources, c.Sources...)
		got.arts = append(got.arts, c.Artifacts...)
		if c.Music != nil {
			got.music = c.Music
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got.conv != "38447714412500738" {
		t.Errorf("conv = %q（Agw-Js-Conv 下 int64 应为字符串）", got.conv)
	}
	if len(got.sources) != 1 || got.sources[0].URL != "https://a.example/1" {
		t.Errorf("sources = %+v", got.sources)
	}
	if got.music == nil || got.music.VideoID != "v123" {
		t.Errorf("music = %+v", got.music)
	}
	if len(got.arts) != 1 || got.arts[0].ResourceID != "r1" {
		t.Errorf("artifacts = %+v", got.arts)
	}
}

func TestInt64Precision(t *testing.T) {
	obj, ok := decodeObj(`{"conversation_id":38447714412500738}`)
	if !ok {
		t.Fatal("decodeObj failed")
	}
	if got := idOf(obj["conversation_id"]); got != "38447714412500738" {
		t.Fatalf("idOf = %q", got)
	}
}
