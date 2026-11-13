package doubao

import "testing"

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
