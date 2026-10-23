// Package auth 管理小浣熊凭证：解析、临期判定与原子落盘。
//
// 凭证形状取自桌面端登录态文件 ~/.box-agent/config/auth.json，字段口径与
// github.com/aimod-cc/agent2api 的 raccoon credentials 一致：
// access_token ?? token ?? auth_token，refresh_token 单独取，expires_at 缺失时不预刷新。
package auth

import (
	"encoding/base64"
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
	sub, jwtExp := tokenClaims(access)
	derived := false
	// DesktopUID 是哨兵值而不是真实账号身份：早期导入会把空 uid 规范化成它再落盘，
	// 所以文件里显式写着 raccoon-desktop 同样等于「没声明身份」，要一并重新推导。
	if uid == "" || uid == DesktopUID {
		// 桌面端 auth.json 只有 access_token / refresh_token / office_identity，不含 uid。
		// 空 uid 一律落到 DesktopUID，会让所有账号写进同一个 raccoon-desktop.json 并
		// 互相覆盖——多账号池因此永远只有一行。JWT 的 sub 是上游用户标识（pu-…），
		// 同一账号恒定、不同账号必不同，用它当 uid 才真的能并存。
		if sub != "" {
			uid = sub
			derived = true
		}
	}
	if uid == "" {
		uid = DesktopUID
	}
	nickname := firstNonEmpty(strings.TrimSpace(d.Nickname), strings.TrimSpace(d.Name))
	if nickname == "" {
		nickname = uid
		if derived {
			// 派生的 uid 有 42 字符，直接当展示名会把账号表那一列撑开；截短只影响显示。
			nickname = shortUID(uid)
		}
	}
	// 显式 expires_at 优先；桌面端 auth.json 不带这个字段，就从 JWT 的 exp 补。
	// 补不上时保持 0（= 不预刷新），行为与旧版一致。
	exp := normalizeExpiresAt(d.ExpiresAt)
	if exp == 0 {
		exp = jwtExp
	}
	return &Auth{
		uid:          uid,
		nickname:     nickname,
		accessToken:  access,
		refreshToken: strings.TrimSpace(d.RefreshToken),
		expiresAt:    exp,
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
		// 文件名是持久化的身份来源：Parse 改进后，早期导入的老文件（文件名仍是
		// raccoon-desktop）能从 JWT 的 sub 解析出真实身份。两者不一致必须收敛，否则
		// 下次导入同一账号会另存一份，池里出现两条指向同一账号的记录——它们共用
		// refreshToken，轮换时会把对方踢下线。
		if uid := uidFromFile(f); uid != "" && uid != a.uid {
			if uid == DesktopUID && a.uid != DesktopUID {
				// 历史遗留的 raccoon-desktop.json：把文件迁到新名字。目标已存在时既不
				// 迁移也不覆盖，两份都留着让用户自己决定删哪个。
				dst := FilePathFor(dir, a.uid)
				if _, statErr := os.Stat(dst); os.IsNotExist(statErr) {
					if renameErr := os.Rename(f, dst); renameErr == nil {
						f = dst
					}
				}
			} else {
				// 其余情况文件名优先：它是 uid 已经落盘的形式。
				a.uid = uid
				if a.nickname == DesktopUID {
					a.nickname = uid
				}
			}
		}
		// 早期版本的自动昵称取 uid 前 12 字符，UUIDv7 下同期账号会撞成同一个名字
		// （pu-01a1365c-… 与 pu-01a1365c-… 都显示 pu-01a1365c）。认出来就按新口径
		// 重算；用户手动改过的名字不匹配这个特征，不会被动。
		if legacy := legacyShortUID(a.uid); a.nickname == legacy && legacy != shortUID(a.uid) {
			a.nickname = shortUID(a.uid)
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

// uidFromToken 从 access_token 的 JWT payload 里取 sub 当账号 ID。
//
// 只读不验签：这里取的是身份标签，凭证有效性由上游判定；伪造的 sub 顶多让自己在本机
// 多占一个文件名，拿不到别人的额度。格式不对就返回空串，由调用方退回 DesktopUID。
func uidFromToken(token string) string {
	sub, _ := tokenClaims(token)
	return sub
}

// tokenClaims 解出 JWT payload 里的 sub 与 exp。桌面端 auth.json 两个都不带，
// 不从这里补的话：uid 会全塌到 DesktopUID，expires_at 会停在 0（调度器据此
// 判定「无需预刷新」，token 到期后账号直接失效、只能重新导入）。
func tokenClaims(token string) (string, int64) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", 0
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return "", 0
	}
	var claims struct {
		Sub string `json:"sub"`
		Exp int64  `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", 0
	}
	return sanitizeUID(claims.Sub), normalizeExpiresAt(claims.Exp)
}

// sanitizeUID 把 sub 收敛成可安全落进文件名的字符集（uid 会拼进 raccoon-<uid>.json）。
func sanitizeUID(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "-_")
}

// legacyShortUID 是旧版自动昵称的口径（uid 前 12 字符），只用来识别并纠正历史数据。
func legacyShortUID(uid string) string {
	if len(uid) <= 12 {
		return uid
	}
	return strings.TrimRight(uid[:12], "-_")
}

// shortUID 截短派生 uid，只用于默认展示名。
//
// 取尾部而不是头部：小浣熊的 sub 是 UUIDv7（pu-<时间戳>-<随机位>），同一时期创建的
// 账号前缀完全相同，按前缀截会把两个账号显示成同一个名字。末段才是随机位。
func shortUID(uid string) string {
	if len(uid) <= 12 {
		return uid
	}
	if i := strings.LastIndex(uid, "-"); i >= 0 && len(uid)-i-1 >= 6 {
		return uid[i+1:]
	}
	return uid[len(uid)-12:]
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
