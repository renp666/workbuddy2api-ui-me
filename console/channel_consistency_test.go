package main

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// channel_consistency_test.go 守卫「前端名单」与「后端真源」不漂移。
//
// 背景（2026-10-08 侦察确认）：探测白名单与通道顺序各有两个真源——
// 后端 probe.go 的 probeableChannels / route.go 的 routeChannelOrder，
// 前端 web/app.js 的 probeChannelNames / usageChannelOrder。二者被要求同口径，
// 但此前没有任何测试把两处绑在一起：web_test.cjs 只从 probeChannelNames 现读名单
// 去核对 HTML 按钮是否存在，从不回头看后端。于是两处漂移时测试全绿，这正是
// trae/autoclaw 接入时「公共面漏改」的未修复根因。
//
// 这里从内嵌的 web/app.js 现读字面量，与后端真源逐项比对，任一真源漂移即红。

// TestProbeChannelWhitelistMatchesFrontend 对应计划书 §6.4 G-1。
// probeableChannels 与 probeChannelNames 的值集合必须相等，且都必须排除 opencode
// （它由 OW Bridge 自带探测，页面上不该有手工探测按钮）。
func TestProbeChannelWhitelistMatchesFrontend(t *testing.T) {
	js := readWebAppJS(t)
	front := jsObjectLiteralValues(t, js, "probeChannelNames")
	back := make([]string, 0, len(probeableChannels))
	for _, c := range probeableChannels {
		back = append(back, string(c))
	}

	sortedFront := append([]string(nil), front...)
	sortedBack := append([]string(nil), back...)
	sort.Strings(sortedFront)
	sort.Strings(sortedBack)
	if strings.Join(sortedFront, ",") != strings.Join(sortedBack, ",") {
		t.Fatalf("探测白名单漂移：前端 probeChannelNames 值 = %v，后端 probeableChannels = %v", sortedFront, sortedBack)
	}

	if containsChannel(probeableChannels, channelOpenCode) {
		t.Fatalf("probeableChannels 不应包含 opencode：它由 OW Bridge 自带探测")
	}
	for _, v := range front {
		if v == string(channelOpenCode) {
			t.Fatalf("probeChannelNames 不应包含 opencode：页面上不该有它的手工探测按钮")
		}
	}
}

// TestRouteChannelOrderMatchesFrontend 对应计划书 §6.4 G-2。
// routeChannelOrder 与 usageChannelOrder 必须集合与顺序一致；前端账本口径的
// workbuddy/zcode 与后端通道 core/glm 指同一条链路。
func TestRouteChannelOrderMatchesFrontend(t *testing.T) {
	js := readWebAppJS(t)
	front := jsArrayLiteralStrings(t, js, "usageChannelOrder")
	back := make([]string, 0, len(routeChannelOrder))
	for _, c := range routeChannelOrder {
		back = append(back, string(c))
	}
	if len(front) != len(back) {
		t.Fatalf("通道数量漂移：前端 usageChannelOrder = %v（%d 项），后端 routeChannelOrder = %v（%d 项）",
			front, len(front), back, len(back))
	}
	// 前端账本名 → 后端通道名，只在这一处翻译。
	alias := map[string]routeChannel{"workbuddy": channelCore, "zcode": channelGLM}
	for i, name := range front {
		mapped := name
		if c, ok := alias[name]; ok {
			mapped = string(c)
		}
		if mapped != back[i] {
			t.Fatalf("通道顺序漂移：第 %d 项前端 %q 应对应后端 %q，实际后端为 %q", i, name, mapped, back[i])
		}
	}
}

// readWebAppJS 读取内嵌的 web/app.js 源码文本。用内嵌 FS 而非磁盘路径，
// 与生产同源，避免测试读到未打包的旧副本。
func readWebAppJS(t *testing.T) string {
	t.Helper()
	raw, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("读取内嵌 web/app.js 失败：%v", err)
	}
	return string(raw)
}

// jsObjectLiteralValues 从 `const <name> = {k:v,...}` 抽出全部值（按源码顺序）。
func jsObjectLiteralValues(t *testing.T, js, name string) []string {
	t.Helper()
	re := regexp.MustCompile(`const\s+` + regexp.QuoteMeta(name) + `\s*=\s*\{([^}]*)\}`)
	m := re.FindStringSubmatch(js)
	if m == nil {
		t.Fatalf("app.js 里找不到 const %s 的对象字面量（锚点被改？）", name)
	}
	out := []string{}
	for _, pair := range strings.Split(m[1], ",") {
		parts := strings.SplitN(pair, ":", 2)
		if len(parts) != 2 {
			continue
		}
		v := strings.Trim(strings.TrimSpace(parts[1]), `'"`)
		if v != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		t.Fatalf("const %s 解析出空名单", name)
	}
	return out
}

// jsArrayLiteralStrings 从 `const <name> = ['a','b',...]` 抽出全部字符串项。
func jsArrayLiteralStrings(t *testing.T, js, name string) []string {
	t.Helper()
	re := regexp.MustCompile(`const\s+` + regexp.QuoteMeta(name) + `\s*=\s*\[([^\]]*)\]`)
	m := re.FindStringSubmatch(js)
	if m == nil {
		t.Fatalf("app.js 里找不到 const %s 的数组字面量（锚点被改？）", name)
	}
	out := []string{}
	for _, item := range strings.Split(m[1], ",") {
		v := strings.Trim(strings.TrimSpace(item), `'"`)
		if v != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		t.Fatalf("const %s 解析出空名单", name)
	}
	return out
}

func containsChannel(list []routeChannel, want routeChannel) bool {
	for _, c := range list {
		if c == want {
			return true
		}
	}
	return false
}
