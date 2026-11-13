package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"doubao2api/internal/doubao"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("Open() 失败: %v", err)
	}
	return s
}

func testAccount(id, session string) *doubao.Account {
	return &doubao.Account{
		ID:      id,
		Name:    "acct-" + id,
		Enabled: true,
		Status:  "ok",
		Cookies: map[string]string{"sessionid": session},
	}
}

func TestOpenDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open() 失败: %v", err)
	}
	if s.Path() != path {
		t.Errorf("Path() = %q, want %q", s.Path(), path)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("Open() 不应立即创建状态文件")
	}

	st := s.Settings()
	if st.RequestTimeout != 300 || st.MaxRetries != 3 || st.LogKeep != 500 {
		t.Errorf("默认设置不对: timeout=%d retries=%d keep=%d",
			st.RequestTimeout, st.MaxRetries, st.LogKeep)
	}
	if st.ModelAliases == nil {
		t.Error("默认 ModelAliases 应初始化为空 map（而非 nil）")
	}
	if st.AllowAnonymous {
		t.Error("allow_anonymous 默认必须为 false（无密钥模式只放行本机）")
	}
	if s.Stats().StartedAt == 0 {
		t.Error("StartedAt 应被初始化为当前时间")
	}
	if s.HasKeys() {
		t.Error("新状态不应有任何密钥")
	}
	if got := s.NextAccount(); got != nil {
		t.Errorf("无账号时 NextAccount() = %v, want nil", got)
	}
	if got := s.Accounts(); len(got) != 0 {
		t.Errorf("Accounts() 长度 = %d, want 0", len(got))
	}
}

func TestOpenRejectsBrokenJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("损坏的状态文件应返回错误")
	} else if !strings.Contains(err.Error(), "解析失败") {
		t.Errorf("错误信息应说明解析失败，实际: %v", err)
	}
}

func TestSaveReloadRoundTrip(t *testing.T) {
	s := newStore(t)
	s.UpsertAccount(testAccount("a1", "sess-1"))
	k := s.AddKey("default")
	s.UpdateSettings(func(st *Settings) {
		st.AllowAnonymous = true
		st.LogKeep = 10
		st.ModelAliases = map[string]string{"gpt-4o": "doubao"}
	})
	s.Record(LogEntry{Status: 200, Model: "doubao"}, 42)

	if err := s.Save(); err != nil {
		t.Fatalf("Save() 失败: %v", err)
	}
	if _, err := os.Stat(s.Path() + ".tmp"); !os.IsNotExist(err) {
		t.Error("Save() 后不应残留 .tmp 文件")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(s.Path())
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("状态文件权限 = %o, want 600（含账号凭据）", perm)
		}
	}

	s2, err := Open(s.Path())
	if err != nil {
		t.Fatalf("重新 Open() 失败: %v", err)
	}
	if len(s2.Accounts()) != 1 || s2.Accounts()[0].ID != "a1" {
		t.Fatalf("账号未持久化: %+v", s2.Accounts())
	}
	if !s2.HasKeys() || !s2.ValidKey(k.Key) {
		t.Error("密钥未持久化")
	}
	st := s2.Settings()
	if !st.AllowAnonymous || st.LogKeep != 10 {
		t.Errorf("设置未持久化: %+v", st)
	}
	if st.ModelAliases["gpt-4o"] != "doubao" {
		t.Errorf("模型别名未持久化: %+v", st.ModelAliases)
	}
	if got := s2.Stats().TotalRequests; got != 1 {
		t.Errorf("TotalRequests = %d, want 1", got)
	}
	if got := s2.Stats().TotalCharsOut; got != 42 {
		t.Errorf("TotalCharsOut = %d, want 42", got)
	}
	if logs := s2.Logs(0); len(logs) != 1 || logs[0].Model != "doubao" {
		t.Errorf("日志未持久化: %+v", logs)
	}
}

var uuidV4Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestOpenMigratesLegacyState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := map[string]any{
		"accounts": []any{map[string]any{
			"id":          "a1",
			"name":        "legacy",
			"device_id":   "dev-1",
			"fp":          "deadbeefdeadbeef",
			"app_version": "2.36.15",
			"enabled":     true,
			"cookies":     map[string]string{"sessionid": "sid-1"},
		}},
		"settings": map[string]any{"cors_origin": "*"},
	}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open() 失败: %v", err)
	}
	a := s.Account("a1")
	if a == nil {
		t.Fatal("账号未载入")
	}
	if want := "verify_" + a.DeviceID; a.FP != want {
		t.Errorf("FP = %q, want %q（旧指纹应迁移为 verify_<device_id>）", a.FP, want)
	}
	if !uuidV4Re.MatchString(a.WebTabID) {
		t.Errorf("WebTabID = %q, 期望补齐为 UUID v4", a.WebTabID)
	}
	if a.PCVersion != "2.36.15" {
		t.Errorf("PCVersion = %q, want 回退到 AppVersion", a.PCVersion)
	}

	st := s.Settings()
	if st.CORSOrigin != "" {
		t.Errorf("历史通配符 CORSOrigin 应被收紧为空，实际 %q", st.CORSOrigin)
	}
	if st.RequestTimeout != 300 || st.MaxRetries != 3 || st.LogKeep != 500 {
		t.Error("缺失的设置字段应补齐默认值")
	}
	if s.Stats().StartedAt == 0 {
		t.Error("缺失的 StartedAt 应补齐")
	}

	// 二次打开不应改动已迁移的字段（幂等）。
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = s2
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s3, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if a3 := s3.Account("a1"); a3.WebTabID != a.WebTabID || a3.FP != a.FP {
		t.Error("迁移结果应持久化且保持稳定")
	}
}

func TestUpsertAccountDedup(t *testing.T) {
	s := newStore(t)

	if isNew := s.UpsertAccount(testAccount("a1", "same")); !isNew {
		t.Error("首次写入应返回 true")
	}
	second := testAccount("a2", "same")
	second.Name = "renamed"
	if isNew := s.UpsertAccount(second); isNew {
		t.Error("相同 sessionid 应视为更新，返回 false")
	}
	accts := s.Accounts()
	if len(accts) != 1 {
		t.Fatalf("去重后账号数 = %d, want 1", len(accts))
	}
	if accts[0].ID != "a1" {
		t.Errorf("更新时应保留原 ID，实际 %q", accts[0].ID)
	}
	if accts[0].Name != "renamed" {
		t.Errorf("更新时应替换账号内容，实际 %q", accts[0].Name)
	}

	// 空 sessionid 无法去重，按新增处理（保持宽松，避免误合并）。
	if isNew := s.UpsertAccount(testAccount("b1", "")); !isNew {
		t.Error("空 session 应作为新账号")
	}
	if isNew := s.UpsertAccount(testAccount("b2", "")); !isNew {
		t.Error("空 session 之间不应互相去重")
	}
	if got := len(s.Accounts()); got != 3 {
		t.Errorf("账号数 = %d, want 3", got)
	}
}

func TestRemoveAccount(t *testing.T) {
	s := newStore(t)
	s.UpsertAccount(testAccount("a1", "s1"))
	s.UpsertAccount(testAccount("a2", "s2"))

	if !s.RemoveAccount("a1") {
		t.Error("删除已存在的账号应返回 true")
	}
	if s.Account("a1") != nil {
		t.Error("删除后不应再查到账号")
	}
	if got := len(s.Accounts()); got != 1 {
		t.Errorf("删除后账号数 = %d, want 1", got)
	}
	if s.RemoveAccount("nope") {
		t.Error("删除不存在的账号应返回 false")
	}
}

func TestNextAccountRotation(t *testing.T) {
	s := newStore(t)
	a1 := testAccount("a1", "s1")
	a1.Failures = 2 // 有失败记录，优先级最低
	a2 := testAccount("a2", "s2")
	a2.Enabled = false // 停用，必须跳过
	a3 := testAccount("a3", "s3")
	for _, a := range []*doubao.Account{a1, a2, a3} {
		s.UpsertAccount(a)
	}

	for i := 0; i < 3; i++ {
		got := s.NextAccount()
		if got == nil {
			t.Fatal("NextAccount() = nil, want a3")
		}
		if got.ID != "a3" {
			t.Fatalf("第 %d 次轮询选中 %q, want a3（a2 停用、a1 有失败）", i, got.ID)
		}
	}

	// 全部有失败时回退到失败最少的账号，且不硬阻塞。
	s.MarkAccountResult("a3", false, "boom")
	got := s.NextAccount()
	if got == nil || got.ID != "a3" {
		t.Fatalf("应选择失败最少的 a3，实际 %+v", got)
	}

	// 唯一启用的账号也失败时仍应返回它，而不是 nil。
	s.MarkAccountResult("a3", false, "boom")
	if got := s.NextAccount(); got == nil || got.ID != "a3" {
		t.Fatalf("全部失败时仍应返回可用账号，实际 %+v", got)
	}

	// 全部停用时返回 nil。
	s.UpdateSettings(func(*Settings) {})
	for _, a := range s.Accounts() {
		a.Enabled = false
	}
	if got := s.NextAccount(); got != nil {
		t.Errorf("全部停用时 NextAccount() = %v, want nil", got)
	}
}

func TestMarkAccountResult(t *testing.T) {
	s := newStore(t)
	s.UpsertAccount(testAccount("a1", "s1"))

	s.MarkAccountResult("a1", false, "网关错误")
	a := s.Account("a1")
	if a.Failures != 1 || a.Status != "error" || a.LastErr != "网关错误" {
		t.Errorf("失败记录不对: %+v", a)
	}
	if a.LastUsed == 0 {
		t.Error("LastUsed 应被更新")
	}

	s.MarkAccountResult("a1", true, "探测通过")
	a = s.Account("a1")
	if a.Failures != 0 || a.Status != "ok" || a.LastErr != "" {
		t.Errorf("成功应重置失败状态: %+v", a)
	}
	if a.Note != "探测通过" {
		t.Errorf("Note = %q, want 探测通过", a.Note)
	}

	// 成功且无备注时保留旧备注。
	s.MarkAccountResult("a1", true, "")
	if a = s.Account("a1"); a.Note != "探测通过" {
		t.Errorf("空备注不应覆盖已有备注，实际 %q", a.Note)
	}

	// 未知账号不应 panic。
	s.MarkAccountResult("missing", false, "x")
}

func TestMarkCheckinAndTaskRun(t *testing.T) {
	s := newStore(t)
	s.UpsertAccount(testAccount("a1", "s1"))
	today := time.Now().Format("2006-01-02")

	s.MarkCheckin("a1", "", 120)
	a := s.Account("a1")
	if a.LastCheckinDate != today {
		t.Errorf("空日期应补为今天 %q，实际 %q", today, a.LastCheckinDate)
	}
	if a.LastCheckinReward != 120 || a.LastCheckinAt == 0 {
		t.Errorf("签到记录不对: %+v", a)
	}

	s.MarkCheckin("a1", "2026-01-01", 5)
	if a = s.Account("a1"); a.LastCheckinDate != "2026-01-01" || a.LastCheckinReward != 5 {
		t.Errorf("显式日期应覆盖: %+v", a)
	}

	s.MarkTaskRun("a1", "", 1)
	s.MarkTaskRun("a1", "", 2)
	a = s.Account("a1")
	if a.LastTaskDate != today || a.LastTaskCount != 3 {
		t.Errorf("同一天任务数应累加: date=%q count=%d", a.LastTaskDate, a.LastTaskCount)
	}

	s.MarkTaskRun("a1", "2026-01-02", 1)
	a = s.Account("a1")
	if a.LastTaskDate != "2026-01-02" || a.LastTaskCount != 1 {
		t.Errorf("跨天应重置计数: date=%q count=%d", a.LastTaskDate, a.LastTaskCount)
	}

	// 未知账号不应 panic。
	s.MarkCheckin("missing", "", 1)
	s.MarkTaskRun("missing", "", 1)
}

func TestAPIKeyLifecycle(t *testing.T) {
	s := newStore(t)
	if s.HasKeys() {
		t.Fatal("初始状态不应有密钥")
	}
	k := s.AddKey("ci")
	if !strings.HasPrefix(k.Key, "sk-") {
		t.Errorf("密钥应以 sk- 开头，实际 %q", k.Key)
	}
	if !k.Enabled || k.CreatedAt == 0 {
		t.Error("新密钥应默认启用并记录创建时间")
	}
	if !s.HasKeys() {
		t.Error("签发后 HasKeys() 应为 true")
	}

	if !s.ValidKey(k.Key) {
		t.Error("启用状态的密钥应校验通过")
	}
	if k.Requests != 1 || k.LastUsed == 0 {
		t.Errorf("校验应计数: requests=%d last_used=%d", k.Requests, k.LastUsed)
	}
	if s.ValidKey("sk-not-exist") {
		t.Error("未知密钥不应通过")
	}

	if !s.ToggleKey(k.Key) {
		t.Fatal("停用应返回 true")
	}
	if s.ValidKey(k.Key) {
		t.Error("停用后的密钥不应通过")
	}
	if !s.HasKeys() {
		t.Error("停用但未删除时 HasKeys() 仍应为 true")
	}
	if !s.ToggleKey(k.Key) || !s.ValidKey(k.Key) {
		t.Error("重新启用后应再次通过")
	}
	if s.ToggleKey("sk-not-exist") {
		t.Error("停用未知密钥应返回 false")
	}

	if !s.DeleteKey(k.Key) {
		t.Fatal("删除应返回 true")
	}
	if s.ValidKey(k.Key) || s.HasKeys() {
		t.Error("删除后不应再有效")
	}
	if s.DeleteKey(k.Key) {
		t.Error("重复删除应返回 false")
	}
}

func TestValidKeyConcurrent(t *testing.T) {
	s := newStore(t)
	k := s.AddKey("load")

	const n = 64
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			s.ValidKey(k.Key)
		}()
	}
	wg.Wait()

	if k.Requests != n {
		t.Errorf("并发校验后 Requests = %d, want %d", k.Requests, n)
	}
}

func TestSettingsSnapshotIsCopy(t *testing.T) {
	s := newStore(t)
	s.UpdateSettings(func(st *Settings) {
		st.ModelAliases = map[string]string{"a": "doubao"}
	})

	snap := s.Settings()
	snap.ModelAliases["a"] = "tampered"
	snap.ModelAliases["b"] = "tampered"

	st := s.Settings()
	if st.ModelAliases["a"] != "doubao" {
		t.Error("返回值修改不应影响状态（值类型设置）")
	}
	if _, ok := st.ModelAliases["b"]; ok {
		t.Error("返回值修改不应影响状态（map 必须深拷贝）")
	}
}

func TestLogsAndRecord(t *testing.T) {
	s := newStore(t)
	s.UpdateSettings(func(st *Settings) { st.LogKeep = 3 })

	s.Record(LogEntry{Status: 200, Model: "m1"}, 10)
	s.Record(LogEntry{Status: 500, Model: "m2"}, 0)            // 失败：状态码
	s.Record(LogEntry{Status: 200, Err: "断流", Model: "m3"}, 5) // 失败：有错误
	s.Record(LogEntry{Status: 200, Model: "m4"}, 7)            // 触发裁剪
	s.Record(LogEntry{Status: 200, Model: "m5"}, 8)

	logs := s.Logs(0)
	if len(logs) != 3 {
		t.Fatalf("日志应裁剪到 3 条，实际 %d", len(logs))
	}
	if logs[0].Model != "m3" || logs[2].Model != "m5" {
		t.Errorf("裁剪应保留最新的 3 条（按时间顺序），实际 %q..%q", logs[0].Model, logs[2].Model)
	}
	if got := s.Logs(1); len(got) != 1 || got[0].Model != "m5" {
		t.Errorf("Logs(1) 应返回最后一条，实际 %+v", got)
	}
	if got := s.Logs(-1); len(got) != 3 {
		t.Errorf("负数 limit 应返回全部，实际 %d", len(got))
	}

	stats := s.Stats()
	if stats.TotalRequests != 5 {
		t.Errorf("TotalRequests = %d, want 5", stats.TotalRequests)
	}
	if stats.FailedRequests != 2 {
		t.Errorf("FailedRequests = %d, want 2", stats.FailedRequests)
	}
	if stats.TotalCharsOut != 30 {
		t.Errorf("TotalCharsOut = %d, want 30", stats.TotalCharsOut)
	}

	// LogKeep <= 0 表示不裁剪。
	s.UpdateSettings(func(st *Settings) { st.LogKeep = 0 })
	for i := 0; i < 5; i++ {
		s.Record(LogEntry{Status: 200}, 0)
	}
	if got := len(s.Logs(0)); got != 8 {
		t.Errorf("LogKeep=0 时不应裁剪，实际 %d 条", got)
	}
}

func TestClearLogsAndStats(t *testing.T) {
	s := newStore(t)
	s.Record(LogEntry{Status: 500}, 1)
	s.ClearLogs()
	if got := s.Logs(0); len(got) != 0 {
		t.Errorf("ClearLogs 后仍有 %d 条日志", len(got))
	}
	s.ClearStats()
	st := s.Stats()
	if st.TotalRequests != 0 || st.FailedRequests != 0 || st.TotalCharsOut != 0 {
		t.Errorf("ClearStats 后统计应归零: %+v", st)
	}
	if st.StartedAt == 0 {
		t.Error("ClearStats 后应重置 StartedAt")
	}
}

func TestUpdateSettingsConcurrent(t *testing.T) {
	s := newStore(t)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			s.UpdateSettings(func(st *Settings) { st.MaxRetries = i%5 + 1 })
		}(i)
		go func() {
			defer wg.Done()
			_ = s.Settings()
			s.Record(LogEntry{Status: 200}, 1)
			_ = s.Stats()
			_ = s.Logs(10)
		}()
	}
	wg.Wait()
	if got := s.Settings().MaxRetries; got < 1 || got > 5 {
		t.Errorf("并发更新后 MaxRetries = %d, want 1..5", got)
	}
}
