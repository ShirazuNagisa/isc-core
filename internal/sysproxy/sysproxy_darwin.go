//go:build darwin

package sysproxy

import (
	"context"
	"net/http"
	"net/url"
	"os/exec"
	"sync"
	"time"
)

// cacheTTL 是系统代理设置的缓存时长。
//
// 代理设置会在用户切换代理软件时变化，所以不能只在进程启动时读一次；
// 但每个请求都去起一个 scutil 子进程也太浪费。30 秒是"用户改完设置
// 很快就生效"和"不反复 fork"之间的折中。
const cacheTTL = 30 * time.Second

// scutilTimeout 限制读取系统代理的耗时：拿不到设置不该拖慢用户请求。
const scutilTimeout = 3 * time.Second

var (
	cacheMu   sync.Mutex
	cacheCfg  config
	cacheAt   time.Time
	cacheOnce bool
)

// Func 返回可直接用于 http.Transport.Proxy 的函数。
func Func() func(*http.Request) (*url.URL, error) {
	return func(req *http.Request) (*url.URL, error) {
		// 环境变量优先：显式设置应当能覆盖系统设置，这是标准库语义，
		// 也是容器/脚本化部署的预期。
		if u, err := http.ProxyFromEnvironment(req); err != nil || u != nil {
			return u, err
		}
		cfg, err := settings()
		if err != nil {
			return nil, nil
		}
		return pick(req, cfg)
	}
}

// Describe 返回一句话描述当前生效的代理来源，用于启动日志。
//
// 这类故障（"浏览器正常、内核连不上"）几乎只能靠日志归因，
// 所以把内核眼中的代理配置打出来是值得的。
func Describe() string {
	if names := proxyEnvNames(); names != "" {
		return "env(" + names + ")"
	}
	cfg, err := settings()
	if err != nil {
		return "system(unreadable)"
	}
	return describe(cfg)
}

// settings 返回缓存的系统代理配置。
func settings() (config, error) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if cacheOnce && time.Since(cacheAt) < cacheTTL {
		return cacheCfg, nil
	}
	cfg, err := readSystemProxy()
	if err != nil {
		// 读不到就当作没有代理（直连），不要把错误往上抛：
		// 代理只是手段，不该让正常请求连带失败。
		cfg = config{}
	}
	cacheCfg, cacheAt, cacheOnce = cfg, time.Now(), true
	return cacheCfg, err
}

// readSystemProxy 执行 `scutil --proxy` 并解析结果。
//
// 这里必须起子进程：内核主体按 D24 用 CGO_ENABLED=0 构建，
// 没法调用 SystemConfiguration 框架。固定 argv、不经 shell，
// 与 metrics 包读 top/vm_stat 的做法一致。
func readSystemProxy() (config, error) {
	ctx, cancel := context.WithTimeout(context.Background(), scutilTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "scutil", "--proxy").Output()
	if err != nil {
		return config{}, err
	}
	return parseScutilProxy(string(out)), nil
}
