package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// —— 多 API Key 归因（对标项 #5 · 归因+报表先行）—————————————————————
//
// 鉴权权威在 console：公共出口（/v1/*、GET /status）先过 apiKeyGate，命中主 Key
// （cfg.APIKey，标签恒「默认」）或附加 Key 清单才放行；core 通道转发时统一换发
// cfg.APIKey 并注入 X-Console-Key-Label 内部头，core 账本按标签归属（OriginOf 读头，
// 源码模式下该头只能当客户端自报）。旁路通道由 console 侧 usage 上报带 key_label，
// sidecar 不感知。
//
// 清单持久化在 consoleDataDir/apikeys.json（与 routes.json 同目录）。文件损坏时
// 按空清单降级并告警：gate 退化为只认主 Key（fail-closed），不阻塞启动。
// Key 级模型白名单（对标项 #16）：每把附加 Key 可声明 models 白名单，只拦三个
// 对话出口的实际调用；/v1/models 目录恒返回全量（用户拍板不裁剪）。

// defaultKeyLabel 主 Key 在账本与页面里的固定标签，附加 Key 不得占用。
const defaultKeyLabel = "默认"

// apiKeyEntry 附加 Key 清单的单条记录。Key 是明文（gate 需要精确比较），文件
// 权限 0600、目录与 routes.json 同级；管理界面回显与 /admin/access 回 api_key
// 同一口径（管理会话强鉴权 + CSRF）。Models 是模型白名单（path.Match 通配符，
// 空数组或缺省=不限），只约束该 Key 的对话调用。
type apiKeyEntry struct {
	Label     string   `json:"label"`
	Key       string   `json:"key"`
	Enabled   bool     `json:"enabled"`
	CreatedAt int64    `json:"created_at"`
	Models    []string `json:"models,omitempty"`
}

// maxAPIKeys 附加 Key 条数上限，防失控循环撑爆文件与内存。
const maxAPIKeys = 32

// maxKeyModels 单把 Key 的白名单 pattern 条数上限；maxKeyModelPatternBytes 是单条
// pattern 的字节上限。都是防滥用口径：白名单是逐条字符串匹配，不给无限输入留口子。
const (
	maxKeyModels            = 32
	maxKeyModelPatternBytes = 128
)

// keyModelPrefixes 白名单匹配时循环剥离的公共模型名前缀：九条旁路前缀 + core 的
// cn:/global: 区域命名空间。请求模型名逐层剥离、逐层试 pattern，任一层命中即放行。
var keyModelPrefixes = []string{"cn:", "global:", "glm-", "qoder-", "opencode-", "autoclaw-", "trae-", "traeai-", "raccoon-", "freellm-", "zcode-"}

// modelAllowed 判定请求模型是否被白名单放行。patterns 为空（缺省或空数组）=不限；
// 否则把请求模型名与其「逐层剥前缀后的裸名」依次试 pattern（path.Match 通配符，
// 区分大小写），任一 pattern 命中任一层名字即放行。例：请求 autoclaw-cn:glm-5.3-flash
// 时候选名依次为原名、cn:glm-5.3-flash、glm-5.3-flash、5.3-flash，能被
// "glm-5.3-flash" 或 "autoclaw-*" 命中。
func modelAllowed(patterns []string, model string) bool {
	if len(patterns) == 0 {
		return true
	}
	candidate := model
	for {
		for _, p := range patterns {
			if ok, err := path.Match(p, candidate); err == nil && ok {
				return true
			}
		}
		stripped := candidate
		for _, prefix := range keyModelPrefixes {
			if strings.HasPrefix(candidate, prefix) {
				stripped = strings.TrimPrefix(candidate, prefix)
				break
			}
		}
		if stripped == candidate {
			return false
		}
		candidate = stripped
	}
}

// apiKeyStore 附加 Key 清单。nil store 合法（RouteFile 未配置的部署），
// gate 只认主 Key，行为与没有本功能时一致。
type apiKeyStore struct {
	mu   sync.RWMutex
	path string
	keys []apiKeyEntry
}

// newAPIKeyStore 从 path 加载清单；文件缺失视为空清单，损坏时告警后按空清单
// 降级（fail-closed：附加 Key 全部失效，主 Key 照常）。
func newAPIKeyStore(path string) *apiKeyStore {
	s := &apiKeyStore{path: path}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Print("WARN: [apikeys] 读取失败，按空清单运行：", err)
		}
		return s
	}
	var body struct {
		Keys []apiKeyEntry `json:"keys"`
	}
	if json.Unmarshal(raw, &body) != nil {
		log.Print("WARN: [apikeys] 清单文件损坏，按空清单运行（附加 Key 已全部失效，主 Key 不受影响）：", path)
		return s
	}
	for _, k := range body.Keys {
		if validateAPIKeyEntry(k) == nil {
			s.keys = append(s.keys, k)
		} else {
			log.Print("WARN: [apikeys] 清单含非法条目，已跳过 label=", k.Label)
		}
	}
	return s
}

// list 返回清单副本。
func (s *apiKeyStore) list() []apiKeyEntry {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]apiKeyEntry(nil), s.keys...)
}

// replace 校验并全量替换清单，原子落盘后更新内存。
func (s *apiKeyStore) replace(keys []apiKeyEntry) error {
	if s == nil {
		return errors.New("未配置可写数据目录，附加 API Key 不可用")
	}
	if len(keys) > maxAPIKeys {
		return errors.New("附加 Key 数量超过上限 32")
	}
	for _, k := range keys {
		if err := validateAPIKeyEntry(k); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, k := range keys {
		if seen[k.Label] {
			return errors.New("标签重复：" + k.Label)
		}
		seen[k.Label] = true
	}
	raw, err := json.Marshal(map[string]any{"keys": keys})
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "apikeys-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, s.path); err != nil {
		os.Remove(name)
		return err
	}
	s.mu.Lock()
	s.keys = append([]apiKeyEntry(nil), keys...)
	s.mu.Unlock()
	return nil
}

// matchEntry 解析 Bearer 凭据并返回命中的完整条目。主 Key 恒可命中（合成条目，
// 标签「默认」、无白名单）；附加 Key 只认 enabled 条目。全部比较走 equalSecret
// （SHA-256 + 恒时比较）。
func (s *apiKeyStore) matchEntry(authz, mainKey string) (apiKeyEntry, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(authz, prefix) {
		return apiKeyEntry{}, false
	}
	token := strings.TrimPrefix(authz, prefix)
	if token == "" {
		return apiKeyEntry{}, false
	}
	if equalSecret(token, mainKey) {
		return apiKeyEntry{Label: defaultKeyLabel, Enabled: true}, true
	}
	if s == nil {
		return apiKeyEntry{}, false
	}
	for _, k := range s.list() {
		if k.Enabled && equalSecret(token, k.Key) {
			return k, true
		}
	}
	return apiKeyEntry{}, false
}

// validateAPIKeyEntry 校验单条清单。标签与 core 落账侧 usagelog.SanitizeKeyLabel
// 同一口径：可打印 ASCII、不含空白、1..64 字节；key 为 16..128 字节无空白 ASCII。
// 「默认」保留给主 Key，附加 Key 不得占用（账本归属歧义）。
func validateAPIKeyEntry(k apiKeyEntry) error {
	label := k.Label
	if label == "" || len(label) > 64 {
		return errors.New("标签须为 1..64 字节")
	}
	for i := 0; i < len(label); i++ {
		if label[i] < 0x21 || label[i] > 0x7e {
			return errors.New("标签须为不含空白的可打印 ASCII（1..64 字节）")
		}
	}
	if label == defaultKeyLabel {
		return errors.New("「默认」保留给主 Key，附加 Key 不得使用该标签")
	}
	if len(k.Key) < 16 || len(k.Key) > 128 {
		return errors.New("Key 须为 16..128 字节")
	}
	for i := 0; i < len(k.Key); i++ {
		if k.Key[i] < 0x21 || k.Key[i] > 0x7e {
			return errors.New("Key 须为不含空白的可打印 ASCII（16..128 字节）")
		}
	}
	// 模型白名单：可整体缺省；出现则逐条校验。条数与单条长度是防滥用上限；
	// path.Match 语法错误（如 "[a-"）在匹配处恒不命中，这里先拦掉，避免
	// 「保存成功但永远不生效」的哑 pattern 蒙混过关。
	if len(k.Models) > maxKeyModels {
		return errors.New("模型白名单最多 32 条")
	}
	seen := make(map[string]struct{}, len(k.Models))
	for _, p := range k.Models {
		if p == "" || len(p) > maxKeyModelPatternBytes {
			return errors.New("模型白名单每条须为 1..128 字节")
		}
		if _, err := path.Match(p, ""); err != nil {
			return errors.New("模型白名单含非法通配符 pattern：" + p)
		}
		if _, dup := seen[p]; dup {
			return errors.New("模型白名单存在重复条目：" + p)
		}
		seen[p] = struct{}{}
	}
	return nil
}

// keyLabelCtxKey 携带 gate 命中的 Key 标签，供 core 通道换发注入内部头与
// 旁路 usage 上报归属。
type keyLabelCtxKey struct{}

func withKeyLabel(ctx context.Context, label string) context.Context {
	return context.WithValue(ctx, keyLabelCtxKey{}, label)
}

func keyLabelFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	label, _ := ctx.Value(keyLabelCtxKey{}).(string)
	return label
}

// keyUnauthorizedBody 未命中清单时 401 响应体（OpenAI 错误形态，与 core withAuth 同形）。
var keyUnauthorizedBody = map[string]any{
	"error": map[string]string{"message": "无效的 API Key", "type": "invalid_request_error", "code": "invalid_api_key"},
}

// keyForbiddenBody 白名单拒绝时 403 响应体（OpenAI 错误形态）。model 在调用点拼进
// message，让客户端不用猜是哪个名字被拒。
func keyForbiddenBody(model string) map[string]any {
	return map[string]any{
		"error": map[string]string{"message": "该 API Key 未授权使用模型 " + model, "type": "invalid_request_error", "code": "model_not_allowed"},
	}
}

// apiKeyGate 公共出口鉴权权威：仅在附加 Key 清单非空时介入——未配置附加 Key 的
// 部署直通（core withAuth 照旧权威，公共出口行为与没有本功能时完全一致，既有
// 测试的透传语义不受影响）。清单非空后，未命中主 Key 与附加清单自产 401；命中后
// 把标签注入请求 ctx，core 通道转发时统一换发 cfg.APIKey（附加 Key 的明文不出
// console）；观测包装器在场的场景（对话端点）同时回写到 exitObserver，让错误账本
// 带上归属标签。
//
// 账本留痕：observePublicExits 包在最外层，gate 自产的 401/403 经 exitObserver 捕获
// 后由 finishExitObserve 归档（403 → auth 类、produced=false、带 X-Request-Id 与
// key_label），gate 只负责把响应写出去。
//
// Key 级模型白名单（对标项 #16）：只拦三个对话出口（/v1/chat/completions、
// /v1/messages、/v1/responses）的实际调用——请求体 model 与 Key 白名单明确不匹配
// 时 403 model_not_allowed；/v1/models 目录与 GET /status 恒不裁剪。请求体只窥探
// 256 KiB（远小于下游 32 MiB 分流上限），超限或缺 model 字段/解析失败一律放行：
// 模型名合法性校验是 core 的职责，gate 只在「声明了白名单且明确不匹配」时拦。
func (h *server) apiKeyGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.apiKeys == nil || len(h.apiKeys.list()) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		entry, ok := h.apiKeys.matchEntry(r.Header.Get("Authorization"), h.cfg.APIKey)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, keyUnauthorizedBody)
			return
		}
		// 先落标签再判白名单：403 也要进账本，且要带 key_label 归属。
		if obs, okw := w.(*exitObserver); okw {
			obs.keyLabel = entry.Label
		}
		if r.Method == http.MethodPost {
			switch r.URL.Path {
			case "/v1/chat/completions", "/v1/messages", "/v1/responses":
				if !h.enforceKeyModelWhitelist(w, r, entry) {
					return
				}
			}
		}
		next.ServeHTTP(w, r.WithContext(withKeyLabel(r.Context(), entry.Label)))
	})
}

// keyModelPeekLimit 白名单检查的请求体窥探上限。Gate 在链路最前端，只读不消费：
// 用 MultiReader 拼回窥探段与剩余体，下游 publicRouter/core 照常读到完整请求。
const keyModelPeekLimit = 256 << 10

// gateBody 是窥探后拼回去的只读体：Go 1.20+ 的 http.Request.Body 只要求 Read/Close，
// 匿名结构体组合即可，不引第三方类型。
type gateBody struct {
	io.Reader
	io.Closer
}

// enforceKeyModelWhitelist 对话出口的白名单拦截点。窥探 body 的 model 字段：
// 解析失败、缺字段、空值或体超上限都放行（模型名校验归 core）；白名单为空恒放行。
// 明确不匹配时写 403 model_not_allowed（OpenAI 形态；X-Request-Id 与账本由外层
// observePublicExits 统一处理）。
func (h *server) enforceKeyModelWhitelist(w http.ResponseWriter, r *http.Request, entry apiKeyEntry) bool {
	if len(entry.Models) == 0 {
		return true
	}
	head, _ := io.ReadAll(io.LimitReader(r.Body, keyModelPeekLimit))
	r.Body = gateBody{io.MultiReader(bytes.NewReader(head), r.Body), r.Body}
	if len(head) == keyModelPeekLimit {
		// 体达到窥探上限：不再向后读（剩余体原样留给下游），按放行处理。
		return true
	}
	var envelope struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(head, &envelope) != nil || envelope.Model == "" {
		return true
	}
	if modelAllowed(entry.Models, envelope.Model) {
		return true
	}
	writeJSON(w, http.StatusForbidden, keyForbiddenBody(envelope.Model))
	return false
}

// adminAPIKeysState GET /admin/apikeys：读当前附加 Key 清单（明文回显）。
func (h *server) adminAPIKeysState(w http.ResponseWriter, r *http.Request) {
	keys := []apiKeyEntry{}
	if h.apiKeys != nil {
		keys = h.apiKeys.list()
	}
	writeJSON(w, 200, map[string]any{"keys": keys, "default_label": defaultKeyLabel, "editable": h.apiKeys != nil})
}

// adminAPIKeysSave POST /admin/apikeys：全量保存附加 Key 清单（新增/启停/删除
// 都由前端整表提交），落盘即生效。
func (h *server) adminAPIKeysSave(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Keys []apiKeyEntry `json:"keys"`
	}
	if !decodeAdmin(w, r, &body) {
		return
	}
	// 未提交 created_at 的条目补当前时间；已带的按原值（前端整表回传保留首次创建时刻）。
	for i := range body.Keys {
		if body.Keys[i].CreatedAt == 0 {
			body.Keys[i].CreatedAt = time.Now().Unix()
		}
	}
	if err := h.apiKeys.replace(body.Keys); err != nil {
		adminError(w, 400, err.Error())
		return
	}
	h.adminAPIKeysState(w, r)
}
