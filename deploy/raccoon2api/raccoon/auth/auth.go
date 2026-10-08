// Package auth 管理小浣熊凭证：解析、临期判定与原子落盘。
//
// 凭证形状取自桌面端登录态文件 ~/.box-agent/config/auth.json，字段口径与
// github.com/aimod-cc/agent2api 的 raccoon credentials 一致：
// access_token ?? token ?? auth_token，refresh_token 单独取，expires_at 缺失时不预刷新。
package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DesktopUID 是凭证未声明账号身份时使用的固定账号 ID（与上游同口径）。
const DesktopUID = "raccoon-desktop"

// RefreshMargin 是临期前主动刷新的余量（上游 PROACTIVE_REFRESH_MARGIN_MS）。
const RefreshMargin = 5 * time.Minute

// Auth 是一份小浣熊凭证。token 字段由 mu 保护：刷新在途时读取方拿到旧快照而不是半写状态。
type Auth struct {
	mu sync.RWMutex

	uid          string
	nickname     string
	accessToken  string
	refreshToken string
	expiresAt    int64

	filePath string
}

// doc 是落盘与解析共用的 JSON 形状。Auth 子对象用于兼容把凭证嵌在 auth 下的写法。
type doc struct {
	UID          string `json:"uid,omitempty"`
	Nickname     string `json:"nickname,omitempty"`
	Name         string `json:"name,omitempty"`
	AccessToken  string `json:"access_token,omitempty"`
	Token        string `json:"token,omitempty"`
	AuthToken    string `json:"auth_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresAt    any    `json:"expires_at,omitempty"`
	Auth         *doc   `json:"auth,omitempty"`
}

// Parse 解析一份凭证。raw 为空或缺 access_token 时返回错误（调用方据此拒绝导入）。
func Parse(raw []byte) (*Auth, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, fmt.Errorf("empty auth storage")
	}
	var d doc
	if err := json.Unmarshal([]byte(trimmed), &d); err != nil {
		return nil, fmt.Errorf("parse_error: %v", err)
	}
	if d.Auth != nil {
		// 嵌套形：{"auth":{...}} —— 凭证在子对象里，身份字段仍可能留在根上。
		inner := *d.Auth
		inner.UID = d.UID
		inner.Nickname = firstNonEmpty(d.Nickname, d.Name, inner.Nickname, inner.Name)
		d = inner
	}
	access := firstNonEmpty(d.AccessToken, d.Token, d.AuthToken)
	if access == "" {
		return nil, fmt.Errorf("parse_error: missing access_token")
	}
	uid := strings.TrimSpace(d.UID)
	if uid == "" {
		uid = DesktopUID
	}
	nickname := firstNonEmpty(strings.TrimSpace(d.Nickname), strings.TrimSpace(d.Name), uid)
	return &Auth{
		uid:          uid,
		nickname:     nickname,
		accessToken:  access,
		refreshToken: strings.TrimSpace(d.RefreshToken),
		expiresAt:    normalizeExpiresAt(d.ExpiresAt),
	}, nil
}

// UID 是账号 ID，加载后不变。
func (a *Auth) UID() string { return a.uid }

// Nickname 返回展示名。
func (a *Auth) Nickname() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.nickname
}

// SetNickname 改展示名（控制台 PATCH 账号用）。
func (a *Auth) SetNickname(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nickname = name
}

// JWT 返回当前 access token 快照。
func (a *Auth) JWT() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.accessToken
}

// RefreshTokenValue 返回当前 refresh token 快照。
func (a *Auth) RefreshTokenValue() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.refreshToken
}

// ExpiresAtValue 返回过期时间戳（秒）；0 = 上游未声明。
func (a *Auth) ExpiresAtValue() int64 {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.expiresAt
}

// FilePathValue 返回落盘路径（空 = 未绑定文件，SaveAtomic 会失败）。
func (a *Auth) FilePathValue() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.filePath
}

// CanRefresh 报告是否具备自助刷新所需的 refresh token。
func (a *Auth) CanRefresh() bool { return a.RefreshTokenValue() != "" }

// NeedsRefresh 报告是否在 within 内临期。上游未声明 expires_at 时不预刷新，
// 交由 401 之后的错误分支强制刷新。
func (a *Auth) NeedsRefresh(within time.Duration) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.needsRefreshLocked(within)
}

func (a *Auth) needsRefreshLocked(within time.Duration) bool {
	if a.expiresAt <= 0 {
		return false
	}
	return time.Now().Add(within).Unix() >= a.expiresAt
}

// SetTokens 写入新的 access/refresh token 与过期时间。
func (a *Auth) SetTokens(accessToken, refreshToken string, expiresAt int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.accessToken = accessToken
	if refreshToken != "" {
		a.refreshToken = refreshToken
	}
	if expiresAt > 0 {
		a.expiresAt = expiresAt
	}
}

// SetFilePath 绑定落盘路径。
func (a *Auth) SetFilePath(p string) { a.filePath = p }

// SaveAtomic 原子落盘（tmp + rename），避免刷新在途被读到半截文件。
func (a *Auth) SaveAtomic() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.saveAtomicLocked()
}

func (a *Auth) saveAtomicLocked() error {
	if a.filePath == "" {
		return fmt.Errorf("no FilePath set")
	}
	if err := os.MkdirAll(filepath.Dir(a.filePath), 0o700); err != nil {
		return err
	}
	out := doc{
		UID:          a.uid,
		Nickname:     a.nickname,
		AccessToken:  a.accessToken,
		RefreshToken: a.refreshToken,
		ExpiresAt:    a.expiresAt,
	}
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	tmp := a.filePath + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.filePath)
}

// FilePathFor 返回某账号的凭证路径。
func FilePathFor(authDir, uid string) string {
	return filepath.Join(authDir, "raccoon-"+uid+".json")
}

// MaskToken 保留前 n 个字符供人眼比对，其余只报长度。
func MaskToken(s string, n int) string {
	if len(s) <= n {
		return s + "…"
	}
	return s[:n] + fmt.Sprintf("…(%d chars)", len(s))
}

// LoadDir 读取目录下全部 raccoon-*.json；解析失败的文件静默跳过（不阻塞启动）。
func LoadDir(dir string) ([]*Auth, error) {
	files, err := filepath.Glob(filepath.Join(dir, "raccoon-*.json"))
	if err != nil {
		return nil, err
	}
	out := make([]*Auth, 0, len(files))
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		a, err := Parse(raw)
		if err != nil {
			continue
		}
		if a.uid == DesktopUID {
			// 文件名是更可靠的账号身份来源：避免多个文件都落到固定 UID 互相覆盖。
			if uid := uidFromFile(f); uid != "" && uid != DesktopUID {
				a.uid = uid
				if a.nickname == DesktopUID {
					a.nickname = uid
				}
			}
		}
		a.filePath = f
		out = append(out, a)
	}
	return out, nil
}

func uidFromFile(path string) string {
	base := filepath.Base(path)
	base = strings.TrimSuffix(base, ".json")
	return strings.TrimPrefix(base, "raccoon-")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// normalizeExpiresAt 兼容秒与毫秒两种时间戳，以及字符串形式。
func normalizeExpiresAt(v any) int64 {
	switch t := v.(type) {
	case float64:
		return normalizeNumber(int64(t))
	case int64:
		return normalizeNumber(t)
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return normalizeNumber(n)
		}
		if f, err := t.Float64(); err == nil {
			return normalizeNumber(int64(f))
		}
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0
		}
		var n int64
		if _, err := fmt.Sscanf(s, "%d", &n); err == nil {
			return normalizeNumber(n)
		}
		if f, err := time.Parse(time.RFC3339, s); err == nil {
			return f.Unix()
		}
	}
	return 0
}

// 超过 1e12 视作毫秒（1e12 秒对应公元 33658 年，不可能是真实秒级时间戳）。
func normalizeNumber(n int64) int64 {
	if n > 1e12 {
		return n / 1000
	}
	return n
}

// NormalizeExpiresAt 导出包装：刷新响应的 expires_at 与凭证解析共用同一归一口径。
func NormalizeExpiresAt(v any) int64 { return normalizeExpiresAt(v) }
