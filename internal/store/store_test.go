package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"doubao2api/internal/account"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("Open() 失败: %v", err)
	}
	return s
}

func testAccount(id, session string) *account.Account {
	return &account.Account{
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
	_, plain := s.AddKey("default")
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
	if !s2.HasKeys() || !s2.ValidKey(plain) {
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
	a1.Failures = 2 // 历史失败但已过冷却：仍可被选中
	a2 := testAccount("a2", "s2")
	a2.Enabled = false // 停用，必须跳过
	a3 := testAccount("a3", "s3")
	for _, a := range []*account.Account{a1, a2, a3} {
		s.UpsertAccount(a)
	}

	for i := 0; i < 3; i++ {
		got := s.NextAccount()
		if got == nil {
			t.Fatal("NextAccount() = nil, want a3")
		}
		if got.ID != "a3" {
			t.Fatalf("第 %d 次轮询选中 %q, want a3（a2 停用、a1 失败次数更多）", i, got.ID)
		}
	}

	// 账号失败后进入冷却：本次请求内不再选它，改选其它可用账号。
	s.MarkAccountResult("a3", false, "boom")
	got := s.NextAccount()
	if got == nil || got.ID != "a1" {
		t.Fatalf("a3 失败冷却后应改选 a1，实际 %+v", got)
	}

	// 剔除已试过的账号后应返回 nil（一次请求内不重复选同一个）。
	if got := s.NextAccountExcluding(map[string]bool{"a1": true, "a3": true}); got != nil {
		t.Fatalf("全部账号都被剔除时应返回 nil，实际 %+v", got)
	}

	// 清除错误状态后 a3 立即恢复，不需要等待冷却。
	s.ClearAccountErrors("a3")
	if got := s.NextAccount(); got == nil || got.ID != "a3" {
		t.Fatalf("清除错误后应恢复 a3，实际 %+v", got)
	}

	// 全部停用时返回 nil。
	// 注意：Accounts() 返回的是深拷贝，必须经 store 方法修改（这本身也是一条回归断言）。
	for _, a := range s.Accounts() {
		s.SetAccountEnabled(a.ID, false)
	}
	if got := s.NextAccount(); got != nil {
		t.Fatalf("全部停用时应返回 nil，实际 %+v", got)
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
	k, plain := s.AddKey("ci")
	if !strings.HasPrefix(plain, "sk-") {
		t.Errorf("密钥应以 sk- 开头，实际 %q", plain)
	}
	if k.ID == "" || k.KeyHash == "" || k.Display == "" {
		t.Error("新密钥应带 ID / 指纹 / 展示片段")
	}
	if strings.Contains(k.KeyHash, plain) || k.Display == plain {
		t.Error("持久化字段不得包含明文密钥")
	}
	if !k.Enabled || k.CreatedAt == 0 {
		t.Error("新密钥应默认启用并记录创建时间")
	}
	if !s.HasKeys() {
		t.Error("签发后 HasKeys() 应为 true")
	}

	if !s.ValidKey(plain) {
		t.Error("启用状态的密钥应校验通过")
	}
	if got := s.Keys()[0]; got.Requests != 1 || got.LastUsed == 0 {
		t.Errorf("校验应计数: requests=%d last_used=%d", got.Requests, got.LastUsed)
	}
	if s.ValidKey("sk-not-exist") {
		t.Error("未知密钥不应通过")
	}

	if !s.ToggleKey(k.ID) {
		t.Fatal("停用应返回 true")
	}
	if s.ValidKey(plain) {
		t.Error("停用后的密钥不应通过")
	}
	if !s.HasKeys() {
		t.Error("停用但未删除时 HasKeys() 仍应为 true")
	}
	if !s.ToggleKey(k.ID) || !s.ValidKey(plain) {
		t.Error("重新启用后应再次通过")
	}
	if s.ToggleKey("sk-not-exist") {
		t.Error("停用未知密钥应返回 false")
	}

	if k2 := s.KeyByRef(k.ID); k2 == nil || k2.ID != k.ID {
		t.Error("KeyByRef(ID) 应能解析")
	}
	if k2 := s.KeyByRef(k.Display); k2 == nil || k2.ID != k.ID {
		t.Error("KeyByRef(展示片段) 应能解析")
	}
	if k2 := s.KeyByRef(plain); k2 == nil || k2.ID != k.ID {
		t.Error("KeyByRef(明文) 应能解析")
	}
	if s.KeyByRef("sk-nope") != nil {
		t.Error("未知引用应返回 nil")
	}

	if !s.DeleteKey(k.ID) {
		t.Fatal("删除应返回 true")
	}
	if s.ValidKey(plain) || s.HasKeys() {
		t.Error("删除后不应再有效")
	}
	if s.DeleteKey(k.ID) {
		t.Error("重复删除应返回 false")
	}
}

func TestValidKeyConcurrent(t *testing.T) {
	s := newStore(t)
	_, plain := s.AddKey("load")

	const n = 64
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			s.ValidKey(plain)
		}()
	}
	wg.Wait()

	if got := s.Keys()[0].Requests; got != n {
		t.Errorf("并发校验后 Requests = %d, want %d", got, n)
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

// --- 并发约定回归（配合 go test -race） ---

// TestAccountSnapshotsAreIsolated 锁死「读取接口返回深拷贝」这一约定。
//
// 历史问题：Accounts/Account/NextAccount 曾直接返回内部指针，
// handler 在锁外改 a.Enabled 会与轮询、落盘并发读写同一块内存。
func TestAccountSnapshotsAreIsolated(t *testing.T) {
	s := newStore(t)
	s.UpsertAccount(testAccount("a1", "s1"))

	snap := s.Account("a1")
	snap.Enabled = false
	snap.Cookies["sessionid"] = "tampered"
	snap.Failures = 99

	got := s.Account("a1")
	if !got.Enabled || got.Failures != 0 || got.Cookies["sessionid"] != "s1" {
		t.Fatalf("修改快照污染了内部状态: %+v", got)
	}
	if got := s.NextAccount(); got == nil || !got.Enabled {
		t.Fatal("NextAccount 也应返回可安全修改的副本")
	}

	// 入参同样被拷贝：调用方持有原对象不能影响状态。
	in := testAccount("a2", "s2")
	s.UpsertAccount(in)
	in.Enabled = false
	if a := s.Account("a2"); !a.Enabled {
		t.Error("UpsertAccount 应保存入参的副本")
	}
}

// TestAccountMutationsPersist 验证写操作经过 store 方法后确实落到状态里。
func TestAccountMutationsPersist(t *testing.T) {
	s := newStore(t)
	s.UpsertAccount(testAccount("a1", "s1"))

	if enabled, ok := s.ToggleAccount("a1"); !ok || enabled {
		t.Fatalf("ToggleAccount() = (%v, %v), want (false, true)", enabled, ok)
	}
	if a := s.Account("a1"); a.Enabled {
		t.Error("停用应已生效")
	}
	s.MarkAccountResult("a1", false, "boom")
	if !s.ClearAccountErrors("a1") {
		t.Fatal("ClearAccountErrors() = false")
	}
	a := s.Account("a1")
	if a.Failures != 0 || a.Status != "ok" || a.LastErr != "" {
		t.Errorf("清理后仍有错误状态: %+v", a)
	}

	// 不存在的账号不应 panic，返回 false。
	if _, ok := s.ToggleAccount("nope"); ok {
		t.Error("未知账号 ToggleAccount 应返回 false")
	}
	if s.ClearAccountErrors("nope") || s.SetAccountEnabled("nope", true) {
		t.Error("未知账号的写操作应返回 false")
	}
}

// TestCSRFNotPersisted 断言运行时状态（CSRF token）不落盘。
//
// 历史问题：Account 里带 csrf_token 字段，既延长了敏感值在磁盘上的生命周期，
// 又让 json.Marshal 与 setCSRF 并发读写同一字段。
func TestCSRFNotPersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	a := testAccount("a1", "s1")
	s.UpsertAccount(a)
	a.SetCSRF("csrf-secret-value")
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "csrf-secret-value") || strings.Contains(string(raw), "csrf_token") {
		t.Fatalf("CSRF token 不应写入状态文件:\n%s", raw)
	}

	// 落盘后重新加载，账号仍可正常使用（token 会按需重新刷新）。
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Account("a1"); got == nil || got.SessionID() != "s1" {
		t.Fatalf("重新加载后账号丢失: %+v", got)
	}
}

// TestConcurrentReadWriteSave 并发跑「读快照 / 写状态 / 落盘 / 记日志」四条路径。
//
// 这是历史数据竞争的最小复现：不加 -race 也能跑通，加 -race 才暴露问题。
func TestConcurrentReadWriteSave(t *testing.T) {
	s := newStore(t)
	for i := 0; i < 4; i++ {
		s.UpsertAccount(testAccount("a"+string(rune('1'+i)), "s"+string(rune('1'+i))))
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// 1) 落盘（模拟定时 / 每次修改后的 Save）
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = s.Save()
			}
		}
	}()

	// 2) 账号池轮询 + 标记结果
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				if a := s.NextAccount(); a != nil {
					s.MarkAccountResult(a.ID, true, "")
				}
			}
		}
	}()

	// 3) 控制台式读改（Toggle / Clear）
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				if _, ok := s.ToggleAccount("a1"); ok {
					s.ClearAccountErrors("a1")
				}
				for _, a := range s.Accounts() {
					a.Enabled = !a.Enabled // 改副本必须无副作用
				}
			}
		}
	}()

	// 4) 设置 / 日志 / 统计
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				s.UpdateSettings(func(st *Settings) { st.MaxRetries = 1 + i%3 })
				s.Record(LogEntry{Model: "doubao", Status: 200}, 1)
				_ = s.Settings()
				_ = s.Stats()
				_ = s.Logs(0)
			}
		}
	}()

	time.Sleep(60 * time.Millisecond)
	close(stop)
	wg.Wait()

	if err := s.Save(); err != nil {
		t.Fatalf("最终落盘失败: %v", err)
	}
}

// TestCheckinPendingLifecycle 覆盖待确认签到的登记 / 重试计数 / 清除。
//
// 语义：登记(MarkCheckinPending)不计数，重试(RecordCheckinRetry)才 +1；
// 跨天登记会重置次数（签到按天幂等）。
func TestCheckinPendingLifecycle(t *testing.T) {
	s := newStore(t)
	s.UpsertAccount(testAccount("a1", "s1"))

	if !s.MarkCheckinPending("a1", "2026-11-14") {
		t.Fatal("登记失败")
	}
	a := s.Account("a1")
	if a.PendingCheckinDate != "2026-11-14" || a.PendingCheckinTries != 0 || a.PendingCheckinAt == 0 {
		t.Fatalf("登记后状态不对（应 tries=0）: %+v", a)
	}

	if !s.RecordCheckinRetry("a1", "2026-11-14") || !s.RecordCheckinRetry("a1", "2026-11-14") {
		t.Fatal("记录重试失败")
	}
	if a = s.Account("a1"); a.PendingCheckinTries != 2 {
		t.Errorf("重试次数 = %d, want 2", a.PendingCheckinTries)
	}

	// 队列快照只包含有待确认项的账号，且是副本。
	pending := s.PendingCheckins()
	if len(pending) != 1 || pending[0].ID != "a1" {
		t.Fatalf("PendingCheckins() = %+v", pending)
	}
	pending[0].PendingCheckinTries = 99
	if s.Account("a1").PendingCheckinTries != 2 {
		t.Error("PendingCheckins 应返回副本")
	}

	// 跨天登记重置计数。
	s.MarkCheckinPending("a1", "2026-11-15")
	if a = s.Account("a1"); a.PendingCheckinDate != "2026-11-15" || a.PendingCheckinTries != 0 {
		t.Errorf("跨天应重置次数: %+v", a)
	}

	if !s.ClearCheckinPending("a1") {
		t.Fatal("清除失败")
	}
	if a = s.Account("a1"); a.PendingCheckinDate != "" || a.PendingCheckinTries != 0 || a.PendingCheckinAt != 0 {
		t.Errorf("清除后仍残留: %+v", a)
	}
	if len(s.PendingCheckins()) != 0 {
		t.Error("清除后队列应为空")
	}
	if s.ClearCheckinPending("a1") {
		t.Error("重复清除应返回 false")
	}

	// 未知账号不应 panic。
	if s.MarkCheckinPending("nope", "") || s.RecordCheckinRetry("nope", "") || s.ClearCheckinPending("nope") {
		t.Error("未知账号的写操作应返回 false")
	}
}

// --- 凭据哈希 / 备份 / 导出导入 ---

func TestAdminPasswordIsHashed(t *testing.T) {
	s := newStore(t)
	if s.HasAdminPassword() {
		t.Fatal("初始状态不应有管理密码")
	}
	if err := s.SetAdminPassword("Plain-Text-PW"); err != nil {
		t.Fatalf("SetAdminPassword: %v", err)
	}
	if !s.VerifyAdminPassword("Plain-Text-PW") {
		t.Error("正确密码应校验通过")
	}
	if s.VerifyAdminPassword("plain-text-pw") {
		t.Error("密码必须区分大小写")
	}
	if got := s.Settings().AdminPasswordHash; got == "" || got == "Plain-Text-PW" {
		t.Errorf("设置里只能有哈希，实际 %q", got)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "Plain-Text-PW") {
		t.Error("状态文件不得包含明文管理密码")
	}

	// 清除密码。
	if err := s.SetAdminPassword(""); err != nil {
		t.Fatal(err)
	}
	if s.HasAdminPassword() {
		t.Error("空密码应表示清除")
	}
}

func TestLegacyPlaintextPasswordMigrated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	legacy := `{"settings":{"admin_password":"legacy-pw","request_timeout_sec":300}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !s.VerifyAdminPassword("legacy-pw") {
		t.Error("旧版明文密码应迁移为可校验的哈希")
	}
	if got := s.Settings().AdminPasswordLegacy; got != "" {
		t.Errorf("迁移后不应保留明文字段，实际 %q", got)
	}
}

func TestSaveCreatesBackupAndSnapshot(t *testing.T) {
	s := newStore(t)
	s.UpsertAccount(testAccount("a1", "s1"))
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	// 第二次保存会先把旧文件留成 .bak，并生成当日快照。
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Path() + ".bak"); err != nil {
		t.Errorf("应生成 .bak 备份: %v", err)
	}
	backups := filepath.Join(filepath.Dir(s.Path()), "backups")
	entries, err := os.ReadDir(backups)
	if err != nil || len(entries) == 0 {
		t.Fatalf("应生成 backups/ 快照: %v", err)
	}
}

func TestSnapshotAndExportImport(t *testing.T) {
	s := newStore(t)
	s.UpsertAccount(testAccount("a1", "s1"))
	k, plain := s.AddKey("exported")
	_ = k
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Snapshot(); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	raw, err := s.Export()
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if strings.Contains(string(raw), `"logs"`) {
		t.Error("导出不应包含请求日志")
	}
	if strings.Contains(string(raw), plain) {
		t.Error("导出不得包含明文密钥")
	}

	// merge=true：只合并账号，不动设置与密钥。
	other := newStore(t)
	other.UpsertAccount(testAccount("a2", "s2"))
	added, updated, err := other.Import(raw, true)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if added != 1 || updated != 0 {
		t.Errorf("merge 导入结果 = added:%d updated:%d, want 1/0", added, updated)
	}
	if len(other.Accounts()) != 2 {
		t.Errorf("merge 后账号数 = %d, want 2", len(other.Accounts()))
	}
	if !other.HasKeys() {
		t.Error("merge 不应导入密钥")
	}

	// 覆盖导入前设置的密码，覆盖后必须保留（防止被旧导出锁死）。
	if err := other.SetAdminPassword("keep-me"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := other.Import(raw, false); err != nil {
		t.Fatalf("覆盖导入: %v", err)
	}
	if !other.VerifyAdminPassword("keep-me") {
		t.Error("导入不得覆盖当前管理密码")
	}
	if !other.HasKeys() {
		t.Error("覆盖导入应带上源里的密钥")
	}
	if !other.ValidKey(plain) {
		t.Error("导入的密钥指纹应能校验原始明文")
	}

	if _, _, err := other.Import([]byte(`{}`), false); err == nil {
		t.Error("空导入应报错")
	}
}

func TestMaskKey(t *testing.T) {
	cases := map[string]string{
		"":              "",
		"a":             "a***",
		"sk-1234567890": "sk-123...7890",
		"sk-12345678":   "sk-123...5678",
		"short":         "s***",
	}
	for in, want := range cases {
		if got := MaskKey(in); got != want {
			t.Errorf("MaskKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCooldownLadder(t *testing.T) {
	want := map[int]time.Duration{
		1: 30 * time.Second, 2: 2 * time.Minute, 3: 10 * time.Minute, 4: 30 * time.Minute, 9: 30 * time.Minute,
	}
	for failures, d := range want {
		if got := cooldownFor(failures); got != d {
			t.Errorf("cooldownFor(%d) = %v, want %v", failures, got, d)
		}
	}
	// 失败后写入冷却，成功即清除。
	s := newStore(t)
	s.UpsertAccount(testAccount("a1", "s1"))
	s.MarkAccountResult("a1", false, "boom")
	if a := s.Account("a1"); a.CooldownUntil == 0 {
		t.Error("失败应写入冷却截止时间")
	}
	s.MarkAccountResult("a1", true, "")
	if a := s.Account("a1"); a.CooldownUntil != 0 || a.LastSuccess == 0 {
		t.Errorf("成功后应清除冷却并记录成功时间: %+v", a)
	}
}

func TestRecordClassifiesFailures(t *testing.T) {
	s := newStore(t)
	s.Record(LogEntry{Status: 200, Kind: "ok"}, 10)
	s.Record(LogEntry{Status: 499, Kind: "canceled"}, 10)
	s.Record(LogEntry{Status: 422, Kind: "business", Err: "campaign not found"}, 0)
	s.Record(LogEntry{Status: 502, Kind: "transport", Err: "network"}, 0)
	st := s.Stats()
	if st.TotalRequests != 4 {
		t.Errorf("TotalRequests = %d, want 4", st.TotalRequests)
	}
	if st.FailedRequests != 1 {
		t.Errorf("只有 transport 计入失败，实际 %d", st.FailedRequests)
	}
	if st.CanceledRequests != 1 || st.BusinessErrors != 1 {
		t.Errorf("分类计数不符: %+v", st)
	}
	if st.TotalCharsOut != 20 {
		t.Errorf("TotalCharsOut = %d, want 20", st.TotalCharsOut)
	}
}

func TestMetaAndAutosave(t *testing.T) {
	s := newStore(t)
	meta := s.Meta()
	if meta.SchemaVersion != schemaVersion || meta.CreatedAt == 0 || meta.BootedAt == 0 {
		t.Fatalf("Meta 不完整: %+v", meta)
	}

	ctx, cancel := context.WithCancel(context.Background())
	stop := s.StartAutosave(ctx, 10*time.Millisecond)
	defer stop()
	s.UpsertAccount(testAccount("a1", "s1")) // 标记 dirty
	// 等到自动保存把文件写出来。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(s.Path()); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if _, err := os.Stat(s.Path()); err != nil {
		t.Fatalf("自动保存应写出状态文件: %v", err)
	}
	// 关闭后再次 MarkDirty 不应该 panic（goroutine 已退出）。
	s.MarkDirty()
}

func TestImportRejectsGarbage(t *testing.T) {
	s := newStore(t)
	if _, _, err := s.Import([]byte("not json"), true); err == nil {
		t.Error("非法 JSON 应报错")
	}
	// 只有设置、没有账号或密钥：应拒绝（避免「导入成功但什么都没变」）。
	if _, _, err := s.Import([]byte(`{"settings":{}}`), true); err == nil {
		t.Error("空导入应报错")
	}
}

func TestLegacyPlaintextKeyMigrated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	legacy := `{"keys":[{"key":"sk-legacy-plain-key-0123456789","name":"old","enabled":true}]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !s.ValidKey("sk-legacy-plain-key-0123456789") {
		t.Error("旧版明文密钥应迁移为指纹并可继续使用")
	}
	if !s.HasKeys() {
		t.Error("迁移后仍应存在密钥")
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "sk-legacy-plain-key-0123456789") {
		t.Error("保存后状态文件不得再含明文密钥")
	}
}

// TestNextAccountSpreadsConcurrentPicks 保证并发选号会分散到不同账号。
//
// 回归背景：仅在请求完成后更新 LastUsed 时，同一时刻的多个请求会看到相同的
// 排序键，全部选中同一个账号（模型指纹测试里 3 路并发全打到一个号）。
func TestNextAccountSpreadsConcurrentPicks(t *testing.T) {
	s := newStore(t)
	for _, id := range []string{"a1", "a2", "a3"} {
		s.UpsertAccount(testAccount(id, "sess-"+id))
	}

	const n = 30
	var mu sync.Mutex
	counts := map[string]int{}
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			a := s.NextAccount()
			if a == nil {
				return
			}
			mu.Lock()
			counts[a.ID]++
			mu.Unlock()
		}()
	}
	wg.Wait()

	if len(counts) != 3 {
		t.Fatalf("并发选号应覆盖 3 个账号，实际 %v", counts)
	}
	for id, c := range counts {
		if c < n/6 {
			t.Errorf("账号 %s 只被选中 %d 次，分布过于集中: %v", id, c, counts)
		}
	}
}
