// Package agent 提供工作区受限的本地工具运行时（沙箱执行端）。
//
// 安全前提：这里的每一次文件操作与命令执行都由**模型生成的内容**驱动，等价于
// 把不可信输入接到本机文件系统与进程上。因此运行时强制三条边界：
//
//  1. 路径必须落在 Root 之内（拒绝绝对路径、盘符以及任何 .. 逃逸）；
//  2. 命令执行默认关闭；开启后只允许白名单可执行文件、在 Root 下运行、
//     带超时与输出上限，且不经过 shell（避免管道/重定向注入）；
//  3. 读写与输出都有字节上限，避免一次性吃掉内存或磁盘。
package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// 默认限额。
const (
	DefaultMaxBytes = 1 << 20 // 单次读/写 1 MiB
	DefaultTimeout  = 30 * time.Second
)

// ErrPathEscape 表示路径试图逃出工作区。
var ErrPathEscape = errors.New("路径超出工作区范围")

// Sandbox 是工作区受限的工具运行时。
type Sandbox struct {
	// Root 是工作区根目录；所有相对路径都相对它解析。
	Root string
	// MaxBytes 限制单次读取 / 写入 / 命令输出的字节数。
	MaxBytes int64
	// Timeout 是单条命令的超时。
	Timeout time.Duration
	// AllowShell 为 true 时才允许执行命令。
	AllowShell bool
	// ShellAllow 是可执行文件白名单（按二进制的 basename 匹配，不含路径）。
	ShellAllow []string
	// SandboxID 是上报给上游的沙箱实例 id（本地生成，形如 sandbox-<uuid>）。
	SandboxID string
}

// New 构造一个沙箱；root 不存在时会被创建。
func New(root string, sandboxID string) (*Sandbox, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("工作区路径无效: %w", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("创建工作区失败: %w", err)
	}
	if sandboxID == "" {
		sandboxID = "sandbox-" + strings.ReplaceAll(fmt.Sprintf("%d", time.Now().UnixNano()), "-", "")
	}
	return &Sandbox{
		Root:      abs,
		MaxBytes:  DefaultMaxBytes,
		Timeout:   DefaultTimeout,
		SandboxID: sandboxID,
	}, nil
}

// Resolve 把模型给的相对路径解析成工作区内的绝对路径。
//
// 拒绝绝对路径、盘符与 .. 逃逸；解析结果必须仍在 Root 之下。
func (s *Sandbox) Resolve(rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return s.Root, nil
	}
	// 统一分隔符后再判定，避免 Windows 下的混合写法绕过。
	norm := filepath.FromSlash(strings.ReplaceAll(rel, "\\", "/"))
	// Windows 上 "/etc/passwd" 既不是 IsAbs、VolumeName 也为空，
	// 但它以分隔符开头（根路径），必须显式拒绝。
	if filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" ||
		strings.HasPrefix(norm, string(filepath.Separator)) || strings.HasPrefix(norm, "/") {
		return "", fmt.Errorf("%w: 不接受绝对路径 %q", ErrPathEscape, rel)
	}
	full := filepath.Join(s.Root, norm)
	relToRoot, err := filepath.Rel(s.Root, full)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrPathEscape, err)
	}
	if relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q", ErrPathEscape, rel)
	}
	// 软链接复查：工作区内可能有指向外部的 symlink（os.ReadFile 会跟随），
	// 因此把最近一个已存在的祖先解析成真实路径后再判一次包含关系。
	if resolved, err := s.resolveExisting(full); err != nil {
		return "", err
	} else if rel, err := filepath.Rel(s.realRoot(), resolved); err != nil ||
		rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q 经软链接指向工作区外", ErrPathEscape, rel)
	}
	return full, nil
}

// realRoot 返回工作区的真实路径（对 Root 本身做一次软链解析）。
func (s *Sandbox) realRoot() string {
	if r, err := filepath.EvalSymlinks(s.Root); err == nil {
		return r
	}
	return s.Root
}

// resolveExisting 找到 target 最近的存在祖先并解析软链，再拼回未存在的部分。
func (s *Sandbox) resolveExisting(target string) (string, error) {
	cur := target
	var tail []string
	for {
		if _, err := os.Lstat(cur); err == nil {
			real, err := filepath.EvalSymlinks(cur)
			if err != nil {
				return "", fmt.Errorf("%w: 软链接解析失败: %v", ErrPathEscape, err)
			}
			for i := len(tail) - 1; i >= 0; i-- {
				real = filepath.Join(real, tail[i])
			}
			return real, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return target, nil // 连根都不存在，交给调用方报错
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}

// Exec 按工具名执行并返回可回传上游的结果。
//
// 支持的工具：write_file / read_file / list_dir / run_command。
func (s *Sandbox) Exec(ctx context.Context, tool string, input map[string]any) (map[string]any, error) {
	tool = strings.TrimSpace(strings.ToLower(tool))
	switch tool {
	case "write_file", "create_file":
		return s.writeFile(input)
	case "read_file":
		return s.readFile(input)
	case "list_dir", "list_files":
		return s.listDir(input)
	case "run_command", "execute_command":
		return s.runCommand(ctx, input)
	}
	return nil, fmt.Errorf("不支持的工具: %q", tool)
}

func (s *Sandbox) writeFile(input map[string]any) (map[string]any, error) {
	path, err := s.Resolve(strAny(input["path"]))
	if err != nil {
		return nil, err
	}
	content := strAny(input["content"])
	if int64(len(content)) > s.MaxBytes {
		return nil, fmt.Errorf("内容超过上限 %d 字节", s.MaxBytes)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return nil, err
	}
	rel, _ := filepath.Rel(s.Root, path)
	return map[string]any{
		"ok":      true,
		"path":    filepath.ToSlash(rel),
		"bytes":   len(content),
		"message": "文件已写入",
	}, nil
}

func (s *Sandbox) readFile(input map[string]any) (map[string]any, error) {
	path, err := s.Resolve(strAny(input["path"]))
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > s.MaxBytes {
		return nil, fmt.Errorf("文件超过上限 %d 字节（实际 %d）", s.MaxBytes, info.Size())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	rel, _ := filepath.Rel(s.Root, path)
	return map[string]any{
		"ok":      true,
		"path":    filepath.ToSlash(rel),
		"content": string(raw),
		"bytes":   len(raw),
	}, nil
}

func (s *Sandbox) listDir(input map[string]any) (map[string]any, error) {
	dir, err := s.Resolve(strAny(input["path"]))
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(entries))
	for _, e := range entries {
		item := map[string]any{"name": e.Name(), "dir": e.IsDir()}
		if info, err := e.Info(); err == nil {
			item["size"] = info.Size()
		}
		out = append(out, item)
	}
	rel, _ := filepath.Rel(s.Root, dir)
	return map[string]any{
		"ok":      true,
		"path":    filepath.ToSlash(rel),
		"entries": out,
		"count":   len(out),
	}, nil
}

// runCommand 执行白名单内的命令。
//
// 不经过 shell：argv[0] 必须是白名单里的可执行文件（按 basename 比较），
// 因此模型无法用管道 / 重定向 / 命令串联做额外的事情。
func (s *Sandbox) runCommand(ctx context.Context, input map[string]any) (map[string]any, error) {
	if !s.AllowShell {
		return nil, fmt.Errorf("命令执行未开启（沙箱 AllowShell=false）")
	}
	cmdline := strAny(input["command"])
	if strings.TrimSpace(cmdline) == "" {
		return nil, fmt.Errorf("command 不能为空")
	}
	argv := splitArgs(cmdline)
	if len(argv) == 0 {
		return nil, fmt.Errorf("command 解析为空")
	}
	if !s.allowed(argv[0]) {
		return nil, fmt.Errorf("可执行文件不在白名单内: %s", filepath.Base(argv[0]))
	}

	timeout := s.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cwd := s.Root
	if p := strAny(input["cwd"]); p != "" {
		resolved, err := s.Resolve(p)
		if err != nil {
			return nil, err
		}
		cwd = resolved
	}

	cmd := exec.CommandContext(cctx, argv[0], argv[1:]...)
	cmd.Dir = cwd
	var out capWriter
	out.max = s.MaxBytes
	cmd.Stdout = &out
	cmd.Stderr = &out
	runErr := cmd.Run()

	res := map[string]any{
		"ok":        runErr == nil,
		"command":   cmdline,
		"output":    out.String(),
		"truncated": out.truncated,
	}
	if runErr != nil {
		res["error"] = runErr.Error()
	}
	if cctx.Err() == context.DeadlineExceeded {
		res["ok"] = false
		res["error"] = fmt.Sprintf("命令超时（%s）", timeout)
	}
	return res, nil
}

func (s *Sandbox) allowed(bin string) bool {
	base := strings.ToLower(filepath.Base(bin))
	if base == "" {
		return false
	}
	for _, a := range s.ShellAllow {
		if strings.ToLower(strings.TrimSpace(a)) == base {
			return true
		}
	}
	return false
}

// capWriter 是有字节上限的写入器（超过上限后丢弃并标记 truncated）。
type capWriter struct {
	max       int64
	buf       []byte
	truncated bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	if w.max <= 0 {
		w.buf = append(w.buf, p...)
		return len(p), nil
	}
	room := w.max - int64(len(w.buf))
	if room <= 0 {
		w.truncated = true
		return len(p), nil
	}
	if int64(len(p)) > room {
		w.buf = append(w.buf, p[:room]...)
		w.truncated = true
		return len(p), nil
	}
	w.buf = append(w.buf, p...)
	return len(p), nil
}

func (w *capWriter) String() string { return string(w.buf) }

// splitArgs 做最小化的命令行拆分：支持双引号包裹与反斜杠转义，不解释 shell 语法。
func splitArgs(s string) []string {
	var (
		args    []string
		cur     strings.Builder
		quote   bool
		escaped bool
		started bool
	)
	flush := func() {
		if started {
			args = append(args, cur.String())
			cur.Reset()
			started = false
		}
	}
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
			started = true
		case r == '\\':
			escaped = true
			started = true
		case r == '"':
			quote = !quote
			started = true
		case (r == ' ' || r == '\t') && !quote:
			flush()
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	flush()
	return args
}

// strAny 把任意标量转字符串（模型入参里数字/布尔都可能出现）。
func strAny(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%v", t)
	case int:
		return fmt.Sprintf("%d", t)
	case int64:
		return fmt.Sprintf("%d", t)
	case bool:
		if t {
			return "true"
		}
		return "false"
	}
	return fmt.Sprintf("%v", v)
}
