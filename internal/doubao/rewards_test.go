package doubao

import (
	"errors"
	"testing"
)

func TestParseCampaignTasks(t *testing.T) {
	obj, ok := decodeObj(`{"tasks":[
		{"campaign_code":"pc_task_a","campaign_group_code":"pc_incentive_task_mvp_v3","task_kind":"chat_activity","task_status":2,"reward_mode":1,"instance_task_id":"inst-1","title":"A","reward_summaries":[{"reward_amount":100,"reward_type":"quota_grant"}]},
		{"campaign_code":"pc_task_b","campaign_group_code":"pc_incentive_task_mvp_v3","task_status":1,"reward_mode":2,"title":"B","reward_summaries":[{"reward_amount":50,"reward_type":"quota_grant"}]},
		{"campaign_code":"pc_task_c","campaign_group_code":"pc_incentive_task_mvp_v3","task_status":3,"reward_mode":1,"title":"C"}
	]}`)
	if !ok {
		t.Fatal("decode failed")
	}
	tasks := parseCampaignTasks(asSlice(obj["tasks"]))
	if len(tasks) != 3 {
		t.Fatalf("tasks = %d", len(tasks))
	}
	if tasks[0].StatusName != "completed" || !tasks[0].Claimable || tasks[0].InstanceTaskID != "inst-1" {
		t.Errorf("task0 = %+v", tasks[0])
	}
	if tasks[0].Rewards[0].Amount != 100 || tasks[0].Rewards[0].Type != "quota_grant" {
		t.Errorf("task0 rewards = %+v", tasks[0].Rewards)
	}
	// DirectClaim 即使未完成也允许领取
	if !tasks[1].Claimable {
		t.Errorf("direct claim task should be claimable: %+v", tasks[1])
	}
	// 已领取的不再可领
	if tasks[2].Claimable {
		t.Errorf("rewarded task should not be claimable: %+v", tasks[2])
	}
}

func TestTaskStatusName(t *testing.T) {
	cases := map[int]string{
		TaskStatusUnknown:    "unknown",
		TaskStatusCreated:    "created",
		TaskStatusCompleted:  "completed",
		TaskStatusRewarded:   "rewarded",
		TaskStatusInProgress: "in_progress",
	}
	for in, want := range cases {
		if got := TaskStatusName(in); got != want {
			t.Errorf("TaskStatusName(%d) = %q, want %q", in, got, want)
		}
	}
}

// TestClassifyClaimResult 锁死领取响应的三种语义：
// 0/4/6 = 成功，3 = 确认中（pending），其余 = 失败。
//
// 历史 bug：3 被当成失败，导致「签到」被误报为错误。
func TestClassifyClaimResult(t *testing.T) {
	cases := []struct {
		name    string
		obj     map[string]any
		pending bool
		wantErr bool
	}{
		{name: "无 biz_error", obj: map[string]any{"data": map[string]any{}}},
		{name: "biz 0 成功", obj: map[string]any{"data": map[string]any{"biz_error": map[string]any{"code": 0}}}},
		{name: "biz 4 成功", obj: map[string]any{"data": map[string]any{"biz_error": map[string]any{"code": 4}}}},
		{name: "biz 6 成功", obj: map[string]any{"data": map[string]any{"biz_error": map[string]any{"code": 6}}}},
		{
			name:    "biz 3 确认中",
			obj:     map[string]any{"data": map[string]any{"biz_error": map[string]any{"code": 3, "message": "领取结果确认中，请稍后重试"}}},
			pending: true,
			wantErr: true,
		},
		{
			name:    "biz 2 失败",
			obj:     map[string]any{"data": map[string]any{"biz_error": map[string]any{"code": 2, "message": "已领取过"}}},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := classifyClaimResult(tc.obj)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if tc.pending && !errors.Is(err, ErrClaimPending) {
				t.Errorf("biz 3 应映射为 ErrClaimPending，实际 %v", err)
			}
			if !tc.pending && tc.wantErr && errors.Is(err, ErrClaimPending) {
				t.Errorf("非 3 的错误不应被判为 pending: %v", err)
			}
		})
	}
}

// TestParseDailyCheckin 验证「每日签到」状态块的解析。
func TestParseDailyCheckin(t *testing.T) {
	if got := parseDailyCheckin(nil); got != nil {
		t.Errorf("nil 输入应返回 nil，实际 %+v", got)
	}

	raw := map[string]any{
		"checked_in_today":  false,
		"continuous_days":   1,
		"last_checkin_date": "2026-11-13",
		"total_days":        1,
		"reward": map[string]any{
			"reward_amount": 200,
			"reward_type":   "quota_grant",
		},
	}
	got := parseDailyCheckin(raw)
	if got == nil {
		t.Fatal("应解析出签到状态")
	}
	if got.CheckedInToday || got.ContinuousDays != 1 || got.LastCheckinDate != "2026-11-13" ||
		got.TotalDays != 1 || got.RewardAmount != 200 || got.RewardType != "quota_grant" {
		t.Errorf("解析结果不对: %+v", got)
	}

	// 已签到
	raw["checked_in_today"] = true
	if got := parseDailyCheckin(raw); !got.CheckedInToday {
		t.Error("checked_in_today 应解析为 true")
	}
	// 无 reward 字段时不应 panic
	if got := parseDailyCheckin(map[string]any{"checked_in_today": true}); got == nil || got.RewardAmount != 0 {
		t.Errorf("缺字段时应零值填充，实际 %+v", got)
	}
}

// TestParseCampaignTasksDailySignin 验证任务解析会把 daily_checkin 带上。
func TestParseCampaignTasksDailySignin(t *testing.T) {
	items := []any{
		map[string]any{
			"campaign_code":       "daily_signin",
			"campaign_group_code": "pc_incentive_task_mvp_v3",
			"task_kind":           "daily_signin",
			"task_status":         1,
			"reward_mode":         2,
			"daily_checkin": map[string]any{
				"checked_in_today": false,
				"continuous_days":  3,
				"reward":           map[string]any{"reward_amount": 200, "reward_type": "quota_grant"},
			},
		},
	}
	out := parseCampaignTasks(items)
	if len(out) != 1 || out[0].DailyCheckin == nil {
		t.Fatalf("daily_checkin 未解析: %+v", out)
	}
	if out[0].DailyCheckin.ContinuousDays != 3 || out[0].DailyCheckin.RewardAmount != 200 {
		t.Errorf("daily_checkin 内容不对: %+v", out[0].DailyCheckin)
	}
	if !out[0].Claimable {
		t.Error("reward_mode=2 的任务应标记为可领取")
	}
}
