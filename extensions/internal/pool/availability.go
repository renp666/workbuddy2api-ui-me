// 模型列表过滤的可用性口径：静态健康判定（不含在途租约瞬态因素）。
package pool

import "time"

// AccountRef 是模型归属标签里的最小账号标识：UID 稳定可寻址，昵称仅用于展示。
type AccountRef struct {
	UID      string
	Nickname string
}

// HasAvailableAccountForModelRealm 报告 realm 内是否存在对 model 可选的账号
// （healthyForModel 口径：未禁用、无账号级冷却/熔断、该模型不在 6004 独立冷却）。
// 与 AvailableUIDsForModelRealm 的差别是**不排除在途满额**：在途是并发瞬态，
// 若计入会让 /v1/models 列表随请求并发闪烁。仅供模型列表过滤等只读展示使用，
// 选号路径照常走 Pick/AvailableUIDs 系列。realm=="" 表示不限域。
func (p *Pool) HasAvailableAccountForModelRealm(model, realm string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	now := time.Now()
	for _, e := range p.byUID {
		if realm != "" && e.a.Realm() != realm {
			continue
		}
		if e.healthyForModel(now, model) {
			return true
		}
	}
	return false
}

// AvailableAccountsForModelRealm 返回 realm 内对 model 可选的账号清单（同
// HasAvailableAccountForModelRealm 的静态健康口径，UID 升序稳定排序），供模型列表
// 逐模型透出「哪些账号能跑这个模型」。realm=="" 表示不限域。仅只读展示用。
func (p *Pool) AvailableAccountsForModelRealm(model, realm string) []AccountRef {
	p.mu.RLock()
	defer p.mu.RUnlock()
	now := time.Now()
	var out []AccountRef
	for _, e := range p.byUID {
		if realm != "" && e.a.Realm() != realm {
			continue
		}
		if e.healthyForModel(now, model) {
			out = append(out, AccountRef{UID: e.a.UID, Nickname: e.a.Nickname})
		}
	}
	for i := 1; i < len(out); i++ { // 插入排序：账号数量级很小，避免额外依赖
		for j := i; j > 0 && out[j].UID < out[j-1].UID; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
