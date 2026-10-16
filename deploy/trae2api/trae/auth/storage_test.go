package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// encryptTCForTest 是与 decryptTC 互逆的夹具生成器（只服务测试，不进生产代码）。
//
// 注意它的证明边界：往返只能证明"我的加解密自洽"，不能证明"我解得开真客户端写的文件"。
// 后者由 TestParseStorageJSONRealClientFixture 用真文件锚定（默认 skip，见其说明）。
func encryptTCForTest(t *testing.T, plain []byte, privateForm bool) string {
	t.Helper()
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	var salt [64]byte
	header := []byte{'t', 'c', 0x05, 0x10, 0x00, 0x00}
	if privateForm {
		salt = xorBytes(&tcSaltC, &tcSaltD)
		header = []byte{18, 57, 32, 32, 2, 3}
	} else {
		salt = xorBytes(&tcSaltA, &tcSaltB)
	}
	h1 := sha512.Sum512(random)
	h2 := sha512.Sum512(append(h1[:], salt[:]...))

	pad := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append([]byte{}, plain...)
	padded = append(padded, bytes_repeat(byte(pad), pad)...)
	sum := sha512.Sum512(plain)
	body := append(sum[:], padded...)

	block, err := aes.NewCipher(h2[:16])
	if err != nil {
		t.Fatal(err)
	}
	ct := make([]byte, len(body))
	cipher.NewCBCEncrypter(block, h2[16:32]).CryptBlocks(ct, body)

	var blob []byte
	blob = append(blob, header...)
	blob = append(blob, random...)
	blob = append(blob, ct...)
	return base64.StdEncoding.EncodeToString(blob)
}

func bytes_repeat(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

func storageFixture(t *testing.T, privateForm bool) []byte {
	t.Helper()
	authJSON := map[string]any{
		"token":            "Cloud-IDE-JWT-VALUE",
		"refreshToken":     "rt-value",
		"expiredAt":        "2026-10-19T13:30:02.228Z",
		"refreshExpiredAt": "2027-04-03T13:30:02.228Z",
		"userId":           "7508573128935932946",
		"host":             "https://growsg-normal.trae.ai",
		"account":          map[string]any{"username": "row-user"},
		"userRegion":       map[string]any{"region": "SG"},
	}
	raw, err := json.Marshal(authJSON)
	if err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{
		storageKeyAuth:                          encryptTCForTest(t, raw, privateForm),
		storageKeyMach:                          strings.Repeat("a", 64),
		storageKeyDcPfx + "7508573523997509121": encryptTCForTest(t, []byte(`{"publicKeyPEM":"x"}`), false),
		"windowsState":                          map[string]any{"whatever": true},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseStorageJSONRoundTrip(t *testing.T) {
	a, err := ParseStorageJSON(storageFixture(t, false))
	if err != nil {
		t.Fatalf("ParseStorageJSON: %v", err)
	}
	if a.AccessToken != "Cloud-IDE-JWT-VALUE" || a.RefreshToken != "rt-value" {
		t.Errorf("token 解错: %+v", a)
	}
	if a.UID != "7508573128935932946" {
		t.Errorf("UID = %q（池键必须是上游 userId）", a.UID)
	}
	// host 必须原样带进 ApiHost：国际版凭据若回落到进程默认 OAuthHost，
	// ExchangeToken 会打到别的区域主机上去。
	if a.ApiHost != "https://growsg-normal.trae.ai" {
		t.Errorf("ApiHost = %q", a.ApiHost)
	}
	if a.Domain != "trae.ai" {
		t.Errorf("Domain = %q, want trae.ai", a.Domain)
	}
	want := time.Date(2026, 10, 19, 13, 30, 2, 0, time.UTC).Unix()
	if a.ExpiresAt != want {
		t.Errorf("ExpiresAt = %d, want %d（RFC3339 带毫秒要能解）", a.ExpiresAt, want)
	}
	if a.MachineID != strings.Repeat("a", 64) {
		t.Errorf("MachineID = %q", a.MachineID)
	}
	// 设备数字 ID 在键名里，不在值里。
	if a.DeviceID != "7508573523997509121" {
		t.Errorf("DeviceID = %q", a.DeviceID)
	}
	if a.Nickname != "row-user" {
		t.Errorf("Nickname = %q", a.Nickname)
	}
}

func TestParseStorageJSONPrivateForm(t *testing.T) {
	a, err := ParseStorageJSON(storageFixture(t, true))
	if err != nil {
		t.Fatalf("AES_PRIVATE 头形态没被认出来: %v", err)
	}
	if a.AccessToken != "Cloud-IDE-JWT-VALUE" {
		t.Errorf("token = %q", a.AccessToken)
	}
}

func TestParseStorageJSONRejectsGarbage(t *testing.T) {
	cases := map[string][]byte{
		"not json":            []byte("hello"),
		"no auth key":         []byte(`{"telemetry.machineId":"x"}`),
		"auth not base64":     []byte(`{"` + storageKeyAuth + `":"@@@","` + storageKeyMach + `":"x"}`),
		"auth too short":      []byte(`{"` + storageKeyAuth + `":"aGVsbG8=","` + storageKeyMach + `":"x"}`),
		"unknown header form": []byte(`{"` + storageKeyAuth + `":"` + base64.StdEncoding.EncodeToString(make([]byte, 80)) + `","` + storageKeyMach + `":"x"}`),
	}
	for name, raw := range cases {
		if _, err := ParseStorageJSON(raw); err == nil {
			t.Errorf("%s: 应报错而不是返回一个空账号", name)
		}
	}
}

// TestParseStorageJSONUnparsableExpiry 钉住"解不出过期时间就标成需要刷新"：
// 拿 0 冒充一个未来时间会让账号看起来"还有一个多月有效期"，调度器从此不再刷新。
func TestParseStorageJSONUnparsableExpiry(t *testing.T) {
	doc := map[string]any{}
	raw, _ := json.Marshal(map[string]any{
		"token": "tk", "refreshToken": "rt", "expiredAt": "明天吧", "userId": "u1",
	})
	doc[storageKeyAuth] = encryptTCForTest(t, raw, false)
	doc[storageKeyMach] = "m"
	b, _ := json.Marshal(doc)
	a, err := ParseStorageJSON(b)
	if err != nil {
		t.Fatal(err)
	}
	if a.ExpiresAt != 0 {
		t.Errorf("ExpiresAt = %d, want 0", a.ExpiresAt)
	}
	if !a.NeedsRefresh(time.Hour) {
		t.Error("过期时间未知必须判成需要刷新")
	}
}

func TestLooksLikeStorageJSON(t *testing.T) {
	if !LooksLikeStorageJSON(storageFixture(t, false)) {
		t.Error("真 storage.json 形态没被认出")
	}
	// 凭据 JSON 两种形态都不能被误判成 storage.json，否则 ParseStorageJSON 会去解一个根本不是密文的值。
	for _, s := range []string{
		`{"auth":{"accessToken":"t"},"account":{"uid":"u"}}`,
		`{"accessToken":"t","uid":"u"}`,
		`{"iCubeAuthInfo://icube.cloudide":"x"}`, // 只有 auth 键、没有 machineId
	} {
		if LooksLikeStorageJSON([]byte(s)) {
			t.Errorf("凭据 JSON 被误判成 storage.json: %s", s)
		}
	}
}

// TestParseStorageJSONRealClientFixture 用真桌面客户端的 storage.json 锚定算法。
//
// 默认 skip：真文件里是活凭据，绝不能进仓库。开发机上这样跑（只读，不外传）：
//
//	WB2A_TRAE_STORAGE_FIXTURE="$APPDATA/TRAE SOLO/User/globalStorage/storage.json" \
//	  go test ./trae/auth -run RealClientFixture -v
//
// 往返用例自洽不等于解得开真文件——这个用例存在的意义就是把那一步补上。
func TestParseStorageJSONRealClientFixture(t *testing.T) {
	path := strings.TrimSpace(os.Getenv("WB2A_TRAE_STORAGE_FIXTURE"))
	if path == "" {
		t.Skip("WB2A_TRAE_STORAGE_FIXTURE 未设置（真凭据不入库）")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读夹具失败: %v", err)
	}
	a, err := ParseStorageJSON(raw)
	if err != nil {
		t.Fatalf("真客户端 storage.json 解不开，算法与线上形态不符: %v", err)
	}
	if a.AccessToken == "" || a.UID == "" {
		t.Fatalf("解开了但字段空: uid=%q tokenLen=%d", a.UID, len(a.AccessToken))
	}
	t.Logf("fixture ok: uid=%s… host=%s domain=%s machine=%s… device=%s expiresAt=%d refreshNeeded=%v",
		a.UID[:4], a.ApiHost, a.Domain, short(a.MachineID), a.DeviceID, a.ExpiresAt, a.NeedsRefresh(time.Hour))
}

func short(s string) string {
	if len(s) > 6 {
		return s[:6]
	}
	return s
}
