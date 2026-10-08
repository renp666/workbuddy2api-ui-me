package upstream

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
)

const sseBufSize = 64 << 10

// RewriteModelInSSE 把上游 SSE 里的 model 字段换回客户端请求的名字，其余字节原样透传。
// 上游会把响应里的 model 改成它自己的内部名，不回写会让客户端看到模型名漂移。
// model 为空时退化为纯透传。
func RewriteModelInSSE(dst io.Writer, src io.Reader, model string) error {
	if model == "" {
		_, err := io.Copy(dst, src)
		return err
	}
	br := bufio.NewReaderSize(src, sseBufSize)
	bw := bufio.NewWriter(dst)
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			if _, werr := bw.WriteString(rewriteSSELine(line, model)); werr != nil {
				return werr
			}
			// 逐行 flush：流式增量必须立刻可见，不能等缓冲满。
			if werr := bw.Flush(); werr != nil {
				return werr
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func rewriteSSELine(line, model string) string {
	trimmed := strings.TrimRight(line, "\r\n")
	body := strings.TrimLeft(trimmed, "data:")
	body = strings.TrimLeft(body, " ")
	if body == "" || body == "[DONE]" {
		return line
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(body), &obj); err != nil {
		return line
	}
	if s, _ := obj["model"].(string); s == model {
		return line
	}
	obj["model"] = model
	raw, err := json.Marshal(obj)
	if err != nil {
		return line
	}
	return "data: " + string(raw) + line[len(trimmed):]
}

// RewriteModelInJSON 回写非流式响应里的 model 字段；解析失败时原样返回。
func RewriteModelInJSON(raw []byte, model string) []byte {
	if model == "" {
		return raw
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return raw
	}
	if s, _ := obj["model"].(string); s == model {
		return raw
	}
	obj["model"] = model
	out, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return out
}
