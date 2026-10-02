package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// 本文件测**控制台的资源与消息表**。
//
// 它是 internal/api 下的第一组测试。之所以现在才写：控制台的消息表原先
// 是硬编码在三个前端文件里的，没有可断言的服务端行为。加了
// `/console/i18n.json` 之后，路由能不能命中、语言对不对，都成了
// **可以测的东西** —— 而这两点都出过错。

// consoleTestServer 构造一个只挂了控制台路由的服务。
func consoleTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	s := New(Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Token: "test-token"})
	mux := http.NewServeMux()
	s.MountConsole(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestConsoleI18nRouteIsReachable 是这一组里最重要的一条。
//
// 路由注册的顺序是 `/console/`（子树）在前、`/console/i18n.json`（字面量）
// 在后。Go 1.22 的 ServeMux 按"最具体者胜"选，因此字面量应当赢 ——
// 但那是**规则**，而这台路由器上还有一层 `http.FileServer`：
// 如果命中了子树，请求会被当成静态文件，返回 404 而不是消息表。
//
// 换句话说：推理说它对，而实测才作数。
func TestConsoleI18nRouteIsReachable(t *testing.T) {
	t.Parallel()

	srv := consoleTestServer(t)

	resp, err := http.Get(srv.URL + "/console/i18n.json")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200 —— "+
			"命中子树的话会走 FileServer 并返回 404", resp.StatusCode)
	}
	// 只要求是 JSON：writeJSON 会带上 `; charset=utf-8`，
	// 而断言完整相等会把那个后缀当成失败。
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q，期望 application/json 开头", ct)
	}
	// 一次性内容，不该被缓存。
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q，期望 no-store", cc)
	}

	var msgs map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&msgs); err != nil {
		t.Fatalf("响应不是合法的 JSON: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("消息表是空的 —— 前端的 data-i18n 会全部落空")
	}
}

// TestConsoleI18nFollowsKernelLanguage 验证语言跟随内核设置。
func TestConsoleI18nFollowsKernelLanguage(t *testing.T) {
	// 不能用 t.Parallel：它改的是包级默认语言。
	defer i18n.SetDefault(i18n.ZhCN)

	srv := consoleTestServer(t)

	// 期望值从目录本身取，而不是写死字符串。
	//
	// 写死的话，每次改一句文案都要回来改测试 —— 而那种测试守不住任何东西：
	// 它只是把文案抄了一遍。
	zhTitle := i18n.ConsoleMessages(i18n.ZhCN)["web.title"]
	enTitle := i18n.ConsoleMessages(i18n.En)["web.title"]
	if zhTitle == enTitle || zhTitle == "" {
		t.Fatalf("两种语言的 web.title 不该相同或为空：zh=%q en=%q", zhTitle, enTitle)
	}

	for _, tc := range []struct {
		lang   i18n.Lang
		want   string
		absent string
	}{
		{i18n.ZhCN, zhTitle, enTitle},
		{i18n.En, enTitle, zhTitle},
	} {
		i18n.SetDefault(tc.lang)

		resp, err := http.Get(srv.URL + "/console/i18n.json")
		if err != nil {
			t.Fatal(err)
		}
		var msgs map[string]string
		if err := json.NewDecoder(resp.Body).Decode(&msgs); err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()

		if got := msgs["web.title"]; got != tc.want {
			t.Errorf("%v: web.title = %q，期望 %q", tc.lang, got, tc.want)
		}
		for _, v := range msgs {
			if v == tc.absent {
				t.Errorf("%v: 消息表里出现了另一种语言的文案 %q", tc.lang, v)
			}
		}
	}
}

// TestConsoleI18nCarriesNoInternalMessages 确认消息表**不含内核的错误文案**。
//
// ConsoleMessages 刻意只导出控制台那一层。把整个目录倒给浏览器不仅浪费
// 带宽，还会把数据库失败、迁移校验和不匹配这类内部信息送到页面里 ——
// 而页面是唯一一个在浏览器里跑的东西。
func TestConsoleI18nCarriesNoInternalMessages(t *testing.T) {
	t.Parallel()

	srv := consoleTestServer(t)

	resp, err := http.Get(srv.URL + "/console/i18n.json")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)

	for _, banned := range []string{
		"store.err", "secret.err", "acme.", "ddnsgo", "migration_changed",
	} {
		if containsString(text, banned) {
			t.Errorf("消息表里出现了 %q —— 前端用不到内核的错误文案，"+
				"送过去只是把它们暴露在浏览器里", banned)
		}
	}
}

func containsString(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
