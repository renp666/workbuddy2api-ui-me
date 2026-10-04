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
)

// routeChannelOrder 是页面展示与目录抓取的固定通道顺序：core 在前，
// 让「回退核心通道」的默认目标在界面上排在最前面。
var routeChannelOrder = []routeChannel{channelCore, channelGLM, channelQoder, channelOpenCode}

// autoModelName 是 console 自建的虚拟模型名：不在任何上游目录里，由 console
// 在请求时按探测结论挑一条最优链路。名字带 gateway- 前缀，是为了和各通道可能
// 存在的原生 auto 模型（cn:auto、qoder-auto 等）区分开，避免客户端填 auto
// 时说不清到底指谁。开发阶段直接切换，不为旧名 auto 保留兼容解析。
const autoModelName = "gateway-auto"

// routeFileVersion 是路由配置文件的结构版本。版本不匹配时按上一份配置处理，
// 不做静默迁移，避免把旧语义当成新语义路由。
const routeFileVersion = 1

// routeEntry 借鉴 LiteLLM model_list 的数据模型：一条「对外别名 → 真实上游」的
// 映射。Alias 是客户端写进请求 model 字段的名字，Channel+Model 决定实际去哪。
type routeEntry struct {
	Alias   string       `json:"alias"`
	Channel routeChannel `json:"channel"`
	Model   string       `json:"model"`
	Enabled bool         `json:"enabled"`
	Note    string       `json:"note,omitempty"`
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
// 只有 qoder 与 opencode 的上游目录是裸名字，需要补公共前缀——这三条判断
// 与 publicRouter 的分流前缀完全一致，这是别名能复用既有分流的前提。
func (c routeChannel) prefix() string {
	switch c {
	case channelQoder:
		return qoderModelPrefix
	case channelOpenCode:
		return opencodeModelPrefix
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
	}
	if strings.HasPrefix(model, "global:") {
		return "global"
	}
	return "cn"
}

// aliasModelEntries 把当前启用、且解析目标放行的别名条目转成 /v1/models 的虚拟条目，
// 让下游（含拒绝模型名带空格的消费方）能直接发现并按别名引用。条目沿用所属通道的
// realm，并额外带 alias/routed_model/virtual 字段标明它是间接层而非真实上游模型。
// 目标被积分开关停用（含默认停用）时该别名一并隐藏，保持「可见即可用」的口径，
// 不会 advertise 一个请求必然 404 的名字。gateway-auto 由 mergedModels 单独追加。
func (h *server) aliasModelEntries(ctx context.Context) []json.RawMessage {
	if h.routes == nil {
		return nil
	}
	var out []json.RawMessage
	for _, entry := range h.routes.snapshot() {
		if !entry.Enabled || entry.Alias == autoModelName {
			continue
		}
		// 通道未配置上游时不广告：请求会因前缀分流落空而失败，别名不该承诺一个到不了的链路。
		if !h.channelEnabled(entry.Channel) {
			continue
		}
		routed := entry.publicModel()
		if !h.modelGate(ctx, routed) {
			continue
		}
		item, err := json.Marshal(map[string]any{
			"id":           entry.Alias,
			"object":       "model",
			"owned_by":     "prism",
			"realm":        entry.Channel.realm(entry.Model),
			"virtual":      true,
			"alias":        true,
			"routed_model": routed,
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

// reservedAliasPrefixes 是别名不得占用的前缀：三个旁路通道的公共前缀加上 core 的
// 两个命名空间。别名解析发生在前缀分流之前，占用这些前缀会静默遮蔽同名真实模型。
var reservedAliasPrefixes = []string{zcodeModelPrefix, qoderModelPrefix, opencodeModelPrefix, "cn:", "global:"}

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
		switch entry.Channel {
		case channelCore, channelGLM, channelQoder, channelOpenCode:
		default:
			return fmt.Errorf("别名 %q 的通道必须是 core、glm、qoder 或 opencode", alias)
		}
		model := strings.TrimSpace(entry.Model)
		if !validRouteName(model, 128, routeModelChars) {
			return fmt.Errorf("别名 %q 的模型名不合法：只允许字母、数字、点、下划线、冒号、连字符、空格和中点，长度 1-128", alias)
		}
		// core 的模型 ID 由上游以 cn:/global: 命名空间下发，缺前缀的裸名字
		// core 一定不认；反之旁路通道收不到带命名空间的名字。
		if entry.Channel == channelCore {
			if !strings.HasPrefix(model, "cn:") && !strings.HasPrefix(model, "global:") {
				return fmt.Errorf("别名 %q 指向 core 时模型名须带 cn: 或 global: 前缀", alias)
			}
		} else if strings.Contains(model, ":") {
			return fmt.Errorf("别名 %q 指向 %s 时模型名不能含冒号（那是 core 的命名空间）", alias, entry.Channel)
		}
		if len(entry.Note) > 200 {
			return fmt.Errorf("别名 %q 的备注不得超过 200 字", alias)
		}
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
func normalizeRoutes(models []routeEntry) []routeEntry {
	out := make([]routeEntry, 0, len(models))
	for _, entry := range models {
		entry.Alias = strings.TrimSpace(entry.Alias)
		entry.Model = strings.TrimSpace(entry.Model)
		out = append(out, entry)
	}
	return out
}

// routeModelChars 是合法公共模型名的字符集，别名模型名与模型开关表共用。
const routeModelChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._:- ·"

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
			return fmt.Errorf("%s 含非法模型名：只允许字母、数字、点、下划线、冒号、连字符、空格和中点，长度 1-128", name)
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
		return errors.New("模型名不合法：只允许字母、数字、点、下划线、冒号、连字符、空格和中点，长度 1-128")
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
	for _, channel := range []routeChannel{channelCore, channelGLM, channelQoder} {
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
}

// resolveAlias 把客户端请求的模型名解析成一条具体链路。返回 false 表示这个
// model 不是别名也不是 auto，调用方应继续走既有前缀分流。
//
// 解析规则只有两条：
//  1. 普通别名 → 配置里那一条固定的 通道+模型。
//  2. auto → 在探测结论里挑最优；完全没有探测结论时回退到核心通道（主账号池）。
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
	return routeDecision{
		RoutedModel: entry.publicModel(),
		Channel:     entry.Channel,
		Alias:       entry.Alias,
		Reason:      "别名命中配置",
	}, true
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
		}
	}
	sortCandidates(pool)
	best := pool[0]
	return routeDecision{
		RoutedModel: best.Channel.publicModel(best.Model),
		Channel:     best.Channel,
		Alias:       autoModelName,
		Reason:      fmt.Sprintf("auto 选中 %s（%s）", best.Model, probeBandLabel(best.Band)),
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

func (h *server) writeRouteState(w http.ResponseWriter, r *http.Request) {
	aliases := []map[string]any{}
	channels := []routeChannelCatalog{}
	preview := map[string]any{}
	autoFallback := ""
	if h.routes != nil {
		for _, entry := range h.routes.snapshot() {
			aliases = append(aliases, map[string]any{
				"alias":       entry.Alias,
				"channel":     string(entry.Channel),
				"model":       entry.Model,
				"public_model": entry.publicModel(),
				"enabled":     entry.Enabled,
				"note":        entry.Note,
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
		}
	}
	writeJSON(w, 200, map[string]any{
		"enabled":        h.routes != nil,
		"auto_model":     autoModelName,
		"auto_fallback":  autoFallback,
		"default_auto_fallback": defaultAutoFallback,
		"auto_preview":   preview,
		"aliases":        aliases,
		"channels":       channels,
	})
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
