// storage.go 解析 Trae 桌面客户端的 globalStorage/storage.json，把登录态取成 Auth。
//
// 为什么需要它：国际版没有每日签到、也拿不到国内版那套"手机号+验证码"网页登录，
// 而装了 Trae/Trae SOLO 国际版客户端的机器上，登录态本来就在本地这个文件里。
// 容器读不到宿主 %APPDATA%，所以入口是管理页签粘贴文件内容 → 本函数解密。
//
// 密文形状（对客户端本地文件实测，2026-10-16）：
//
//	base64( [6B header][32B random][AES-128-CBC( SHA512(plain) || PKCS7(plain) )] )
//	key/iv = SHA512( SHA512(random) || salt )[0:16] / [16:32]
//	salt   = XOR(四个 64B 常量表中的两个)：header "tc"+05 10 00 00 用前一对，
//	         header 12 39 20 20 02 03 用后一对
//
// 四个常量表是从客户端自带前端 JS 里取出的固定值（区域无关，国际版同一套），
// 不是本项目自造的密钥；写死在此处是因为它们就是格式的一部分。
package auth

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

var tcSaltA = [64]byte{
	82, 9, 106, 213, 48, 54, 165, 56, 191, 64, 163, 158, 129, 243, 215, 251,
	124, 227, 57, 130, 155, 47, 255, 135, 52, 142, 67, 68, 196, 222, 233, 203,
	84, 123, 148, 50, 166, 194, 35, 61, 238, 76, 149, 11, 66, 250, 195, 78,
	8, 46, 161, 102, 40, 217, 36, 178, 118, 91, 162, 73, 109, 139, 209, 37,
}

var tcSaltB = [64]byte{
	31, 221, 168, 51, 136, 7, 199, 49, 177, 18, 16, 89, 39, 128, 236, 95,
	96, 81, 127, 169, 25, 181, 74, 13, 45, 229, 122, 159, 147, 201, 156, 239,
	160, 224, 59, 77, 174, 42, 245, 176, 200, 235, 187, 60, 131, 83, 153, 97,
	23, 43, 4, 126, 186, 119, 214, 38, 225, 105, 20, 99, 85, 33, 12, 125,
}

var tcSaltC = [64]byte{
	191, 192, 216, 250, 122, 246, 220, 97, 31, 254, 98, 27, 8, 72, 71, 176,
	135, 99, 96, 18, 127, 101, 203, 104, 211, 102, 191, 125, 37, 72, 150, 156,
	51, 229, 121, 35, 17, 153, 141, 177, 110, 131, 150, 128, 172, 255, 254, 6,
	18, 140, 55, 62, 236, 249, 135, 64, 135, 12, 117, 4, 89, 149, 168, 209,
}

var tcSaltD = [64]byte{
	246, 204, 26, 232, 232, 70, 129, 109, 223, 146, 169, 242, 23, 241, 105, 145,
	50, 196, 165, 42, 254, 120, 3, 54, 244, 207, 209, 85, 53, 6, 138, 106,
	175, 148, 31, 204, 186, 186, 165, 182, 87, 142, 49, 10, 39, 110, 26, 154,
	86, 56, 173, 125, 18, 64, 198, 225, 99, 99, 83, 82, 191, 134, 76, 170,
}

const (
	storageKeyAuth  = "iCubeAuthInfo://icube.cloudide"
	storageKeyDcPfx = "iCubeAuthInfo://icube-dc:"
	storageKeyMach  = "telemetry.machineId"
)

// xorBytes 对两个等长表逐字节异或，得到格式真正使用的 salt。
func xorBytes(a, b *[64]byte) [64]byte {
	var out [64]byte
	for i := range out {
		out[i] = a[i] ^ b[i]
	}
	return out
}

// decryptTC 解开单个 tc 形态的 base64 值。
func decryptTC(value string) ([]byte, error) {
	buf, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return nil, fmt.Errorf("storage_parse_error: base64: %w", err)
	}
	if len(buf) < 6+32+aes.BlockSize {
		return nil, fmt.Errorf("storage_parse_error: tc blob too short (%d bytes)", len(buf))
	}
	header := buf[:6]
	random := buf[6 : 32+6]
	ct := buf[38:]

	var salt [64]byte
	switch {
	case header[0] == 't' && header[1] == 'c' && header[2] == 0x05 && header[3] == 0x10:
		salt = xorBytes(&tcSaltA, &tcSaltB)
	case bytes.Equal(header, []byte{18, 57, 32, 32, 2, 3}):
		salt = xorBytes(&tcSaltC, &tcSaltD)
	default:
		return nil, fmt.Errorf("storage_parse_error: unknown tc header %x", header)
	}
	if len(ct)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("storage_parse_error: ciphertext not block-aligned (%d)", len(ct))
	}

	h1 := sha512.Sum512(random)
	h2 := sha512.Sum512(append(h1[:], salt[:]...))
	block, err := aes.NewCipher(h2[:16])
	if err != nil {
		return nil, err
	}
	pt := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, h2[16:32]).CryptBlocks(pt, ct)

	storedHash := pt[:sha512.Size]
	body, err := pkcs7Unpad(pt[sha512.Size:])
	if err != nil {
		return nil, err
	}
	if got := sha512.Sum512(body); !constantTimeEqual(got[:], storedHash) {
		return nil, fmt.Errorf("decrypt_failed: hash mismatch (wrong salt or corrupted blob)")
	}
	return body, nil
}

func pkcs7Unpad(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("decrypt_failed: empty plaintext")
	}
	pad := int(data[len(data)-1])
	if pad <= 0 || pad > aes.BlockSize || pad > len(data) {
		return nil, fmt.Errorf("decrypt_failed: bad pkcs7 padding length %d", pad)
	}
	for _, b := range data[len(data)-pad:] {
		if int(b) != pad {
			return nil, fmt.Errorf("decrypt_failed: bad pkcs7 padding content")
		}
	}
	return data[:len(data)-pad], nil
}

func constantTimeEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

// ParseStorageJSON 从桌面客户端 storage.json 原文提取一个账号。
//
// 只依赖四个字段：登录态（iCubeAuthInfo://icube.cloudide）、machineId、dc 设备数字 ID，
// 以及解出的 JSON 里的 host/userId/token 系列。host 落进 ApiHost，让 ExchangeToken 与
// GetUserInfo 打到账号自己所属的区域主机（国际版是 growsg-normal.trae.ai 一族），
// 不回落到进程级默认值——这是"一份凭据只会刷新它自己的区域"的根据。
func ParseStorageJSON(raw []byte) (*Auth, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("storage_parse_error: %w", err)
	}
	encBlob := func(key string) (string, error) {
		var s string
		if err := json.Unmarshal(doc[key], &s); err != nil {
			return "", fmt.Errorf("storage_parse_error: %s is not a string", key)
		}
		if strings.TrimSpace(s) == "" {
			return "", fmt.Errorf("parse_error: %s missing or empty（客户端未登录？）", key)
		}
		return s, nil
	}

	enc, err := encBlob(storageKeyAuth)
	if err != nil {
		return nil, err
	}
	plain, err := decryptTC(enc)
	if err != nil {
		return nil, err
	}
	var sa struct {
		Token          string `json:"token"`
		RefreshToken   string `json:"refreshToken"`
		ExpiredAt      string `json:"expiredAt"`
		RefreshExpireT string `json:"refreshExpiredAt"`
		UserID         string `json:"userId"`
		Host           string `json:"host"`
		Account        struct {
			Username string `json:"username"`
		} `json:"account"`
		UserRegion struct {
			Region string `json:"region"`
		} `json:"userRegion"`
	}
	if err := json.Unmarshal(plain, &sa); err != nil {
		return nil, fmt.Errorf("storage_parse_error: decoded auth is not JSON: %w", err)
	}
	if strings.TrimSpace(sa.Token) == "" {
		return nil, fmt.Errorf("parse_error: storage.json 里没有 accessToken")
	}
	if strings.TrimSpace(sa.UserID) == "" {
		return nil, fmt.Errorf("parse_error: storage.json 里没有 userId，无法作为池键")
	}

	a := &Auth{
		AccessToken:  sa.Token,
		RefreshToken: sa.RefreshToken,
		ExpiresAt:    parseRFC3339OrZero(sa.ExpiredAt),
		ApiHost:      strings.TrimRight(strings.TrimSpace(sa.Host), "/"),
		UID:          sa.UserID,
		Nickname:     sa.Account.Username,
	}
	if a.ApiHost != "" {
		a.Domain = domainOfHost(a.ApiHost)
	}
	if r := strings.TrimSpace(sa.UserRegion.Region); r != "" && a.Nickname == "" {
		a.Nickname = "trae-" + r // 无用户名时用区域标记，面板上至少能认出这是哪个区域的号
	}

	if m, err := encBlob(storageKeyMach); err == nil {
		a.MachineID = strings.TrimSpace(m)
	}
	// 设备数字 ID 是键名本身（iCubeAuthInfo://icube-dc:<deviceId>），值里是 EC 密钥对，
	// 对话请求只需要那个 ID。找不到不致命：缺 X-Device-Id 时上游按 machine id 归因。
	for k := range doc {
		if id := strings.TrimPrefix(k, storageKeyDcPfx); id != k {
			a.DeviceID = strings.TrimSpace(id)
			break
		}
	}
	return a, nil
}

// LooksLikeStorageJSON 判别导入体是不是客户端 storage.json（而不是凭据 JSON）。
// 两个条件同时成立才算：顶层有 iCubeAuthInfo 命名的键，且有 telemetry.machineId。
func LooksLikeStorageJSON(raw []byte) bool {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return false
	}
	_, hasAuth := doc[storageKeyAuth]
	_, hasMach := doc[storageKeyMach]
	return hasAuth && hasMach
}

// parseRFC3339OrZero 解析 RFC3339（含毫秒）为 Unix 秒；解析不出来返回 0，
// 由 NeedsRefresh 判成"需要刷新"，而不是拿一个猜测的时间假装 token 还有效。
func parseRFC3339OrZero(v string) int64 {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.Unix()
		}
	}
	return 0
}

func domainOfHost(host string) string {
	h := strings.ToLower(host)
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	if i := strings.IndexAny(h, "/:"); i >= 0 {
		h = h[:i]
	}
	if j := strings.Index(h, "."); j >= 0 {
		return h[j+1:] // growsg-normal.trae.ai → trae.ai
	}
	return h
}
