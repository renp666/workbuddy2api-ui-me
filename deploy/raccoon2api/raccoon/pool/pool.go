// Package pool 维护小浣熊账号池：启停、冷却、轮转与状态持久化。
//
// 冷却口径按小浣熊上游协议重映射（无 plan 权益概念）：
//   - 429 配额限流 → CoolQuota
//   - 404 / 未知模型等软失败 → CoolSoft（不计错误计数）
//   - 其余上游错误累计达阈值 → CoolErr
//
// 401 不走冷却：先刷新 token 同账号重试一次，仍失败才 Disable。
package pool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"workbuddy2api-raccoon/raccoon/auth"
)

// CoolKind 是冷却原因分类。
type CoolKind int

const (
	CoolQuota CoolKind = iota // 上游 429
	CoolSoft                  // 软失败，短冷却且不累计错误计数
	CoolErr                   // 连续错误达阈值
)

func (k CoolKind) String() string {
	switch k {
	case CoolQuota:
		return "quota_limited"
	case CoolSoft:
		return "soft_failure"
	case CoolErr:
		return "error_threshold"
	default:
		return "unknown"
	}
}

// Status 是对外的脱敏账号视图。
type Status struct {
	UID      string `json:"uid"`
	Nickname string `json:"nickname"`
	Points   int64  `json:"points"`
	Cooling  bool   `json:"cooling"`
	Until    string `json:"until,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
	Enabled  bool   `json:"enabled"`
	ErrCount int    `json:"err_count,omitempty"`
}

type entry struct {
	a        *auth.Auth
	points   int64
	disabled bool
	enabled  bool
	reason   string
	until    time.Time
	errCount int
}

func (e *entry) healthy(now time.Time) bool {
	if e.disabled || !e.enabled {
		return false
	}
	return e.until.IsZero() || !now.Before(e.until)
}

type stateEntry struct {
	Points   int64  `json:"points,omitempty"`
	Disabled *bool  `json:"disabled,omitempty"`
	Enabled  *bool  `json:"enabled,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Until    string `json:"until,omitempty"`
}

type stateFile struct {
	Accounts map[string]stateEntry `json:"accounts"`
}

// Pool 是账号池。statePath 非空时启停、冷却与积分落盘。
type Pool struct {
	mu        sync.RWMutex
	accounts  map[string]*entry
	statePath string
	state     stateFile
}

// New 建池；statePath 非空时先读旧状态。
func New(statePath string) *Pool {
	p := &Pool{
		accounts:  make(map[string]*entry),
		statePath: statePath,
		state:     stateFile{Accounts: map[string]stateEntry{}},
	}
	if statePath != "" {
		// 状态文件缺失或损坏不阻塞启动：池从空状态重建。
		_ = p.load()
	}
	return p
}

// Add 收编一份凭证；已存在时保留状态（启停、冷却、积分）只换凭证。
func (p *Pool) Add(a *auth.Auth) {
	if a == nil {
		return
	}
	uid := a.UID()
	p.mu.Lock()
	defer p.mu.Unlock()
	if old, ok := p.accounts[uid]; ok {
		// 换入的凭证若算「重新登录」，就清掉硬禁用与冷却：access token 变了说明是新登录态，
		// 过期时间更晚且旧的尚未过期同理（与导入的 409 保护同口径）。
		// 不做这一步的话，401 被禁用后重新导入凭证仍然不可用，且没有任何入口能恢复。
		relogin := a.JWT() != old.a.JWT() ||
			(a.ExpiresAtValue() > old.a.ExpiresAtValue() && old.a.ExpiresAtValue() > time.Now().Unix())
		old.a = a
		if relogin && (old.disabled || !old.until.IsZero() || old.errCount != 0) {
			old.disabled = false
			old.until = time.Time{}
			old.errCount = 0
			old.reason = ""
			p.saveLocked()
		}
		return
	}
	e := &entry{a: a, enabled: true}
	if st, ok := p.state.Accounts[uid]; ok {
		e.points = st.Points
		e.reason = st.Reason
		e.disabled = st.Disabled != nil && *st.Disabled
		e.enabled = st.Enabled == nil || *st.Enabled
		if st.Until != "" {
			if until, err := time.Parse(time.RFC3339, st.Until); err == nil {
				e.until = until
			}
		}
	}
	p.accounts[uid] = e
	p.saveLocked()
}

// SyncToDir 让池与磁盘凭证对齐：消失的账号剔除，新出现的收编。
func (p *Pool) SyncToDir(auths []*auth.Auth) {
	seen := make(map[string]bool, len(auths))
	for _, a := range auths {
		seen[a.UID()] = true
		p.Add(a)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	changed := false
	for uid := range p.accounts {
		if !seen[uid] {
			delete(p.accounts, uid)
			changed = true
		}
	}
	if changed {
		p.saveLocked()
	}
}

// Remove 删除账号；返回是否命中。
func (p *Pool) Remove(uid string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.accounts[uid]; !ok {
		return false
	}
	delete(p.accounts, uid)
	p.saveLocked()
	return true
}

// SetEnabled 改用户软开关。reason 非空时一并记录。
// 显式启用是一次人工干预：连同硬禁用、冷却与错误计数一起清，让「启用」名副其实。
// 若凭证确已失效，下一次请求会再次 Disable 并写入新原因，属自愈而非误放行。
func (p *Pool) SetEnabled(uid string, enabled bool, reason string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.accounts[uid]
	if !ok {
		return false
	}
	e.enabled = enabled
	if reason != "" {
		e.reason = reason
	} else if enabled {
		e.disabled = false
		e.reason = ""
		e.until = time.Time{}
		e.errCount = 0
	}
	p.saveLocked()
	return true
}

// Pick 取一个健康账号（积分最高者）。
func (p *Pool) Pick() (*auth.Auth, bool) { return p.PickExcluding(nil) }

// PickExcluding 在健康账号里挑积分最高、且不在 tried 中的一个。
func (p *Pool) PickExcluding(tried map[string]bool) (*auth.Auth, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	now := time.Now()
	var best *entry
	for uid, e := range p.accounts {
		if tried != nil && tried[uid] {
			continue
		}
		if !e.healthy(now) {
			continue
		}
		if best == nil || e.points > best.points {
			best = e
		}
	}
	if best == nil {
		return nil, false
	}
	return best.a, true
}

// HealthyCount 返回当前可轮转的账号数。
func (p *Pool) HealthyCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	now := time.Now()
	n := 0
	for _, e := range p.accounts {
		if e.healthy(now) {
			n++
		}
	}
	return n
}

// SetPoints 写回上游余额。
func (p *Pool) SetPoints(uid string, points int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.accounts[uid]
	if !ok {
		return
	}
	e.points = points
	p.saveLocked()
}

// Cooldown 让账号冷却 d；kind 只用于原因展示。冷却会清零错误计数。
func (p *Pool) Cooldown(uid string, kind CoolKind, d time.Duration, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.accounts[uid]
	if !ok {
		return
	}
	e.until = time.Now().Add(d)
	e.errCount = 0
	if reason != "" {
		e.reason = reason
	} else {
		e.reason = kind.String()
	}
	p.saveLocked()
}

// Disable 硬禁用（登录态失效且刷新失败）。
func (p *Pool) Disable(uid, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.accounts[uid]
	if !ok {
		return
	}
	e.disabled = true
	e.until = time.Time{}
	e.errCount = 0
	e.reason = reason
	p.saveLocked()
}

// ReenableIfPoints 在确认账号仍有余额时解冻（仅解除冷却，不动 disabled）。
func (p *Pool) ReenableIfPoints(uid string, remain int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.accounts[uid]
	if !ok {
		return
	}
	if remain <= 0 || e.disabled || e.until.IsZero() {
		return
	}
	e.until = time.Time{}
	e.reason = ""
	e.errCount = 0
	p.saveLocked()
}

// NoteError 累计错误；达阈值后按 d 冷却。
func (p *Pool) NoteError(uid string, threshold int, d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.accounts[uid]
	if !ok {
		return
	}
	e.errCount++
	if threshold > 0 && e.errCount >= threshold {
		e.errCount = 0
		e.until = time.Now().Add(d)
		if e.reason == "" {
			e.reason = "error_threshold"
		}
	}
	p.saveLocked()
}

// NoteSuccess 成功一次，清错误计数。
func (p *Pool) NoteSuccess(uid string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.accounts[uid]
	if !ok {
		return
	}
	if e.errCount != 0 {
		e.errCount = 0
		p.saveLocked()
	}
}

// Status 返回单账号视图。
func (p *Pool) Status(uid string) (Status, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	e, ok := p.accounts[uid]
	if !ok {
		return Status{}, false
	}
	return e.status(time.Now()), true
}

// AuthByUID 返回凭证指针（供控制面读脱敏信息与刷新）。
func (p *Pool) AuthByUID(uid string) *auth.Auth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if e, ok := p.accounts[uid]; ok {
		return e.a
	}
	return nil
}

// List 返回按 UID 排序的全部账号视图。
func (p *Pool) List() []Status {
	p.mu.RLock()
	defer p.mu.RUnlock()
	now := time.Now()
	out := make([]Status, 0, len(p.accounts))
	for _, e := range p.accounts {
		out = append(out, e.status(now))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UID < out[j].UID })
	return out
}

func (e *entry) status(now time.Time) Status {
	cooling := !e.until.IsZero() && now.Before(e.until)
	st := Status{
		UID:      e.a.UID(),
		Nickname: e.a.Nickname(),
		Points:   e.points,
		Cooling:  cooling,
		Reason:   e.reason,
		Disabled: e.disabled,
		Enabled:  e.enabled,
		ErrCount: e.errCount,
	}
	if cooling {
		st.Until = e.until.UTC().Format(time.RFC3339)
	}
	return st
}

func (p *Pool) load() error {
	raw, err := os.ReadFile(p.statePath)
	if err != nil {
		return err
	}
	var sf stateFile
	if err := json.Unmarshal(raw, &sf); err != nil {
		return err
	}
	if sf.Accounts != nil {
		p.state.Accounts = sf.Accounts
	}
	return nil
}

func (p *Pool) saveLocked() {
	if p.statePath == "" {
		return
	}
	if p.state.Accounts == nil {
		p.state.Accounts = map[string]stateEntry{}
	}
	for uid, e := range p.accounts {
		st := stateEntry{Points: e.points, Reason: e.reason}
		if e.disabled {
			disabled := true
			st.Disabled = &disabled
		}
		// 只在关停时写 enabled=false，避免状态文件被默认 true 淹没。
		if !e.enabled {
			enabled := false
			st.Enabled = &enabled
		}
		if !e.until.IsZero() {
			st.Until = e.until.UTC().Format(time.RFC3339)
		}
		p.state.Accounts[uid] = st
	}
	raw, err := json.MarshalIndent(p.state, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p.statePath), 0o700); err != nil {
		return
	}
	tmp := p.statePath + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, p.statePath)
}
