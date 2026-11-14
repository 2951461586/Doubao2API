package store

import (
	"context"
	"doubao2api/internal/ids"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Save 原子写回状态文件，并维护上一版与按日备份。
//
// 写入顺序：
//  1. 旧文件先复制为 <path>.bak（回滚用，不覆盖已有 .bak 之外的任何东西）；
//  2. 若今天还没做过快照，复制一份到 backups/data-<时间戳>.json（保留 7 份）；
//  3. tmp → fsync → rename，保证读者永远看到完整 JSON。
func (s *Store) Save() error {
	// 串行化落盘，避免自动任务与 HTTP 请求同时写同一个 .tmp 文件。
	s.saveMu.Lock()
	defer s.saveMu.Unlock()

	raw, err := s.marshalState()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}

	if prev, err := os.ReadFile(s.path); err == nil {
		_ = writeFileSync(s.path+".bak", prev)
		s.snapshotIfNeeded(prev)
	}

	tmp := s.path + ".tmp"
	if err := writeFileSync(tmp, raw); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.dirty.Store(false)
	return nil
}

// marshalState 在写锁内把密钥运行时计数合并进状态并序列化。
func (s *Store) marshalState() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.st.Keys {
		st, ok := s.keyStats.Load(k.ID)
		if !ok {
			continue
		}
		ks := st.(*keyStat)
		if n := ks.requests.Swap(0); n > 0 {
			k.Requests += n
		}
		if v := ks.lastUsed.Load(); v > k.LastUsed {
			k.LastUsed = v
		}
	}
	s.st.Meta.SchemaVersion = schemaVersion
	return json.MarshalIndent(s.st, "", "  ")
}

// writeFileSync 写文件并落盘。
func writeFileSync(path string, raw []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// snapshotIfNeeded 每天保留一份带时间戳的状态文件副本，最多 7 份。
//
// 刻意与 .bak 分开：.bak 每次保存都覆盖（只回溯一步），
// backups/ 里的副本按天保留（能回溯一周，包含误删账号 / 密钥之前的状态）。
func (s *Store) snapshotIfNeeded(raw []byte) {
	today := time.Now().Format("2006-01-02")
	if s.lastSnapshot == today {
		return
	}
	dir := filepath.Join(filepath.Dir(s.path), "backups")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	name := filepath.Join(dir, "data-"+time.Now().Format("20060102-150405")+".json")
	if err := writeFileSync(name, raw); err != nil {
		return
	}
	s.lastSnapshot = today
	s.pruneSnapshots(dir, 7)
}

// pruneSnapshots 只保留最近 keep 份快照。
func (s *Store) pruneSnapshots(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "data-") && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	if len(names) <= keep {
		return
	}
	sort.Strings(names)
	for _, name := range names[:len(names)-keep] {
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// Snapshot 立即生成一份带时间戳的备份（不改变主文件）。
func (s *Store) Snapshot() (string, error) {
	raw, err := s.marshalState()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(filepath.Dir(s.path), "backups")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := filepath.Join(dir, "data-manual-"+time.Now().Format("20060102-150405")+".json")
	if err := writeFileSync(name, raw); err != nil {
		return "", err
	}
	return name, nil
}

// Export 返回可导出/可迁移的状态（不含请求日志，避免把排障信息带出去）。
func (s *Store) Export() ([]byte, error) {
	raw, err := s.marshalState()
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, err
	}
	st.Logs = nil
	return json.MarshalIndent(&st, "", "  ")
}

// Import 导入状态。
//
// merge=true 时只新增/更新账号（按 sessionid 去重），不动设置与密钥；
// merge=false 时整体替换账号、密钥与设置，但**保留当前管理密码**，
// 防止用一份旧导出把管理入口锁死。
func (s *Store) Import(raw []byte, merge bool) (added, updated int, err error) {
	var in State
	if err := json.Unmarshal(raw, &in); err != nil {
		return 0, 0, fmt.Errorf("导入文件解析失败: %w", err)
	}
	if len(in.Accounts) == 0 && len(in.Keys) == 0 {
		return 0, 0, fmt.Errorf("导入文件里没有账号或密钥")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if !merge {
		// 保留当前管理密码：一份旧导出不应该把管理入口锁死。
		keepHash := s.st.Settings.AdminPasswordHash
		s.st.Accounts = nil
		s.st.Keys = nil
		s.st.Settings = in.Settings
		s.st.Settings.AdminPasswordHash = keepHash
		s.st.Settings.AdminPasswordLegacy = ""
	}
	for _, a := range in.Accounts {
		if a == nil {
			continue
		}
		a.Normalize()
		if a.ID == "" {
			a.ID = ids.Hex(8)
		}
		replaced := false
		for i, ex := range s.st.Accounts {
			if ex.SessionID() != "" && ex.SessionID() == a.SessionID() {
				a.ID = ex.ID
				s.st.Accounts[i] = a
				replaced = true
				break
			}
		}
		if replaced {
			updated++
		} else {
			s.st.Accounts = append(s.st.Accounts, a)
			added++
		}
	}
	for _, k := range in.Keys {
		if k == nil || k.KeyHash == "" || s.keyRecordLocked(k.KeyHash) != nil {
			continue
		}
		if k.ID == "" {
			k.ID = ids.Hex(8)
		}
		s.st.Keys = append(s.st.Keys, k)
	}
	s.dirty.Store(true)
	return added, updated, nil
}

// keyRecordLocked 按指纹查找密钥；调用方必须已持锁。
func (s *Store) keyRecordLocked(hash string) *APIKey {
	for _, k := range s.st.Keys {
		if k.KeyHash == hash {
			return k
		}
	}
	return nil
}

// MarkDirty 标记内存状态需要落盘。
func (s *Store) MarkDirty() { s.dirty.Store(true) }

// StartAutosave 启动合并落盘：有变更才写，且最多每 interval 写一次。
//
// 请求级的账号失败计数、调用统计、密钥用量以前只在进程正常退出时保存，
// 容器被强停 / 主机断电就丢了。
func (s *Store) StartAutosave(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				s.flush()
				return
			case <-ticker.C:
				s.flush()
			}
		}
	}()
}

// flush 在有变更时落盘。
func (s *Store) flush() {
	if !s.dirty.Load() {
		return
	}
	if err := s.Save(); err != nil {
		log.Printf("自动保存状态失败: %v", err)
	}
}
