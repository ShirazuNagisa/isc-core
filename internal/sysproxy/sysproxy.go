// Package sysproxy 让内核的外发请求遵循操作系统的代理设置。
//
// 为什么需要这个包：Go 标准库只认 HTTP_PROXY/HTTPS_PROXY/NO_PROXY
// 环境变量，不读操作系统的代理配置。macOS 上的原生应用走的是系统代理，
// 用户对"我开了代理，应用就该走代理"也有同样的直觉。这两者的差距会
// 制造一类极难归因的故障：
//
//	用户开着代理软件（系统代理指向 127.0.0.1:7897），浏览器一切正常，
//	而内核直连出去被本地网络拦截/中间人，表现为 connection reset 或
//	证书不匹配。用户看到的是"凭据验证失败"，于是去怀疑凭据。
//
// 所以这里的原则是：**先看环境变量（标准库语义，显式覆盖），
// 没有再看系统代理**。环境变量优先是为了让脚本化部署能覆盖系统设置。
package sysproxy

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// config 是从系统代理设置里读出来的结果。
//
// 空字符串表示"这一项没有启用"，此时对应协议直连 —— 我们不会拿
// HTTP 代理去顶替 HTTPS 代理：macOS 的系统设置里两者是独立开关，
// 用户在浏览器上观察到的行为就是"只开 Web 代理时，https 走直连"。
type config struct {
	http         string
	httpPort     string
	httpEnabled  bool
	https        string
	httpsPort    string
	httpsEnabled bool
	socks        string
	socksPort    string
	socksEnabled bool
	// exceptions 是"例外列表"：命中这些主机时不走代理。
	exceptions []string
	// pacURL 非空表示用户配置的是自动代理（PAC）。
	// 我们无法执行 PAC 里的 JavaScript，只能忽略并如实上报。
	pacURL string
}

// pick 依据系统代理设置挑选代理；返回 nil 表示直连。
//
// 单独的纯函数，方便测试覆盖各种组合。
func pick(req *http.Request, cfg config) (*url.URL, error) {
	if req == nil || req.URL == nil {
		return nil, nil
	}
	if cfg.bypass(req.URL.Hostname()) {
		return nil, nil
	}
	switch req.URL.Scheme {
	case "https":
		if cfg.httpsEnabled {
			if u := cfg.proxyURL("http", cfg.https, cfg.httpsPort, "443"); u != nil {
				return u, nil
			}
		}
	case "http":
		if cfg.httpEnabled {
			if u := cfg.proxyURL("http", cfg.http, cfg.httpPort, "80"); u != nil {
				return u, nil
			}
		}
	}
	// SOCKS 是"全局"开关：只在对应协议没有专属代理时兜底。
	if cfg.socksEnabled {
		if u := cfg.proxyURL("socks5", cfg.socks, cfg.socksPort, "1080"); u != nil {
			return u, nil
		}
	}
	return nil, nil
}

// proxyURL 拼接代理地址；host 为空时返回 nil。
func (c config) proxyURL(scheme, host, port, fallbackPort string) *url.URL {
	return buildProxyURL(scheme, host, port, fallbackPort)
}

// buildProxyURL 拼接代理地址；host 为空时返回 nil。
func buildProxyURL(scheme, host, port, fallbackPort string) *url.URL {
	host = strings.TrimSpace(host)
	if host == "" {
		return nil
	}
	// 系统设置里主机和端口是分开两项，但也有人直接写成 host:port。
	if _, _, err := net.SplitHostPort(host); err != nil {
		p := strings.TrimSpace(port)
		if p == "" {
			p = fallbackPort
		}
		host = net.JoinHostPort(host, p)
	}
	return &url.URL{Scheme: scheme, Host: host}
}

// proxyHostPort 返回 host:port 形式，供日志展示。
func proxyHostPort(host, port, fallbackPort string) string {
	if u := buildProxyURL("http", host, port, fallbackPort); u != nil {
		return u.Host
	}
	return host
}

// proxyEnvNames 列出实际设置了值的代理环境变量名，没有则返回空串。
func proxyEnvNames() string {
	var set []string
	for _, name := range []string{
		"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy",
	} {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			set = append(set, name)
		}
	}
	return strings.Join(set, ",")
}

// describe 把系统代理配置渲染成人类可读的一行。
func describe(cfg config) string {
	var parts []string
	if cfg.httpsEnabled && cfg.https != "" {
		parts = append(parts, "https="+proxyHostPort(cfg.https, cfg.httpsPort, "443"))
	}
	if cfg.httpEnabled && cfg.http != "" {
		parts = append(parts, "http="+proxyHostPort(cfg.http, cfg.httpPort, "80"))
	}
	if cfg.socksEnabled && cfg.socks != "" {
		parts = append(parts, "socks="+proxyHostPort(cfg.socks, cfg.socksPort, "1080"))
	}
	if cfg.pacURL != "" {
		parts = append(parts, "pac="+cfg.pacURL)
	}
	if len(parts) == 0 {
		return "system(direct)"
	}
	return "system(" + strings.Join(parts, " ") + ")"
}

// bypass 判断某个主机是否命中例外列表。
//
// 只做字面匹配，不对主机名做 DNS 解析：例外列表里的 CIDR 只对
// "主机本身就是 IP" 的请求生效。为了判断例外去解析域名会引入
// 额外延迟和隐私问题，收益也不大。
func (c config) bypass(host string) bool {
	if host == "" {
		return false
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	ip := net.ParseIP(host)
	for _, raw := range c.exceptions {
		e := strings.ToLower(strings.TrimSpace(raw))
		if e == "" {
			continue
		}
		if e == "*" {
			return true
		}
		// macOS 的 <local> 指不含点的主机名（局域网内的短名）。
		if e == "<local>" {
			if !strings.Contains(host, ".") {
				return true
			}
			continue
		}
		if strings.Contains(e, "/") {
			if ip == nil {
				continue
			}
			if _, ipnet, err := net.ParseCIDR(e); err == nil && ipnet.Contains(ip) {
				return true
			}
			continue
		}
		if strings.HasPrefix(e, "*") {
			// 形如 *.example.com；去掉 * 后剩下的必须以 . 开头才算后缀匹配，
			// 否则 "*.example.com" 会误命中 "notexample.com"。
			suffix := strings.TrimPrefix(e, "*")
			if strings.HasPrefix(suffix, ".") && strings.HasSuffix(host, suffix) {
				return true
			}
			continue
		}
		if strings.HasPrefix(e, ".") {
			if strings.HasSuffix(host, e) {
				return true
			}
			continue
		}
		if e == host {
			return true
		}
	}
	return false
}

// parseScutilProxy 解析 `scutil --proxy` 的输出。
//
// 输出是旧式 plist 文本，形如：
//
//	<dictionary> {
//	  ExceptionsList : <array> {
//	    0 : 127.0.0.1
//	    1 : *.local
//	  }
//	  HTTPEnable : 1
//	  HTTPPort : 7897
//	  HTTPProxy : 127.0.0.1
//	  HTTPSEnable : 1
//	  HTTPSPort : 7897
//	  HTTPSProxy : 127.0.0.1
//	  ProxyAutoConfigEnable : 0
//	  SOCKSEnable : 1
//	  SOCKSPort : 7897
//	  SOCKSProxy : 127.0.0.1
//	}
//
// 我们只按行扫描，不引入 plist 解析器：需要的字段是扁平的，
// 多一层依赖不划算。enable 标志缺失时按"启用"处理 —— 地址为空
// 自然就不会产生代理，两种写法都能得到正确结果。
func parseScutilProxy(text string) config {
	cfg := config{httpEnabled: true, httpsEnabled: true, socksEnabled: true}
	inExceptions := false
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "":
			continue
		case line == "}":
			inExceptions = false
			continue
		}
		key, value, ok := strings.Cut(line, " : ")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if value == "<array> {" {
			inExceptions = key == "ExceptionsList"
			continue
		}
		if inExceptions {
			cfg.exceptions = append(cfg.exceptions, value)
			continue
		}
		switch key {
		case "HTTPProxy":
			cfg.http = value
		case "HTTPPort":
			cfg.httpPort = value
		case "HTTPEnable":
			cfg.httpEnabled = value == "1"
		case "HTTPSProxy":
			cfg.https = value
		case "HTTPSPort":
			cfg.httpsPort = value
		case "HTTPSEnable":
			cfg.httpsEnabled = value == "1"
		case "SOCKSProxy":
			cfg.socks = value
		case "SOCKSPort":
			cfg.socksPort = value
		case "SOCKSEnable":
			cfg.socksEnabled = value == "1"
		case "ProxyAutoConfigURLString":
			cfg.pacURL = value
		}
	}
	return cfg
}
