//go:build !darwin

package sysproxy

import (
	"net/http"
	"net/url"
)

// Func 返回可直接用于 http.Transport.Proxy 的函数。
//
// 其它平台沿用标准库行为（HTTP_PROXY/HTTPS_PROXY/NO_PROXY 环境变量）：
// 在非 macOS 环境里"系统代理"通常就是通过这些变量表达的。
func Func() func(*http.Request) (*url.URL, error) {
	return http.ProxyFromEnvironment
}

// Describe 返回一句话描述当前生效的代理来源，用于启动日志。
func Describe() string {
	if names := proxyEnvNames(); names != "" {
		return "env(" + names + ")"
	}
	return "direct"
}
