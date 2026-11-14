package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 沙箱的安全边界比功能更重要：模型输出会驱动这些操作，一旦越界就是本机文件系统暴露。
// 因此这里重点验证「路径不逃逸、命令默认禁用、白名单生效、输出有上限」。

func newSandbox(t *testing.T) *Sandbox {
	t.Helper()
	s, err := New(t.TempDir(), "sandbox-test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestWriteReadListRoundTrip(t *testing.T) {
	s := newSandbox(t)
	ctx := context.Background()

	res, err := s.Exec(ctx, "write_file", map[string]any{"path": "notes/hello.txt", "content": "OK"})
	if err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if res["ok"] != true || res["path"] != "notes/hello.txt" {
		t.Fatalf("write_file 结果异常: %+v", res)
	}

	got, err := s.Exec(ctx, "read_file", map[string]any{"path": "notes/hello.txt"})
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if got["content"] != "OK" {
		t.Errorf("content = %v, want OK", got["content"])
	}

	list, err := s.Exec(ctx, "list_dir", map[string]any{"path": "notes"})
	if err != nil {
		t.Fatalf("list_dir: %v", err)
	}
	if list["count"] != 1 {
		t.Errorf("count = %v, want 1", list["count"])
	}
}

// TestResolveRejectsEscape 覆盖各种逃逸写法（含 Windows 盘符与混合分隔符）。
func TestResolveRejectsEscape(t *testing.T) {
	s := newSandbox(t)
	bad := []string{
		"../outside.txt",
		"..\\outside.txt",
		"a/../../outside.txt",
		"a\\..\\..\\outside.txt",
		"/etc/passwd",
		`C:\Windows\win.ini`,
		"C:relative.txt",
	}
	for _, p := range bad {
		if _, err := s.Resolve(p); !errors.Is(err, ErrPathEscape) {
			t.Errorf("Resolve(%q) 应被拒绝，实际 err=%v", p, err)
		}
	}
	// 工作区内的正常路径必须放行。
	for _, p := range []string{"a.txt", "dir/a.txt", "dir\\a.txt", "", "."} {
		if _, err := s.Resolve(p); err != nil {
			t.Errorf("Resolve(%q) 不应被拒绝: %v", p, err)
		}
	}
}

func TestWriteRejectsEscapeAndOversize(t *testing.T) {
	s := newSandbox(t)
	s.MaxBytes = 16
	ctx := context.Background()

	if _, err := s.Exec(ctx, "write_file", map[string]any{"path": "../x.txt", "content": "x"}); !errors.Is(err, ErrPathEscape) {
		t.Errorf("越界写应被拒绝，实际 %v", err)
	}
	if _, err := s.Exec(ctx, "write_file", map[string]any{"path": "big.txt", "content": strings.Repeat("x", 32)}); err == nil {
		t.Error("超限写应报错")
	}
}

// TestRunCommandDisabledByDefault 命令执行必须显式开启。
func TestRunCommandDisabledByDefault(t *testing.T) {
	s := newSandbox(t)
	if _, err := s.Exec(context.Background(), "run_command", map[string]any{"command": "echo hi"}); err == nil {
		t.Fatal("默认应禁止执行命令")
	}
}

func TestRunCommandAllowlist(t *testing.T) {
	s := newSandbox(t)
	s.AllowShell = true
	s.ShellAllow = []string{"go"}
	ctx := context.Background()

	// 白名单内：真实执行（用 go 自身，避免依赖 shell 内建命令）。
	res, err := s.Exec(ctx, "run_command", map[string]any{"command": "go version"})
	if err != nil {
		t.Fatalf("run_command: %v", err)
	}
	if res["ok"] != true {
		t.Errorf("go version 应成功: %+v", res)
	}
	if !strings.Contains(strAny(res["output"]), "go version") {
		t.Errorf("输出异常: %v", res["output"])
	}

	// 白名单外：一律拒绝。
	for _, c := range []string{"rm -rf /", "cmd /c dir", "whoami"} {
		if _, err := s.Exec(ctx, "run_command", map[string]any{"command": c}); err == nil {
			t.Errorf("%q 不在白名单内，应被拒绝", c)
		}
	}

	// 不经过 shell：`&&` 之类只是传给 go 的普通参数，不会被解释成第二条命令。
	res2, err := s.Exec(ctx, "run_command", map[string]any{"command": "go version && whoami"})
	if err != nil {
		t.Fatalf("白名单内的可执行文件应放行（由 go 自己报参数错误）: %v", err)
	}
	if res2["ok"] == true {
		t.Errorf("go 收到非法参数应失败: %+v", res2)
	}
	if strings.Contains(strings.ToLower(strAny(res2["output"])), "\\") {
		t.Errorf("不应产生第二条命令的输出: %v", res2["output"])
	}
}

func TestRunCommandTimeoutAndOutputCap(t *testing.T) {
	s := newSandbox(t)
	s.AllowShell = true
	s.ShellAllow = []string{"go"}
	s.Timeout = 200 * time.Millisecond
	s.MaxBytes = 8

	res, err := s.Exec(context.Background(), "run_command", map[string]any{"command": "go version"})
	if err != nil {
		t.Fatalf("run_command: %v", err)
	}
	if out := strAny(res["output"]); len(out) > 8 && res["truncated"] != true {
		t.Errorf("输出超过上限却未标记 truncated: %q", out)
	}
}

func TestUnknownTool(t *testing.T) {
	s := newSandbox(t)
	if _, err := s.Exec(context.Background(), "rm_rf", nil); err == nil {
		t.Error("未知工具应报错")
	}
}

// TestSandboxIDGenerated 未显式传 id 时应生成 sandbox- 前缀的标识。
func TestSandboxIDGenerated(t *testing.T) {
	s, err := New(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(s.SandboxID, "sandbox-") {
		t.Errorf("SandboxID = %q, want sandbox- 前缀", s.SandboxID)
	}
	if abs, err := filepath.Abs(s.Root); err != nil || abs != s.Root {
		t.Errorf("Root 应为绝对路径: %q", s.Root)
	}
}

func TestSplitArgs(t *testing.T) {
	cases := map[string][]string{
		`go version`:         {"go", "version"},
		`echo "hello world"`: {"echo", "hello world"},
		`a  b`:               {"a", "b"},
		`x\ y z`:             {"x y", "z"},
		`""`:                 {""},
	}
	for in, want := range cases {
		got := splitArgs(in)
		if len(got) != len(want) {
			t.Errorf("splitArgs(%q) = %q, want %q", in, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("splitArgs(%q)[%d] = %q, want %q", in, i, got[i], want[i])
			}
		}
	}
}

// 确保测试本身不会留下越界文件。
func TestNoEscapeArtifacts(t *testing.T) {
	root := t.TempDir()
	s, err := New(root, "")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.Exec(context.Background(), "write_file", map[string]any{"path": "../escape.txt", "content": "x"})
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape.txt")); err == nil {
		t.Fatal("越界写入竟然落盘了")
	}
}

// 保证 exec 包被使用（go vet 对未使用导入更严格时也安全）。
var _ = exec.CommandContext

// TestResolveRejectsSymlinkEscape 工作区内的软链接指向外部时必须拒绝。
//
// 这是路径校验最容易漏的一条：字面路径在 Root 之内，但 os.ReadFile 会跟随软链。
func TestResolveRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("当前平台不支持创建软链接: %v", err)
	}

	s, err := New(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve("link/secret.txt"); !errors.Is(err, ErrPathEscape) {
		t.Errorf("经软链接指向工作区外应被拒绝，实际 err=%v", err)
	}
	// 工作区内的普通文件仍然可用。
	if _, err := s.Exec(context.Background(), "write_file", map[string]any{"path": "ok.txt", "content": "x"}); err != nil {
		t.Errorf("正常写入不应受影响: %v", err)
	}
}
