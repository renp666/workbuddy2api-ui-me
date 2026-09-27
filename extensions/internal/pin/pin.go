// Package pin 保存「人工锁定消费账号」状态：每个 realm（cn/global）最多锁定一个
// 账号 UID，锁定后该平台的对话请求严格只走该账号（严格语义由 server 层执行）。
// 状态持久化在 core 数据目录的 pin.json，与账号状态同生命周期；损坏文件拒绝启动，
// 不静默清空，避免一次解析异常把用户显式锁定的省钱配置悄悄解除。
package pin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// 受支持的平台域；锁定状态按域独立，不允许前端伪造其他键名。
var allowedRealms = map[string]bool{"cn": true, "global": true}

const maxUIDLen = 128

// Store 是 realm -> 账号 UID 的并发安全映射，所有变更同步落盘。
type Store struct {
	mu      sync.RWMutex
	path    string
	byRealm map[string]string
}

type diskFormat struct {
	CN     string `json:"cn,omitempty"`
	Global string `json:"global,omitempty"`
}

// Open 加载 path 下的锁定文件。文件不存在视为「未锁定任何账号」并惰性创建目录；
// 文件存在但无法解析或含有非法字段时返回错误，调用方应拒绝启动而非自动重置。
func Open(path string) (*Store, error) {
	s := &Store{path: path, byRealm: make(map[string]string)}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, fmt.Errorf("读取账号锁定文件失败: %w", err)
	}
	var disk diskFormat
	if err := json.Unmarshal(raw, &disk); err != nil {
		return nil, fmt.Errorf("pin.json 损坏，请恢复备份或删除后重新锁定: %w", err)
	}
	if disk.CN != "" {
		if err := validate("cn", disk.CN); err != nil {
			return nil, fmt.Errorf("pin.json 内容非法: %w", err)
		}
		s.byRealm["cn"] = disk.CN
	}
	if disk.Global != "" {
		if err := validate("global", disk.Global); err != nil {
			return nil, fmt.Errorf("pin.json 内容非法: %w", err)
		}
		s.byRealm["global"] = disk.Global
	}
	return s, nil
}

// PinnedUID 返回 realm 当前锁定的账号 UID；未锁定返回空串。
// 供选号路径高频调用（server.Pinner 接口）。
func (s *Store) PinnedUID(realm string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byRealm[realm]
}

// Snapshot 返回 realm -> UID 的副本，供管理接口展示（只含已锁定的域）。
func (s *Store) Snapshot() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]string, len(s.byRealm))
	for realm, uid := range s.byRealm {
		out[realm] = uid
	}
	return out
}

// Set 把 realm 的消费锁定到 uid（同域再次 Set 即替换），并原子落盘。
func (s *Store) Set(realm, uid string) error {
	if err := validate(realm, uid); err != nil {
		return err
	}
	s.mu.Lock()
	s.byRealm[realm] = uid
	s.mu.Unlock()
	return s.persist()
}

// Clear 解除 realm 的锁定；原本未锁定也是成功。落盘后返回。
func (s *Store) Clear(realm string) error {
	if !allowedRealms[realm] {
		return errors.New("仅支持 cn 或 global 平台")
	}
	s.mu.Lock()
	delete(s.byRealm, realm)
	s.mu.Unlock()
	return s.persist()
}

// persist 在锁外做磁盘 IO（映射本身已一致），临时文件 0600 + rename 原子替换，
// 避免崩溃留下半写入文件；目录 fsync 保证 rename 结果落盘。
func (s *Store) persist() error {
	s.mu.RLock()
	disk := diskFormat{CN: s.byRealm["cn"], Global: s.byRealm["global"]}
	s.mu.RUnlock()
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建锁定文件目录失败: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".pin-*")
	if err != nil {
		return fmt.Errorf("写入锁定文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	payload, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		tmp.Close()
		return err
	}
	payload = append(payload, '\n')
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		return fmt.Errorf("写入锁定文件失败: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("写入锁定文件失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("写入锁定文件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("写入锁定文件失败: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("写入锁定文件失败: %w", err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func validate(realm, uid string) error {
	if !allowedRealms[realm] {
		return errors.New("仅支持 cn 或 global 平台")
	}
	if uid == "" || len(uid) > maxUIDLen {
		return fmt.Errorf("账号标识长度须在 1~%d 之间", maxUIDLen)
	}
	for _, r := range uid {
		if r < 0x21 || r == 0x7f {
			return errors.New("账号标识含有非法字符")
		}
	}
	return nil
}
