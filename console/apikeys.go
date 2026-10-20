package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
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
// 本批范围是「归因+报表」；按 Key 限制可用模型留待下一批。

// defaultKeyLabel 主 Key 在账本与页面里的固定标签，附加 Key 不得占用。
const defaultKeyLabel = "默认"

// apiKeyEntry 附加 Key 清单的单条记录。Key 是明文（gate 需要精确比较），文件
// 权限 0600、目录与 routes.json 同级；管理界面回显与 /admin/access 回 api_key
// 同一口径（管理会话强鉴权 + CSRF）。
type apiKeyEntry struct {
	Label     string `json:"label"`
	Key       string `json:"key"`
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"created_at"`
}

// maxAPIKeys 附加 Key 条数上限，防失控循环撑爆文件与内存。
const maxAPIKeys = 32

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

// match 解析 Bearer 凭据并返回命中标签。主 Key 恒可命中（标签「默认」）；
// 附加 Key 只认 enabled 条目。全部比较走 equalSecret（SHA-256 + 恒时比较）。
func (s *apiKeyStore) match(authz, mainKey string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(authz, prefix) {
		return "", false
	}
	token := strings.TrimPrefix(authz, prefix)
	if token == "" {
		return "", false
	}
	if equalSecret(token, mainKey) {
		return defaultKeyLabel, true
	}
	if s == nil {
		return "", false
	}
	for _, k := range s.list() {
		if k.Enabled && equalSecret(token, k.Key) {
			return k.Label, true
		}
	}
	return "", false
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

// apiKeyGate 公共出口鉴权权威：仅在附加 Key 清单非空时介入——未配置附加 Key 的
// 部署直通（core withAuth 照旧权威，公共出口行为与没有本功能时完全一致，既有
// 测试的透传语义不受影响）。清单非空后，未命中主 Key 与附加清单自产 401（OpenAI
// 错误形态，与 core withAuth 同形）；命中后把标签注入请求 ctx，core 通道转发时
// 统一换发 cfg.APIKey（附加 Key 的明文不出 console）；观测包装器在场的场景
// （对话端点）同时回写到 exitObserver，让错误账本带上归属标签。
func (h *server) apiKeyGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.apiKeys == nil || len(h.apiKeys.list()) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		label, ok := h.apiKeys.match(r.Header.Get("Authorization"), h.cfg.APIKey)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"error": map[string]string{"message": "无效的 API Key", "type": "invalid_request_error", "code": "invalid_api_key"},
			})
			return
		}
		if obs, okw := w.(*exitObserver); okw {
			obs.keyLabel = label
		}
		next.ServeHTTP(w, r.WithContext(withKeyLabel(r.Context(), label)))
	})
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
