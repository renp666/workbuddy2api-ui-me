// edition_import_test.go 区域相关的导入不变量：国际版凭据不能被标成国内版。
//
// 存在的理由是一条真实事故形态（社区实现 2026-09-17 记录过同一 bug）：edition 判错时
// ExchangeToken 会拿 SG 的 refreshToken 去打 api.trae.cn，刷新必失败，而账号看起来
// "还在池里"。本仓的对应设计是"凭据自带的 host 优先，区域默认值只兜空值"。
package server

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"workbuddy2api-trae/trae/pool"
	"workbuddy2api-trae/trae/upstream"
)

// tcRoundTripLock 说明：这里的加密夹具与 auth 包的解密实现互逆。
// 真客户端文件的锚定在 auth.TestParseStorageJSONRealClientFixture，不在这个文件重复。
func storageBlob(t *testing.T, host string) string {
	t.Helper()
	plain, err := json.Marshal(map[string]any{
		"token": "tk", "refreshToken": "rt", "expiredAt": "2026-10-19T13:30:02.228Z",
		"userId": "7508573128935932946", "host": host,
	})
	if err != nil {
		t.Fatal(err)
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	salt := tcSaltPair()
	h1 := sha512.Sum512(random)
	h2 := sha512.Sum512(append(h1[:], salt[:]...))
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(append([]byte{}, plain...), bytesN(byte(pad), pad)...)
	sum := sha512.Sum512(plain)
	body := append(sum[:], padded...)
	block, err := aes.NewCipher(h2[:16])
	if err != nil {
		t.Fatal(err)
	}
	ct := make([]byte, len(body))
	cipher.NewCBCEncrypter(block, h2[16:32]).CryptBlocks(ct, body)
	blob := append([]byte{'t', 'c', 0x05, 0x10, 0x00, 0x00}, random...)
	blob = append(blob, ct...)
	return base64.StdEncoding.EncodeToString(blob)
}

// tcSaltPair 复取 auth 包使用的 salt 组合：AES 形态 = 表 A 异或表 B。
// 夹具与解密实现互逆即可跑通；若那边换了盐，用例会红在这里而不是静默跳过。
func tcSaltPair() []byte {
	return saltAB()
}

func saltAB() []byte {
	a := []byte{
		82, 9, 106, 213, 48, 54, 165, 56, 191, 64, 163, 158, 129, 243, 215, 251,
		124, 227, 57, 130, 155, 47, 255, 135, 52, 142, 67, 68, 196, 222, 233, 203,
		84, 123, 148, 50, 166, 194, 35, 61, 238, 76, 149, 11, 66, 250, 195, 78,
		8, 46, 161, 102, 40, 217, 36, 178, 118, 91, 162, 73, 109, 139, 209, 37,
	}
	b := []byte{
		31, 221, 168, 51, 136, 7, 199, 49, 177, 18, 16, 89, 39, 128, 236, 95,
		96, 81, 127, 169, 25, 181, 74, 13, 45, 229, 122, 159, 147, 201, 156, 239,
		160, 224, 59, 77, 174, 42, 245, 176, 200, 235, 187, 60, 131, 83, 153, 97,
		23, 43, 4, 126, 186, 119, 214, 38, 225, 105, 20, 99, 85, 33, 12, 125,
	}
	out := make([]byte, 64)
	for i := range out {
		out[i] = a[i] ^ b[i]
	}
	return out
}

func bytesN(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

func importStorageFixture(t *testing.T, host string) []byte {
	t.Helper()
	doc := map[string]any{
		"iCubeAuthInfo://icube.cloudide":             storageBlob(t, host),
		"telemetry.machineId":                        strings.Repeat("b", 64),
		"iCubeAuthInfo://icube-dc:75085735239975091": "ignored",
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func importHandler(ed upstream.Edition, dir string) *Handler {
	p := pool.New("")
	return NewHandler(Config{
		Pool:     p,
		Upstream: upstream.New(ed),
		AuthDir:  dir,
	})
}

func postImport(t *testing.T, h *Handler, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/admin/api/accounts/import", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestImportStorageJSONKeepsItsOwnHost(t *testing.T) {
	for _, tc := range []struct {
		name       string
		ed         upstream.Edition
		blobHost   string
		wantApi    string
		wantDomain string
	}{
		{"intl 进程 + SG 凭据", upstream.EditionIntl(), "https://growsg-normal.trae.ai", "https://growsg-normal.trae.ai", "trae.ai"},
		// 关键反例：把国际版凭据塞进国内版进程也不能把它改写成国内 host——
		// 那样 ExchangeToken 会打 api.trae.com.cn，刷新静默失败而账号仍在池里。
		{"cn 进程 + SG 凭据（凭据赢）", upstream.EditionCN(), "https://growsg-normal.trae.ai", "https://growsg-normal.trae.ai", "trae.ai"},
		{"intl 进程 + 无 host 的凭据 JSON（区域默认值兜底）", upstream.EditionIntl(), "", upstream.EditionIntl().OAuthHost, "trae.ai"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "auths")
			h := importHandler(tc.ed, dir)
			var body []byte
			if tc.blobHost != "" {
				body, _ = json.Marshal(map[string]any{"json": string(importStorageFixture(t, tc.blobHost))})
			} else {
				body, _ = json.Marshal(map[string]any{"json": `{"accessToken":"tk","uid":"u9","machineId":"m","deviceId":"d"}`})
			}
			rec := postImport(t, h, body)
			if rec.Code != http.StatusOK {
				t.Fatalf("import http=%d body=%s", rec.Code, rec.Body.String())
			}
			// 直接读落盘文件，验证进池的凭据本身，而不是响应里回显的摘要。
			files, _ := filepath.Glob(filepath.Join(dir, "trae-*.json"))
			if len(files) != 1 {
				t.Fatalf("落盘文件数 = %d, want 1 (%v)", len(files), files)
			}
			raw, err := os.ReadFile(files[0])
			if err != nil {
				t.Fatalf("read saved file: %v", err)
			}
			var nested struct {
				Auth struct {
					ApiHost string `json:"apiHost"`
					Domain  string `json:"domain"`
				} `json:"auth"`
			}
			if err := json.Unmarshal(raw, &nested); err != nil {
				t.Fatalf("parse saved file: %v", err)
			}
			if nested.Auth.ApiHost != tc.wantApi {
				t.Errorf("apiHost = %q, want %q", nested.Auth.ApiHost, tc.wantApi)
			}
			if nested.Auth.Domain != tc.wantDomain {
				t.Errorf("domain = %q, want %q", nested.Auth.Domain, tc.wantDomain)
			}
		})
	}
}
