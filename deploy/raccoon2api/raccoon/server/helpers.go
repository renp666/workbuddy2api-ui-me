package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
)

const maxDecodeBody = 1 << 20

// decodeBody 严格读取并解码一个 JSON 请求体；超限或非法返回错误（调用方转 400）。
func decodeBody(r *http.Request, v any) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxDecodeBody+1))
	if err != nil {
		return err
	}
	if len(raw) > maxDecodeBody {
		return errTooLarge
	}
	return json.Unmarshal(raw, v)
}

// decodeBodyOptional 允许空体：空体时不做任何事返回 nil。
func decodeBodyOptional(r *http.Request, v any) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxDecodeBody+1))
	if err != nil {
		return err
	}
	if len(raw) > maxDecodeBody {
		return errTooLarge
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	return json.Unmarshal(raw, v)
}

type constError string

func (e constError) Error() string { return string(e) }

const errTooLarge = constError("request body too large")

func mkdirAll(dir string) error { return os.MkdirAll(dir, 0o700) }

func writeJSON(w http.ResponseWriter, status int, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		raw = []byte(`{"error":{"message":"marshal_error","type":"api_error","code":"marshal_error"}}`)
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(raw, '\n'))
}

// writeOpenAIError 写 OpenAI 形状的错误包；控制台与客户端都按这个形状解析。
func writeOpenAIError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    "api_error",
			"code":    code,
		},
	})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func prefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
