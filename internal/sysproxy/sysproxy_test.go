package sysproxy

import (
	"net/http"
	"net/url"
	"testing"
)

// realScutilOutput 是这台机器上 `scutil --proxy` 的真实输出
// （Clash Verge 把系统代理设到 127.0.0.1:7897，带例外列表）。
// 用真实样本而不是手搓的样本，解析器才不会只对我们想象出的格式正确。
const realScutilOutput = `<dictionary> {
  ExceptionsList : <array> {
    0 : 127.0.0.1
    1 : 192.168.0.0/16
    2 : 10.0.0.0/8
    3 : 172.16.0.0/12
    4 : localhost
    5 : *.local
    6 : *.crashlytics.com
    7 : <local>
  }
  FTPPassive : 1
  HTTPEnable : 1
  HTTPPort : 7897
  HTTPProxy : 127.0.0.1
  HTTPSEnable : 1
  HTTPSPort : 7897
  HTTPSProxy : 127.0.0.1
  ProxyAutoConfigEnable : 0
  SOCKSEnable : 1
  SOCKSPort : 7897
  SOCKSProxy : 127.0.0.1
}`

func TestParseScutilProxyReadsTheRealOutput(t *testing.T) {
	cfg := parseScutilProxy(realScutilOutput)

	if cfg.https != "127.0.0.1" || cfg.httpsPort != "7897" {
		t.Fatalf("https 代理没解析出来: host=%q port=%q", cfg.https, cfg.httpsPort)
	}
	if cfg.http != "127.0.0.1" || cfg.httpPort != "7897" {
		t.Fatalf("http 代理没解析出来: host=%q port=%q", cfg.http, cfg.httpPort)
	}
	if cfg.socks != "127.0.0.1" || cfg.socksPort != "7897" {
		t.Fatalf("socks 代理没解析出来: host=%q port=%q", cfg.socks, cfg.socksPort)
	}
	if !cfg.httpsEnabled || !cfg.httpEnabled || !cfg.socksEnabled {
		t.Fatalf("启用标志应全部为 true: %+v", cfg)
	}
	if len(cfg.exceptions) != 8 {
		t.Fatalf("例外列表应有 8 条，实际 %d: %v", len(cfg.exceptions), cfg.exceptions)
	}
	if cfg.exceptions[0] != "127.0.0.1" || cfg.exceptions[6] != "*.crashlytics.com" {
		t.Fatalf("例外列表内容不对: %v", cfg.exceptions)
	}
}

func TestParseScutilProxyHonoursEnableFlags(t *testing.T) {
	const off = `<dictionary> {
  HTTPEnable : 0
  HTTPPort : 7897
  HTTPProxy : 127.0.0.1
  HTTPSEnable : 1
  HTTPSPort : 7897
  HTTPSProxy : 127.0.0.1
  SOCKSEnable : 0
  SOCKSPort : 1080
  SOCKSProxy : 127.0.0.1
}`
	cfg := parseScutilProxy(off)
	req, _ := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	if u, _ := pick(req, cfg); u != nil {
		t.Fatalf("HTTPEnable=0 时不该走 HTTP 代理，得到 %v", u)
	}
	req, _ = http.NewRequest(http.MethodGet, "https://example.com/", nil)
	u, _ := pick(req, cfg)
	if u == nil || u.Host != "127.0.0.1:7897" {
		t.Fatalf("HTTPSEnable=1 时应走 HTTPS 代理，得到 %v", u)
	}
}

func TestPickPrefersSchemeSpecificProxyOverSOCKS(t *testing.T) {
	cfg := parseScutilProxy(realScutilOutput)
	req, _ := http.NewRequest(http.MethodGet, "https://api.cloudflare.com/client/v4/", nil)
	u, err := pick(req, cfg)
	if err != nil {
		t.Fatalf("pick 出错: %v", err)
	}
	if u == nil {
		t.Fatal("https 请求应当走代理，结果却是直连")
	}
	if u.Scheme != "http" || u.Host != "127.0.0.1:7897" {
		t.Fatalf("代理地址不对: %s", u)
	}
}

func TestPickFallsBackToSOCKSWhenNoSchemeProxy(t *testing.T) {
	cfg := parseScutilProxy(`<dictionary> {
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : 127.0.0.1
}`)
	req, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	u, _ := pick(req, cfg)
	if u == nil || u.Scheme != "socks5" || u.Host != "127.0.0.1:1080" {
		t.Fatalf("应回落到 socks5，得到 %v", u)
	}
}

// 例外列表命中时必须直连 —— 否则本地服务（127.0.0.1）会被推给代理，
// 那会把"内核自己的回环请求"也绕一圈。
func TestPickRespectsExceptions(t *testing.T) {
	cfg := parseScutilProxy(realScutilOutput)
	cases := []struct {
		url       string
		wantProxy bool
	}{
		{"https://api.cloudflare.com/client/v4/", true},
		{"http://127.0.0.1:53518/v1/health", false},
		{"http://localhost:8080/x", false},
		{"https://printer.local/", false},
		{"https://api.crashlytics.com/x", false},
		{"https://192.168.1.10/", false},
		{"https://10.1.2.3/", false},
		{"https://11.1.2.3/", true},
	}
	for _, tc := range cases {
		req, err := http.NewRequest(http.MethodGet, tc.url, nil)
		if err != nil {
			t.Fatalf("构造请求失败 %s: %v", tc.url, err)
		}
		u, _ := pick(req, cfg)
		if got := u != nil; got != tc.wantProxy {
			t.Errorf("%s: 走代理=%v，期望 %v（得到 %v）", tc.url, got, tc.wantProxy, u)
		}
	}
}

// 后缀通配不能误伤 "notexample.com" 这类仅前缀相同的域名。
func TestBypassWildcardDoesNotMatchLookalikeDomain(t *testing.T) {
	cfg := config{exceptions: []string{"*.example.com"}}
	if cfg.bypass("notexample.com") {
		t.Fatal("*.example.com 不应命中 notexample.com")
	}
	if !cfg.bypass("a.example.com") {
		t.Fatal("*.example.com 应命中 a.example.com")
	}
	if !cfg.bypass("a.b.example.com") {
		t.Fatal("*.example.com 应命中多级子域 a.b.example.com")
	}
}

func TestBuildProxyURLHandlesHostAndPortForms(t *testing.T) {
	cases := []struct {
		host, port, fallback, want string
	}{
		{"127.0.0.1", "7897", "443", "127.0.0.1:7897"},
		{"127.0.0.1:7897", "", "443", "127.0.0.1:7897"},
		{"127.0.0.1", "", "443", "127.0.0.1:443"},
		{"::1", "7897", "443", "[::1]:7897"},
		{"", "7897", "443", ""},
	}
	for _, tc := range cases {
		u := buildProxyURL("http", tc.host, tc.port, tc.fallback)
		got := ""
		if u != nil {
			got = u.Host
		}
		if got != tc.want {
			t.Errorf("buildProxyURL(%q,%q,%q) = %q，期望 %q", tc.host, tc.port, tc.fallback, got, tc.want)
		}
	}
}

func TestParseScutilProxyOnGarbageIsEmpty(t *testing.T) {
	cfg := parseScutilProxy("No such key\n")
	if cfg.https != "" || cfg.http != "" || cfg.socks != "" {
		t.Fatalf("垃圾输入应解析出空配置，得到 %+v", cfg)
	}
	if u, _ := pick(&http.Request{URL: &url.URL{Scheme: "https", Host: "example.com"}}, cfg); u != nil {
		t.Fatalf("空配置应直连，得到 %v", u)
	}
}
