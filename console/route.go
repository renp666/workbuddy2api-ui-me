package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// routeChannel 是一条路由目标所属的通道，取值与公共模型前缀一一对应；
// core 没有前缀，它承载 cn:/global: 这类主账号池模型。
type routeChannel string

const (
	channelCore     routeChannel = "core"
	channelGLM      routeChannel = "glm"
	channelQoder    routeChannel = "qoder"
	channelOpenCode routeChannel = "opencode"
	channelAutoClaw routeChannel = "autoclaw"
	channelTrae     routeChannel = "trae"
	channelRaccoon  routeChannel = "raccoon"
)

// routeChannelOrder 是页面展示与目录抓取的固定通道顺序：core 在前，
// 让「回退核心通道」的默认目标在界面上排在最前面。
var routeChannelOrder = []routeChannel{channelCore, channelGLM, channelQoder, channelOpenCode, channelAutoClaw, channelTrae, channelRaccoon}

// autoModelName 是 console 自建的虚拟模型名：不在任何上游目录里，由 console
// 在请求时按探测结论挑一条最优链路。名字带 gateway- 前缀，是为了和各通道可能
// 存在的原生 auto 模型（cn:auto、qoder-auto 等）区分开，避免客户端填 auto
// 时说不清到底指谁。开发阶段直接切换，不为旧名 auto 保留兼容解析。
const autoModelName = "gateway-auto"

// routeFileVersion 是路由配置文件的结构版本。版本不匹配时按上一份配置处理，
// 不做静默迁移，避免把旧语义当成新语义路由。
const routeFileVersion = 1

// routeTarget 是别名候选链上的一跳：通道 + 该通道的模型名。模型名允许写成
// 通道原生裸名，也允许写成已带公共前缀的对外形态，出口统一走 publicModel 的
// 幂等换算，两种写法都能正确分流。
type routeTarget struct {
	Channel routeChannel `json:"channel"`
	Model   string       `json:"model"`
}

// 别名选路策略。
const (
	// strategyPriority 恒用候选链第一跳（主目标）。这是默认值，也是引入候选链
	// 之前的行为，保证存量配置升级后路由结果一字不变。
	strategyPriority = "priority"
	// strategyHealth 用探测结论在候选链内挑 band 最优的一跳，实现「同一类模型
	// 跨通道热备」；链上没有任何探测结论时按链序回退到主目标，不随机挑。
	strategyHealth = "health"
)

// maxRouteTargets 是单个别名的候选链长度上限（主目标 + 备用）。8 跳足够覆盖
// 「一个模型在六条通道各有部署」的极端情况，同时防止失控配置拖慢每次请求解析。
const maxRouteTargets = 8

// routeEntry 借鉴 LiteLLM model_list 的数据模型：一条「对外别名 → 真实上游」的
// 映射。Alias 是客户端写进请求 model 字段的名字，Channel+Model 是主目标；
// Fallbacks 是同一别名的备用链路，与主目标一起构成候选链。
type routeEntry struct {
	Alias     string        `json:"alias"`
	Channel   routeChannel  `json:"channel"`
	Model     string        `json:"model"`
	Fallbacks []routeTarget `json:"fallbacks,omitempty"`
	Strategy  string        `json:"strategy,omitempty"`
	Enabled   bool          `json:"enabled"`
	Note      string        `json:"note,omitempty"`
}

// strategy 返回生效的策略。空值与未知值一律按 priority 处理——配置里写错策略名
// 不该让整个别名失效，退化成「恒用主目标」是更安全的默认。
func (e routeEntry) strategy() string {
	if e.Strategy == strategyHealth {
		return strategyHealth
	}
	return strategyPriority
}

// targets 返回完整候选链：主目标在链首，备用按配置顺序在后。
func (e routeEntry) targets() []routeTarget {
	out := make([]routeTarget, 0, 1+len(e.Fallbacks))
	out = append(out, routeTarget{Channel: e.Channel, Model: e.Model})
	return append(out, e.Fallbacks...)
}

// publicModel 把一跳换算成该通道对外的公共模型名（幂等）。
func (t routeTarget) publicModel() string { return t.Channel.publicModel(t.Model) }

// nativeModel 返回一跳的通道原生模型名（剥掉公共前缀）。探测结论按原生名存候选
// （见 appendProbeEntries），比对时必须用同一形态，否则 qoder-qwen3.8-flash 这类
// 公共名永远匹配不上它的探测结论。
func (t routeTarget) nativeModel() string {
	public := t.publicModel()
	if prefix := t.Channel.prefix(); prefix != "" {
		return strings.TrimPrefix(public, prefix)
	}
	return public
}

// routeFile 是落盘到 console 可写目录的路由配置。
type routeFile struct {
	Version int          `json:"version"`
	Models  []routeEntry `json:"models"`
	// AutoFallback 是 auto 虚拟模型在没有探测结论时使用的核心通道模型。
	// 核心通道的模型 ID 由上游动态下发，因此这个值由管理员在页面上声明，
	// 缺省才用 defaultAutoFallback，不在代码里固化任何其他真实模型名。
	AutoFallback string `json:"auto_fallback"`
	// DisabledModels / EnabledModels 是模型积分开关的人工覆盖表，按公共模型名
	// （带通道前缀的对外形态）记录。两张表互斥：一个模型最多出现在其中一张。
	// 不在任何表里的模型按积分消耗规则取默认态：倍率已知且为 0 默认开启，
	// 倍率大于 0 或规则未知默认关闭。
	DisabledModels []string `json:"disabled_models,omitempty"`
	EnabledModels  []string `json:"enabled_models,omitempty"`
}

// defaultAutoFallback 是 AutoFallback 缺省值，也是实测中 core 目录下唯一
// 与 auto 语义对应的模型。
const defaultAutoFallback = "cn:auto"

// prefix 把通道名映射为「该通道公共模型名相对其原生模型名多出来的前缀」。
// core 的 cn:/global: 命名空间和 glm 的 glm- 前缀都由上游自己下发，属于原生
// 模型名的一部分，这里不能再补一次，否则会拼出 glm-glm-5.3 这种双前缀。
// 只有 qoder、opencode 与 autoclaw 的上游目录是裸名字，需要补公共前缀——这几条
// 判断与 publicRouter 的分流前缀完全一致，这是别名能复用既有分流的前提。
func (c routeChannel) prefix() string {
	switch c {
	case channelQoder:
		return qoderModelPrefix
	case channelOpenCode:
		return opencodeModelPrefix
	case channelAutoClaw:
		return autoclawModelPrefix
	case channelTrae:
		return traeModelPrefix
	case channelRaccoon:
		return raccoonModelPrefix
	}
	return ""
}

// publicModel 拼出该通道在 /v1/models 里对外呈现的模型名。别名解析结果最终
// 会被改写成这个值，再交给既有前缀分流。
//
// 这里的换算是幂等的：Model 传入的既可能是通道原生裸名，也可能已经是带公共
// 前缀的对外形态。控制台页面上的模型下拉来自 routeCatalog→fetchChannelModels，
// 那份目录对 qoder/opencode 已经补过一次前缀（返回「qoder-x」「opencode-OC · …」），
// 管理员据此写进 routes.json 的 model 字段本身就是公共形态。若这里再无条件补前缀，
// 就会拼出「qoder-qoder-x」这种双前缀，剥前缀转发给上游后上游不认，表现为别名
// 请求 model_not_found。core 的 cn:/global: 与 glm 的 glm- 由上游自带、prefix() 为空
// 或名字已含前缀，同样靠这个幂等判断保持原样。
func (e routeEntry) publicModel() string { return e.Channel.publicModel(e.Model) }

func (c routeChannel) publicModel(model string) string {
	if prefix := c.prefix(); prefix != "" && !strings.HasPrefix(model, prefix) {
		return prefix + model
	}
	return model
}

// realm 给别名条目挑一个 realm 标签，与各通道在 /v1/models 里的真实 realm 对齐：
// core 按模型名的 cn:/global: 命名空间，旁路通道用各自的 realm 名。别名是虚拟条目，
// realm 只作展示与下游归类用，不参与鉴权或额度。
func (c routeChannel) realm(model string) string {
	switch c {
	case channelGLM:
		return "glm"
	case channelQoder:
		return "qoder"
	case channelOpenCode:
		return "opencode"
	case channelAutoClaw:
		return "autoclaw"
	case channelTrae:
		return "trae"
	case channelRaccoon:
		return "raccoon"
	}
	if strings.HasPrefix(model, "global:") {
		return "global"
	}
	return "cn"
}

// aliasModelEntries 把当前配置的所有别名条目转成 /v1/models 的虚拟条目，让下游
// （含拒绝模型名带空格的消费方）能直接发现并按别名引用。条目沿用所属通道的
// realm，并额外带 alias/routed_model/virtual 字段标明它是间接层而非真实上游模型。
// 公布与开关解耦（用户口径「关机的手机仍拿在手里」）：别名配置了就公布，停用的
// 别名也在列表里、调用时报「已停用」；目标模型被停用不再隐藏别名——手机关机了
// 昵称也打不通，但名字还在。gateway-auto 由 mergedModels 单独追加。
func (h *server) aliasModelEntries() []json.RawMessage {
	if h.routes == nil {
		return nil
	}
	var out []json.RawMessage
	for _, entry := range h.routes.snapshot() {
		if entry.Alias == autoModelName {
			continue
		}
		// 通道未配置上游时不广告：请求会因前缀分流落空而失败，别名不该承诺一个到不了的链路。
		if !h.channelEnabled(entry.Channel) {
			continue
		}
		item, err := json.Marshal(map[string]any{
			"id":           entry.Alias,
			"object":       "model",
			"owned_by":     "prism",
			"realm":        entry.Channel.realm(entry.Model),
			"virtual":      true,
			"alias":        true,
			"routed_model": entry.publicModel(),
		})
		if err != nil {
			continue
		}
		out = append(out, item)
	}
	return out
}

// maxRouteEntries 限制单份配置的条目数，避免一个失控的页面把内存和文件都撑满。
const maxRouteEntries = 200

// routeBodyLimit 是路由配置 POST 的请求体上限。单条别名连备注最长约 300 字节，
// 200 条约 60 KiB，通用管理端点 8 KiB 的上限装不下，这里单独放宽并仍保持有界。
const routeBodyLimit = 256 << 10

// reservedAliasPrefixes 是别名不得占用的前缀：各旁路通道的公共前缀加上 core 的
// 两个命名空间。别名解析发生在前缀分流之前，占用这些前缀会静默遮蔽同名真实模型。
var reservedAliasPrefixes = []string{zcodeModelPrefix, qoderModelPrefix, opencodeModelPrefix, autoclawModelPrefix, traeModelPrefix, raccoonModelPrefix, "cn:", "global:"}

// validateRouteEntries 校验整份配置，返回第一条可读的错误文案直接给页面用。
// 校验失败一律拒绝写入，不做「尽力而为的部分生效」。
func validateRouteEntries(models []routeEntry) error {
	if len(models) > maxRouteEntries {
		return fmt.Errorf("路由条目不得超过 %d 条", maxRouteEntries)
	}
	seen := make(map[string]bool, len(models))
	for i, entry := range models {
		alias := strings.TrimSpace(entry.Alias)
		if !validRouteName(alias, 64, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._:-") {
			return fmt.Errorf("第 %d 条的别名不合法：只允许字母、数字、点、下划线、冒号和连字符，长度 1-64", i+1)
		}
		if alias == autoModelName {
			return fmt.Errorf("别名 %q 是保留的虚拟模型名，不能被覆盖", autoModelName)
		}
		if seen[alias] {
			return fmt.Errorf("别名 %q 重复", alias)
		}
		seen[alias] = true
		for _, reserved := range reservedAliasPrefixes {
			if strings.HasPrefix(alias, reserved) {
				return fmt.Errorf("别名 %q 不能以 %q 开头，那是上游命名空间或通道前缀，会遮蔽同名真实模型", alias, reserved)
			}
		}
		if err := validateRouteStrategy(entry); err != nil {
			return fmt.Errorf("别名 %q %s", alias, err)
		}
		model := strings.TrimSpace(entry.Model)
		if err := validateTarget(entry.Channel, model); err != nil {
			return fmt.Errorf("别名 %q %s", alias, err)
		}
		if len(entry.Fallbacks) > maxRouteTargets-1 {
			return fmt.Errorf("别名 %q 的备用链路不得超过 %d 条", alias, maxRouteTargets-1)
		}
		// 候选链去重：主目标与备用之间、备用彼此之间都不能指向同一跳。重复跳只会
		// 在 health 策略里制造无意义的并列，及早拒绝好过运行时静默容忍。
		seenTargets := map[routeTarget]bool{{Channel: entry.Channel, Model: model}: true}
		for j, fb := range entry.Fallbacks {
			fbModel := strings.TrimSpace(fb.Model)
			if err := validateTarget(fb.Channel, fbModel); err != nil {
				return fmt.Errorf("别名 %q 第 %d 条备用：%s", alias, j+1, err)
			}
			key := routeTarget{Channel: fb.Channel, Model: fbModel}
			if seenTargets[key] {
				return fmt.Errorf("别名 %q 的候选链含重复目标：%s + %s", alias, fb.Channel, fbModel)
			}
			seenTargets[key] = true
		}
		if len(entry.Note) > 200 {
			return fmt.Errorf("别名 %q 的备注不得超过 200 字", alias)
		}
	}
	return nil
}

// validateRouteStrategy 校验选路策略取值。空串合法（按 priority 生效）。
func validateRouteStrategy(entry routeEntry) error {
	switch entry.Strategy {
	case "", strategyPriority, strategyHealth:
		return nil
	}
	return fmt.Errorf("的策略只能是 %s 或 %s", strategyPriority, strategyHealth)
}

// validateTarget 校验一跳「通道 + 模型名」。core 的模型 ID 由上游以 cn:/global:
// 命名空间下发，缺前缀的裸名字 core 一定不认；反之旁路通道收不到带命名空间的名字。
// 主目标与每一条备用共用这套口径，避免两处判断漂移。
func validateTarget(channel routeChannel, model string) error {
	switch channel {
	case channelCore, channelGLM, channelQoder, channelOpenCode, channelAutoClaw, channelTrae, channelRaccoon:
	default:
		return errors.New("通道必须是 core、glm、qoder、opencode、autoclaw、trae 或 raccoon")
	}
	if !validRouteName(model, 128, routeModelChars) {
		return errors.New("模型名不合法：只允许字母、数字、点、下划线、冒号、连字符、空格、中点和斜杠，长度 1-128")
	}
	if channel == channelCore {
		if !strings.HasPrefix(model, "cn:") && !strings.HasPrefix(model, "global:") {
			return errors.New("指向 core 时模型名须带 cn: 或 global: 前缀")
		}
	} else if strings.Contains(model, ":") {
		return fmt.Errorf("指向 %s 时模型名不能含冒号（那是 core 的命名空间）", channel)
	}
	return nil
}

// validateAutoFallback 校验 auto 的核心通道回退目标。
func validateAutoFallback(model string) error {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return nil
	}
	if !validRouteName(trimmed, 128, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._:- ") {
		return errors.New("auto 回退模型名不合法：只允许字母、数字、点、下划线、冒号、连字符和空格，长度 1-128")
	}
	if !strings.HasPrefix(trimmed, "cn:") && !strings.HasPrefix(trimmed, "global:") {
		return errors.New("auto 回退模型须带 cn: 或 global: 前缀（那是 core 下发的命名空间）")
	}
	return nil
}

func validRouteName(s string, max int, allowed string) bool {
	if len(s) == 0 || len(s) > max {
		return false
	}
	for _, r := range s {
		// 只认 allowed 里显式列出的字符。alias 的 allowed 是纯 ASCII，非 ASCII
		// 天然被拒；模型名的 allowed 额外含中点，因为 opencode 上游目录下发的
		// 真实 id 形如「OC · FreeModel」，不放进来的话管理员根本配不进别名表。
		if !strings.ContainsRune(allowed, r) {
			return false
		}
	}
	return true
}

// normalizeRoutes去掉别名与模型名两端的空白，让校验与落盘用的是同一份规范化值。
// 候选链上的每一跳按同样口径处理，否则页面上带尾空格的备用条目会校验失败。
func normalizeRoutes(models []routeEntry) []routeEntry {
	out := make([]routeEntry, 0, len(models))
	for _, entry := range models {
		entry.Alias = strings.TrimSpace(entry.Alias)
		entry.Model = strings.TrimSpace(entry.Model)
		entry.Strategy = strings.TrimSpace(entry.Strategy)
		if len(entry.Fallbacks) > 0 {
			fbs := make([]routeTarget, 0, len(entry.Fallbacks))
			for _, fb := range entry.Fallbacks {
				fb.Model = strings.TrimSpace(fb.Model)
				fbs = append(fbs, fb)
			}
			entry.Fallbacks = fbs
		}
		out = append(out, entry)
	}
	return out
}

// routeModelChars 是合法公共模型名的字符集，别名模型名与模型开关表共用。
// 含 / 是因为 autoclaw 上游下发的原生 id 就带它（openclaw/default、global/zai_auto），
// 别名与开关表必须原样收下，改名会让配置和 /v1/models 公布的公共名对不上。
const routeModelChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._:- ·/"

// maxModelSwitchEntries 限制单张开关表的条目数。core 约 30 个模型、旁路通道各
// 十来个，四通道合计远小于此上限，留足冗余同时防止失控写入撑满文件。
const maxModelSwitchEntries = 200

// validateModelSwitchList 校验一张开关表的模型名：数量有界、名字合法、去重。
func validateModelSwitchList(name string, list []string) error {
	if len(list) > maxModelSwitchEntries {
		return fmt.Errorf("%s 不得超过 %d 条", name, maxModelSwitchEntries)
	}
	seen := make(map[string]bool, len(list))
	for _, raw := range list {
		model := strings.TrimSpace(raw)
		if !validRouteName(model, 128, routeModelChars) {
			return fmt.Errorf("%s 含非法模型名：只允许字母、数字、点、下划线、冒号、连字符、空格、中点和斜杠，长度 1-128", name)
		}
		if seen[model] {
			return fmt.Errorf("%s 含重复模型名 %q", name, model)
		}
		seen[model] = true
	}
	return nil
}

// normalizeSwitchList 去掉两端空白、丢弃空项并去重，保持首次出现的顺序。
func normalizeSwitchList(list []string) []string {
	out := make([]string, 0, len(list))
	seen := make(map[string]bool, len(list))
	for _, raw := range list {
		model := strings.TrimSpace(raw)
		if model == "" || seen[model] {
			continue
		}
		seen[model] = true
		out = append(out, model)
	}
	return out
}

// switchLists 返回当前生效的两张开关表副本，调用方可安全遍历。
func (s *routeStore) switchLists() (disabled, enabled []string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string{}, s.file.DisabledModels...), append([]string{}, s.file.EnabledModels...)
}

// modelOverride 报告某个公共模型名是否被人工设过开关，以及被设成什么。
// 返回 (override, has) —— has=false 表示该模型不在任何表里，应按积分规则取默认态。
func (s *routeStore) modelOverride(model string) (enabled bool, has bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, m := range s.file.EnabledModels {
		if m == model {
			return true, true
		}
	}
	for _, m := range s.file.DisabledModels {
		if m == model {
			return false, true
		}
	}
	return false, false
}

// setModelSwitch 把某个公共模型名设为开启或关闭并落盘。开启时从禁用表移除、
// 加入启用表；关闭时反之。auto 虚拟模型不受开关约束（它不是真实上游模型）。
// 返回更新后的两张表副本，供端点回写状态。
func (s *routeStore) setModelSwitch(model string, enable bool) error {
	model = strings.TrimSpace(model)
	if !validRouteName(model, 128, routeModelChars) {
		return errors.New("模型名不合法：只允许字母、数字、点、下划线、冒号、连字符、空格、中点和斜杠，长度 1-128")
	}
	if model == autoModelName {
		return errors.New("auto 是虚拟模型，不参与积分开关")
	}
	current := s.snapshotFile()
	disabled := removeModel(current.DisabledModels, model)
	enabled := removeModel(current.EnabledModels, model)
	if enable {
		enabled = append(enabled, model)
	} else {
		disabled = append(disabled, model)
	}
	disabled = normalizeSwitchList(disabled)
	enabled = normalizeSwitchList(enabled)
	if err := validateModelSwitchList("禁用模型表", disabled); err != nil {
		return err
	}
	if err := validateModelSwitchList("启用模型表", enabled); err != nil {
		return err
	}
	next := routeFile{Version: routeFileVersion, Models: current.Models, AutoFallback: current.AutoFallback, DisabledModels: disabled, EnabledModels: enabled}
	return s.saveFile(next)
}

// setGroupSwitch 是「整组启停」的落盘实现：一个真实模型与所有指向它的别名
// 视为同一个同步组，整体开或整体关。用户口径是控制台模型列表与别名表的开关
// 必须永远一致、点哪一处都一样，所以这里一次性写两件事：模型开关表（决定
// modelGate 是否放行调用与 modelPublished 是否上榜公布）和指向它的每个别名
// 的 Enabled（决定别名调用是否报「已停用」）。分成两次写会出现
// 「模型开了但别名还关着」的中间态。
func (s *routeStore) setGroupSwitch(model string, enable bool) error {
	model = strings.TrimSpace(model)
	if !validRouteName(model, 128, routeModelChars) {
		return errors.New("模型名不合法：只允许字母、数字、点、下划线、冒号、连字符、空格、中点和斜杠，长度 1-128")
	}
	if model == autoModelName {
		return errors.New("auto 是虚拟模型，不参与积分开关")
	}
	current := s.snapshotFile()
	disabled := removeModel(current.DisabledModels, model)
	enabled := removeModel(current.EnabledModels, model)
	if enable {
		enabled = append(enabled, model)
	} else {
		disabled = append(disabled, model)
	}
	disabled = normalizeSwitchList(disabled)
	enabled = normalizeSwitchList(enabled)
	if err := validateModelSwitchList("禁用模型表", disabled); err != nil {
		return err
	}
	if err := validateModelSwitchList("启用模型表", enabled); err != nil {
		return err
	}
	models := make([]routeEntry, 0, len(current.Models))
	for _, entry := range current.Models {
		if entry.publicModel() == model {
			entry.Enabled = enable
		}
		models = append(models, entry)
	}
	next := routeFile{Version: routeFileVersion, Models: models, AutoFallback: current.AutoFallback, DisabledModels: disabled, EnabledModels: enabled}
	return s.saveFile(next)
}

// removeModel 返回去掉某个模型名后的新切片（不改原切片）。
func removeModel(list []string, model string) []string {
	out := make([]string, 0, len(list))
	for _, m := range list {
		if m != model {
			out = append(out, m)
		}
	}
	return out
}

// saveFile 原子落盘整份配置并替换内存快照，复用与 save() 相同的临时文件 +
// rename 语义，只是入参已是组装好的 routeFile。
func (s *routeStore) saveFile(next routeFile) error {
	if next.Models == nil {
		next.Models = []routeEntry{}
	}
	encoded, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return errors.New("路由配置序列化失败")
	}
	encoded = append(encoded, '\n')
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return errors.New("路由配置目录不可创建")
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".routes-*.json")
	if err != nil {
		return errors.New("路由配置临时文件不可创建")
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(encoded); err != nil {
		tmp.Close()
		return errors.New("路由配置写入失败")
	}
	if err := tmp.Close(); err != nil {
		return errors.New("路由配置写入失败")
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return errors.New("路由配置文件权限设置失败")
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return errors.New("路由配置替换失败")
	}
	s.mu.Lock()
	s.file = next
	s.mu.Unlock()
	return nil
}

// routeStore 持有当前生效的路由配置快照。管理端点写入后原子替换内存快照并
// 落盘，公共 /v1/* 每次解析都读这份快照——保存即生效，不需要重启容器。
type routeStore struct {
	mu   sync.RWMutex
	path string
	file routeFile
}

func newRouteStore(path string) *routeStore {
	s := &routeStore{path: path}
	s.file = routeFile{Version: routeFileVersion, Models: []routeEntry{}, AutoFallback: defaultAutoFallback}
	s.reload()
	return s
}

// reload 从磁盘重读配置。文件不存在是正常初始状态（还没有任何别名）；
// 其他读取或解析失败都退回上一次可用快照，只打日志，不让一次手滑的编辑
// 把正在服务的公共出口整个掀翻。
func (s *routeStore) reload() routeFile {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logRouteWarn("读取路由配置失败，沿用上一份配置：", err)
		}
		return s.snapshotFile()
	}
	var file routeFile
	if json.Unmarshal(raw, &file) != nil {
		logRouteWarn("路由配置不是合法 JSON，沿用上一份配置")
		return s.snapshotFile()
	}
	if file.Version != routeFileVersion {
		logRouteWarn("路由配置版本不匹配，沿用上一份配置")
		return s.snapshotFile()
	}
	if file.Models == nil {
		file.Models = []routeEntry{}
	}
	// 磁盘上的配置是页面写出来的，仍然按同一套规则复核一次：手工编辑过的
	// 文件也不允许把非法别名塞进公共出口。
	if err := validateRouteEntries(file.Models); err != nil {
		logRouteWarn("路由配置校验失败，沿用上一份配置：", err)
		return s.snapshotFile()
	}
	// 校验时按去空白后的值判断，内存里也必须落同一份规范化值：否则手工编辑过的
	// 文件里带尾空格的别名能通过校验，却在 lookup 时永远匹配不上请求。
	file.Models = normalizeRoutes(file.Models)
	if err := validateAutoFallback(file.AutoFallback); err != nil {
		logRouteWarn("auto 回退配置非法，沿用上一份配置：", err)
		return s.snapshotFile()
	}
	file.DisabledModels = normalizeSwitchList(file.DisabledModels)
	file.EnabledModels = normalizeSwitchList(file.EnabledModels)
	if err := validateModelSwitchList("禁用模型表", file.DisabledModels); err != nil {
		logRouteWarn("模型开关表非法，沿用上一份配置：", err)
		return s.snapshotFile()
	}
	if err := validateModelSwitchList("启用模型表", file.EnabledModels); err != nil {
		logRouteWarn("模型开关表非法，沿用上一份配置：", err)
		return s.snapshotFile()
	}
	file.AutoFallback = strings.TrimSpace(file.AutoFallback)
	if file.AutoFallback == "" {
		file.AutoFallback = defaultAutoFallback
	}
	s.mu.Lock()
	s.file = file
	s.mu.Unlock()
	return file
}

func (s *routeStore) snapshotFile() routeFile {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.file
}

// save 校验后原子落盘。写临时文件再 rename，避免进程在写入中途退出时留下
// 一个被 reload 读成空配置的半截文件。
func (s *routeStore) save(models []routeEntry, autoFallback string) error {
	normalized := normalizeRoutes(models)
	if err := validateRouteEntries(normalized); err != nil {
		return err
	}
	if err := validateAutoFallback(autoFallback); err != nil {
		return err
	}
	fallback := strings.TrimSpace(autoFallback)
	if fallback == "" {
		fallback = defaultAutoFallback
	}
	// 开关表不属于别名编辑的范围，保存别名时必须原样带上，否则一次别名保存
	// 会把模型开关的全部人工覆盖静默清空。
	current := s.snapshotFile()
	next := routeFile{Version: routeFileVersion, Models: normalized, AutoFallback: fallback, DisabledModels: current.DisabledModels, EnabledModels: current.EnabledModels}
	encoded, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return errors.New("路由配置序列化失败")
	}
	encoded = append(encoded, '\n')
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return errors.New("路由配置目录不可创建")
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".routes-*.json")
	if err != nil {
		return errors.New("路由配置临时文件不可创建")
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(encoded); err != nil {
		tmp.Close()
		return errors.New("路由配置写入失败")
	}
	if err := tmp.Close(); err != nil {
		return errors.New("路由配置写入失败")
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return errors.New("路由配置文件权限设置失败")
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return errors.New("路由配置替换失败")
	}
	s.mu.Lock()
	s.file = next
	s.mu.Unlock()
	return nil
}

// snapshot 返回当前生效配置的副本，调用方可以安全遍历。
func (s *routeStore) snapshot() []routeEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]routeEntry, len(s.file.Models))
	copy(out, s.file.Models)
	return out
}

// autoFallback 返回 auto 的核心通道回退目标，缺省 defaultAutoFallback。
func (s *routeStore) autoFallback() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.file.AutoFallback == "" {
		return defaultAutoFallback
	}
	return s.file.AutoFallback
}

// lookup 返回别名对应的条目。别名未命中或该条目被停用时返回 false，
// 请求继续走既有前缀分流，行为与没有这个功能时完全一致。
func (s *routeStore) lookup(alias string) (routeEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, entry := range s.file.Models {
		if entry.Enabled && entry.Alias == alias {
			return entry, true
		}
	}
	return routeEntry{}, false
}

// lookupAny 返回别名对应的条目，不问启用与否。lookup 只认启用条目，无法区分
// 「没这个别名」和「别名配置了但停用」——后者调用方要明确报「已停用」，不能让
// 请求带着别名原名静默落到 core，造出一个莫名其妙的 404 模型不存在。
func (s *routeStore) lookupAny(alias string) (routeEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, entry := range s.file.Models {
		if entry.Alias == alias {
			return entry, true
		}
	}
	return routeEntry{}, false
}

// probeCandidate 是 auto 虚拟模型的一条候选链路。band 越小越优先：
// 0=可用，1=仅对话（探测时只回了文本，没产生工具动作），3=不可用。
type probeCandidate struct {
	Channel    routeChannel
	Model      string
	Band       int
	DurationMs int
}

// probeTTL 是探测结论的缓存时长。auto 只需要「此刻该选谁」，30 秒内的抖动
// 没有意义；缓存同时避免每次对话都去打一次上游 /health。
const probeTTL = 30 * time.Second

// probeCache 缓存按通道分组的候选链路。haveData=false 表示本轮没有取到任何
// 探测结论，此时 auto 直接回退到核心通道，而不是随便挑一个。
type probeCache struct {
	mu        sync.Mutex
	fetchedAt time.Time
	byChannel map[routeChannel][]probeCandidate
	haveData  bool
}

func (c *probeCache) get(ctx context.Context, h *server) (map[routeChannel][]probeCandidate, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.fetchedAt) < probeTTL {
		return c.byChannel, c.haveData
	}
	byChannel, haveData := h.collectProbeCandidates(ctx)
	c.fetchedAt = time.Now()
	c.byChannel = byChannel
	c.haveData = haveData
	return byChannel, haveData
}

// invalidate 让缓存立即过期。手工探测跑完后调用，新结论马上参与 auto 选路，
// 不必等满一个 TTL。
func (c *probeCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fetchedAt = time.Time{}
}

// collectProbeCandidates 汇总各通道的探测结论：opencode 用 OW Bridge 自带的
// /health.modelResults，core/glm/qoder 用管理员手工触发的测速探测结论。
// 没有结论的模型保持「未探测」，不参与 auto 竞争，也不该被当成可用的回退候选。
func (h *server) collectProbeCandidates(ctx context.Context) (map[routeChannel][]probeCandidate, bool) {
	out := map[routeChannel][]probeCandidate{}
	if h.cfg.OpenCodeURL != nil {
		if health := h.opencodeHealth(ctx); health != nil {
			if raw, ok := health["modelResults"].(map[string]any); ok {
				appendProbeEntries(out, channelOpenCode, raw)
			}
		}
	}
	for _, channel := range []routeChannel{channelCore, channelGLM, channelQoder, channelAutoClaw, channelTrae, channelRaccoon} {
		results := h.probe.channelResults(channel)
		if len(results) == 0 {
			continue
		}
		raw := make(map[string]any, len(results))
		for name, entry := range results {
			raw[name] = entry
		}
		appendProbeEntries(out, channel, raw)
	}
	if len(out) == 0 {
		return out, false
	}
	for channel := range out {
		sortCandidates(out[channel])
	}
	return out, true
}

// appendProbeEntries 把一份 modelResults（键为公共模型名）归类进 out。
// band：0=可用，1=仅对话，3=不可用；模型名去掉通道前缀后存入候选，
// 出口再经 publicModel 拼回公共形态。
func appendProbeEntries(out map[routeChannel][]probeCandidate, channel routeChannel, raw map[string]any) {
	for name, value := range raw {
		entry, ok := value.(map[string]any)
		if !ok {
			continue
		}
		success, _ := entry["ok"].(bool)
		var band int
		switch {
		case !success:
			band = 3
		case entry["chatOnly"] == true:
			band = 1
		case entry["category"] == "available":
			band = 0
		default:
			// 探测成功但既没有 available 归类也没有 chatOnly 标记：按仅对话处理，
			// 比未探测更可信，又不会排到确认可用的模型前面。
			band = 1
		}
		native := name
		if prefix := channel.prefix(); prefix != "" {
			native = strings.TrimPrefix(name, prefix)
		}
		candidate := probeCandidate{Channel: channel, Model: native, Band: band}
		if ms, ok := entry["durationMs"].(float64); ok && ms >= 0 {
			candidate.DurationMs = int(ms)
		}
		out[channel] = append(out[channel], candidate)
	}
}

// sortCandidates 按「可用 → 仅对话 → 不可用」分档，档内按探测耗时升序，
// 耗时缺失排档尾，模型名兜底保证顺序稳定。
func sortCandidates(list []probeCandidate) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.Band != b.Band {
			return a.Band < b.Band
		}
		ad, bd := a.DurationMs, b.DurationMs
		if ad <= 0 {
			ad = int(^uint(0) >> 1)
		}
		if bd <= 0 {
			bd = int(^uint(0) >> 1)
		}
		if ad != bd {
			return ad < bd
		}
		return a.Model < b.Model
	})
}

// routeDecision 是一次别名解析的结果。Reason 是给人和给排障看的短说明，
// 会原样写进 X-Prism-Reason 响应头。
type routeDecision struct {
	RoutedModel string       // 改写后写进请求体的模型名（公共形态）
	Channel     routeChannel // 实际承载请求的通道
	Alias       string       // 客户端原本请求的模型名
	Reason      string
	Fallback    bool // auto 没有可选链路、已回退核心通道
	// TargetIndex 是命中的候选链下标：0=主目标，>0=第几条备用，-1=不适用（如 auto）。
	TargetIndex int
}

// resolveAlias 把客户端请求的模型名解析成一条具体链路。返回 false 表示这个
// model 不是别名也不是 auto，调用方应继续走既有前缀分流。
//
// 解析规则：
//  1. 别名 + priority（默认）→ 候选链第一跳（主目标），与引入候选链之前完全一致。
//  2. 别名 + health → 在该别名自己的候选链内用探测结论挑 band 最优的一跳；
//     链上没有任何结论时按链序取主目标。
//  3. auto → 在全部通道的探测结论里挑最优；完全没有结论时回退核心通道。
//
// 路由功能未启用（未配置可写配置文件）时一律返回 false，出口与现状完全一致。
func (h *server) resolveAlias(ctx context.Context, model string) (routeDecision, bool) {
	if h.routes == nil || model == "" {
		return routeDecision{}, false
	}
	if model == autoModelName {
		return h.resolveAuto(ctx), true
	}
	entry, ok := h.routes.lookup(model)
	if !ok {
		return routeDecision{}, false
	}
	return h.resolveEntry(ctx, entry), true
}

// resolveEntry 在一条别名自己的候选链上选一跳。
//
// priority 恒取链首，不看探测结论——这是「我就要这条链路」的显式表达，不能被
// 探测抖动改变。health 才引入探测结论，且只在链内比较，不会跳到链外。
func (h *server) resolveEntry(ctx context.Context, entry routeEntry) routeDecision {
	chain := entry.targets()
	primary := chain[0]
	if entry.strategy() != strategyHealth {
		return routeDecision{
			RoutedModel: primary.publicModel(),
			Channel:     primary.Channel,
			Alias:       entry.Alias,
			Reason:      "别名命中配置",
			TargetIndex: 0,
		}
	}
	index, band, found := h.pickChainTarget(ctx, chain)
	if !found {
		return routeDecision{
			RoutedModel: primary.publicModel(),
			Channel:     primary.Channel,
			Alias:       entry.Alias,
			Reason:      "别名无可用探测结论，按链序用主目标",
			TargetIndex: 0,
		}
	}
	reason := fmt.Sprintf("别名选中第 %d 跳（%s）", index+1, probeBandLabel(band))
	if index > 0 {
		reason = fmt.Sprintf("别名主目标不可用，切第 %d 跳备用（%s）", index+1, probeBandLabel(band))
	}
	return routeDecision{
		RoutedModel: chain[index].publicModel(),
		Channel:     chain[index].Channel,
		Alias:       entry.Alias,
		Reason:      reason,
		TargetIndex: index,
	}
}

// pickChainTarget 在候选链里挑一跳：优先 band 最小的（可用 → 仅对话），band 相同时
// 取链序靠前的，因此「主目标可用就用主目标」是天然结果，只有它确实不可用才轮到备用。
//
// band=3（探测失败）不作为候选——把已知不可用的链路放进竞争等于制造随机故障。
// 链上没有任何一跳有探测结论时返回 found=false，由调用方按链序回退，与 resolveAuto
// 的克制口径一致。
func (h *server) pickChainTarget(ctx context.Context, chain []routeTarget) (int, int, bool) {
	candidates, haveData := h.probes.get(ctx, h)
	if !haveData {
		return 0, 0, false
	}
	bestIndex, bestBand := -1, 0
	for i, target := range chain {
		band, ok := probeBandFor(candidates, target)
		if !ok || band > 1 {
			continue
		}
		if bestIndex < 0 || band < bestBand {
			bestIndex, bestBand = i, band
		}
	}
	if bestIndex < 0 {
		return 0, 0, false
	}
	return bestIndex, bestBand, true
}

// probeBandFor 在探测结论里查一跳的档位。比对用通道原生名，与 appendProbeEntries
// 存入候选时的形态保持一致。
func probeBandFor(candidates map[routeChannel][]probeCandidate, target routeTarget) (int, bool) {
	native := target.nativeModel()
	for _, candidate := range candidates[target.Channel] {
		if candidate.Model == native {
			return candidate.Band, true
		}
	}
	return 0, false
}

func (h *server) resolveAuto(ctx context.Context) routeDecision {
	fallback := h.routes.autoFallback()
	candidates, haveData := h.probes.get(ctx, h)
	// 只收「可用」和「仅对话」两档：把探测失败的模型放进回退链等于把 auto
	// 变成随机故障源；未探测的模型也不收——没有任何证据说它能跑。
	var pool []probeCandidate
	if haveData {
		for _, list := range candidates {
			for _, candidate := range list {
				if candidate.Band == 0 || candidate.Band == 1 {
					pool = append(pool, candidate)
				}
			}
		}
	}
	if len(pool) == 0 {
		reason := "auto 无探测结论，回退核心通道"
		if haveData {
			reason = "auto 无可用探测结论，回退核心通道"
		}
		return routeDecision{
			RoutedModel: fallback,
			Channel:     channelCore,
			Alias:       autoModelName,
			Reason:      reason,
			Fallback:    true,
			TargetIndex: -1,
		}
	}
	sortCandidates(pool)
	best := pool[0]
	return routeDecision{
		RoutedModel: best.Channel.publicModel(best.Model),
		Channel:     best.Channel,
		Alias:       autoModelName,
		Reason:      fmt.Sprintf("auto 选中 %s（%s）", best.Model, probeBandLabel(best.Band)),
		TargetIndex: -1,
	}
}

func probeBandLabel(band int) string {
	switch band {
	case 0:
		return "可用"
	case 1:
		return "仅对话"
	}
	return "不可用"
}

// rewriteModelField 只替换请求体里的 model 字段，其余字段原样保留。用
// json.RawMessage 承接未知字段，避免把 messages 之类的大数组反序列化再序列化
// 带来的无谓内存与顺序抖动。
func rewriteModelField(body []byte, model string) ([]byte, bool) {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil {
		return body, false
	}
	if _, ok := envelope["model"]; !ok {
		return body, false
	}
	encoded, err := json.Marshal(model)
	if err != nil {
		return body, false
	}
	envelope["model"] = encoded
	out, err := json.Marshal(envelope)
	if err != nil {
		return body, false
	}
	return out, true
}

// logRouteWarn 统一记录路由配置相关的降级事件。降级不影响调用，只留痕。
func logRouteWarn(msg string, err ...error) {
	if len(err) > 0 && err[0] != nil {
		log.Print("WARN: [route] ", msg, err[0])
		return
	}
	log.Print("WARN: [route] ", msg)
}

// prismHeaderWriter 在响应首次写出前打上路由决策头。必须在 WriteHeader 时机
// 写入而不是提前 Set：ReverseProxy 会把上游响应头拷进同一个 Header 再写出，
// 提前 Set 的同名值会变成两条重复头。请求方向的 x-prism-* 会被
// stripPrivateHeaders 剥掉，客户端无法伪造；响应方向由这里单点写入。
type prismHeaderWriter struct {
	http.ResponseWriter
	decision routeDecision
}

func withPrismHeaders(w http.ResponseWriter, decision routeDecision) http.ResponseWriter {
	return &prismHeaderWriter{ResponseWriter: w, decision: decision}
}

func (p *prismHeaderWriter) WriteHeader(code int) {
	header := p.Header()
	header.Set("X-Prism-Routed-Model", p.decision.RoutedModel)
	header.Set("X-Prism-Channel", string(p.decision.Channel))
	header.Set("X-Prism-Reason", p.decision.Reason)
	// TargetIndex 只在别名候选链上有意义（0=主目标，>0=第几条备用）；auto 用 -1
	// 表示不适用，此时不下发该头，避免调用方把 auto 也当成一条链去解释。
	if p.decision.TargetIndex >= 0 {
		header.Set("X-Prism-Target-Index", strconv.Itoa(p.decision.TargetIndex))
	}
	if p.decision.Fallback {
		header.Set("X-Prism-Fallback", "core")
	}
	p.ResponseWriter.WriteHeader(code)
}

// Flush 与 Unwrap 一起保证流式响应不被本包装截断。嵌入的是接口，接口方法
// 不会被提升，必须显式实现：少了 Flush，下游 usageCaptureWriter 的类型断言
// 会失败，ReverseProxy 的 SSE 冲刷也会退化成一次性返回。
func (p *prismHeaderWriter) Flush() {
	_ = http.NewResponseController(p.ResponseWriter).Flush()
}

// Unwrap 让 http.ResponseController 能穿过本包装找到底层 writer。
func (p *prismHeaderWriter) Unwrap() http.ResponseWriter { return p.ResponseWriter }

// decodeRouteBody 是路由配置端点专用的请求体解码：语义与 decodeAdmin 一致
// （禁未知字段、必须解码到 EOF），只是把体积上限换成本文件需要的量级。
func decodeRouteBody(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, routeBodyLimit)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		adminError(w, 400, "请求内容无效")
		return false
	}
	return true
}

// adminRouteSave 校验并落盘别名表。保存即生效：下一次请求就用新配置，无需重启。
func (h *server) adminRouteSave(w http.ResponseWriter, r *http.Request) {
	if h.routes == nil {
		adminError(w, 409, "路由别名功能未启用：未配置 console 可写路由目录")
		return
	}
	var body struct {
		Models       []routeEntry `json:"models"`
		AutoFallback string       `json:"auto_fallback"`
	}
	if !decodeRouteBody(w, r, &body) {
		return
	}
	if err := h.routes.save(body.Models, body.AutoFallback); err != nil {
		adminError(w, 400, err.Error())
		return
	}
	h.writeRouteState(w, r)
}

// adminRouteSwitch 是「整组启停」的唯一写入口：模型列表与别名表的开关按钮都打
// 这个端点，由服务端一次把模型开关表与所有指向它的别名写成同一状态。两边各写
// 一半（别名走 route/save、模型走 model-switch）会留下中间态，也容易互相覆盖。
func (h *server) adminRouteSwitch(w http.ResponseWriter, r *http.Request) {
	if h.routes == nil {
		adminError(w, 409, "路由别名功能未启用：未配置 console 可写路由目录")
		return
	}
	var body struct {
		Model   string `json:"model"`
		Enabled bool   `json:"enabled"`
	}
	if !decodeRouteBody(w, r, &body) {
		return
	}
	if err := h.routes.setGroupSwitch(body.Model, body.Enabled); err != nil {
		adminError(w, 400, err.Error())
		return
	}
	h.writeRouteState(w, r)
}

// adminRouteTest 对单个别名做一次真实最小对话并把结论直接回给页面。复用
// probeOne 的测速口径（流式 + 校验 content 非空 + 真实 usage 算速度），只打
// 这一条别名的目标模型。别名停用、或目标模型被显式停用时直接返回原因、不发出
// 上游请求——这两种情况测试必然失败，报清楚比报超时有用。
func (h *server) adminRouteTest(w http.ResponseWriter, r *http.Request) {
	if h.routes == nil {
		adminError(w, 409, "路由别名功能未启用：未配置 console 可写路由目录")
		return
	}
	var body struct {
		Alias string `json:"alias"`
	}
	if !decodeRouteBody(w, r, &body) {
		return
	}
	alias := strings.TrimSpace(body.Alias)
	var found *routeEntry
	for _, entry := range h.routes.snapshot() {
		if entry.Alias == alias {
			copied := entry
			found = &copied
			break
		}
	}
	if found == nil {
		adminError(w, 404, fmt.Sprintf("别名 %q 不存在", alias))
		return
	}
	if !found.Enabled {
		writeJSON(w, 200, probeFailResult("alias_disabled", fmt.Sprintf("别名 %s 已停用，先在别名表启用再测", alias), 0))
		return
	}
	public := found.publicModel()
	if !h.modelGate(public) {
		writeJSON(w, 200, probeFailResult("model_disabled", fmt.Sprintf("目标模型 %s 已停用，先在模型列表启用再测", public), 0))
		return
	}
	result := h.probeOne(found.Channel, public)
	// 结论回写探测缓存：模型列表与别名表的状态列随即看到同一份结论，不必等下一轮全量探测。
	h.probe.record(found.Channel, public, result)
	h.probes.invalidate()
	writeJSON(w, 200, result)
}

// aliasProbe 取别名主目标的探测结论。结论按「通道 + 公共模型名」存在探测器里，
// 别名页据此渲染状态与速度列，与模型列表读的是同一份数据，两处不会打架。
// 没有结论返回 nil——「没测过」和「测不通」是两件事，不能混。
func (h *server) aliasProbe(entry routeEntry) map[string]any {
	if h.probe == nil {
		return nil
	}
	results := h.probe.channelResults(entry.Channel)
	if len(results) == 0 {
		return nil
	}
	return results[entry.publicModel()]
}

func (h *server) writeRouteState(w http.ResponseWriter, r *http.Request) {
	aliases := []map[string]any{}
	channels := []routeChannelCatalog{}
	preview := map[string]any{}
	autoFallback := ""
	if h.routes != nil {
		for _, entry := range h.routes.snapshot() {
			chain := make([]map[string]any, 0, 1+len(entry.Fallbacks))
			for _, target := range entry.targets() {
				chain = append(chain, routeTargetState(target))
			}
			aliases = append(aliases, map[string]any{
				"alias":        entry.Alias,
				"channel":      string(entry.Channel),
				"model":        entry.Model,
				"public_model": entry.publicModel(),
				"fallbacks":    routeTargetStates(entry.Fallbacks),
				"chain":        chain,
				"strategy":     entry.strategy(),
				"enabled":      entry.Enabled,
				"note":         entry.Note,
				// probe 是主目标的探测结论（可能为 null）。页面拿它画状态与速度，
				// 与模型列表同源，避免两处各算一套、显示不一致。
				"probe": h.aliasProbe(entry),
			})
		}
		channels = h.routeCatalog(r.Context())
		decision := h.resolveAuto(r.Context())
		autoFallback = h.routes.autoFallback()
		preview = map[string]any{
			"routed_model": decision.RoutedModel,
			"channel":      string(decision.Channel),
			"reason":       decision.Reason,
			"fallback":     decision.Fallback,
			"target_index": decision.TargetIndex,
		}
	}
	writeJSON(w, 200, map[string]any{
		"enabled":               h.routes != nil,
		"auto_model":            autoModelName,
		"auto_fallback":         autoFallback,
		"default_auto_fallback": defaultAutoFallback,
		"auto_preview":          preview,
		"aliases":               aliases,
		"channels":              channels,
		// model_costs 只覆盖 core 通道（公共模型名 → credits 原始串）：glm- / qoder-
		// 旁路上游不公布倍率，opencode / autoclaw / trae 是免费通道，前端各自判定，
		// 缺失不等于零价。
		"model_costs": h.consoleCreditRules(r.Context()),
		// overrides 与 /admin/model-switch 同源。整组开关（route/switch）复用本响应，
		// 带上它前端一次请求就能同时刷新别名表与模型列表，省掉一次往返。
		"overrides": h.modelSwitchState()["overrides"],
	})
}

// routeTargetState 把一跳序列化成页面认识的形态：原生模型名与公共模型名都给，
// 页面据此展示，不必自己拼前缀（前缀规则只应该有一处实现）。
func routeTargetState(target routeTarget) map[string]any {
	return map[string]any{
		"channel":      string(target.Channel),
		"model":        target.Model,
		"public_model": target.publicModel(),
	}
}

// routeTargetStates 序列化备用链路；无备用时返回空切片而不是 null，前端可直接遍历。
func routeTargetStates(fallbacks []routeTarget) []map[string]any {
	out := make([]map[string]any, 0, len(fallbacks))
	for _, target := range fallbacks {
		out = append(out, routeTargetState(target))
	}
	return out
}

// routeChannelCatalog 是单个通道的模型目录快照。
type routeChannelCatalog struct {
	Channel   routeChannel `json:"channel"`
	Enabled   bool         `json:"enabled"`
	Reachable bool         `json:"reachable"`
	Models    []string     `json:"models"`
}

// channelEnabled 报告通道是否已配置上游。core 恒可用。
func (h *server) channelEnabled(c routeChannel) bool {
	switch c {
	case channelCore:
		return true
	case channelGLM:
		return h.cfg.ZCodeURL != nil
	case channelQoder:
		return h.cfg.QoderURL != nil
	case channelOpenCode:
		return h.cfg.OpenCodeURL != nil
	case channelAutoClaw:
		return h.cfg.AutoClawURL != nil
	case channelTrae:
		return h.cfg.TraeURL != nil
	case channelRaccoon:
		return h.cfg.RaccoonURL != nil
	}
	return false
}

// channelUpstream 返回通道的上游地址与转发密钥。core 用部署 API Key 走
// 公共鉴权，其余通道用各自的容器密钥。
func (h *server) channelUpstream(c routeChannel) (*url.URL, string) {
	switch c {
	case channelGLM:
		return h.cfg.ZCodeURL, h.cfg.ZCodeKey
	case channelQoder:
		return h.cfg.QoderURL, h.cfg.QoderKey
	case channelOpenCode:
		return h.cfg.OpenCodeURL, h.cfg.OpenCodeKey
	case channelAutoClaw:
		return h.cfg.AutoClawURL, h.cfg.AutoClawKey
	case channelTrae:
		return h.cfg.TraeURL, h.cfg.TraeKey
	case channelRaccoon:
		return h.cfg.RaccoonURL, h.cfg.RaccoonKey
	}
	return h.cfg.CoreURL, h.cfg.APIKey
}

// routeCatalog 并行抓取各已启用通道的 /v1/models，返回对外形态的模型名列表。
// 抓取失败只把该通道标成不可达，不牵连其他通道。
func (h *server) routeCatalog(ctx context.Context) []routeChannelCatalog {
	out := make([]routeChannelCatalog, len(routeChannelOrder))
	var wg sync.WaitGroup
	for i, channel := range routeChannelOrder {
		out[i] = routeChannelCatalog{Channel: channel, Enabled: h.channelEnabled(channel), Models: []string{}}
		if !out[i].Enabled {
			continue
		}
		wg.Add(1)
		go func(index int, c routeChannel) {
			defer wg.Done()
			ids, ok := h.fetchChannelModels(ctx, c)
			out[index].Models = ids
			out[index].Reachable = ok
		}(i, channel)
	}
	wg.Wait()
	return out
}

// fetchChannelModels 抓一个通道的模型目录并统一加上公共前缀。core 的 ID 本身
// 已带命名空间、glm 上游直接下发带前缀的名字，这两类保持原样。
func (h *server) fetchChannelModels(ctx context.Context, c routeChannel) ([]string, bool) {
	base, key := h.channelUpstream(c)
	if base == nil {
		return nil, false
	}
	target := *base
	target.Path = "/v1/models"
	req, err := http.NewRequestWithContext(ctx, "GET", target.String(), nil)
	if err != nil {
		return nil, false
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	timeout, cancel := context.WithTimeout(req.Context(), 3*time.Second)
	defer cancel()
	response := h.fetchUpstream(req.WithContext(timeout))
	list, ok := parseModelList(response)
	if !ok {
		return nil, false
	}
	ids := make([]string, 0, len(list))
	for _, item := range list {
		var entry struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(item, &entry) != nil || entry.ID == "" {
			continue
		}
		if prefix := c.prefix(); prefix != "" && !strings.HasPrefix(entry.ID, prefix) {
			entry.ID = prefix + entry.ID
		}
		ids = append(ids, entry.ID)
	}
	return ids, true
}
