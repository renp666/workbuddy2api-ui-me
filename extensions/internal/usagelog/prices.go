// 价目快照（对标 hub 的 OpenRouter 价估算，§三 #9）。
//
// 语义：账本在落账时把「当次调用的等价美元花费」快照进 Entry.CostUSD——之后
// 调价、改表都不影响已落账的历史数字；未定价的调用保持 nil（缺失≠0，与积分
// 口径同款），页面如实显示「—」并给出未定价条数。
//
// 价目来源（按优先级）：data 目录下的 prices.json（size/mtime 变化即热重载）
// → 内置默认表（当前为空：启用模型全是内部命名，OpenRouter 没有同名公布价，
// 编一份「看起来像」的数字就是伪造；用户把 OpenRouter 公布价复制进
// prices.json 即生效，键用剥前缀后的裸模型名）。零第三方依赖，仅标准库。
//
// prices.json 形状（input/output 均为「每 1M tokens 的美元价」）：
//
//	{
//	  "deepseek-v4.1-flash": {"input": 0.27, "output": 1.10},
//	  "glm-5.3-flash":       {"input": 0.10, "output": 0.40}
//	}
package usagelog

import (
	"encoding/json"
	"log"
	"os"
	"strings"
	"sync/atomic"
)

// PriceEntry 单个模型每 1M tokens 的美元单价（输入/输出分开）。
type PriceEntry struct {
	InputUSD  float64 `json:"input"`
	OutputUSD float64 `json:"output"`
}

// priceTable 当前生效价目表；nil 等价空表。Open 时指向内置默认 + 文件覆盖的合并结果。
var priceTable atomic.Pointer[map[string]PriceEntry]

// priceFile 价目文件路径（Open 时由 usage 目录推导 data 目录下的 prices.json），
// 空串表示无文件；测试可用 SetPriceFile 显式指定。
var priceFile atomic.Pointer[string]

// priceFileStat 上次加载文件时的 (size, mtimeUnix)，用于廉价热重载判定。
type priceFileStat struct {
	size    int64
	modUnix int64
}

var priceFileSeen atomic.Pointer[priceFileStat]

// priceModelPrefixes 归一化时逐级剥离的 realm/通道前缀。九通道账本统一保留
// 公共前缀（cn:glm-5.3-flash、traeai-deepseek-v4.1-flash、autoclaw-cn:xxx），
// 价目键用裸名，匹配时按候选序列命中第一个。
var priceModelPrefixes = []string{
	"cn:", "global:", "glm-", "qoder-", "opencode-", "autoclaw-",
	"trae-", "traeai-", "raccoon-", "freellm-", "zcode-",
}

// priceCandidates 生成模型名的匹配候选：原名 + 逐级剥前缀（循环剥离直到不再变化，
// 处理 autoclaw-cn:glm-5.3-flash 这类双层前缀）。顺序即优先级，原名最先。
func priceCandidates(model string) []string {
	name := strings.TrimSpace(model)
	if name == "" {
		return nil
	}
	candidates := []string{name}
	for {
		stripped := name
		for _, p := range priceModelPrefixes {
			if strings.HasPrefix(stripped, p) {
				stripped = strings.TrimPrefix(stripped, p)
				break
			}
		}
		if stripped == name {
			return candidates
		}
		candidates = append(candidates, stripped)
		name = stripped
	}
}

// CostUSDFor 按当前价目表折算一次调用的美元花费；输入/输出 token 都已观测且
// 命中价目才返回 (cost, true)，任何一步缺失都返回 (0, false)——部分 token 缺失
// 时只算已知半边会静悄悄少报，宁可整体不定价。
func CostUSDFor(model string, prompt, completion int) (float64, bool) {
	if prompt < 0 || completion < 0 {
		return 0, false
	}
	table := priceTable.Load()
	if table == nil {
		return 0, false
	}
	for _, name := range priceCandidates(model) {
		if p, ok := (*table)[name]; ok {
			return float64(prompt)/1e6*p.InputUSD + float64(completion)/1e6*p.OutputUSD, true
		}
	}
	return 0, false
}

// SetPriceFile 显式指定价目文件并立即重载；Open 场景不需要调用（自动推导）。
func SetPriceFile(path string) {
	priceFile.Store(&path)
	refreshPriceTable(true)
}

// SetPricesForTest 直接注入价目表（测试专用；生产路径只经 Open/文件热重载）。
func SetPricesForTest(table map[string]PriceEntry) {
	priceTable.Store(&table)
}

// defaultPrices 内置默认价目表。刻意为空：见包注释——内部命名没有可引用的
// 公布价，编造即是伪造。表结构保留在这里，将来若有官方公布价可直接补。
var defaultPrices = map[string]PriceEntry{}

// refreshPriceTable 合并内置默认与文件内容；force=false 时仅当文件 stat 变化才
// 重读（RecordAuth 热路径上的代价是一次 stat）。解析失败时保留当前生效表不动
// （坏文件不该把好价目清空），文件缺失时整体回到默认表。
func refreshPriceTable(force bool) {
	pathPtr := priceFile.Load()
	if pathPtr == nil || *pathPtr == "" {
		refreshDefaults()
		return
	}
	info, err := os.Stat(*pathPtr)
	if err != nil {
		// 文件不存在不是错误：整体未定价是合法状态，页面如实显示。
		// 「从有到无」按变化处理（force 或上一轮见过文件时），整体回到默认表。
		if force || priceFileSeen.Load() != nil {
			priceFileSeen.Store(nil)
			refreshDefaults()
		}
		return
	}
	st := &priceFileStat{size: info.Size(), modUnix: info.ModTime().UnixNano()}
	if !force {
		if seen := priceFileSeen.Load(); seen != nil && *seen == *st {
			return
		}
	}
	priceFileSeen.Store(st)
	raw, err := os.ReadFile(*pathPtr)
	if err != nil {
		log.Print("WARN: [usagelog] prices.json 读取失败，沿用上次有效价目: ", err)
		return
	}
	var fromFile map[string]PriceEntry
	if err := json.Unmarshal(raw, &fromFile); err != nil {
		log.Print("WARN: [usagelog] prices.json 解析失败，沿用上次有效价目: ", err)
		return
	}
	merged := make(map[string]PriceEntry, len(defaultPrices)+len(fromFile))
	for k, v := range defaultPrices {
		merged[k] = v
	}
	for k, v := range fromFile {
		merged[k] = v
	}
	priceTable.Store(&merged)
}

// refreshDefaults 价目整体回落到内置默认表（当前为空 = 全部未定价）。
func refreshDefaults() {
	table := defaultPrices
	priceTable.Store(&table)
}
