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
	// channelTraeAI 是同一 sidecar 镜像的国际版实例（traeai-* 上游，trae.ai 账号体系）。
	// 与国内版是两条独立通道：各自账号池、各自凭据文件目录、各自权益口径。
	channelTraeAI  routeChannel = "traeai"
	channelRaccoon routeChannel = "raccoon"
	channelFreellm routeChannel = "freellm"
)

// routeChannelOrder 是页面展示与目录抓取的固定通道顺序：core 在前，
// 让「回退核心通道」的默认目标在界面上排在最前面。两条 Trae 区域相邻，
// 国内版在前（它是既有部署的默认形态）。
var routeChannelOrder = []routeChannel{channelCore, channelGLM, channelQoder, channelOpenCode, channelAutoClaw, channelTrae, channelTraeAI, channelRaccoon, channelFreellm}

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
	case channelTraeAI:
		return traeAIModelPrefix
	case channelRaccoon:
		return raccoonModelPrefix
	case channelFreellm:
		return freellmModelPrefix
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
	case channelTraeAI:
		return "traeai"
	case channelRaccoon:
		return "raccoon"
	case channelFreellm:
		return "freellm"
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
var reservedAliasPrefixes = []string{zcodeModelPrefix, qoderModelPrefix, opencodeModelPrefix, autoclawModelPrefix, traeModelPrefix, traeAIModelPrefix, raccoonModelPrefix, freellmModelPrefix, "cn:", "global:"}

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
	case channelCore, channelGLM, channelQoder, channelOpenCode, channelAutoClaw, channelTrae, channelTraeAI, channelRaccoon, channelFreellm:
	default:
		return errors.New("通道必须是 core、glm、qoder、opencode、autoclaw、trae、raccoon 或 freellm")
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
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
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
	return s.saveFileLocked(next)
}

// setGroupSwitch 是「整组启停」的落盘实现：一个真实模型与所有指向它的别名
// 视为同一个同步组，整体开或整体关。用户口径是控制台模型列表与别名表的开关
// 必须永远一致、点哪一处都一样，所以这里一次性写两件事：模型开关表（决定
// modelGate 是否放行调用与 modelPublished 是否上榜公布）和指向它的每个别名
// 的 Enabled（决定别名调用是否报「已停用」）。分成两次写会出现
// 「模型开了但别名还关着」的中间态。
func (s *routeStore) setGroupSwitch(model string, enable bool) error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
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
		original := entry
		if s.circuits != nil {
			original = s.circuits.originalEntry(entry)
		}
		if original.publicModel() == model {
			entry.Enabled = enable
		}
		models = append(models, entry)
	}
	next := routeFile{Version: routeFileVersion, Models: models, AutoFallback: current.AutoFallback, DisabledModels: disabled, EnabledModels: enabled}
	return s.saveFileLocked(next)
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
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	return s.saveFileLocked(next)
}

func (s *routeStore) saveFileLocked(next routeFile) error {
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
	mu       sync.RWMutex
	circuits *circuitBreaker
	saveMu   sync.Mutex // 整个读-改-写事务串行，包括熔断与 enabled_models。
	path     string
	file     routeFile
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
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
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
	validationModels := append([]routeEntry(nil), file.Models...)
	if s.circuits != nil {
		for i, e := range validationModels {
			validationModels[i] = s.circuits.originalEntry(e)
		}
	}
	if err := validateRouteEntries(validationModels); err != nil {
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
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
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
	return s.saveFileLocked(next)
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
	for _, channel := range []routeChannel{channelCore, channelGLM, channelQoder, channelAutoClaw, channelTrae, channelTraeAI, channelRaccoon, channelFreellm} {
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
	sort.SliceStable(list, func(i, j int) bool { return candidateLess(list[i], list[j]) })
}

// candidateLess 是候选链路的稳定排序口径：先比探测档位，再比探测耗时（缺失排档尾），
// 最后用模型名兜底。auto 的选路在档位相同时还要先比错误率，因此把这一份比较单独
// 抽出来复用，避免两处各写一套、日后漂移。
func candidateLess(a, b probeCandidate) bool {
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
	decision, _, _, ok := h.resolveAliasChain(ctx, model)
	return decision, ok
}

// resolveAliasChain 是 resolveAlias 的带链版本：别名命中时额外返回完整候选链
//（已选跳在链首、其余按原链序跟随）与各跳在原链中的下标，供请求内重试逐跳尝试。
// auto 与未命中场景没有候选链（链为 nil），调用方按单跳处理。
func (h *server) resolveAliasChain(ctx context.Context, model string) (routeDecision, []routeTarget, []int, bool) {
	if h.routes == nil || model == "" {
		return routeDecision{}, nil, nil, false
	}
	if model == autoModelName {
		return h.resolveAuto(ctx), nil, nil, true
	}
	entry, ok := h.routes.lookup(model)
	if !ok {
		return routeDecision{}, nil, nil, false
	}
	decision, chain, indexes := h.resolveEntryChain(ctx, entry)
	return decision, chain, indexes, true
}

// resolveEntry 在一条别名自己的候选链上选一跳。
//
// priority 恒取链首，不看探测结论——这是「我就要这条链路」的显式表达，不能被
// 探测抖动改变。health 才引入探测结论，且只在链内比较，不会跳到链外。
func (h *server) resolveEntry(ctx context.Context, entry routeEntry) routeDecision {
	decision, _, _ := h.resolveEntryChain(ctx, entry)
	return decision
}

// resolveEntryChain 在 resolveEntry 的结论之上返回完整候选链：已选跳在链首，其余
// 按原链序跟随，indexes 记录各跳在原链中的下标（0=主目标）。熔断跳已被过滤；
// 候选链全部熔断时返回 __circuit_open__ 决策与空链。
func (h *server) resolveEntryChain(ctx context.Context, entry routeEntry) (routeDecision, []routeTarget, []int) {
	if h.circuits != nil && !circuitProtected(entry) {
		entry = h.circuits.originalEntry(entry)
	}
	chain := entry.targets()
	indexes := make([]int, len(chain))
	for i := range indexes {
		indexes[i] = i
	}
	if h.circuits != nil && !circuitProtected(entry) {
		filtered := make([]routeTarget, 0, len(chain))
		filteredIndexes := make([]int, 0, len(chain))
		for i, target := range chain {
			if !h.circuits.blocked(target) {
				filtered = append(filtered, target)
				filteredIndexes = append(filteredIndexes, indexes[i])
			}
		}
		chain = filtered
		indexes = filteredIndexes
		if len(chain) == 0 {
			return routeDecision{Alias: entry.Alias, Channel: channelCore, RoutedModel: "__circuit_open__", Reason: "候选链全部熔断", TargetIndex: -1}, nil, nil
		}
	}
	primary := chain[0]
	if entry.strategy() != strategyHealth {
		return routeDecision{
			RoutedModel: primary.publicModel(),
			Channel:     primary.Channel,
			Alias:       entry.Alias,
			Reason:      circuitRouteReason("别名命中配置", indexes[0]),
			TargetIndex: indexes[0],
		}, chain, indexes
	}
	pick, found := h.pickChainTarget(ctx, chain)
	if !found {
		return routeDecision{
			RoutedModel: primary.publicModel(),
			Channel:     primary.Channel,
			Alias:       entry.Alias,
			Reason:      "别名无可用探测结论，按链序用主目标",
			TargetIndex: indexes[0],
		}, chain, indexes
	}
	index, band := pick.index, pick.band
	reason := fmt.Sprintf("别名选中第 %d 跳（%s）", indexes[index]+1, probeBandLabel(band))
	switch {
	case pick.factorDriven && indexes[index] > 0:
		// 结论被错误率因子改变（不是探测判死的），措辞不能沿用「不可用」。
		reason = fmt.Sprintf("别名按错误率因子，切第 %d 跳（%s）", indexes[index]+1, probeBandLabel(band))
	case indexes[index] > 0:
		reason = fmt.Sprintf("别名主目标不可用，切第 %d 跳备用（%s）", indexes[index]+1, probeBandLabel(band))
	}
	// F6 错误率反哺：因子影响选路时把依据写进 Reason（如「错误率 67%（n=9），降档」）。
	if pick.note != "" {
		reason = fmt.Sprintf("%s · %s", reason, pick.note)
	}
	// 已选跳提到链首，其余按原链序跟随，重试从已选跳开始逐跳向后。
	ordered := make([]routeTarget, 0, len(chain))
	orderedIndexes := make([]int, 0, len(chain))
	ordered = append(ordered, chain[index])
	orderedIndexes = append(orderedIndexes, indexes[index])
	for i := range chain {
		if i != index {
			ordered = append(ordered, chain[i])
			orderedIndexes = append(orderedIndexes, indexes[i])
		}
	}
	return routeDecision{
		RoutedModel: chain[index].publicModel(),
		Channel:     chain[index].Channel,
		Alias:       entry.Alias,
		Reason:      reason,
		TargetIndex: indexes[index],
	}, ordered, orderedIndexes
}

// —— F6 错误率反哺选路（PRD §2-F6）————————————————
//
// 数据源是 F1 错误账本的 15min 滚动窗口（errors.go 的 outcomeWindow），按
// (channel, 原生模型名) 聚合。这里只读取窗口，不重复实现聚合口径。
//
// 防呆铁律：
//   - 窗口调用样本 <5 不参与选路因子（样本不足不降权，避免单次失败摘链）。
//   - 无错误数据 = 无因子，行为与现状完全一致。
//   - 样本 ≥5 且错误率 >50% 的链路：band 视为 +1 参与比较（可用→仅对话降档；
//     band>1 本就排除）。
//   - band 相同时错误率低者优先（仅在两跳都有有效因子时才比较）。
//
// 纪律不变：3-strong 组未经用户同意不得自动启用（本文件不触碰梯队纪律）；
// priority 策略恒取链首，不看因子。
const (
	// routeErrorWindow 是选路因子看的滚动窗口时长，与 /admin/errors?window=15m 同口径。
	routeErrorWindow = 15 * time.Minute
	// routeErrorMinSamples 是参与选路的最小样本数（防呆铁律）。
	routeErrorMinSamples = 5
	// routeErrorDemoteRate 是触发降档的错误率阈值，严格大于才降档。
	routeErrorDemoteRate = 0.5
)

// errorFactor 是一跳的错误率因子。samples 不足时 valid=false，表示「样本不足，
// 不参与选路因子」，与没有错误数据完全等价。
type errorFactor struct {
	rate    float64
	samples int
	valid   bool
}

// demote 返回该因子是否要求降档：样本足够且错误率超过阈值。
func (f errorFactor) demote() bool {
	return f.valid && f.rate > routeErrorDemoteRate
}

// note 返回给 Reason 用的短说明，如「错误率 67%（n=9）」。无有效因子时为空串。
func (f errorFactor) note() string {
	if !f.valid {
		return ""
	}
	return fmt.Sprintf("错误率 %.0f%%（n=%d）", f.rate*100, f.samples)
}

// factorLess 在档位（含降档后的有效档）相同时比较两个因子：两跳都有有效因子时
// 错误率低者优先；任一跳样本不足则不可比，返回 comparable=false，由调用方按
// 链序/候选顺序兜底（保持「样本不足不参与因子」的铁律）。
func factorLess(a, b errorFactor) (less bool, comparable bool) {
	if !a.valid || !b.valid {
		return false, false
	}
	if a.rate != b.rate {
		return a.rate < b.rate, true
	}
	return false, false
}

// routeErrorFactors 取 15min 滚动窗口里按 (channel, 原生模型名) 聚合的错误率因子。
// outcomes 未启用（nil）时返回 nil，调用方一律拿到「无因子」，行为与现状一致。
// 返回的键是通道原生模型名，与窗口样本的 model 字段（errors.go 的 nativeModelName）
// 及探测候选的形态一致，比对不会因公共前缀对不上。
func (h *server) routeErrorFactors() map[routeChannel]map[string]errorFactor {
	if h.outcomes == nil {
		return nil
	}
	agg := h.outcomes.collect(routeErrorWindow)
	if len(agg.models) == 0 {
		return nil
	}
	out := make(map[routeChannel]map[string]errorFactor, len(agg.models))
	for channel, byModel := range agg.models {
		row := make(map[string]errorFactor, len(byModel))
		for model, stat := range byModel {
			samples := stat.ok + stat.fail
			row[model] = errorFactor{rate: stat.rate(), samples: samples, valid: samples >= routeErrorMinSamples}
		}
		out[channel] = row
	}
	return out
}

// errorFactorFor 查一跳的错误率因子；查不到返回零值因子，即「无因子」。
func errorFactorFor(factors map[routeChannel]map[string]errorFactor, channel routeChannel, native string) errorFactor {
	byModel, ok := factors[channel]
	if !ok {
		return errorFactor{}
	}
	return byModel[native]
}

// effectiveBand 把探测档位按错误率因子降档：需要降档时 +1，否则原样返回。
// 降档后的档位可能超过 1，调用方据此把该跳排除出候选（band>1 本就不选）。
func effectiveBand(band int, factor errorFactor) int {
	if factor.demote() {
		return band + 1
	}
	return band
}

// chainPick 是 pickChainTarget 的选路结论。
type chainPick struct {
	index int
	band  int
	// note 是错误率影响选路的说明（如「错误率 67%（n=9），降档」）；无影响时为空。
	note string
	// factorDriven 表示错误率因子改变了「不看因子」时的链序结论（降档或同档优先）。
	factorDriven bool
}

// pickChainTarget 在候选链里挑一跳：优先 band 最小的（可用 → 仅对话），band 相同时
// 取链序靠前的，因此「主目标可用就用主目标」是天然结果，只有它确实不可用才轮到备用。
//
// band=3（探测失败）不作为候选——把已知不可用的链路放进竞争等于制造随机故障。
// 链上没有任何一跳有探测结论时返回 found=false，由调用方按链序回退，与 resolveAuto
// 的克制口径一致。
//
// F6 错误率反哺：样本 ≥5 且错误率 >50% 的跳 band 视为 +1（可用→仅对话降档），
// 降档后 band>1 的跳被排除；档位相同时错误率低者优先（仅在两跳都有有效因子时）。
// 样本不足或无错误数据时因子不生效，结论与现状完全一致。
func (h *server) pickChainTarget(ctx context.Context, chain []routeTarget) (chainPick, bool) {
	candidates, haveData := h.probes.get(ctx, h)
	if !haveData {
		return chainPick{}, false
	}
	factors := h.routeErrorFactors()
	// 每个候选一跳：探测档位 + 错误率因子 + 降档后的有效档。
	type chainCandidate struct {
		index  int
		band   int
		eff    int
		factor errorFactor
	}
	var eligible []chainCandidate
	baselineIndex, baselineBand := -1, 0
	var baselineFactor errorFactor
	for i, target := range chain {
		band, ok := probeBandFor(candidates, target)
		if !ok || band > 1 {
			continue
		}
		factor := errorFactorFor(factors, target.Channel, target.nativeModel())
		if baselineIndex < 0 || band < baselineBand {
			baselineIndex, baselineBand = i, band
			baselineFactor = factor
		}
		eff := effectiveBand(band, factor)
		if eff > 1 {
			// 降档后越过「仅对话」档：本就不该进候选。
			continue
		}
		eligible = append(eligible, chainCandidate{index: i, band: band, eff: eff, factor: factor})
	}
	if len(eligible) == 0 {
		return chainPick{}, false
	}
	best := eligible[0]
	for _, cand := range eligible[1:] {
		if cand.eff != best.eff {
			if cand.eff < best.eff {
				best = cand
			}
			continue
		}
		// 档位相同：错误率低者优先（仅两跳都有有效因子时可比），否则保持链序。
		if less, comparable := factorLess(cand.factor, best.factor); comparable && less {
			best = cand
		}
	}
	return chainPick{
		index:        best.index,
		band:         best.band,
		note:         errorRateNote(best.index != baselineIndex, baselineFactor, best.factor),
		factorDriven: best.index != baselineIndex,
	}, true
}

// errorRateNote 生成「错误率影响了选路」的说明。changed 表示因子把结论从「不看因子」
// 的基线改成了另一跳；baseline / final 分别是基线与最终跳的错误率因子。只有结论被
// 因子改变、或最终跳自身被降档时才有说明，其余情况返回空串（Reason 与现状一字不变）。
func errorRateNote(changed bool, baseline, final errorFactor) string {
	if changed {
		if baseline.demote() {
			return baseline.note() + "，降档"
		}
		if final.valid {
			return final.note() + "，同档优先"
		}
	}
	if final.demote() {
		return final.note() + "，降档"
	}
	return ""
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
	if h.circuits != nil && h.circuits.blocked(routeTarget{Channel: channelCore, Model: fallback}) {
		fallback = "__circuit_open__"
	}
	candidates, haveData := h.probes.get(ctx, h)
	// 只收「可用」和「仅对话」两档：把探测失败的模型放进回退链等于把 auto
	// 变成随机故障源；未探测的模型也不收——没有任何证据说它能跑。
	//
	// F6 错误率反哺：样本 ≥5 且错误率 >50% 的候选 band 视为 +1（可用→仅对话降档），
	// 降档后越过「仅对话」档的候选被排除；档位相同时错误率低者优先。
	var eligible []autoCandidate // band 0/1 的全部候选（含后续被降档排除的），用于算基线
	if haveData {
		factors := h.routeErrorFactors()
		for _, list := range candidates {
			for _, candidate := range list {
				if h.circuits != nil && h.circuits.blocked(routeTarget{Channel: candidate.Channel, Model: candidate.Model}) {
					continue
				}
				if candidate.Band != 0 && candidate.Band != 1 {
					continue
				}
				factor := errorFactorFor(factors, candidate.Channel, candidate.Model)
				eligible = append(eligible, autoCandidate{
					candidate: candidate,
					eff:       effectiveBand(candidate.Band, factor),
					factor:    factor,
				})
			}
		}
	}
	if len(eligible) == 0 {
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
	// 不看因子时的现状口径结论（档位 → 耗时 → 模型名），作为「因子是否改变结论」的基线。
	baseline := eligible[0]
	for _, cand := range eligible[1:] {
		if candidateLess(cand.candidate, baseline.candidate) {
			baseline = cand
		}
	}
	// 降档后越过「仅对话」档的候选不进池。
	pool := make([]autoCandidate, 0, len(eligible))
	for _, cand := range eligible {
		if cand.eff > 1 {
			continue
		}
		pool = append(pool, cand)
	}
	if len(pool) == 0 {
		// 有探测结论但全部因错误率降档越过「仅对话」档：Reason 必须注明降权依据。
		return routeDecision{
			RoutedModel: fallback,
			Channel:     channelCore,
			Alias:       autoModelName,
			Reason:      "auto 候选均因错误率降档，回退核心通道",
			Fallback:    true,
			TargetIndex: -1,
		}
	}
	// 先按现状口径（档位 → 耗时 → 模型名）排序，让结论与池的遍历顺序无关；再线性扫描，
	// 只让「可比且更优」的候选顶替当前最优。未参与因子的候选（样本不足/无数据）因此
	// 不会被因子拉下马（防呆铁律），与 pickChainTarget 同构。
	sort.SliceStable(pool, func(i, j int) bool { return candidateLess(pool[i].candidate, pool[j].candidate) })
	best := pool[0]
	for _, cand := range pool[1:] {
		if autoBeats(cand, best) {
			best = cand
		}
	}
	reason := fmt.Sprintf("auto 选中 %s（%s）", best.candidate.Model, probeBandLabel(best.candidate.Band))
	if note := errorRateNote(!sameCandidate(best, baseline), baseline.factor, best.factor); note != "" {
		reason = fmt.Sprintf("%s · %s", reason, note)
	}
	return routeDecision{
		RoutedModel: best.candidate.Channel.publicModel(best.candidate.Model),
		Channel:     best.candidate.Channel,
		Alias:       autoModelName,
		Reason:      reason,
		TargetIndex: -1,
	}
}

// sameCandidate 判断两条 auto 候选是不是同一条链路（通道 + 原生模型名）。
func sameCandidate(a, b autoCandidate) bool {
	return a.candidate.Channel == b.candidate.Channel && a.candidate.Model == b.candidate.Model
}

// autoCandidate 是 auto 池里的一条候选：探测结论 + 错误率因子 + 降档后的有效档。
type autoCandidate struct {
	candidate probeCandidate
	eff       int
	factor    errorFactor
}

// autoBeats 判断 a 是否严格优于 b：先比有效档位（降档后），档位相同再比错误率，
// 且只在两跳都有有效因子时才可比（样本不足不参与因子）。不可比时返回 false，
// 由调用方的现状口径兜底。
func autoBeats(a, b autoCandidate) bool {
	if a.eff != b.eff {
		return a.eff < b.eff
	}
	less, comparable := factorLess(a.factor, b.factor)
	return comparable && less
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
	// attempts 传 0：单跳路径不下发 X-Prism-Attempts，响应头形态与引入请求内
	// 重试之前完全一致，该头只在真正发生多跳尝试时出现。
	writePrismHeaders(p.Header(), p.decision, 0)
	p.ResponseWriter.WriteHeader(code)
}

// writePrismHeaders 是路由决策响应头的唯一写入点：请求内重试（retry.go）与单跳
// 路径（prismHeaderWriter）共用，禁止出现两份拼装逻辑。attempts>0 时附带
// X-Prism-Attempts（实际服务客户端的尝试序号）。
func writePrismHeaders(header http.Header, decision routeDecision, attempts int) {
	header.Set("X-Prism-Routed-Model", decision.RoutedModel)
	header.Set("X-Prism-Channel", string(decision.Channel))
	header.Set("X-Prism-Reason", decision.Reason)
	// TargetIndex 只在别名候选链上有意义（0=主目标，>0=第几条备用）；auto 用 -1
	// 表示不适用，此时不下发该头，避免调用方把 auto 也当成一条链去解释。
	if decision.TargetIndex >= 0 {
		header.Set("X-Prism-Target-Index", strconv.Itoa(decision.TargetIndex))
	}
	if decision.Fallback {
		header.Set("X-Prism-Fallback", "core")
	}
	if attempts > 0 {
		header.Set("X-Prism-Attempts", strconv.Itoa(attempts))
	}
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
	case channelTraeAI:
		return h.cfg.TraeAIURL != nil
	case channelRaccoon:
		return h.cfg.RaccoonURL != nil
	case channelFreellm:
		return h.cfg.FreellmURL != nil
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
	case channelTraeAI:
		return h.cfg.TraeAIURL, h.cfg.TraeAIKey
	case channelRaccoon:
		return h.cfg.RaccoonURL, h.cfg.RaccoonKey
	case channelFreellm:
		return h.cfg.FreellmURL, h.cfg.FreellmKey
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
// 已带命名空间、glm 上游直接下发带前缀的名字，这两类保持原样。GLM 通道额外按账号
// 权益/档位裁剪（见下），与 /admin/zcode 的目录口径一致。
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
	// GLM 通道（zcode）必须按账号真实权益裁剪目录。上游 /v1/models 会把整个目录都列出来
	// （glm-4.5-air、glm-4.6… 这些当前计划未授权的也在内），而 /admin/zcode 展示时已用
	// filterZcodeModels 裁过一次。若这里拿原始目录去播种探测，探测就会挑中一个「计划根本
	// 不允许」的模型，稳定拿 3006 model not allowed，把通道永久钉成红灯——这个红与真实可用
	// 状态无关（踩过：glm 红灯报 model not allowed，别名真正用的 glm-5.3-flash 却是另一回事）。
	// 目录与探测必须用同一份「账号真能服务」的清单，因此这里复用同一判据。
	if c == channelGLM {
		list = filterZcodeModels(list, h.zcodeEntitlements(ctx), h.zcodePlan(ctx))
	}
	// FreeLLMAPI 的目录是「全网能力清单」，不是「本实例可服务清单」：实测无供应商
	// key 时 259 条里 257 条 execution_status=needsKey。拿原始目录去播种，探测与别名
	// 选择器都会被钉在一批必然 503 的目标上，与 GLM 那条坑同构，所以共用同一判据。
	if c == channelFreellm {
		list = filterFreellmModels(list)
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
