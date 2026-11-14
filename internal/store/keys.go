package store

import (
	"time"

	"doubao2api/internal/ids"
	"doubao2api/internal/secret"
)

// --- 密钥 ---

// Keys 返回密钥快照（含运行时计数）。
func (s *Store) Keys() []*APIKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*APIKey, 0, len(s.st.Keys))
	for _, k := range s.st.Keys {
		cp := *k
		if st, ok := s.keyStats.Load(k.ID); ok {
			ks := st.(*keyStat)
			cp.Requests += ks.requests.Load()
			if v := ks.lastUsed.Load(); v > cp.LastUsed {
				cp.LastUsed = v
			}
		}
		out = append(out, &cp)
	}
	return out
}

// AddKey 签发一把新密钥，同时返回一次性的明文。
//
// 明文不会落盘，也不会再次出现在任何接口返回里；调用方（控制台）只展示一次。
func (s *Store) AddKey(name string) (*APIKey, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	plain := "sk-" + ids.Hex(24)
	k := &APIKey{
		ID:        ids.Hex(8),
		KeyHash:   secret.HashKey(plain),
		Display:   MaskKey(plain),
		Name:      name,
		Enabled:   true,
		CreatedAt: time.Now().Unix(),
	}
	s.st.Keys = append(s.st.Keys, k)
	s.keyStats.Store(k.ID, &keyStat{})
	s.dirty.Store(true)
	return k, plain
}

// MaskKey 返回密钥的展示用片段（sk-abc123…wxyz）。
func MaskKey(key string) string {
	if len(key) <= 10 {
		if key == "" {
			return ""
		}
		return key[:1] + "***"
	}
	return key[:6] + "..." + key[len(key)-4:]
}

// DeleteKey 按 ID 删除密钥。
func (s *Store) DeleteKey(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, k := range s.st.Keys {
		if k.ID == id {
			s.st.Keys = append(s.st.Keys[:i], s.st.Keys[i+1:]...)
			s.keyStats.Delete(id)
			s.dirty.Store(true)
			return true
		}
	}
	return false
}

// ToggleKey 按 ID 启用/停用密钥。
func (s *Store) ToggleKey(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.st.Keys {
		if k.ID == id {
			k.Enabled = !k.Enabled
			s.dirty.Store(true)
			return true
		}
	}
	return false
}

// KeyByRef 按 ID / 展示片段 / 明文密钥解析出一把密钥（深拷贝）。
//
// 控制台只拿到展示片段，因此它必须可解析；同时保留明文解析，便于脚本一次性
// 引用（解析后立刻丢弃，不写回状态文件）。
func (s *Store) KeyByRef(ref string) *APIKey {
	if ref == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	hash := secret.HashKey(ref)
	for _, k := range s.st.Keys {
		if k.ID == ref || k.Display == ref || k.KeyHash == hash {
			cp := *k
			return &cp
		}
	}
	return nil
}

// ValidKey 校验明文密钥并累加计数。
//
// 查表走读锁（不阻塞其它请求），计数走每把密钥独立的原子变量；
// 此前这里是「每请求抢一次全局写锁」，并发流式请求会明显互相拖慢。
func (s *Store) ValidKey(key string) bool {
	if key == "" {
		return false
	}
	hash := secret.HashKey(key)
	s.mu.RLock()
	var id string
	for _, k := range s.st.Keys {
		if k.Enabled && k.KeyHash == hash {
			id = k.ID
			break
		}
	}
	s.mu.RUnlock()
	if id == "" {
		return false
	}
	if st, ok := s.keyStats.Load(id); ok {
		ks := st.(*keyStat)
		ks.requests.Add(1)
		ks.lastUsed.Store(time.Now().Unix())
	}
	s.dirty.Store(true)
	return true
}

// HasKeys 报告是否已签发任何密钥。
func (s *Store) HasKeys() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.st.Keys) > 0
}

// --- 管理密码 ---

// SetAdminPassword 设置管理密码（仅保存哈希）；空串表示清除密码。
func (s *Store) SetAdminPassword(pw string) error {
	hash := ""
	if pw != "" {
		var err error
		hash, err = secret.HashPassword(pw)
		if err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.st.Settings.AdminPasswordHash = hash
	s.st.Settings.AdminPasswordLegacy = ""
	s.mu.Unlock()
	s.dirty.Store(true)
	return nil
}

// VerifyAdminPassword 校验管理密码。
//
// 未设置密码时返回 false：调用方据此判断「管理面是否可用」，
// 而不是把「没密码」当成「谁都能进」。
func (s *Store) VerifyAdminPassword(pw string) bool {
	s.mu.RLock()
	hash := s.st.Settings.AdminPasswordHash
	s.mu.RUnlock()
	if hash == "" {
		return false
	}
	return secret.VerifyPassword(hash, pw)
}

// HasAdminPassword 报告是否已设置管理密码。
func (s *Store) HasAdminPassword() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.st.Settings.AdminPasswordHash != ""
}
